package api

import (
	"testing"

	"github.com/zenon-network/go-zenon/chain"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/chain/store"
	"github.com/zenon-network/go-zenon/common/types"
)

// stubMomentumStoreAtHeight satisfies store.Momentum by embedding a nil interface and
// overriding only GetMomentumByHeight, mirroring verifier's stubMomentumStore pattern.
type stubMomentumStoreAtHeight struct {
	store.Momentum
	momentum *nom.Momentum
}

func (s *stubMomentumStoreAtHeight) GetMomentumByHeight(uint64) (*nom.Momentum, error) {
	return s.momentum, nil
}

// stubChainWithMomentumStore satisfies chain.Chain by embedding a nil interface and overriding
// only GetFrontierMomentumStore.
type stubChainWithMomentumStore struct {
	chain.Chain
	store store.Momentum
}

func (c *stubChainWithMomentumStore) GetFrontierMomentumStore() store.Momentum {
	return c.store
}

func TestStateRootIdentifierAtHeightRejectsPreActivation(t *testing.T) {
	momentum := &nom.Momentum{Height: 10, Version: nom.DynamicPlasmaMomentumVersion}
	l := &LedgerApi{
		chain: &stubChainWithMomentumStore{
			store: &stubMomentumStoreAtHeight{momentum: momentum},
		},
	}

	if _, err := l.stateRootIdentifierAtHeight(10); err != ErrStateRootNotActivated {
		t.Fatalf("stateRootIdentifierAtHeight(pre-activation) = %v, want ErrStateRootNotActivated", err)
	}
}

func TestStateRootIdentifierAtHeightAcceptsActive(t *testing.T) {
	momentum := &nom.Momentum{Height: 10, Version: nom.StateRootMomentumVersion, Hash: types.NewHash([]byte("h"))}
	l := &LedgerApi{
		chain: &stubChainWithMomentumStore{
			store: &stubMomentumStoreAtHeight{momentum: momentum},
		},
	}

	got, err := l.stateRootIdentifierAtHeight(10)
	if err != nil {
		t.Fatalf("stateRootIdentifierAtHeight(active) unexpected error %v", err)
	}
	if got != momentum.Identifier() {
		t.Fatalf("stateRootIdentifierAtHeight(active) = %v, want %v", got, momentum.Identifier())
	}
}

func TestStateRootIdentifierAtHeightRejectsZeroHeight(t *testing.T) {
	l := &LedgerApi{chain: &stubChainWithMomentumStore{}}

	if _, err := l.stateRootIdentifierAtHeight(0); err != ErrHeightParamIsZero {
		t.Fatalf("stateRootIdentifierAtHeight(0) = %v, want ErrHeightParamIsZero", err)
	}
}

func TestGetStateRootRejectsPreActivation(t *testing.T) {
	momentum := &nom.Momentum{Height: 10, Version: nom.DynamicPlasmaMomentumVersion}
	l := &LedgerApi{
		chain: &stubChainWithMomentumStore{
			store: &stubMomentumStoreAtHeight{momentum: momentum},
		},
	}

	if _, err := l.GetStateRoot(10); err != ErrStateRootNotActivated {
		t.Fatalf("GetStateRoot(pre-activation) = %v, want ErrStateRootNotActivated", err)
	}
}

func TestGetProofRejectsPreActivation(t *testing.T) {
	momentum := &nom.Momentum{Height: 10, Version: nom.DynamicPlasmaMomentumVersion}
	l := &LedgerApi{
		chain: &stubChainWithMomentumStore{
			store: &stubMomentumStoreAtHeight{momentum: momentum},
		},
	}

	if _, err := l.GetProof(10, []byte("key")); err != ErrStateRootNotActivated {
		t.Fatalf("GetProof(pre-activation) = %v, want ErrStateRootNotActivated", err)
	}
}
