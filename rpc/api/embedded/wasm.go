package embedded

import (
	"encoding/hex"
	"encoding/json"

	"github.com/inconshreveable/log15"

	"github.com/zenon-network/go-zenon/chain"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc/api"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/vm/wasm"
	"github.com/zenon-network/go-zenon/zenon"
)

type WasmApi struct {
	chain chain.Chain
	z     zenon.Zenon
	log   log15.Logger
}

func NewWasmApi(z zenon.Zenon) *WasmApi {
	return &WasmApi{
		chain: z.Chain(),
		z:     z,
		log:   common.RPCLogger.New("module", "embedded_wasm_api"),
	}
}

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

type WasmContractResponse struct {
	Address       types.Address `json:"address"`
	Deployer      types.Address `json:"deployer"`
	Version       uint32        `json:"version"`
	Upgradeable   bool          `json:"upgradeable"`
	Activated     bool          `json:"activated"`
	BytecodeHash  types.Hash    `json:"bytecodeHash"`
	BytecodeCost  string        `json:"bytecodeCost"`
	Halted        bool          `json:"halted"`
	Administrator types.Address `json:"administrator"`
	Deployed      bool          `json:"deployed"`
}

type WasmHaltStatusResponse struct {
	Halted        bool          `json:"halted"`
	Administrator types.Address `json:"administrator"`
}

type WasmEventResponse struct {
	ContractAddress types.Address `json:"contractAddress"`
	Topic           types.Hash    `json:"topic"`
	Indexed         bool          `json:"indexed"`
	Data            []byte        `json:"data"`
	BlockHash       types.Hash    `json:"blockHash"`
	BlockHeight     uint64        `json:"blockHeight"`
}

type WasmEventList struct {
	Count int                  `json:"count"`
	List  []*WasmEventResponse `json:"list"`
}

type WasmStatsResponse struct {
	PendingReceiveBacklog int `json:"pendingReceiveBacklog"`
}

// ---------------------------------------------------------------------------
// RPCs
// ---------------------------------------------------------------------------

// GetContract returns the metadata and halt status for a deployed WASM contract.
func (a *WasmApi) GetContract(contractAddress types.Address) (*WasmContractResponse, error) {
	_, context, err := api.GetFrontierContext(a.chain, types.WasmContract)
	if err != nil {
		return nil, err
	}

	metadata, err := definition.GetWasmContractMetadata(context.Storage(), contractAddress)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		return nil, nil
	}

	info, err := definition.GetWasmContractInfo(context.Storage())
	if err != nil {
		return nil, err
	}

	deployed := definition.WasmHasBytecode(context.Storage(), contractAddress)

	return &WasmContractResponse{
		Address:       contractAddress,
		Deployer:      metadata.Deployer,
		Version:       metadata.Version,
		Upgradeable:   metadata.Upgradeable,
		Activated:     metadata.Activated,
		BytecodeHash:  metadata.BytecodeHash,
		BytecodeCost:  metadata.BytecodeCost.String(),
		Halted:        info.Halted,
		Administrator: info.Administrator,
		Deployed:      deployed,
	}, nil
}

// GetHaltStatus returns the halt flag and administrator for a WASM contract.
func (a *WasmApi) GetHaltStatus(contractAddress types.Address) (*WasmHaltStatusResponse, error) {
	_, context, err := api.GetFrontierContext(a.chain, types.WasmContract)
	if err != nil {
		return nil, err
	}

	info, err := definition.GetWasmContractInfo(context.Storage())
	if err != nil {
		return nil, err
	}

	return &WasmHaltStatusResponse{
		Halted:        info.Halted,
		Administrator: info.Administrator,
	}, nil
}

