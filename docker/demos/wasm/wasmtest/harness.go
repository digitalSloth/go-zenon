package wasmtest

import (
	"encoding/binary"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/vm/wasm"
)

// Result holds the outcome of an Execute call.
type Result struct {
	Code      int32
	Events    []wasm.WasmEvent
	Transfers []*nom.AccountBlock
	GasUsed   uint64
	Err       error
}

// Execute runs a contract's execute entry point against the given context.
func Execute(ctx *Context, bytecode []byte, args []byte) Result {
	vars := definition.DefaultWasmVariables()
	wc := wasm.NewWasmContext(
		ctx,
		&nom.AccountBlock{Address: ctx.sender()},
		ctx.contractAddr(),
		vars,
	)

	code, err := wasm.GetWasmRuntime("").Execute(wc, bytecode, args)
	gasUsed := vars.ExecutionGasLimit - wc.GetRemainingGas()

	return Result{
		Code:      code,
		Events:    wc.Events(),
		Transfers: wc.Transfers(),
		GasUsed:   gasUsed,
		Err:       err,
	}
}

// View runs a contract's view entry point in read-only mode.
func View(ctx *Context, bytecode []byte, args []byte) ([]byte, error) {
	vars := definition.DefaultWasmVariables()
	wc := wasm.NewViewContext(ctx, ctx.contractAddr(), vars)
	return wasm.GetWasmRuntime("").CallView(wc, bytecode, args)
}

// Validate checks whether bytecode passes the validator.
func Validate(bytecode []byte) error {
	return wasm.ValidateBytecode(bytecode)
}

// DecodeViewU64 decodes a little-endian u64 from a view return. The runtime's
// CallView already strips the contract's [u32 len] envelope and returns only
// the inner bytes, so `data` is the raw 8-byte LE value.
func DecodeViewU64(data []byte) (uint64, bool) {
	if len(data) < 8 {
		return 0, false
	}
	return binary.LittleEndian.Uint64(data[:8]), true
}

// --- Context accessors used by the harness ---

func (c *Context) sender() types.Address {
	return types.Address{}
}

func (c *Context) contractAddr() types.Address {
	return types.Address{2}
}
