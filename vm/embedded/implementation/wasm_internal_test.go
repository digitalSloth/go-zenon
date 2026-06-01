package implementation

import (
	"math/big"
	"testing"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
)

// TestWasm_ChunksExpired exercises the pure TTL predicate that gates
// Deploy(subsequent), Activate and DiscardChunks. The boundary is
// off-by-one deliberate: an upload is expired only once it is STRICTLY older
// than ChunkTTLMomentums (it survives exactly at firstChunkHeight+TTL).
func TestWasm_ChunksExpired(t *testing.T) {
	ttl := uint64(constants.ChunkTTLMomentums)

	cases := []struct {
		name        string
		current     uint64
		firstChunk  uint64
		wantExpired bool
	}{
		{"same height", 100, 100, false},
		{"one momentum after first", 101, 100, false},
		{"defensive: current before first", 50, 100, false},
		{"exactly at TTL boundary (not yet expired)", 100 + ttl, 100, false},
		{"one past TTL boundary (expired)", 100 + ttl + 1, 100, true},
		{"far past TTL (expired)", 100 + 10*ttl, 100, true},
		{"genesis zero", 0, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chunksExpired(tc.current, tc.firstChunk, ttl)
			if got != tc.wantExpired {
				t.Errorf("chunksExpired(%d, %d, %d) = %v, want %v",
					tc.current, tc.firstChunk, ttl, got, tc.wantExpired)
			}
		})
	}
}

// TestWasm_ChunksExpired_WithCustomTTL confirms chunksExpired uses the
// chunkTTLMomentums parameter, enabling different TTL values.
func TestWasm_ChunksExpired_WithCustomTTL(t *testing.T) {
	const customTTL uint64 = 5
	if chunksExpired(100+customTTL, 100, customTTL) {
		t.Error("upload at exactly firstChunk+5 must NOT be expired with TTL=5")
	}
	if !chunksExpired(100+customTTL+1, 100, customTTL) {
		t.Error("upload at firstChunk+6 MUST be expired with TTL=5")
	}
}

// TestWasm_BytecodeCost verifies the bytecode cost formula:
// max(size * QSRPerByteOfBytecode, MinBytecodeCost).
func TestWasm_BytecodeCost(t *testing.T) {
	vars := definition.DefaultWasmVariables()
	min := big.NewInt(int64(vars.MinBytecodeCost))

	cases := []struct {
		name string
		size int
		want *big.Int
	}{
		{"zero size clamps to min", 0, min},
		{"tiny size clamps to min", 56, min},
		{"size at clamp boundary", int(vars.MinBytecodeCost) / int(vars.QSRPerByteOfBytecode), min},
		{"large size uses linear cost", 250_000, big.NewInt(250_000 * int64(vars.QSRPerByteOfBytecode))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bytecodeCost(tc.size, vars)
			if got.Cmp(tc.want) != 0 {
				t.Errorf("bytecodeCost(%d) = %s, want %s", tc.size, got, tc.want)
			}
		})
	}
}

// M5 regression: Execute must reject an args blob larger than MaxArgsBytes at
// send validation, rather than letting it fail late as an out-of-bounds memory
// write inside the runtime.
func TestExecute_RejectsOversizedArgs(t *testing.T) {
	m := &ExecuteMethod{MethodName: definition.ExecuteMethodName}

	ok := definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName, "fn", make([]byte, constants.MaxArgsBytes))
	if err := m.ValidateSendBlock(&nom.AccountBlock{Data: ok}); err != nil {
		t.Fatalf("args at the cap must be accepted, got %v", err)
	}

	tooBig := definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName, "fn", make([]byte, constants.MaxArgsBytes+1))
	if err := m.ValidateSendBlock(&nom.AccountBlock{Data: tooBig}); err != constants.ErrWasmArgsTooLarge {
		t.Fatalf("oversized args: want ErrWasmArgsTooLarge, got %v", err)
	}
}