// GetEvents performs a height-ranged block scan for events on receive blocks
// addressed to contractAddress. A zero topic matches all events.
func (a *WasmApi) GetEvents(contractAddress types.Address, topic types.Hash, fromHeight, toHeight, pageIndex, pageSize uint32) (*WasmEventList, error) {
	if pageSize > api.RpcMaxPageSize {
		return nil, api.ErrPageSizeParamTooBig
	}

	store := a.chain.GetFrontierMomentumStore()

	// fromHeight/toHeight are heights on contractAddress's OWN account chain, not
	// momentum heights (spec §11.2) — events live on the contract's receive
	// blocks, scanned via GetAccountBlocksByHeight below. When toHeight is 0,
	// default to the contract's account-frontier height. Using the momentum
	// frontier here would make count span the whole chain and walk past the
	// account frontier into absent heights (returned as nil by MoreByHeight).
	if toHeight == 0 {
		frontier, err := a.chain.GetFrontierAccountStore(contractAddress).Frontier()
		if err != nil {
			return nil, err
		}
		if frontier == nil {
			return &WasmEventList{Count: 0, List: []*WasmEventResponse{}}, nil
		}
		toHeight = uint32(frontier.Height)
	}

	if fromHeight == 0 {
		fromHeight = 1
	}
	if fromHeight > toHeight {
		return &WasmEventList{Count: 0, List: []*WasmEventResponse{}}, nil
	}

	count := uint64(toHeight) - uint64(fromHeight) + 1
	blocks, err := store.GetAccountBlocksByHeight(contractAddress, uint64(fromHeight), count)
	if err != nil {
		return nil, err
	}

	zeroTopic := types.Hash{}
	var allEvents []*WasmEventResponse

	for _, block := range blocks {
		// account.MoreByHeight appends nil for absent heights; skip defensively
		// so an over-range scan can never nil-deref.
		if block == nil {
			continue
		}
		if block.Events == nil {
			continue
		}
		for _, event := range block.Events {
			if topic != zeroTopic && event.Topic != topic {
				continue
			}
			allEvents = append(allEvents, &WasmEventResponse{
				ContractAddress: event.ContractAddress,
				Topic:           event.Topic,
				Indexed:         event.Indexed,
				Data:            event.Data,
				BlockHash:       block.Hash,
				BlockHeight:     block.Height,
			})
		}
	}

	total := len(allEvents)
	start, end := api.GetRange(pageIndex, pageSize, uint32(total))
	if start >= uint32(total) {
		return &WasmEventList{Count: total, List: []*WasmEventResponse{}}, nil
	}

	return &WasmEventList{
		Count: total,
		List:  allEvents[start:end],
	}, nil
}

// GetStats returns telemetry for the WASM subsystem.
func (a *WasmApi) GetStats() (*WasmStatsResponse, error) {
	store := a.chain.GetFrontierMomentumStore()

	pending, err := store.GetWasmPendingAddresses()
	if err != nil {
		return nil, err
	}

	return &WasmStatsResponse{
		PendingReceiveBacklog: len(pending),
	}, nil
}

// CallView performs a read-only, off-consensus view call against a deployed WASM
// contract and returns the contract's raw return buffer (spec §11.3). It never
// produces a transaction and the result is NOT part of consensus; state and
// balance reads resolve at the node's frontier momentum height, so nodes at
// slightly different heights MAY return different bytes.
//
// The function name is accepted for spec parity and future ABI Outputs decoding.
// Args bytes are written into guest memory (mirroring Execute) and the raw
// length-prefixed buffer contents are returned. A halted contract is still
// queryable — a view cannot mutate state.
func (a *WasmApi) CallView(contractAddress types.Address, function string, args []byte) (*callViewResult, error) {
	// Bytecode and deploy status live in the WasmContract (0x01) frontier storage.
	_, wasmCtx, err := api.GetFrontierContext(a.chain, types.WasmContract)
	if err != nil {
		return nil, err
	}
	bytecode, err := definition.GetWasmBytecode(wasmCtx.Storage(), contractAddress)
	if err != nil {
		return nil, err
	}
	if len(bytecode) == 0 {
		return nil, constants.ErrWasmContractNotDeployed
	}

	// A contract is only usable once activated. Bytecode left behind by an
	// un-activated single-shot Deploy must not be queryable either — keep views
	// consistent with the Execute gate.
	metadata, err := definition.GetWasmContractMetadata(wasmCtx.Storage(), contractAddress)
	if err != nil {
		return nil, err
	}
	if metadata == nil || !metadata.Activated {
		return nil, constants.ErrWasmContractNotActivated
	}

	// Load governance-tunable variables for the view context.
	vars, err := definition.GetWasmVariables(wasmCtx.Storage())
	if err != nil {
		return nil, err
	}

	// Build a frontier-backed context for the deployed contract (0x02) so the
	// view's state_read / balance_get / get_* host calls resolve at the head.
	_, contractCtx, err := api.GetFrontierContext(a.chain, contractAddress)
	if err != nil {
		return nil, err
	}

	wc := wasm.NewViewContext(contractCtx, contractAddress, vars)
	data, err := wasm.GetWasmRuntime("").CallView(wc, bytecode, args)
	if err != nil {
		return nil, err
	}
	return &callViewResult{Data: data}, nil
}

// callViewResult wraps the raw return bytes from a callView invocation.
type callViewResult struct {
	Data []byte `json:"data"`
}

func (r *callViewResult) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Data string `json:"data"`
	}{
		Data: "0x" + hex.EncodeToString(r.Data),
	})
}
