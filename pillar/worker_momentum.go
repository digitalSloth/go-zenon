package pillar

import (
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/consensus"
	"github.com/zenon-network/go-zenon/dp"
)

func (w *worker) generateMomentum(e consensus.ProducerEvent) (*nom.MomentumTransaction, error) {
	insert := w.chain.AcquireInsert("momentum-generator")
	defer insert.Unlock()

	store := w.chain.GetFrontierMomentumStore()
	previousMomentum, err := store.GetFrontierMomentum()
	if err != nil {
		return nil, err
	}

	isDynamicPlasmaActive, err := store.IsSporkActive(types.DynamicPlasmaSpork)
	if err != nil {
		return nil, err
	}

	var (
		m      *nom.Momentum
		blocks []*nom.AccountBlock
	)

	if isDynamicPlasmaActive {
		config, err := store.GetPlasmaVariables()
		if err != nil {
			return nil, err
		}
		plasma := dp.NewDynamicPlasma(previousMomentum, config)
		blocks = NewMomentumContentSelector(plasma, w.priorityAddresses).Content(w.chain.GetAllUncommittedAccountBlocks())
		basePlasma := plasma.ComputeTotalBasePlasma(blocks)
		m = &nom.Momentum{
			ChainIdentifier: w.chain.ChainIdentifier(),
			PreviousHash:    previousMomentum.Hash,
			Height:          previousMomentum.Height + 1,
			TimestampUnix:   uint64(e.StartTime.Unix()),
			Content:         nom.NewMomentumContent(blocks),
			Version:         nom.DynamicPlasmaMomentumVersion,
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
	return w.supervisor.GenerateMomentum(&nom.DetailedMomentum{
		Momentum:      m,
		AccountBlocks: blocks,
	}, w.coinbase.Signer)
}
