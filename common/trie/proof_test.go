package trie

import (
	"testing"

	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
)

func buildTree(t *testing.T, kv map[string][]byte) (*Tree, types.HashHeight, types.Hash) {
	t.Helper()
	tr := newMemTree(t)
	p := db.NewPatch()
	for k, v := range kv {
		p.Put([]byte(k), v)
	}
	if err := tr.Update(p); err != nil {
		t.Fatal(err)
	}
	id := hh(1)
	if err := tr.Commit(id); err != nil {
		t.Fatal(err)
	}
	root, err := tr.Root(id)
	if err != nil {
		t.Fatal(err)
	}
	return tr, id, root
}

func TestProofInclusionRoundTrip(t *testing.T) {
	kv := map[string][]byte{
		string(accountKey(0x03, 0x01)): []byte("one"),
		string(accountKey(0x03, 0x02)): []byte("two"),
		string(accountKey(0x04, 0x03)): []byte("three"),
	}
	tr, id, root := buildTree(t, kv)

	for k, v := range kv {
		value, proof, err := tr.Prove(id, []byte(k))
		if err != nil {
			t.Fatalf("Prove(%x): %v", k, err)
		}
		if string(value) != string(v) {
			t.Fatalf("Prove value = %q, want %q", value, v)
		}
		ok, err := VerifyProof(root, []byte(k), value, proof)
		if err != nil || !ok {
			t.Fatalf("VerifyProof(%x) ok=%v err=%v", k, ok, err)
		}

		// Tampering must be caught.
		if ok, _ := VerifyProof(root, []byte(k), []byte("wrong"), proof); ok {
			t.Fatalf("verify accepted a wrong value")
		}
		bad := root
		bad[0] ^= 0xff
		if ok, _ := VerifyProof(bad, []byte(k), value, proof); ok {
			t.Fatalf("verify accepted a wrong root")
		}
		tampered := append([]byte{}, proof...)
		tampered[len(tampered)-1] ^= 0xff // flip the last value byte; decode rejects via leaf_hash check
		if ok, _ := VerifyProof(root, []byte(k), value, tampered); ok {
			t.Fatalf("verify accepted a tampered proof")
		}
	}
}

func TestProofAbsenceRoundTrip(t *testing.T) {
	kv := map[string][]byte{
		string(accountKey(0x03, 0x01)): []byte("one"),
		string(accountKey(0x04, 0x02)): []byte("two"),
	}
	tr, id, root := buildTree(t, kv)

	absent := []byte{0x09, 0xaa, 0xbb}
	value, proof, err := tr.Prove(id, absent)
	if err != nil {
		t.Fatalf("Prove(absent): %v", err)
	}
	if value != nil {
		t.Fatalf("absent key returned a value: %q", value)
	}
	ok, err := VerifyAbsence(root, absent, proof)
	if err != nil || !ok {
		t.Fatalf("VerifyAbsence ok=%v err=%v", ok, err)
	}

	// An absence proof must not verify as an inclusion proof, and vice versa.
	if ok, _ := VerifyProof(root, absent, []byte("x"), proof); ok {
		t.Fatalf("absence proof verified as inclusion")
	}
	if _, incProof, _ := tr.Prove(id, accountKey(0x03, 0x01)); true {
		if ok, _ := VerifyAbsence(root, accountKey(0x03, 0x01), incProof); ok {
			t.Fatalf("inclusion proof verified as absence")
		}
	}
}

func TestProofRejectsReservedFlagBits(t *testing.T) {
	tr, id, root := buildTree(t, map[string][]byte{string(accountKey(0x03, 0x01)): []byte("v")})
	absent := []byte{0x09, 0x99}
	_, proof, err := tr.Prove(id, absent)
	if err != nil {
		t.Fatal(err)
	}
	// Set a reserved flag bit (bits 1..7 must be zero).
	tampered := append([]byte{}, proof...)
	tampered[0] |= 0x02
	if _, err := VerifyAbsence(root, absent, tampered); err != ErrProofMalformed {
		t.Fatalf("expected ErrProofMalformed for reserved flag bit, got %v", err)
	}
}

func TestProofRejectsTrailingBytes(t *testing.T) {
	tr, id, root := buildTree(t, map[string][]byte{string(accountKey(0x03, 0x01)): []byte("v")})
	absent := []byte{0x09, 0x99}
	_, proof, err := tr.Prove(id, absent)
	if err != nil {
		t.Fatal(err)
	}
	// Append a trailing byte — must be rejected.
	tampered := append(append([]byte{}, proof...), 0x00)
	if _, err := VerifyAbsence(root, absent, tampered); err != ErrProofMalformed {
		t.Fatalf("expected ErrProofMalformed for trailing bytes, got %v", err)
	}
}

func TestProofWrongKeyRejected(t *testing.T) {
	kv := map[string][]byte{string(accountKey(0x03, 0x01)): []byte("v")}
	tr, id, root := buildTree(t, kv)
	_, proof, err := tr.Prove(id, accountKey(0x03, 0x01))
	if err != nil {
		t.Fatal(err)
	}
	// Same proof, different key: the path check must reject it.
	if _, err := VerifyProof(root, accountKey(0x03, 0x02), []byte("v"), proof); err != ErrProofPathMismatch {
		t.Fatalf("expected ErrProofPathMismatch, got %v", err)
	}
}

func TestProofSizeWithinCap(t *testing.T) {
	const maxDataLength = 16384 // vm/constants/plasma.go MaxDataLength
	tr := newMemTree(t)
	p := db.NewPatch()
	for i := 0; i < 500; i++ {
		k := accountKey(0x03, byte(i), byte(i>>8))
		p.Put(k, []byte{byte(i)})
	}
	if err := tr.Update(p); err != nil {
		t.Fatal(err)
	}
	id := hh(1)
	if err := tr.Commit(id); err != nil {
		t.Fatal(err)
	}
	_, proof, err := tr.Prove(id, accountKey(0x03, 0x00, 0x00))
	if err != nil {
		t.Fatal(err)
	}
	if len(proof) > maxDataLength {
		t.Fatalf("proof size %d exceeds 16KB cap", len(proof))
	}
}
