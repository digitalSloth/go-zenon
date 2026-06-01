package wasm

import (
	"context"

	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"

	"github.com/zenon-network/go-zenon/vm/constants"
)

// HostGasListenerFactory creates FunctionListeners that meter host function gas costs.
type HostGasListenerFactory struct {
	gasTable map[string]uint64
}

// NewHostGasListenerFactory creates a factory with gas costs from constants.
func NewHostGasListenerFactory() *HostGasListenerFactory {
	return &HostGasListenerFactory{
		gasTable: map[string]uint64{
			"state_read":        uint64(constants.WasmGasStateRead),
			"state_write":       uint64(constants.WasmGasStateWrite),
			"state_delete":      uint64(constants.WasmGasStateDelete),
			"state_has":         uint64(constants.WasmGasStateHas),
			"balance_get":       uint64(constants.WasmGasBalanceGet),
			"transfer":          uint64(constants.WasmGasTransfer),
			"get_height":        uint64(constants.WasmGasGetHeight),
			"get_timestamp":     uint64(constants.WasmGasGetTimestamp),
			"get_prev_hash":     uint64(constants.WasmGasGetPrevHash),
			"get_caller":        uint64(constants.WasmGasGetCaller),
			"get_call_token":    uint64(constants.WasmGasGetCallToken),
			"get_call_amount":   uint64(constants.WasmGasGetCallAmount),
			"get_address":       uint64(constants.WasmGasGetAddress),
			"get_block_hash":    uint64(constants.WasmGasGetBlockHash),
			"get_remaining_gas": uint64(constants.WasmGasGetRemainingGas),
			"emit_event":        uint64(constants.WasmGasEmitEventBase),
			"abort":             0,
			"log_msg":           0,
		},
	}
}

// NewFunctionListener implements experimental.FunctionListenerFactory.
func (f *HostGasListenerFactory) NewFunctionListener(def api.FunctionDefinition) experimental.FunctionListener {
	name := def.Name()
	l := &hostGasListener{
		funcName: name,
		gasCost:  f.gasTable[name],
	}
	// emit_event(topicPtr, dataPtr, dataLen, indexed) additionally costs
	// WasmGasEmitEventPerByte per data byte (spec §6.5). dataLen is params[2].
	if name == "emit_event" {
		l.perByteCost = uint64(constants.WasmGasEmitEventPerByte)
		l.lenParamIdx = 2
	}
	return l
}

type hostGasListener struct {
	funcName    string
	gasCost     uint64
	perByteCost uint64 // extra gas per byte of a length-bearing param; 0 if none
	lenParamIdx int    // index of the length param (only meaningful when perByteCost != 0)
}

// Before charges a host function's gas against the injected $__gas_remaining
// global — the same counter the per-opcode metering debits, so host-call and
// opcode gas share one budget (spec §6.3). The wazero interpreter passes the
// calling guest module as mod, so the guest's exported global is reachable here.
//
// A listener cannot trap, so on insufficient gas it clamps the counter to zero;
// the next injected basic-block charge then traps via $__consume_gas. The
// overspend is bounded by a single host-call cost and is deterministic.
func (l *hostGasListener) Before(ctx context.Context, mod api.Module, def api.FunctionDefinition, params []uint64, stackIterator experimental.StackIterator) {
	cost := l.gasCost
	// Add the per-byte component for length-bearing host calls (emit_event). The
	// length param is a WASM i32 (<= ~4.3e9), so perByteCost*len cannot overflow
	// uint64. The charge is on the requested length and is deterministic across
	// nodes, regardless of whether the host call later succeeds.
	if l.perByteCost != 0 && l.lenParamIdx < len(params) {
		cost += l.perByteCost * params[l.lenParamIdx]
	}
	if cost == 0 {
		return
	}

	g, ok := mod.ExportedGlobal(GasGlobalName).(api.MutableGlobal)
	if !ok {
		return
	}

	remaining := g.Get()
	if remaining < cost {
		g.Set(0)
		return
	}
	g.Set(remaining - cost)
}

// After is called after a successful host function return.
func (l *hostGasListener) After(ctx context.Context, mod api.Module, def api.FunctionDefinition, results []uint64) {
}

// Abort is called when a host function traps or panics.
func (l *hostGasListener) Abort(ctx context.Context, mod api.Module, def api.FunctionDefinition, err error) {
}

// WithFunctionListenerFactory adds the gas listener factory to a context.
func WithFunctionListenerFactory(ctx context.Context, factory *HostGasListenerFactory) context.Context {
	return experimental.WithFunctionListenerFactory(ctx, factory)
}
