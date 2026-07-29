package pillar

import (
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/consensus"
	"github.com/zenon-network/go-zenon/dp"
)

func (w *worker) generateMomentum(e consensus.ProducerEvent) (*nom.MomentumTransaction, *nom.DetailedMomentum, error) {
	insert := w.chain.AcquireInsert("momentum-generator")
	defer insert.Unlock()

	store := w.chain.GetFrontierMomentumStore()
	previousMomentum, err := store.GetFrontierMomentum()
	if err != nil {
		return nil, nil, err
	}

	isDynamicPlasmaActive, err := store.IsSporkActive(types.DynamicPlasmaSpork)
	if err != nil {
		return nil, nil, err
	}
	isStateRootActive, err := store.IsSporkActive(types.StateRootSpork)
	if err != nil {
		return nil, nil, err
	}
	// A not-ready state tree must behave exactly like an un-upgraded node: no v3 stamping, no
	// v3 enforcement. momentumVersion below only ever picks version 3 when isStateRootActive is
	// true, so clearing it here also suppresses packMomentum's state-root stamping (gated on
	// momentum.Version >= nom.StateRootMomentumVersion).
	isStateRootActive = stateRootProducible(isStateRootActive, w.chain.StateTreeReady())

	var (
		m      *nom.Momentum
		blocks []*nom.AccountBlock
	)

	if isDynamicPlasmaActive || isStateRootActive {
		config, err := store.GetPlasmaVariables()
		if err != nil {
			return nil, nil, err
		}
		plasma := dp.NewDynamicPlasma(previousMomentum, config)
		blocks = NewMomentumContentSelector(plasma).Content(w.chain.GetAllUncommittedAccountBlocks())
		basePlasma := plasma.ComputeTotalBasePlasma(blocks)
		// version 3 implies the version 2 (dynamic plasma) rules; the StateRoot itself is set
		// in packMomentum once the changes are known.
		version := momentumVersion(isDynamicPlasmaActive, isStateRootActive)
		m = &nom.Momentum{
			ChainIdentifier: w.chain.ChainIdentifier(),
			PreviousHash:    previousMomentum.Hash,
			Height:          previousMomentum.Height + 1,
			TimestampUnix:   uint64(e.StartTime.Unix()),
			Content:         nom.NewMomentumContent(blocks),
			Version:         version,
			NextFusionPrice: plasma.NextFusionPrice(basePlasma.Fusion),
			NextWorkPrice:   plasma.NextWorkPrice(basePlasma.Pow),
		}
	} else {
		blocks = w.chain.GetNewMomentumContent()
		m = &nom.Momentum{
			ChainIdentifier: w.chain.ChainIdentifier(),
			PreviousHash:    previousMomentum.Hash,
			Height:          previousMomentum.Height + 1,
			TimestampUnix:   uint64(e.StartTime.Unix()),
			Content:         nom.NewMomentumContent(blocks),
			Version:         uint64(1),
		}
	}
	m.EnsureCache()
	detailed := &nom.DetailedMomentum{
		Momentum:      m,
		AccountBlocks: blocks,
	}
	transaction, err := w.supervisor.GenerateMomentum(detailed, w.coinbase.Signer)
	return transaction, detailed, err
}

// stateRootProducible reports whether this producer may stamp/enforce v3: the spork must be
// active AND this node's own state tree must be ready. A not-ready node treats the spork as
// inactive, producing exactly as an un-upgraded node would.
func stateRootProducible(isStateRootActive, stateTreeReady bool) bool {
	return isStateRootActive && stateTreeReady
}

// momentumVersion mirrors verifier.rawMomentumVerifier.version: state root wins, then dynamic
// plasma, else legacy.
func momentumVersion(isDynamicPlasmaActive, isStateRootActive bool) uint64 {
	switch {
	case isStateRootActive:
		return nom.StateRootMomentumVersion
	case isDynamicPlasmaActive:
		return nom.DynamicPlasmaMomentumVersion
	default:
		return uint64(1)
	}
}
