package trie

import (
	"testing"

	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
)

// TestGuideWorkedExample is the executable companion to docs/notes/MERKLE_STATE_GUIDE.md. It
// walks the guide's "how a third party verifies" flow end-to-end against the frozen scheme
// (§6.2/§6.3), so the guide's claims stay true as the code evolves. (The trustless link to a
// signed momentum header — verifying the pillar signature and that ComputeHash binds the
// StateRoot — is the chain-level step exercised in vm/embedded/tests TestStateRoot_ProofRPC;
// here we cover the storage-free proof step the guide centres on.)
func TestGuideWorkedExample(t *testing.T) {
	tree := newMemNodeTree(t)

	// 1. WHAT IS RECORDED — every state entry the network agrees on becomes one leaf at
	//    position sha3(key), with its value bound into the leaf hash. Here: a balance and a
	//    contract-storage entry, conforming to the account-store keyspace the state-root fold
	//    commits to: {3}|addr(20)|sub|tail.
	balanceKey := accountKey(0x03, 0x01) // {3}|addr|{3}|zts  (a balance)
	storageKey := accountKey(0x04, 0x77) // {3}|addr|{4}|key  (contract storage)
	balanceVal := []byte("500 ZNN")
	storageVal := []byte("contract-state-bytes")

	p := db.NewPatch()
	p.Put(balanceKey, balanceVal)
	p.Put(storageKey, storageVal)
	if err := tree.Update(p); err != nil {
		t.Fatal(err)
	}
	id := hh(1)
	if err := tree.Commit(id); err != nil {
		t.Fatal(err)
	}

	// 2. WHAT THE ROOT PROVES — the single 32-byte root is the commitment that goes in the
	//    signed momentum header. Everything below verifies against just this value.
	root, err := tree.Root(id)
	if err != nil {
		t.Fatal(err)
	}
	if root.IsZero() {
		t.Fatalf("root should be non-zero for a non-empty tree")
	}

	// 3a. INCLUSION — prove balanceKey = balanceVal. A holder of only `root` and the proof can
	//     check it with the standalone, storage-free verifier: no node, no DB, no trust.
	value, proof, err := tree.Prove(id, balanceKey)
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != string(balanceVal) {
		t.Fatalf("proof value = %q, want %q", value, balanceVal)
	}
	ok, err := VerifyProof(root, balanceKey, value, proof)
	if err != nil || !ok {
		t.Fatalf("inclusion proof must verify: ok=%v err=%v", ok, err)
	}
	// The proof fits well within the 16KB account-block data cap a future on-chain referee uses.
	const maxDataLength = 1024 * 16
	if len(proof) > maxDataLength {
		t.Fatalf("proof size %d exceeds the 16KB cap", len(proof))
	}

	// 3b. TAMPER DETECTION — nothing short of breaking SHA3 fakes a proof. A wrong value, or a
	//     wrong root, fails.
	if ok, _ := VerifyProof(root, balanceKey, []byte("999999 ZNN"), proof); ok {
		t.Fatalf("verifier accepted a forged value")
	}
	wrongRoot := root
	wrongRoot[0] ^= 0xff
	if ok, _ := VerifyProof(wrongRoot, balanceKey, value, proof); ok {
		t.Fatalf("verifier accepted a proof against the wrong root")
	}

	// 4. ABSENCE — prove a never-written key is absent. Sparse trees give this for free; the
	//    proof shows the key's slot reconstructs to the same signed root with an empty leaf.
	absentKey := accountKey(0x04, 0x99, 0x99)
	missingVal, absenceProof, err := tree.Prove(id, absentKey)
	if err != nil {
		t.Fatal(err)
	}
	if missingVal != nil {
		t.Fatalf("absent key returned a value: %q", missingVal)
	}
	ok, err = VerifyAbsence(root, absentKey, absenceProof)
	if err != nil || !ok {
		t.Fatalf("absence proof must verify: ok=%v err=%v", ok, err)
	}
	// An absence proof must not pass as inclusion (and vice-versa).
	if ok, _ := VerifyProof(root, absentKey, []byte("x"), absenceProof); ok {
		t.Fatalf("absence proof verified as inclusion")
	}

	// 5. LIMIT — the root proves state *as of this momentum*. The guide's empty-tree and
	//    single-leaf base cases hold here too: an empty commitment is the level-0 default.
	empty := newMemNodeTree(t)
	if err := empty.Update(db.NewPatch()); err != nil {
		t.Fatal(err)
	}
	if err := empty.Commit(hh(1)); err != nil {
		t.Fatal(err)
	}
	emptyRoot, err := empty.Root(hh(1))
	if err != nil {
		t.Fatal(err)
	}
	if emptyRoot != (types.Hash{}) {
		t.Fatalf("empty-tree root = %v, want zero hash", emptyRoot)
	}
	// Any key is provably absent under the empty root.
	_, ep, err := empty.Prove(hh(1), balanceKey)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyAbsence(emptyRoot, balanceKey, ep); err != nil || !ok {
		t.Fatalf("absence under empty root must verify: ok=%v err=%v", ok, err)
	}
}
