package tests

import (
	"math/big"
	"testing"

	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/trie"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc/api"
	"github.com/zenon-network/go-zenon/rpc/api/embedded"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/zenon/mock"
)

// activateStateRoot activates both the DynamicPlasma and StateRoot sporks (version 3 implies
// the version 2 rules) and advances past their enforcement height.
func activateStateRoot(t *testing.T, z mock.MockZenon) {
	saveSporkState(t)
	sporkAPI := embedded.NewSporkApi(z)

	create := func(name string) {
		z.InsertSendBlock(&nom.AccountBlock{
			Address:   g.Spork.Address,
			ToAddress: types.SporkContract,
			Data:      definition.ABISpork.PackMethodPanic(definition.SporkCreateMethodName, name, name),
		}, nil, mock.SkipVmChanges)
		z.InsertNewMomentum()
	}
	create("dynamic-plasma")
	create("state-root")

	list, err := sporkAPI.GetAll(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var dpId, srId types.Hash
	for _, s := range list.List {
		switch s.Name {
		case "dynamic-plasma":
			dpId = s.Id
		case "state-root":
			srId = s.Id
		}
	}

	activate := func(id types.Hash) {
		z.InsertSendBlock(&nom.AccountBlock{
			Address:   g.Spork.Address,
			ToAddress: types.SporkContract,
			Data:      definition.ABISpork.PackMethodPanic(definition.SporkActivateMethodName, id),
		}, nil, mock.SkipVmChanges)
		z.InsertNewMomentum()
	}
	activate(dpId)
	activate(srId)

	types.DynamicPlasmaSpork.SporkId = dpId
	types.StateRootSpork.SporkId = srId
	types.ImplementedSporksMap[dpId] = true
	types.ImplementedSporksMap[srId] = true

	z.InsertMomentumsTo(30)
}

// TestStateRoot_Activation is the Phase 3 end-to-end gate. Activating the spork makes the
// pillars produce version-3 momentums whose StateRoot is computed in packMomentum and verified
// by every node — exercising BLOCKER-2 (the worker version bump landing before the verify) and
// the producer/verifier/maintainer agreeing on the root for real momentums.
func TestStateRoot_Activation(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	ledgerApi := api.NewLedgerApi(z)

	activateStateRoot(t, z)

	// Some post-activation account activity so the tree is non-trivial.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     g.User2.Address,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(10 * g.Zexp),
	}, nil, mock.SkipVmChanges)
	z.InsertMomentumsTo(40)

	m, err := ledgerApi.GetFrontierMomentum()
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != nom.StateRootMomentumVersion {
		t.Fatalf("frontier momentum version = %d, want %d", m.Version, nom.StateRootMomentumVersion)
	}
	if m.StateRoot.IsZero() {
		t.Fatalf("version-3 momentum has a zero StateRoot")
	}

	// The signed header root equals the locally-maintained tree root at the same height.
	treeRoot, err := z.Chain().StateRoot(m.Identifier())
	if err != nil {
		t.Fatalf("StateRoot(%v): %v", m.Identifier(), err)
	}
	if treeRoot != m.StateRoot {
		t.Fatalf("header StateRoot %v != maintained tree root %v", m.StateRoot, treeRoot)
	}

	// A proof served by the node verifies against the signed header root with the standalone,
	// storage-free verifier — the trustless read the whole design is for.
	absent := []byte{0x09, 0xab, 0xcd, 0xef, 0x01}
	_, proof, err := z.Chain().GetProof(m.Identifier(), absent)
	if err != nil {
		t.Fatalf("GetProof: %v", err)
	}
	ok, err := trie.VerifyAbsence(m.StateRoot, absent, proof)
	if err != nil || !ok {
		t.Fatalf("absence proof failed against header root: ok=%v err=%v", ok, err)
	}
}

// TestStateRoot_ProofRPC is the Phase 5 gate: the ledger.getStateRoot / ledger.getProof RPC
// return the header root and a proof that the standalone storage-free verifier accepts against
// it. (Inclusion-proof byte correctness is covered exhaustively at the trie level in Phase 1;
// here the focus is the RPC plumbing and that the served root equals the signed header root.)
func TestStateRoot_ProofRPC(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	ledgerApi := api.NewLedgerApi(z)

	activateStateRoot(t, z)
	z.InsertMomentumsTo(35)

	m, err := ledgerApi.GetFrontierMomentum()
	if err != nil {
		t.Fatal(err)
	}
	if m.StateRoot.IsZero() {
		t.Fatalf("frontier momentum has a zero StateRoot")
	}

	// getStateRoot returns the header's root.
	root, err := ledgerApi.GetStateRoot(m.Height)
	if err != nil {
		t.Fatalf("GetStateRoot: %v", err)
	}
	if root != m.StateRoot {
		t.Fatalf("getStateRoot %v != header StateRoot %v", root, m.StateRoot)
	}

	// getProof returns the root and a proof that verifies with the standalone verifier.
	absent := []byte{0x09, 0x11, 0x22, 0x33}
	resp, err := ledgerApi.GetProof(m.Height, absent)
	if err != nil {
		t.Fatalf("GetProof: %v", err)
	}
	if resp.Root != m.StateRoot {
		t.Fatalf("proof root %v != header StateRoot %v", resp.Root, m.StateRoot)
	}
	if resp.Value != nil {
		t.Fatalf("absent key returned a value: %x", resp.Value)
	}
	ok, err := trie.VerifyAbsence(resp.Root, absent, resp.Proof)
	if err != nil || !ok {
		t.Fatalf("RPC absence proof failed against header root: ok=%v err=%v", ok, err)
	}

	// Height 0 is rejected.
	if _, err := ledgerApi.GetStateRoot(0); err == nil {
		t.Fatalf("GetStateRoot(0) should error")
	}
}

// TestStateRoot_InactiveStaysV2 confirms that without the spork, momentums stay version 2 with
// an empty StateRoot — the pre-activation invariant the verifier enforces.
func TestStateRoot_InactiveStaysV2(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	ledgerApi := api.NewLedgerApi(z)

	z.InsertMomentumsTo(10)

	m, err := ledgerApi.GetFrontierMomentum()
	if err != nil {
		t.Fatal(err)
	}
	if m.Version == nom.StateRootMomentumVersion {
		t.Fatalf("momentum is version 3 without the spork active")
	}
	if !m.StateRoot.IsZero() {
		t.Fatalf("pre-activation momentum has a non-zero StateRoot: %v", m.StateRoot)
	}
}
