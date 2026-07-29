package types

var (
	AcceleratorSpork        = NewImplementedSpork("6d2b1e6cb4025f2f45533f0fe22e9b7ce2014d91cc960471045fa64eee5a6ba3")
	HtlcSpork               = NewImplementedSpork("ceb7e3808ef17ea910adda2f3ab547be4cdfb54de8400ce3683258d06be1354b")
	BridgeAndLiquiditySpork = NewImplementedSpork("ddd43466769461c5b5d109c639da0f50a7eeb96ad6e7274b1928a35c431d7b1b")

	// PLACEHOLDER HASH. Sporks are identified by the hash of their
	// CreateSpork transaction, which is deterministic from the tx
	// contents and therefore not known until governance broadcasts the
	// proposal. The release flow is:
	//
	//   1. Ship this binary with the placeholder. No on-chain spork can
	//      match it, so IsSporkActive always returns false. Safe-by-default.
	//   2. Governance broadcasts the CreateSpork tx. The resulting
	//      send-block hash is the real SporkId.
	//   3. Replace the placeholder with that hash, release a new binary,
	//      coordinate the operator upgrade campaign.
	//   4. Governance broadcasts ActivateSpork. After
	//      SporkMinHeightDelay momentums, EnforcementHeight passes and
	//      every node activates the spork.
	//
	// Tests and devnet override ImplementedSporksMap at runtime with
	// the locally-generated hash (see vm/embedded/tests/spork_test.go
	// for the existing pattern).

	// Libp2pSpork gates the activation of the libp2p networking stack.
    // Until this spork's EnforcementHeight is reached on a node's local
    // chain, the legacy (devp2p/RLPX) backend is in use; at activation,
    // every node atomically switches to the libp2p backend.
	Libp2pSpork = NewImplementedSpork("0000000000000000000000000000000000000000000000000000000000000001")

	// DynamicPlasmaSpork gates the activation of the dynamic plasma pricing
    // model. Until this spork's EnforcementHeight is reached, the legacy
    // fixed-price plasma model is in use; at activation, momentums switch
    // to version 2 with adaptive fusion/work prices.
	DynamicPlasmaSpork = NewImplementedSpork("0000000000000000000000000000000000000000000000000000000000000002")

	// StateRootSpork gates the activation of the Merkleized state commitment.
	// Until this spork's EnforcementHeight is reached, momentums stay on version 2
	// and carry an empty StateRoot; at activation, every node switches to version 3
	// momentums whose header records the 32-byte state root, enforced against the
	// node's locally-built state tree.
	//
	// Its enforcement height MUST be >= DynamicPlasmaSpork's (version 3 implies the
	// version 2 rules).
	//
	// PLACEHOLDER HASH. Sporks are identified by the hash of their CreateSpork
	// transaction, which is deterministic from the tx contents and therefore not
	// known until governance broadcasts the proposal. The release flow is:
	//
	//   1. Ship this binary with the placeholder. No on-chain spork can match it, so
	//      IsSporkActive always returns false and the network stays on version 2.
	//      Safe-by-default.
	//   2. Governance broadcasts the CreateSpork tx. The resulting send-block hash is
	//      the real SporkId.
	//   3. Replace the placeholder with that hash, release a new binary, coordinate
	//      the operator upgrade campaign (each upgrading node builds its state tree).
	//   4. Governance broadcasts ActivateSpork. After SporkMinHeightDelay momentums,
	//      EnforcementHeight passes and every node enforces the state-root rules.
	//
	// Tests and devnet override ImplementedSporksMap at runtime with the
	// locally-generated hash (see vm/embedded/tests/dp_test.go for the existing pattern).
	StateRootSpork = NewImplementedSpork("0000000000000000000000000000000000000000000000000000000000000003")

	ImplementedSporksMap = map[Hash]bool{
		AcceleratorSpork.SporkId:        true,
		HtlcSpork.SporkId:               true,
		BridgeAndLiquiditySpork.SporkId: true,
		Libp2pSpork.SporkId:             true,
		DynamicPlasmaSpork.SporkId:      true,
		StateRootSpork.SporkId:          true,
	}
)

type ImplementedSpork struct {
	SporkId Hash
}

func NewImplementedSpork(SporkIdStr string) *ImplementedSpork {
	return &ImplementedSpork{
		SporkId: HexToHashPanic(SporkIdStr),
	}
}
