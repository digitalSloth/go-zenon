package wasm

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func TestOpcodeGas_Basic(t *testing.T) {
	// local.get should cost 1
	if g := opcodeGas(0x20); g != gasBasic {
		t.Fatalf("local.get: expected %d, got %d", gasBasic, g)
	}
}

func TestOpcodeGas_Arithmetic(t *testing.T) {
	// i32.add should cost 3
	if g := opcodeGas(0x6A); g != gasArithmetic {
		t.Fatalf("i32.add: expected %d, got %d", gasArithmetic, g)
	}
}

func TestOpcodeGas_Division(t *testing.T) {
	// i32.div_s should cost 10
	if g := opcodeGas(0x6D); g != gasDivision {
		t.Fatalf("i32.div_s: expected %d, got %d", gasDivision, g)
	}
}

func TestOpcodeGas_Call(t *testing.T) {
	if g := opcodeGas(0x10); g != gasCall {
		t.Fatalf("call: expected %d, got %d", gasCall, g)
	}
}

func TestOpcodeGas_CallIndirect(t *testing.T) {
	if g := opcodeGas(0x11); g != gasCallIndirect {
		t.Fatalf("call_indirect: expected %d, got %d", gasCallIndirect, g)
	}
}

func TestOpcodeGas_MemoryLoad(t *testing.T) {
	if g := opcodeGas(0x28); g != gasLoad {
		t.Fatalf("i32.load: expected %d, got %d", gasLoad, g)
	}
}

func TestOpcodeGas_MemoryStore(t *testing.T) {
	// 0x38 = i32.store
	if g := opcodeGas(0x38); g != gasStore {
		t.Fatalf("i32.store: expected %d, got %d", gasStore, g)
	}
}

func TestOpcodeGas_Control(t *testing.T) {
	if g := opcodeGas(0x02); g != gasControl {
		t.Fatalf("block: expected %d, got %d", gasControl, g)
	}
}

func TestEncodeULEB128(t *testing.T) {
	tests := []struct {
		input    uint64
		expected []byte
	}{
		{0, []byte{0}},
		{1, []byte{1}},
		{127, []byte{127}},
		{128, []byte{0x80, 0x01}},
		{624485, []byte{0xE5, 0x8E, 0x26}},
	}
	for _, tt := range tests {
		result := encodeULEB128(tt.input)
		if !bytes.Equal(result, tt.expected) {
			t.Fatalf("ULEB128(%d): expected %v, got %v", tt.input, tt.expected, result)
		}
	}
}

func TestEncodeSLEB128(t *testing.T) {
	tests := []struct {
		input    int64
		expected []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{63, []byte{0x3F}},
		// 64 has bit 6 set, so a positive value needs a trailing 0x00 to avoid
		// being read as the negative -64 (0x40).
		{64, []byte{0xC0, 0x00}},
		{127, []byte{0xFF, 0x00}},
		{128, []byte{0x80, 0x01}},
		{-1, []byte{0x7F}},
		{-64, []byte{0x40}},
	}
	for _, tt := range tests {
		result := encodeSLEB128(tt.input)
		if !bytes.Equal(result, tt.expected) {
			t.Fatalf("SLEB128(%d): expected %v, got %v", tt.input, tt.expected, result)
		}
	}
}

// --- module fixtures ---

// wasmSection wraps a payload as id + ULEB128(size) + payload.
func wasmSection(id byte, payload []byte) []byte {
	out := []byte{id}
	out = append(out, encodeULEB128(uint64(len(payload)))...)
	return append(out, payload...)
}

// buildExecuteModule assembles a minimal valid module exporting memory and an
// execute(i32,i32)->i32 whose body (locals vec ... terminal end) is `body`.
func buildExecuteModule(body []byte) []byte {
	var m []byte
	m = append(m, 0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00) // magic + version

	// type 0: (i32, i32) -> i32
	m = append(m, wasmSection(sectionType, []byte{0x01, 0x60, 0x02, 0x7F, 0x7F, 0x01, 0x7F})...)
	// function 0 uses type 0
	m = append(m, wasmSection(sectionFunction, []byte{0x01, 0x00})...)
	// one memory, min 1 page
	m = append(m, wasmSection(sectionMemory, []byte{0x01, 0x00, 0x01})...)

	// exports: execute (func 0) and memory (mem 0)
	exp := []byte{0x02}
	exp = append(exp, 0x07)
	exp = append(exp, []byte("execute")...)
	exp = append(exp, exportFunc, 0x00)
	exp = append(exp, 0x06)
	exp = append(exp, []byte("memory")...)
	exp = append(exp, exportMem, 0x00)
	m = append(m, wasmSection(sectionExport, exp)...)

	// code: one body
	code := []byte{0x01}
	code = append(code, encodeULEB128(uint64(len(body)))...)
	code = append(code, body...)
	m = append(m, wasmSection(sectionCode, code)...)

	return m
}

