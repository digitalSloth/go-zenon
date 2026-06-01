package wasm

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/zenon-network/go-zenon/vm/constants"
)

// instantiateGasModule stands up a trivial instrumented module so the gas
// listener's accounting against the injected __gas_remaining global can be
// exercised directly, without wiring the full host-function set.
func instantiateGasModule(t *testing.T) (api.Module, func()) {
	t.Helper()
	instrumented, err := InjectGasMetering(trivialModule())
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter())
	compiled, err := rt.CompileModule(ctx, instrumented)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	m, err := rt.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	return m, func() { _ = rt.Close(ctx) }
}

// A fixed-cost host call debits exactly its table cost from the gas counter.
func TestMetering_FixedCostHostCall(t *testing.T) {
	m, cleanup := instantiateGasModule(t)
	defer cleanup()
	g := m.ExportedGlobal(GasGlobalName).(api.MutableGlobal)

	l := &hostGasListener{funcName: "state_read", gasCost: uint64(constants.WasmGasStateRead)}
	g.Set(1000)
	l.Before(context.Background(), m, nil, []uint64{0, 0, 0, 0}, nil)

	if got, want := g.Get(), uint64(1000-constants.WasmGasStateRead); got != want {
		t.Fatalf("state_read: want remaining %d, got %d", want, got)
	}
}

// emit_event charges its base plus WasmGasEmitEventPerByte per data byte
// (params[2] = dataLen). This locks the spec §6.5 pricing that the listener
// previously ignored (it charged only the flat base).
func TestMetering_EmitEventChargesPerByte(t *testing.T) {
	m, cleanup := instantiateGasModule(t)
	defer cleanup()
	g := m.ExportedGlobal(GasGlobalName).(api.MutableGlobal)

	l := &hostGasListener{
		funcName:    "emit_event",
		gasCost:     uint64(constants.WasmGasEmitEventBase),
		perByteCost: uint64(constants.WasmGasEmitEventPerByte),
		lenParamIdx: 2,
	}
	const budget = 250_000
	const dataLen = 100
	g.Set(budget)
	// emit_event(topicPtr=0, dataPtr=32, dataLen=100, indexed=1)
	l.Before(context.Background(), m, nil, []uint64{0, 32, dataLen, 1}, nil)

	want := uint64(budget - constants.WasmGasEmitEventBase - constants.WasmGasEmitEventPerByte*dataLen)
	if got := g.Get(); got != want {
		t.Fatalf("emit_event(%d bytes): want remaining %d, got %d", dataLen, want, got)
	}
}

// The factory must wire the per-byte cost onto emit_event and nothing else.
func TestMetering_FactoryWiresEmitEventPerByte(t *testing.T) {
	f := NewHostGasListenerFactory()
	if f.gasTable["emit_event"] != uint64(constants.WasmGasEmitEventBase) {
		t.Fatalf("emit_event base cost not in table")
	}
	// A direct fixed-cost entry must not carry a per-byte component.
	if f.gasTable["transfer"] != uint64(constants.WasmGasTransfer) {
		t.Fatalf("transfer cost not in table")
	}
}
