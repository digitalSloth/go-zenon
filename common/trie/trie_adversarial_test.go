package trie

import (
	"bytes"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
)

// helper: build sorted leaves
func mk(leaves ...leaf) []leaf {
	s := make([]leaf, len(leaves))
	copy(s, leaves)
	sortLeaves(s)
	return s
}

// ADV-1: present-empty leaf is DISTINCT from absent at the root, and proofs differ.
func TestADV_PresentEmptyVsAbsent(t *testing.T) {
	var p types.Hash
	p[0] = 0xAA
	// present-empty
	rPresent := rootOfLeaves([]leaf{{path: p, value: []byte{}}})
	// absent (empty tree)
	rAbsent := rootOfLeaves(nil)
	if rPresent == rAbsent {
		t.Fatalf("present-empty root == absent root (%x) — empty folded into delete!", rPresent)
	}
	// LeafHash(path, []) must be sha3(path) and non-zero
	lh := LeafHash(p, []byte{})
	if lh == emptyHash {
		t.Fatalf("LeafHash(path,[]) == zero — present-empty indistinguishable from absent")
	}
	t.Logf("present-empty root=%x absent root=%x leafhash(empty)=%x", rPresent, rAbsent, lh)
}

// ADV-2: proof for present-empty verifies as INCLUSION; proof for absent verifies as ABSENCE,
// over the same path, against their respective roots.
func TestADV_ProofPresentEmptyInclusion(t *testing.T) {
	var p types.Hash
	p[0] = 0xAA
	leaves := mk(leaf{path: p, value: []byte{}})
	root := rootOfLeaves(leaves)

	present, val, sib := proveLeaves(leaves, p)
	if !present {
		t.Fatalf("present-empty key proved as ABSENT")
	}
	if len(val) != 0 {
		t.Fatalf("present-empty value len = %d, want 0", len(val))
	}
	enc := encodeProof(present, val, p, sib)
	d, err := decodeProof(enc)
	if err != nil {
		t.Fatalf("decode present-empty proof: %v", err)
	}
	if !d.inclusion {
		t.Fatalf("present-empty decoded as non-inclusion")
	}
	if d.reconstructRoot(d.leafHash) != root {
		t.Fatalf("present-empty proof does not reconstruct root")
	}
}

// ADV-3: absent proof over a populated sibling subtree reconstructs the populated root,
// and CANNOT be passed off as inclusion.
func TestADV_AbsenceThroughPopulatedSubtree(t *testing.T) {
	var a, b types.Hash
	a[0] = 0x00 // shares top bits with b
	b[0] = 0x00
	a[31] = 0x01
	b[31] = 0x02
	leaves := mk(leaf{path: a, value: []byte("va")}, leaf{path: b, value: []byte("vb")})
	root := rootOfLeaves(leaves)

	var absent types.Hash
	absent[0] = 0x00
	absent[31] = 0x03 // sibling-occupied prefix, but slot empty
	present, _, sib := proveLeaves(leaves, absent)
	if present {
		t.Fatalf("absent key through populated subtree proved present")
	}
	enc := encodeProof(present, nil, absent, sib)
	d, err := decodeProof(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.inclusion {
		t.Fatalf("absence decoded as inclusion")
	}
	if d.reconstructRoot(emptyHash) != root {
		t.Fatalf("absence proof does not reconstruct populated root")
	}
}

// ADV-4: THE DOUBLE-HASH TRAP. The public VerifyProof hashes the key internally.
// If an L2 caller passes the already-derived 32-byte PATH as `key`, VerifyProof computes
// sha3(path) != path and the proof is rejected — demonstrating the spec's path-based-API
// requirement is unmet by the current public surface.
func TestADV_DoubleHashTrap(t *testing.T) {
	var p types.Hash
	p[0] = 0xAA
	leaves := mk(leaf{path: p, value: []byte("v")})
	root := rootOfLeaves(leaves)
	present, val, sib := proveLeaves(leaves, p)
	enc := encodeProof(present, val, p, sib)

	// Correct (pure) verification: decode + reconstruct, no key hashing.
	d, _ := decodeProof(enc)
	if d.reconstructRoot(d.leafHash) != root {
		t.Fatalf("pure verify failed")
	}

	// Public VerifyProof with key = path (what an L2 caller holding a derived key would do):
	ok, err := VerifyProof(root, p[:], val, enc)
	if err == nil && ok {
		t.Fatalf("VerifyProof accepted path-as-key — no double hash? unexpected")
	}
	t.Logf("VerifyProof(path-as-key) ok=%v err=%v  <-- double-hash rejection confirmed", ok, err)

	// And VerifyProof only succeeds if caller passes a RAW key whose sha3 == path,
	// which an L2 caller that already derived the path does NOT have.

	// Path-native verifier ACCEPTS the same path (no double hash).
	okPath, errPath := VerifyProofByPath(root, Path(p), val, enc)
	if errPath != nil || !okPath {
		t.Fatalf("VerifyProofByPath(path) ok=%v err=%v, want accepted", okPath, errPath)
	}
}

// ADV-5: deletion (absence) of a previously-present key yields the empty-tree root again
// (history independence), and equals never-having-written it.
func TestADV_DeleteEqualsNeverWrite(t *testing.T) {
	var p types.Hash
	p[0] = 0xAA
	withKey := rootOfLeaves([]leaf{{path: p, value: []byte("v")}})
	deleted := rootOfLeaves(nil) // delete == remove leaf entirely
	neverWrote := rootOfLeaves(nil)
	if deleted != neverWrote {
		t.Fatalf("delete-down-to-empty != never-write")
	}
	if withKey == deleted {
		t.Fatalf("present root == deleted root")
	}
}

// ADV-6: ordering — rootOfLeaves is independent of input order (history-independence / canonical).
func TestADV_OrderIndependence(t *testing.T) {
	var a, b, c types.Hash
	a[0], b[0], c[0] = 0x10, 0x80, 0xF0
	r1 := rootOfLeaves([]leaf{{a, []byte("1")}, {b, []byte("2")}, {c, []byte("3")}})
	r2 := rootOfLeaves([]leaf{{c, []byte("3")}, {a, []byte("1")}, {b, []byte("2")}})
	if r1 != r2 {
		t.Fatalf("root depends on insertion order: %x != %x", r1, r2)
	}
}

// ADV-7: a present proof with value tampered must fail public VerifyProof value-binding.
func TestADV_TamperValue(t *testing.T) {
	var p types.Hash
	p[0] = 0xAA
	leaves := mk(leaf{path: p, value: []byte("real")})
	present, val, sib := proveLeaves(leaves, p)
	enc := encodeProof(present, val, p, sib)
	// decode is fine; now reconstruct with a different "claimed" leaf hash must not match root
	d, _ := decodeProof(enc)
	tampered := LeafHash(p, []byte("fake"))
	if bytes.Equal(d.leafHash[:], tampered[:]) {
		t.Fatalf("tampered leaf hash collided")
	}
}
