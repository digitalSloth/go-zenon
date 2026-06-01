package wasmclient

import (
	"encoding/hex"
	"math/big"
	"strings"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc/server"
	"github.com/zenon-network/go-zenon/wallet"
)

// Client wraps the node's JSON-RPC client with typed WASM methods.
type Client struct {
	rpc *server.Client
	url string
}

// Dial connects to a node's JSON-RPC endpoint.
func Dial(url string) (*Client, error) {
	rpc, err := server.Dial(url)
	if err != nil {
		return nil, err
	}
	return &Client{rpc: rpc, url: url}, nil
}

// Close shuts down the underlying RPC connection.
func (c *Client) Close() {
	c.rpc.Close()
}

// Call forwards a raw JSON-RPC call.
func (c *Client) Call(result interface{}, method string, args ...interface{}) error {
	return c.rpc.Call(result, method, args...)
}

// --- Ledger queries ---

type accountBlockDTO struct {
	Height uint64     `json:"height"`
	Hash   types.Hash `json:"hash"`
}

type momentumDTO struct {
	Hash         types.Hash `json:"hash"`
	Height       uint64     `json:"height"`
	TimestampUnix uint64   `json:"timestamp"`
}

func (c *Client) GetFrontierAccountBlock(addr types.Address) (*accountBlockDTO, error) {
	var result *accountBlockDTO
	err := c.rpc.Call(&result, "ledger.getFrontierAccountBlock", addr.String())
	return result, err
}

func (c *Client) GetFrontierMomentum() (*momentumDTO, error) {
	var result *momentumDTO
	err := c.rpc.Call(&result, "ledger.getFrontierMomentum")
	return result, err
}

// --- WASM queries ---

type WasmContractResponse struct {
	Address         string `json:"address"`
	Deployer        string `json:"deployer"`
	Version         uint32 `json:"version"`
	Upgradeable     bool   `json:"upgradeable"`
	BytecodeHash    string `json:"bytecodeHash"`
	BytecodeCost    string `json:"bytecodeCost"`
	Halted          bool   `json:"halted"`
	Administrator   string `json:"administrator"`
	Deployed        bool   `json:"deployed"`
}

type WasmHaltStatusResponse struct {
	Halted        bool   `json:"halted"`
	Administrator string `json:"administrator"`
}

type WasmStatsResponse struct {
	PendingReceiveBacklog int `json:"pendingReceiveBacklog"`
}

func (c *Client) GetContract(addr types.Address) (*WasmContractResponse, error) {
	var result *WasmContractResponse
	err := c.rpc.Call(&result, "embedded.wasm.getContract", addr.String())
	return result, err
}

func (c *Client) GetHaltStatus(addr types.Address) (*WasmHaltStatusResponse, error) {
	var result *WasmHaltStatusResponse
	err := c.rpc.Call(&result, "embedded.wasm.getHaltStatus", addr.String())
	return result, err
}

func (c *Client) GetStats() (*WasmStatsResponse, error) {
	var result *WasmStatsResponse
	err := c.rpc.Call(&result, "embedded.wasm.getStats")
	return result, err
}

type callViewResult struct {
	Data string `json:"data"` // 0x-prefixed hex (see embedded.WasmApi.callViewResult)
}

func (c *Client) CallView(addr types.Address, function string, args []byte) ([]byte, error) {
	var result callViewResult
	err := c.rpc.Call(&result, "embedded.wasm.callView", addr.String(), function, hex.EncodeToString(args))
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(strings.TrimPrefix(result.Data, "0x"))
}

// --- Plasma ---

func (c *Client) FusePlasma(kp *wallet.KeyPair, amount *big.Int) error {
	fuseData := PackFuse(kp.Address)
	// Use PoW for the fuse transaction since the account has no fused plasma yet.
	// Fuse is an embedded contract method costing EmbeddedSimplePlasma = 52,500.
	// PoWDifficultyPerPlasma = 1500, so difficulty = 52500 * 1500 = 78,750,000.
	difficulty := new(big.Int).SetInt64(int64(EmbeddedSimplePlasma * PoWDifficultyPerPlasma))
	block, err := BuildBlockWithPoW(c, kp, types.PlasmaContract, amount, types.QsrTokenStandard, fuseData, difficulty)
	if err != nil {
		return err
	}
	return PublishBlock(c, block)
}
