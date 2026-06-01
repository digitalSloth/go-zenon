package implementation

import (
	"math/big"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/crypto"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/vm/vm_context"
	"github.com/zenon-network/go-zenon/vm/wasm"
)

var wasmLog = common.EmbeddedLogger.New("contract", "wasm")

// bytecodeCost returns the QSR cost for a given bytecode size.
func bytecodeCost(size int, vars *definition.WasmVariables) *big.Int {
	d := big.NewInt(int64(size) * int64(vars.QSRPerByteOfBytecode))
	min := big.NewInt(int64(vars.MinBytecodeCost))
	if d.Cmp(min) < 0 {
		return min
	}
	return d
}

// chunksExpired reports whether an in-progress chunked upload has exceeded its
// TTL. Per-method enforcement here is the consensus-safe gate.
// The subtraction is guarded against unsigned underflow.
func chunksExpired(currentHeight, firstChunkHeight, chunkTTLMomentums uint64) bool {
	if currentHeight <= firstChunkHeight {
		return false
	}
	return currentHeight-firstChunkHeight > chunkTTLMomentums
}

// loadWasmInfo loads WasmContractInfo from the current context's storage.
func loadWasmInfo(context vm_context.AccountVmContext) (*definition.WasmContractInfo, error) {
	return definition.GetWasmContractInfo(context.Storage())
}

// Deploy — Deploy(wasmAddr address, salt bytes32, chunkIndex uint32, totalChunks uint32, chunkData bytes) + QSR
// Unified method for single-shot and chunked deployment (new and upgrade).
type DeployMethod struct {
	MethodName string
}

func (p *DeployMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return constants.EmbeddedWasmDeploy, nil
}

