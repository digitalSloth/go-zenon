package verifier

import (
	"testing"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/chain/store"
	"github.com/zenon-network/go-zenon/common/types"
)

// fakeMomentumStore embeds store.Momentum (a nil interface) and overrides only the
// methods version() is allowed to touch. Any other method call nil-panics, which is
// intentional: it keeps the fake minimal AND pins version() to the enforced spork
// helper. The IsSporkActive override fails the test loudly so that a regression to
// the raw momentumStore.IsSporkActive(types.WasmRuntimeSpork) — which would silently
// drop the WasmRuntime → DynamicPlasma dependency and let producer/verifier drift —
// is caught immediately rather than masked by the re-execution backstop.
type fakeMomentumStore struct {
	store.Momentum
	t            *testing.T
	wasmEnforced bool
}

func (f *fakeMomentumStore) IsWasmRuntimeSporkEnforced() (bool, error) {
	return f.wasmEnforced, nil
}

func (f *fakeMomentumStore) IsSporkActive(*types.ImplementedSpork) (bool, error) {
	f.t.Fatalf("version() must gate v3 on IsWasmRuntimeSporkEnforced(), not the raw " +
		"IsSporkActive() — the latter drops the DynamicPlasma dependency")
	return false, nil
}

// TestAccountBlockVerifier_Version covers the full version() matrix, including the
// spork gate, its DynamicPlasma dependency, and events-on-sub-v3 rejection.
func TestAccountBlockVerifier_Version(t *testing.T) {
	oneEvent := []nom.AccountBlockEvent{{
		ContractAddress: types.Address{2, 1},
		Topic:           types.Hash{1},
		Data:            []byte{0xAB},
	}}

	cases := []struct {
		name         string
		version      uint64
		blockType    uint64
		events       []nom.AccountBlockEvent
		wasmEnforced bool
		wantErr      error
	}{
		{"v0 missing", 0, nom.BlockTypeUserSend, nil, false, ErrABVersionMissing},
		{"v1 user-send ok", 1, nom.BlockTypeUserSend, nil, false, nil},
		{"v1 contract-receive ok", 1, nom.BlockTypeContractReceive, nil, false, nil},
		{"v1 carrying events rejected", 1, nom.BlockTypeUserSend, oneEvent, false, ErrABVersionInvalid},
		{"v3 enforced contract-receive ok", nom.WasmAccountBlockVersion, nom.BlockTypeContractReceive, nil, true, nil},
		{"v3 enforced contract-receive with events ok", nom.WasmAccountBlockVersion, nom.BlockTypeContractReceive, oneEvent, true, nil},
		{"v3 enforced but user-send rejected", nom.WasmAccountBlockVersion, nom.BlockTypeUserSend, nil, true, ErrABVersionInvalid},
		{"v3 NOT enforced rejected (DynamicPlasma dependency)", nom.WasmAccountBlockVersion, nom.BlockTypeContractReceive, nil, false, ErrABVersionInvalid},
		{"v2 rejected", 2, nom.BlockTypeContractReceive, nil, true, ErrABVersionInvalid},
		{"v4 rejected", 4, nom.BlockTypeContractReceive, nil, true, ErrABVersionInvalid},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			abv := &accountBlockVerifier{
				block: &nom.AccountBlock{
					Version:   tc.version,
					BlockType: tc.blockType,
					Events:    tc.events,
				},
				momentumStore: &fakeMomentumStore{t: t, wasmEnforced: tc.wasmEnforced},
			}
			if err := abv.version(); err != tc.wantErr {
				t.Fatalf("version() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