// trivialModule: execute returns 0. Body = (no locals) i32.const 0; end.
func trivialModule() []byte {
	return buildExecuteModule([]byte{0x00, 0x41, 0x00, 0x0B})
}

// infiniteLoopModule: execute loops forever (loop; br 0; end), then a dead
// i32.const 0 to satisfy the i32 result type.
func infiniteLoopModule() []byte {
	return buildExecuteModule([]byte{
		0x00,       // no locals
		0x03, 0x40, // loop (empty type)
		0x0C, 0x00, // br 0
		0x0B,       // end (loop)
		0x41, 0x00, // i32.const 0  (unreachable)
		0x0B, // end (function)
	})
}

func TestInjectGasMetering_RejectsTooSmall(t *testing.T) {
	if _, err := InjectGasMetering([]byte{0x00, 0x61}); err == nil {
		t.Fatal("expected error for sub-header input")
	}
}

// The transform must be a pure function of the input bytes: every node must
// produce identical instrumented bytecode or consensus forks.
func TestInjectGasMetering_Deterministic(t *testing.T) {
	mod := trivialModule()
	first, err := InjectGasMetering(mod)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := InjectGasMetering(mod)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("injection not deterministic on run %d", i)
		}
	}
	if bytes.Equal(first, mod) {
		t.Fatal("expected instrumented output to differ from input")
	}
}

// compileAndRun instruments, compiles with a bare interpreter runtime, seeds the
// gas budget, and calls execute. Returns the result, remaining gas, and the
// call error. The fixtures import no host functions, so no env module is needed.
func compileAndRun(t *testing.T, mod []byte, budget uint64) (int32, uint64, error) {
	t.Helper()
	instrumented, err := InjectGasMetering(mod)
	if err != nil {
		t.Fatalf("inject: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter())
	defer rt.Close(ctx)

	compiled, err := rt.CompileModule(ctx, instrumented)
	if err != nil {
		t.Fatalf("wazero rejected instrumented module: %v", err)
	}
	m, err := rt.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	defer m.Close(ctx)

	g, ok := m.ExportedGlobal(GasGlobalName).(api.MutableGlobal)
	if !ok {
		t.Fatal("instrumented module is missing the mutable gas global")
	}
	g.Set(budget)

	results, callErr := m.ExportedFunction("execute").Call(ctx, 0, 0)
	remaining := g.Get()
	var res int32
	if callErr == nil {
		res = int32(results[0])
	}
	return res, remaining, callErr
}

// The instrumented trivial module must compile under wazero, run, and charge
// exactly the summed opcode weight of its single basic block (i32.const + end =
// 1 + 1 = 2). Locking the exact figure pins the gas schedule.
func TestInjectGasMetering_Executes(t *testing.T) {
	res, remaining, err := compileAndRun(t, trivialModule(), 250_000)
	if err != nil {
		t.Fatalf("execute trapped unexpectedly: %v", err)
	}
	if res != 0 {
		t.Fatalf("expected result 0, got %d", res)
	}
	if remaining != 250_000-2 {
		t.Fatalf("expected remaining 249998 (charged 2), got %d", remaining)
	}
}

// An unbounded loop must be stopped by gas, not run forever: $__consume_gas
// traps once the counter would underflow, having zeroed it first.
func TestInjectGasMetering_BoundsInfiniteLoop(t *testing.T) {
	_, remaining, err := compileAndRun(t, infiniteLoopModule(), 250_000)
	if err == nil {
		t.Fatal("expected out-of-gas trap, got clean return")
	}
	if remaining != 0 {
		t.Fatalf("expected remaining 0 after out-of-gas trap, got %d", remaining)
	}
}

// Same input, same gas outcome — the property ChangesHash determinism rests on.
func TestInjectGasMetering_DeterministicGasUse(t *testing.T) {
	_, r1, e1 := compileAndRun(t, trivialModule(), 250_000)
	_, r2, e2 := compileAndRun(t, trivialModule(), 250_000)
	if e1 != nil || e2 != nil {
		t.Fatalf("unexpected errors: %v / %v", e1, e2)
	}
	if r1 != r2 {
		t.Fatalf("gas use not deterministic across runs: %d vs %d", r1, r2)
	}
}