func (p *DeployMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	params := new(struct {
		WasmAddr    types.Address `abi:"wasmAddr"`
		Salt        [32]byte      `abi:"salt"`
		ChunkIndex  uint32        `abi:"chunkIndex"`
		TotalChunks uint32        `abi:"totalChunks"`
		ChunkData   []byte        `abi:"chunkData"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}
	// QSR-or-zero only (zero valid for non-first chunks and delta-0 upgrades).
	if block.Amount.Sign() > 0 && block.TokenStandard != types.QsrTokenStandard {
		return constants.ErrInvalidTokenOrAmount
	}
	block.Data, err = definition.ABIWasm.PackMethod(p.MethodName,
		params.WasmAddr, params.Salt, params.ChunkIndex, params.TotalChunks, params.ChunkData)
	return err
}

func (p *DeployMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	// Halt check
	info, err := loadWasmInfo(context)
	if err != nil {
		return nil, err
	}
	if info.Halted {
		return nil, constants.ErrWasmHalted
	}

	// Load governance-tunable variables.
	vars, err := definition.GetWasmVariables(context.Storage())
	if err != nil {
		return nil, err
	}

	// Unpack params
	params := new(struct {
		WasmAddr    types.Address `abi:"wasmAddr"`
		Salt        [32]byte      `abi:"salt"`
		ChunkIndex  uint32        `abi:"chunkIndex"`
		TotalChunks uint32        `abi:"totalChunks"`
		ChunkData   []byte        `abi:"chunkData"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, sendBlock.Data); err != nil {
		return nil, constants.ErrUnpackError
	}

	// Validate chunk parameters
	if params.TotalChunks == 0 || params.TotalChunks > uint32(vars.MaxChunkCount) {
		return nil, constants.ErrInvalidArguments
	}
	if params.ChunkIndex >= params.TotalChunks {
		return nil, constants.ErrInvalidArguments
	}
	if len(params.ChunkData) == 0 || len(params.ChunkData) > int(vars.MaxWasmBytecodeSize) {
		return nil, constants.ErrInvalidArguments
	}

	// Salt binding
	expectedAddr := types.WasmAddress(sendBlock.Address, params.Salt)
	if params.WasmAddr != expectedAddr {
		return nil, constants.ErrPermissionDenied
	}

	// Detect new vs upgrade. Only an ACTIVATED contract is a valid upgrade
	// target. Bytecode left behind by an un-activated single-shot Deploy is an
	// incomplete deployment, not an upgrade target: its chunk metadata is still
	// present, so the chunk-0 guard below (chunkMeta != nil) rejects a duplicate
	// Deploy until it is Activated or abandoned via DiscardChunks.
	isUpgrade := false
	var meta *definition.WasmContractMetadata
	if definition.WasmHasBytecode(context.Storage(), params.WasmAddr) {
		meta, err = definition.GetWasmContractMetadata(context.Storage(), params.WasmAddr)
		if err != nil {
			return nil, err
		}
		if meta != nil && meta.Activated {
			isUpgrade = true
			if sendBlock.Address != meta.Deployer {
				return nil, constants.ErrPermissionDenied
			}
			if !meta.Upgradeable {
				return nil, constants.ErrPermissionDenied
			}
		}
	}

	// Load existing chunk metadata
	chunkMeta, err := definition.GetWasmChunkMetadata(context.Storage(), params.WasmAddr)
	if err != nil {
		return nil, err
	}

	// Chunk 0: charge QSR, save chunk metadata
	if params.ChunkIndex == 0 {
		if chunkMeta != nil {
			return nil, constants.ErrForbiddenParam // already in progress
		}

		// Compute worst-case cost
		var worstCost *big.Int
		if params.TotalChunks == 1 {
			// Single-shot: exact cost for this chunk
			worstCost = bytecodeCost(len(params.ChunkData), vars)
		} else {
			// Multi-chunk: worst-case for all chunks at max size
			worstCost = new(big.Int).Mul(
				big.NewInt(int64(params.TotalChunks)*int64(vars.MaxWasmBytecodeSize)),
				big.NewInt(int64(vars.QSRPerByteOfBytecode)),
			)
			minCost := big.NewInt(int64(vars.MinBytecodeCost))
			if worstCost.Cmp(minCost) < 0 {
				worstCost.Set(minCost)
			}
		}

		// For upgrades, cost is the delta (old cost already burned)
		if isUpgrade {
			delta := new(big.Int).Sub(worstCost, meta.BytecodeCost)
			if delta.Sign() < 0 {
				delta.SetInt64(0)
			}
			worstCost = delta
		}

		// Chunk 0 must carry at least worstCost QSR
		if sendBlock.Amount.Cmp(worstCost) < 0 {
			return nil, constants.ErrNotEnoughDepositedQsr
		}

		momentum, err := context.GetFrontierMomentum()
		if err != nil {
			return nil, err
		}

		chunkMeta = &definition.WasmChunkMetadata{
			TotalChunks:      params.TotalChunks,
			FirstChunkHeight: momentum.Height,
			CollectedQsr:     new(big.Int).Set(sendBlock.Amount),
			Uploader:         sendBlock.Address,
			IsUpgrade:        isUpgrade,
		}
		if err := chunkMeta.Save(context.Storage(), params.WasmAddr); err != nil {
			return nil, err
		}
	} else {
		// Subsequent chunk: reject QSR, verify uploader
		if sendBlock.Amount.Sign() != 0 {
			return nil, constants.ErrInvalidTokenOrAmount
		}
		if chunkMeta == nil {
			return nil, constants.ErrInvalidArguments // chunk 0 must come first
		}
		if chunkMeta.Uploader != sendBlock.Address {
			return nil, constants.ErrPermissionDenied
		}
		if params.TotalChunks != chunkMeta.TotalChunks {
			return nil, constants.ErrInvalidArguments
		}
		// Reject if expired
		momentum, err := context.GetFrontierMomentum()
		if err != nil {
			return nil, err
		}
		if chunksExpired(momentum.Height, chunkMeta.FirstChunkHeight, vars.ChunkTTLMomentums) {
			return nil, constants.ErrWasmChunksExpired
		}
	}

	// Duplicate chunk check
	chunkKey := definition.WasmChunkDataKey(params.WasmAddr, params.ChunkIndex)
	has, err := context.Storage().Has(chunkKey)
	if err != nil {
		return nil, err
	}
	if has {
		return nil, constants.ErrForbiddenParam
	}

	// Store chunk
	if err := context.Storage().Put(chunkKey, params.ChunkData); err != nil {
		return nil, err
	}

	// Single-shot (totalChunks == 1): validate bytecode immediately.
	// On failure, return error — VM auto-refunds via rollbackEmbedded.
	if params.TotalChunks == 1 {
		if err := wasm.ValidateBytecode(params.ChunkData); err != nil {
			return nil, err
		}
		if _, err := wasm.GetWasmRuntime("").CompileModule(params.ChunkData); err != nil {
			return nil, err
		}

		// For a NEW deployment, persist the bytecode + metadata now with
		// Activated=false: the contract cannot execute until the deployer calls
		// Activate (which burns the bytecode cost + ZNN fee and flips Activated).
		//
		// For an UPGRADE, do NOT overwrite the live bytecode here. The existing
		// activated contract keeps running its current code until Activate
		// assembles the new bytecode from chunk storage, stores it, bumps the
		// version, and burns the delta + ZNN fee. Storing it now would make the
		// new code live before the upgrade has been paid for — the same
		// fee-avoidance vector the Activated gate closes for new deployments.
		if !isUpgrade {
			if err := context.Storage().Put(definition.WasmBytecodeKey(params.WasmAddr), params.ChunkData); err != nil {
				return nil, err
			}
			bytecodeHash := types.BytesToHashPanic(crypto.Hash(params.ChunkData))
			newMeta := &definition.WasmContractMetadata{
				Deployer:     sendBlock.Address,
				Version:      1,
				Upgradeable:  false, // set at Activate
				Activated:    false, // set at Activate
				BytecodeHash: bytecodeHash,
				BytecodeCost: bytecodeCost(len(params.ChunkData), vars),
			}
			if err := newMeta.Save(context.Storage(), params.WasmAddr); err != nil {
				return nil, err
			}
		}

		// NOTE: chunk metadata is NOT deleted here for single-shot. Activate needs
		// it to finalize the deployment (assemble bytecode, set Upgradeable, burn
		// the ZNN fee, settle QSR). Activate deletes it.

		wasmLog.Info("deployed contract (single-shot)", "wasmAddr", params.WasmAddr,
			"deployer", sendBlock.Address, "size", len(params.ChunkData), "upgrade", isUpgrade)
	} else {
		wasmLog.Info("stored chunk", "wasmAddr", params.WasmAddr,
			"chunk", params.ChunkIndex, "total", params.TotalChunks)
	}

	return nil, nil
}

// Activate — Activate(wasmAddr address, salt bytes32, upgradeable bool) + 1 ZNN
// Assembles chunks, validates bytecode, settles QSR (burn + prefund-forward), burns ZNN fee.
type ActivateMethod struct {
	MethodName string
}

func (p *ActivateMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return constants.EmbeddedWasmDeploy, nil
}

func (p *ActivateMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	params := new(struct {
		WasmAddr    types.Address `abi:"wasmAddr"`
		Salt        [32]byte      `abi:"salt"`
		Upgradeable bool          `abi:"upgradeable"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}
	// Require exactly 1 ZNN
	if block.TokenStandard != types.ZnnTokenStandard || block.Amount.Cmp(big.NewInt(int64(constants.ZNNDeployFee))) != 0 {
		return constants.ErrInvalidTokenOrAmount
	}
	block.Data, err = definition.ABIWasm.PackMethod(p.MethodName, params.WasmAddr, params.Salt, params.Upgradeable)
	return err
}

func (p *ActivateMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	// Halt check
	info, err := loadWasmInfo(context)
	if err != nil {
		return nil, err
	}
	if info.Halted {
		return nil, constants.ErrWasmHalted
	}

	// Load governance-tunable variables.
	vars, err := definition.GetWasmVariables(context.Storage())
	if err != nil {
		return nil, err
	}

	// Unpack params
	params := new(struct {
		WasmAddr    types.Address `abi:"wasmAddr"`
		Salt        [32]byte      `abi:"salt"`
		Upgradeable bool          `abi:"upgradeable"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, sendBlock.Data); err != nil {
		return nil, constants.ErrUnpackError
	}

	// Load chunk metadata
	chunkMeta, err := definition.GetWasmChunkMetadata(context.Storage(), params.WasmAddr)
	if err != nil {
		return nil, err
	}
	if chunkMeta == nil {
		return nil, constants.ErrInvalidArguments
	}

	// Verify uploader
	if sendBlock.Address != chunkMeta.Uploader {
		return nil, constants.ErrPermissionDenied
	}

	// Expiry check
	momentum, err := context.GetFrontierMomentum()
	if err != nil {
		return nil, err
	}
	if chunksExpired(momentum.Height, chunkMeta.FirstChunkHeight, vars.ChunkTTLMomentums) {
		return nil, constants.ErrWasmChunksExpired
	}

	// Assemble bytecode from chunks
	bytecode := make([]byte, 0, int(chunkMeta.TotalChunks)*int(vars.MaxWasmBytecodeSize))
	for i := uint32(0); i < chunkMeta.TotalChunks; i++ {
		chunkKey := definition.WasmChunkDataKey(params.WasmAddr, i)
		chunkData, err := context.Storage().Get(chunkKey)
		if err != nil {
			return nil, err
		}
		if len(chunkData) == 0 {
			return nil, constants.ErrInvalidArguments
		}
		bytecode = append(bytecode, chunkData...)
	}

	// Compute actual cost
	actualCost := bytecodeCost(len(bytecode), vars)

	// For upgrades, cost is the delta
	isUpgrade := chunkMeta.IsUpgrade
	var meta *definition.WasmContractMetadata
	if isUpgrade {
		meta, err = definition.GetWasmContractMetadata(context.Storage(), params.WasmAddr)
		if err != nil {
			return nil, err
		}
		if meta == nil {
			return nil, constants.ErrWasmContractNotDeployed
		}
		if sendBlock.Address != meta.Deployer {
			return nil, constants.ErrPermissionDenied
		}
		if !meta.Upgradeable {
			return nil, constants.ErrPermissionDenied
		}
		delta := new(big.Int).Sub(actualCost, meta.BytecodeCost)
		if delta.Sign() < 0 {
			delta.SetInt64(0)
		}
		actualCost = delta
	}

	// Validate + pre-compile. On failure: refund QSR + ZNN, delete chunks, return success.
	if err := wasm.ValidateBytecode(bytecode); err != nil {
		return p.refundAndCleanup(context, params.WasmAddr, chunkMeta, sendBlock.Address), nil
	}
	if _, err := wasm.GetWasmRuntime("").CompileModule(bytecode); err != nil {
		return p.refundAndCleanup(context, params.WasmAddr, chunkMeta, sendBlock.Address), nil
	}

	// For upgrades, capture the OLD bytecode before overwriting it so its compiled
	// module can be evicted from the cache. Reading after the Put would return the
	// new bytecode and evict the wrong (new) cache entry, leaving the stale module
	// resident.
	var oldBytecode []byte
	if isUpgrade {
		oldBytecode, err = definition.GetWasmBytecode(context.Storage(), params.WasmAddr)
		if err != nil {
			return nil, err
		}
	}

	// Store bytecode
	if err := context.Storage().Put(definition.WasmBytecodeKey(params.WasmAddr), bytecode); err != nil {
		return nil, err
	}

	// Save/update metadata. Activated flips true here — this is the gate that
	// enables Execute (and view) calls against the contract.
	bytecodeHash := types.BytesToHashPanic(crypto.Hash(bytecode))
	if isUpgrade {
		wasm.GetWasmRuntime("").Evict(oldBytecode)

		meta.Version += 1
		meta.Activated = true
		meta.BytecodeHash = bytecodeHash
		meta.BytecodeCost = bytecodeCost(len(bytecode), vars)
		if err := meta.Save(context.Storage(), params.WasmAddr); err != nil {
			return nil, err
		}
	} else {
		newMeta := &definition.WasmContractMetadata{
			Deployer:     sendBlock.Address,
			Version:      1,
			Upgradeable:  params.Upgradeable,
			Activated:    true,
			BytecodeHash: bytecodeHash,
			BytecodeCost: bytecodeCost(len(bytecode), vars),
		}
		if err := newMeta.Save(context.Storage(), params.WasmAddr); err != nil {
			return nil, err
		}
	}

	// Delete chunk storage
	for i := uint32(0); i < chunkMeta.TotalChunks; i++ {
		chunkKey := definition.WasmChunkDataKey(params.WasmAddr, i)
		if err := context.Storage().Delete(chunkKey); err != nil {
			return nil, err
		}
	}
	if err := chunkMeta.Delete(context.Storage(), params.WasmAddr); err != nil {
		return nil, err
	}

	wasmLog.Info("activated contract", "wasmAddr", params.WasmAddr, "upgrade", isUpgrade, "size", len(bytecode))

	// Build descendant blocks (fixed order: QSR cost-burn → QSR excess-forward → ZNN fee-burn)
	var result []*nom.AccountBlock

	// QSR cost-burn
	if actualCost.Sign() > 0 {
		result = append(result, &nom.AccountBlock{
			ToAddress:     types.TokenContract,
			Data:          definition.ABIToken.PackMethodPanic(definition.BurnMethodName),
			Amount:        actualCost,
			TokenStandard: types.QsrTokenStandard,
		})
	}

	// QSR excess-forward to 0x02 as prefund
	// Address must be stamped here so applySend's bytecode-exists exemption
	// (block.Address == types.WasmContract) fires before finalizeEmbedded.
	excess := new(big.Int).Sub(chunkMeta.CollectedQsr, actualCost)
	if excess.Sign() > 0 {
		result = append(result, &nom.AccountBlock{
			Address:       types.WasmContract,
			ToAddress:     params.WasmAddr,
			Amount:        excess,
			TokenStandard: types.QsrTokenStandard,
			Data:          []byte{},
		})
	}

	// ZNN fee-burn
	result = append(result, &nom.AccountBlock{
		ToAddress:     types.TokenContract,
		Data:          definition.ABIToken.PackMethodPanic(definition.BurnMethodName),
		Amount:        big.NewInt(int64(constants.ZNNDeployFee)),
		TokenStandard: types.ZnnTokenStandard,
	})

	return result, nil
}

// refundAndCleanup handles validation failure at Activate: refunds QSR + ZNN to
// the uploader and deletes all chunk state. Returns the refund descendants.
// Returning nil,nil (success) prevents rollback from stranding the chunk-0 QSR.
func (p *ActivateMethod) refundAndCleanup(context vm_context.AccountVmContext, wasmAddr types.Address, chunkMeta *definition.WasmChunkMetadata, uploader types.Address) []*nom.AccountBlock {
	// Delete chunk storage
	for i := uint32(0); i < chunkMeta.TotalChunks; i++ {
		chunkKey := definition.WasmChunkDataKey(wasmAddr, i)
		context.Storage().Delete(chunkKey)
	}
	chunkMeta.Delete(context.Storage(), wasmAddr)

	var result []*nom.AccountBlock

	// QSR refund
	if chunkMeta.CollectedQsr.Sign() > 0 {
		result = append(result, &nom.AccountBlock{
			ToAddress:     uploader,
			Amount:        new(big.Int).Set(chunkMeta.CollectedQsr),
			TokenStandard: types.QsrTokenStandard,
			Data:          []byte{},
		})
	}

	// ZNN refund
	result = append(result, &nom.AccountBlock{
		ToAddress:     uploader,
		Amount:        big.NewInt(int64(constants.ZNNDeployFee)),
		TokenStandard: types.ZnnTokenStandard,
		Data:          []byte{},
	})

	return result
}

// DiscardChunks — DiscardChunks(wasmAddr address, salt bytes32)
type DiscardChunksMethod struct {
	MethodName string
}

func (p *DiscardChunksMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *DiscardChunksMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	params := new(struct {
		WasmAddr types.Address `abi:"wasmAddr"`
		Salt     [32]byte      `abi:"salt"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}
	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}
	block.Data, err = definition.ABIWasm.PackMethod(p.MethodName, params.WasmAddr, params.Salt)
	return err
}
func (p *DiscardChunksMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	// Unpack params
	params := new(struct {
		WasmAddr types.Address `abi:"wasmAddr"`
		Salt     [32]byte      `abi:"salt"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, sendBlock.Data); err != nil {
		return nil, constants.ErrUnpackError
	}

	// Salt binding
	expectedAddr := types.WasmAddress(sendBlock.Address, params.Salt)
	if params.WasmAddr != expectedAddr {
		return nil, constants.ErrPermissionDenied
	}

	// Load chunk metadata
	chunkMeta, err := definition.GetWasmChunkMetadata(context.Storage(), params.WasmAddr)
	if err != nil {
		return nil, err
	}
	if chunkMeta == nil {
		return nil, constants.ErrInvalidArguments
	}

	// Determine TTL status BEFORE deleting (spec §4.4). An upload past its TTL
	// forfeits its collected QSR: DiscardChunks still frees the chunk storage
	// so the slot can be reused, but the QSR is NOT refunded — otherwise a
	// deployer could squat a slot past the TTL and still reclaim the full
	// amount on demand, defeating the griefing protection.
	momentum, err := context.GetFrontierMomentum()
	if err != nil {
		return nil, err
	}
	vars, err := definition.GetWasmVariables(context.Storage())
	if err != nil {
		return nil, err
	}
	expired := chunksExpired(momentum.Height, chunkMeta.FirstChunkHeight, vars.ChunkTTLMomentums)

	// Delete all chunk storage
	for i := uint32(0); i < chunkMeta.TotalChunks; i++ {
		chunkKey := definition.WasmChunkDataKey(params.WasmAddr, i)
		if err := context.Storage().Delete(chunkKey); err != nil {
			return nil, err
		}
	}
	if err := chunkMeta.Delete(context.Storage(), params.WasmAddr); err != nil {
		return nil, err
	}

	// For a fresh (non-upgrade) deployment, also remove the un-activated
	// artifacts a single-shot Deploy leaves behind (bytecode + metadata), so the
	// address is fully reclaimed. An upgrade-in-progress discard must NOT touch
	// these — the live, already-activated contract keeps running. Deletes of
	// absent keys (e.g. a multi-chunk fresh deploy that never stored bytecode)
	// are no-ops.
	if !chunkMeta.IsUpgrade {
		if err := context.Storage().Delete(definition.WasmBytecodeKey(params.WasmAddr)); err != nil {
			return nil, err
		}
		if err := definition.DeleteWasmContractMetadata(context.Storage(), params.WasmAddr); err != nil {
			return nil, err
		}
	}

	wasmLog.Info("discarded chunks", "wasmAddr", params.WasmAddr, "expired", expired, "upgrade", chunkMeta.IsUpgrade)

	// Refund collected QSR to deployer — only if the upload had not yet expired.
	var result []*nom.AccountBlock
	if !expired && chunkMeta.CollectedQsr.Sign() > 0 {
		result = append(result, &nom.AccountBlock{
			Address:       types.WasmContract,
			ToAddress:     sendBlock.Address,
			BlockType:     nom.BlockTypeContractSend,
			Amount:        new(big.Int).Set(chunkMeta.CollectedQsr),
			TokenStandard: types.QsrTokenStandard,
			Data:          []byte{},
		})
	}

	return result, nil
}

// Halt — Halt() (no params)
type WasmHaltMethod struct {
	MethodName string
}

func (p *WasmHaltMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *WasmHaltMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	if err := definition.ABIWasm.UnpackEmptyMethod(p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}
	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}
	block.Data, err = definition.ABIWasm.PackMethod(p.MethodName)
	return err
}
func (p *WasmHaltMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	info, err := loadWasmInfo(context)
	if err != nil {
		return nil, err
	}

	if sendBlock.Address != info.Administrator {
		return nil, constants.ErrPermissionDenied
	}
	if info.Halted {
		return nil, constants.ErrWasmAlreadyHalted
	}

	info.Halted = true
	if err := info.Save(context.Storage()); err != nil {
		return nil, err
	}

	wasmLog.Info("wasm runtime halted", "by", sendBlock.Address)
	return nil, nil
}

// Unhalt — Unhalt() (no params)
type WasmUnhaltMethod struct {
	MethodName string
}

func (p *WasmUnhaltMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *WasmUnhaltMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	if err := definition.ABIWasm.UnpackEmptyMethod(p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}
	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}
	block.Data, err = definition.ABIWasm.PackMethod(p.MethodName)
	return err
}
func (p *WasmUnhaltMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	info, err := loadWasmInfo(context)
	if err != nil {
		return nil, err
	}

	if sendBlock.Address != info.Administrator {
		return nil, constants.ErrPermissionDenied
	}
	if !info.Halted {
		return nil, constants.ErrWasmNotHalted
	}

	info.Halted = false
	if err := info.Save(context.Storage()); err != nil {
		return nil, err
	}

	wasmLog.Info("wasm runtime unhalted", "by", sendBlock.Address)
	return nil, nil
}

// ChangeAdministrator — ChangeAdministrator(newAdmin address)
type WasmChangeAdministratorMethod struct {
	MethodName string
}

func (p *WasmChangeAdministratorMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *WasmChangeAdministratorMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	params := new(struct {
		NewAdmin types.Address `abi:"newAdmin"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}
	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}
	block.Data, err = definition.ABIWasm.PackMethod(p.MethodName, params.NewAdmin)
	return err
}
func (p *WasmChangeAdministratorMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	// Unpack params
	params := new(struct {
		NewAdmin types.Address `abi:"newAdmin"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, sendBlock.Data); err != nil {
		return nil, constants.ErrUnpackError
	}

	info, err := loadWasmInfo(context)
	if err != nil {
		return nil, err
	}

	// Verify administrator
	if sendBlock.Address != info.Administrator {
		return nil, constants.ErrPermissionDenied
	}

	// Time challenge
	paramsHash := crypto.Hash(params.NewAdmin.Bytes())
	timeChallengeInfo, err := TimeChallenge(context, p.MethodName, paramsHash, constants.WasmAdministratorDelay)
	if err != nil {
		return nil, err
	}
	// If paramsHash is non-zero, challenge was just started
	if !timeChallengeInfo.ParamsHash.IsZero() {
		return nil, nil
	}

	// Challenge passed; update administrator
	info.Administrator = params.NewAdmin
	if err := info.Save(context.Storage()); err != nil {
		return nil, err
	}

	wasmLog.Info("administrator changed", "newAdmin", params.NewAdmin)
	return nil, nil
}

// Execute — Execute(function string, args []byte)
type ExecuteMethod struct {
	MethodName string
}

func (p *ExecuteMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return constants.EmbeddedWasmExecute, nil
}
func (p *ExecuteMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	params := new(struct {
		Function string `abi:"function"`
		Args     []byte `abi:"args"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}
	// Bound the argument blob (§14). Without this the only limit is the guest's
	// linear-memory size, which surfaces late as ErrMemoryOOB; reject oversized
	// args at admission instead.
	if len(params.Args) > constants.MaxArgsBytes {
		return constants.ErrWasmArgsTooLarge
	}
	block.Data, err = definition.ABIWasm.PackMethod(p.MethodName, params.Function, params.Args)
	return err
}
func (p *ExecuteMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	// Load halt status from WasmContract's storage (cross-account read)
	wasmStore := context.MomentumStore().GetAccountStore(types.WasmContract)
	info, err := definition.GetWasmContractInfo(wasmStore.Storage())
	if err != nil {
		return nil, err
	}
	if info.Halted {
		return nil, constants.ErrWasmHalted
	}

	// Load bytecode from WasmContract's storage
	bytecode, err := definition.GetWasmBytecode(wasmStore.Storage(), *context.Address())
	if err != nil {
		return nil, err
	}

	// Load metadata. Bytecode exists (checked above), so metadata MUST exist too
	// — Deploy/Activate always write them together. A nil here is an
	// inconsistent state; reject rather than silently skip the activation gate.
	meta, err := definition.GetWasmContractMetadata(wasmStore.Storage(), *context.Address())
	if err != nil {
		return nil, err
	}
	if meta == nil {
		return nil, constants.ErrWasmContractNotDeployed
	}

	// Execution gate: a contract is only usable once the deployer has called
	// Activate (which burns the bytecode cost + ZNN fee and flips Activated).
	// Bytecode left behind by an un-activated single-shot Deploy must never run,
	// otherwise the bytecode cost/fee could be avoided — the spam/abuse vector.
	if !meta.Activated {
		return nil, constants.ErrWasmContractNotActivated
	}
	revoked, err := definition.IsWasmRevoked(wasmStore.Storage(), *context.Address())
	if err != nil {
		return nil, err
	}
	if revoked {
		return nil, constants.ErrWasmContractRevoked
	}

	// Load governance-tunable variables.
	vars, err := definition.GetWasmVariables(wasmStore.Storage())
	if err != nil {
		return nil, err
	}

	// Parse function name and args
	params := new(struct {
		Function string `abi:"function"`
		Args     []byte `abi:"args"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, sendBlock.Data); err != nil {
		return nil, constants.ErrUnpackError
	}

	// Create WasmContext and execute. State writes accumulate a burnAmount
	// which is emitted as a single descendant Burn block on success.
	wc := wasm.NewWasmContext(context, sendBlock, *context.Address(), vars)
	result, err := wasm.GetWasmRuntime("").Execute(wc, bytecode, params.Args)
	if err != nil {
		return nil, err
	}
	if result != 0 {
		return nil, constants.ErrWasmExecutionFailed
	}

	// Convert WASM events to nom-level events and store on the context.
	// finalizeEmbedded reads context.Events() to wire into the receive block.
	events := make([]nom.AccountBlockEvent, 0, len(wc.Events()))
	for _, e := range wc.Events() {
		events = append(events, nom.AccountBlockEvent{
			ContractAddress: e.ContractAddress,
			Topic:           e.Topic,
			Indexed:         e.Indexed,
			Data:            e.Data,
		})
	}
	context.SetEvents(events)

	// Build descendant blocks: transfers first, then the aggregated burn.
	// finalizeEmbedded stamps Address, BlockType, Version, Hash, etc.
	descendants := wc.Transfers()
	if wc.BurnAmount().Sign() > 0 {
		descendants = append(descendants, &nom.AccountBlock{
			ToAddress:     types.TokenContract,
			Data:          definition.ABIToken.PackMethodPanic(definition.BurnMethodName),
			TokenStandard: types.QsrTokenStandard,
			Amount:        new(big.Int).Set(wc.BurnAmount()),
		})
	}
	return descendants, nil
}

// WasmRevokeMethod — Revoke(wasmAddr address) at the 0x01 factory.
// Permanently bricks a contract by setting metadata.Revoked = true.
// The deployer must first clear all non-QSR balances. Remaining QSR
// is returned to the deployer (not burned). The contract's bytecode
// and state stay on-chain as a permanent record.
type WasmRevokeMethod struct {
	MethodName string
}

func (p *WasmRevokeMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}

func (p *WasmRevokeMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	params := new(struct {
		WasmAddr types.Address `abi:"wasmAddr"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}
	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}
	var err error
	block.Data, err = definition.ABIWasm.PackMethod(p.MethodName, params.WasmAddr)
	return err
}

func (p *WasmRevokeMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	params := new(struct {
		WasmAddr types.Address `abi:"wasmAddr"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, sendBlock.Data); err != nil {
		return nil, constants.ErrUnpackError
	}

	if !types.IsWasmContractAddress(params.WasmAddr) {
		return nil, constants.ErrInvalidArguments
	}

	// Load metadata from the 0x01 factory's storage.
	meta, err := definition.GetWasmContractMetadata(context.Storage(), params.WasmAddr)
	if err != nil {
		return nil, err
	}
	if meta == nil {
		return nil, constants.ErrWasmContractNotDeployed
	}

	// Deployer-only.
	if sendBlock.Address != meta.Deployer {
		return nil, constants.ErrPermissionDenied
	}

	// Already revoked.
	revoked, err := definition.IsWasmRevoked(context.Storage(), params.WasmAddr)
	if err != nil {
		return nil, err
	}
	if revoked {
		return nil, constants.ErrWasmContractRevoked
	}

	// Non-QSR balance check on the 0x02 contract. Presence-test only —
	// never iterate to produce ordered output (map order is nondeterministic).
	wasmStore := context.MomentumStore().GetAccountStore(params.WasmAddr)
	balanceMap, err := wasmStore.GetBalanceMap()
	if err != nil {
		return nil, err
	}
	for zts, amount := range balanceMap {
		if zts != types.QsrTokenStandard && amount.Sign() > 0 {
			return nil, constants.ErrWasmHasNonQsrBalance
		}
	}

	// Set the revoked flag (separate key, avoids ABI round-trip issues).
	if err := definition.SetWasmRevoked(context.Storage(), params.WasmAddr); err != nil {
		return nil, err
	}

	wasmLog.Info("revoked contract", "wasmAddr", params.WasmAddr, "deployer", sendBlock.Address)

	return nil, nil
}

// SetWasmVariables — SetWasmVariables(13x uint64)
// Admin-only method to update governance-tunable WASM runtime parameters.
type SetWasmVariablesMethod struct {
	MethodName string
}

func (p *SetWasmVariablesMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}

func (p *SetWasmVariablesMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	param := new(definition.WasmVariables)
	if err := definition.ABIWasm.UnpackMethod(param, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}
	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}
	// Bounds checks — prevents bricking the runtime.
	if param.ExecutionGasLimit < definition.WasmVarExecutionGasLimitMin {
		return constants.ErrForbiddenParam
	}
	if param.OnReceiveGasLimit < definition.WasmVarOnReceiveGasLimitMin {
		return constants.ErrForbiddenParam
	}
	if param.MaxDescendantBlocks < definition.WasmVarMaxDescendantBlocksMin {
		return constants.ErrForbiddenParam
	}
	if param.MaxEventsPerExecute < definition.WasmVarMaxEventsPerExecuteMin {
		return constants.ErrForbiddenParam
	}
	if param.MaxEventDataPerEvent < definition.WasmVarMaxEventDataPerEventMin {
		return constants.ErrForbiddenParam
	}
	if param.MaxEventBytesPerExecute < definition.WasmVarMaxEventBytesPerExecuteMin {
		return constants.ErrForbiddenParam
	}
	if param.MaxViewReturnSize < definition.WasmVarMaxViewReturnSizeMin {
		return constants.ErrForbiddenParam
	}
	if param.QSRPerByteOfBytecode < definition.WasmVarQSRPerByteOfBytecodeMin {
		return constants.ErrForbiddenParam
	}
	if param.MinBytecodeCost < definition.WasmVarMinBytecodeCostMin {
		return constants.ErrForbiddenParam
	}
	if param.QSRPerByteOfState < definition.WasmVarQSRPerByteOfStateMin {
		return constants.ErrForbiddenParam
	}
	if param.MaxWasmBytecodeSize < definition.WasmVarMaxWasmBytecodeSizeMin {
		return constants.ErrForbiddenParam
	}
	if param.MaxChunkCount < definition.WasmVarMaxChunkCountMin {
		return constants.ErrForbiddenParam
	}
	if param.ChunkTTLMomentums < definition.WasmVarChunkTTLMomentumsMin {
		return constants.ErrForbiddenParam
	}
	// Re-pack for canonical encoding.
	var err error
	block.Data, err = definition.ABIWasm.PackMethod(p.MethodName,
		param.ExecutionGasLimit, param.OnReceiveGasLimit, param.MaxDescendantBlocks,
		param.MaxEventsPerExecute, param.MaxEventDataPerEvent, param.MaxEventBytesPerExecute,
		param.MaxViewReturnSize, param.QSRPerByteOfBytecode, param.MinBytecodeCost,
		param.QSRPerByteOfState, param.MaxWasmBytecodeSize, param.MaxChunkCount,
		param.ChunkTTLMomentums)
	return err
}

func (p *SetWasmVariablesMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	// Admin check (needs storage access — cannot be in ValidateSendBlock).
	info, err := definition.GetWasmContractInfo(context.Storage())
	if err != nil {
		return nil, err
	}
	if sendBlock.Address != info.Administrator {
		return nil, constants.ErrPermissionDenied
	}

	param := new(definition.WasmVariables)
	if err := definition.ABIWasm.UnpackMethod(param, p.MethodName, sendBlock.Data); err != nil {
		return nil, err
	}

	if err := param.Save(context.Storage()); err != nil {
		return nil, err
	}

	wasmLog.Debug("wasm variables updated",
		"executionGasLimit", param.ExecutionGasLimit,
		"onReceiveGasLimit", param.OnReceiveGasLimit,
		"maxDescendantBlocks", param.MaxDescendantBlocks,
		"maxEventsPerExecute", param.MaxEventsPerExecute,
		"maxViewReturnSize", param.MaxViewReturnSize,
		"qsrPerByteOfBytecode", param.QSRPerByteOfBytecode,
		"maxWasmBytecodeSize", param.MaxWasmBytecodeSize,
	)
	return nil, nil
}

// WasmPauseMethod — Pause(wasmAddr address) at the 0x01 factory.
// Sets a per-contract paused flag. The deployer address is stored as the
// value so the VM can check sender exemption without reading metadata.
type WasmPauseMethod struct {
	MethodName string
}

func (p *WasmPauseMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}

func (p *WasmPauseMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	params := new(struct {
		WasmAddr types.Address `abi:"wasmAddr"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}
	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}
	var err error
	block.Data, err = definition.ABIWasm.PackMethod(p.MethodName, params.WasmAddr)
	return err
}

func (p *WasmPauseMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	params := new(struct {
		WasmAddr types.Address `abi:"wasmAddr"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, sendBlock.Data); err != nil {
		return nil, constants.ErrUnpackError
	}

	if !types.IsWasmContractAddress(params.WasmAddr) {
		return nil, constants.ErrInvalidArguments
	}

	// Load metadata for deployer check.
	meta, err := definition.GetWasmContractMetadata(context.Storage(), params.WasmAddr)
	if err != nil {
		return nil, err
	}
	if meta == nil {
		return nil, constants.ErrWasmContractNotDeployed
	}

	if sendBlock.Address != meta.Deployer {
		return nil, constants.ErrPermissionDenied
	}

	// Already paused.
	paused, err := definition.IsWasmPaused(context.Storage(), params.WasmAddr)
	if err != nil {
		return nil, err
	}
	if paused {
		return nil, constants.ErrInvalidArguments
	}

	if err := definition.SetWasmPaused(context.Storage(), params.WasmAddr, sendBlock.Address); err != nil {
		return nil, err
	}

	wasmLog.Info("paused contract", "wasmAddr", params.WasmAddr, "deployer", sendBlock.Address)

	return nil, nil
}

// WasmUnpauseMethod — Unpause(wasmAddr address) at the 0x01 factory.
// Removes the per-contract paused flag.
type WasmUnpauseMethod struct {
	MethodName string
}

func (p *WasmUnpauseMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}

func (p *WasmUnpauseMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	params := new(struct {
		WasmAddr types.Address `abi:"wasmAddr"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}
	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}
	var err error
	block.Data, err = definition.ABIWasm.PackMethod(p.MethodName, params.WasmAddr)
	return err
}

func (p *WasmUnpauseMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	params := new(struct {
		WasmAddr types.Address `abi:"wasmAddr"`
	})
	if err := definition.ABIWasm.UnpackMethod(params, p.MethodName, sendBlock.Data); err != nil {
		return nil, constants.ErrUnpackError
	}

	if !types.IsWasmContractAddress(params.WasmAddr) {
		return nil, constants.ErrInvalidArguments
	}

	// Deployer check via the pause key value (deployer address stored there).
	deployer, err := definition.GetWasmPausedDeployer(context.Storage(), params.WasmAddr)
	if err != nil {
		return nil, err
	}
	if deployer == nil {
		return nil, constants.ErrInvalidArguments // not paused
	}
	if sendBlock.Address != *deployer {
		return nil, constants.ErrPermissionDenied
	}

	if err := definition.DeleteWasmPaused(context.Storage(), params.WasmAddr); err != nil {
		return nil, err
	}

	wasmLog.Info("unpaused contract", "wasmAddr", params.WasmAddr, "deployer", sendBlock.Address)

	return nil, nil
}
