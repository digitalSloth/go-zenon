package wasm

import (
	"bytes"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/vm/wasm/testmodules"
)

// lenPrefixed builds a [u32 little-endian length][payload] buffer, the return
// format callView reads from guest memory (§11.3).
func lenPrefixed(payload []byte) []byte {
	n := uint32(len(payload))
	buf := []byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}
	return append(buf, payload...)
}

// TestCallView_ViewExportPreferred verifies callView invokes the `view` export
// when present (not `execute`). ViewModule's `view` returns the args pointer
// while `execute` returns 0; reading back the length-prefixed args proves `view`
// ran — `execute` would have read offset 0 and returned an empty buffer.
func TestCallView_ViewExportPreferred(t *testing.T) {
	fake := newFakeBalCtx()
	wc := NewViewContext(fake, types.Address{}, definition.DefaultWasmVariables())

	payload := []byte{0x0A, 0x0B, 0x0C, 0x0D}
	data, err := GetWasmRuntime("").CallView(wc, testmodules.ViewModule(), lenPrefixed(payload))
	if err != nil {
		t.Fatalf("CallView returned error: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("view returned %x, want %x", data, payload)
	}
}

// TestCallView_ExecuteFallback verifies that, with no `view` export, callView
// falls back to `execute` (in read-only mode) and reads its length-prefixed
// buffer the same way. EchoExecuteModule's `execute` returns the args pointer.
func TestCallView_ExecuteFallback(t *testing.T) {
	fake := newFakeBalCtx()
	wc := NewViewContext(fake, types.Address{}, definition.DefaultWasmVariables())

	payload := []byte{0x11, 0x22, 0x33}
	data, err := GetWasmRuntime("").CallView(wc, testmodules.EchoExecuteModule(), lenPrefixed(payload))
	if err != nil {
		t.Fatalf("CallView returned error: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("execute fallback returned %x, want %x", data, payload)
	}
}

// TestCallView_EmptyReturn verifies a zero length prefix yields an empty,
// non-error result.
func TestCallView_EmptyReturn(t *testing.T) {
	fake := newFakeBalCtx()
	wc := NewViewContext(fake, types.Address{}, definition.DefaultWasmVariables())

	data, err := GetWasmRuntime("").CallView(wc, testmodules.EchoExecuteModule(), lenPrefixed(nil))
	if err != nil {
		t.Fatalf("CallView returned error: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("expected empty buffer, got %x", data)
	}
}

// TestCallView_ReturnTooLarge verifies the length-prefix cap is enforced: an
// oversized length header is rejected before any copy, bounding RPC memory use.
func TestCallView_ReturnTooLarge(t *testing.T) {
	fake := newFakeBalCtx()
	wc := NewViewContext(fake, types.Address{}, definition.DefaultWasmVariables())

	// Length prefix 0xFFFFFFFF, far above WasmMaxViewReturnSize.
	args := []byte{0xFF, 0xFF, 0xFF, 0xFF}
	_, err := GetWasmRuntime("").CallView(wc, testmodules.EchoExecuteModule(), args)
	if err != ErrViewReturnTooLarge {
		t.Fatalf("got error %v, want ErrViewReturnTooLarge", err)
	}
}

// TestCallView_ReadOnlyTrapsStateWrite verifies a view that attempts a state
// mutation traps: CounterModule's `execute` calls state_write, which must abort
// the call in read-only mode (§11.3).
func TestCallView_ReadOnlyTrapsStateWrite(t *testing.T) {
	fake := newFakeBalCtx()
	wc := NewViewContext(fake, types.Address{}, definition.DefaultWasmVariables())

	_, err := GetWasmRuntime("").CallView(wc, testmodules.CounterModule(), nil)
	if err == nil {
		t.Fatal("expected read-only trap from state_write, got nil error")
	}
}

// TestCallView_ReadOnlyTrapsEmitEvent verifies a view that attempts emit_event
// traps and records no event.
func TestCallView_ReadOnlyTrapsEmitEvent(t *testing.T) {
	fake := newFakeBalCtx()
	wc := NewViewContext(fake, types.Address{}, definition.DefaultWasmVariables())

	_, err := GetWasmRuntime("").CallView(wc, testmodules.EventModule(), nil)
	if err == nil {
		t.Fatal("expected read-only trap from emit_event, got nil error")
	}
	if got := len(wc.Events()); got != 0 {
		t.Fatalf("read-only view recorded %d events, want 0", got)
	}
}
