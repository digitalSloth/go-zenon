package trie

import (
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
)

// newCompactTree allocates an empty CompactTree.
func newCompactTree() *CompactTree {
	return &CompactTree{}
}

// TestCompactEmpty checks that an empty CompactTree returns the default root.
func TestCompactEmpty(t *testing.T) {
	ct := newCompactTree()
	got := ct.Root()
	want := emptyHash
	if got != want {
		t.Fatalf("empty root: got %v want %v", got, want)
	}
}

// TestCompactSingleLeaf checks that a single-leaf tree root equals padLeaf(leaf, 0).
func TestCompactSingleLeaf(t *testing.T) {
	ct := newCompactTree()
	path := types.NewHash([]byte("hello"))
	value := []byte("world")
	ct.Insert(path, value)

	l := leaf{path: path, value: value}
	want := padLeaf(l, 0)
	got := ct.Root()
	if got != want {
		t.Fatalf("single-leaf root: got %v want %v", got, want)
	}

	// Also verify against oracle.
	m := map[types.Hash][]byte{path: value}
	if got != oracleRoot(m) {
		t.Fatalf("single-leaf root vs oracle: got %v want %v", got, oracleRoot(m))
	}
}

// TestCompactDeleteDownToEmpty inserts a leaf, then deletes it, expects empty root.
func TestCompactDeleteDownToEmpty(t *testing.T) {
	ct := newCompactTree()
	path := types.NewHash([]byte("key"))
	ct.Insert(path, []byte("value"))
	ct.Delete(path)

	got := ct.Root()
	want := emptyHash
	if got != want {
		t.Fatalf("delete-to-empty root: got %v want %v", got, want)
	}
}

// TestCompactWriteDeleteEqualsNeverWrite checks that inserting then deleting a key leaves
// the root identical to never having inserted it (history independence).
func TestCompactWriteDeleteEqualsNeverWrite(t *testing.T) {
	// Tree A: insert two keys, delete one.
	pathA := types.NewHash([]byte("a"))
	pathB := types.NewHash([]byte("b"))
	val := []byte("v")

	ctA := newCompactTree()
	ctA.Insert(pathA, val)
	ctA.Insert(pathB, val)
	ctA.Delete(pathA)

	// Tree B: only insert pathB from the start.
	ctB := newCompactTree()
	ctB.Insert(pathB, val)

	if ctA.Root() != ctB.Root() {
		t.Fatalf("write-then-delete != never-write: A=%v B=%v", ctA.Root(), ctB.Root())
	}
}

// TestCompactTopBitDivergence exercises two leaves that diverge at bit 0 (the MSB).
func TestCompactTopBitDivergence(t *testing.T) {
	// Construct paths differing at bit 0: one with MSB=0, one with MSB=1.
	var pathL, pathR types.Hash
	pathL[0] = 0x00 // MSB 0
	pathR[0] = 0x80 // MSB 1

	ct := newCompactTree()
	valL := []byte("left")
	valR := []byte("right")
	ct.Insert(pathL, valL)
	ct.Insert(pathR, valR)

	m := map[types.Hash][]byte{pathL: valL, pathR: valR}
	got := ct.Root()
	want := oracleRoot(m)
	if got != want {
		t.Fatalf("top-bit divergence: got %v want %v", got, want)
	}
}

// TestCompactDeepDivergence exercises two leaves sharing a long prefix (deep branch).
func TestCompactDeepDivergence(t *testing.T) {
	// Paths identical in bytes 0..30, differ only in the last byte.
	var pathA, pathB types.Hash
	for i := range pathA {
		pathA[i] = 0xAB
		pathB[i] = 0xAB
	}
	pathA[31] = 0xAB // last byte: bit pattern 10101011
	pathB[31] = 0xAA // last byte: bit pattern 10101010 — differ at bit 255

	ct := newCompactTree()
	valA := []byte("deep-a")
	valB := []byte("deep-b")
	ct.Insert(pathA, valA)
	ct.Insert(pathB, valB)

	m := map[types.Hash][]byte{pathA: valA, pathB: valB}
	got := ct.Root()
	want := oracleRoot(m)
	if got != want {
		t.Fatalf("deep-divergence: got %v want %v", got, want)
	}
}

// TestCompactFuzzAgainstOracle is the differential gate (§10/§12):
// a long random insert/update/delete sequence with root checked vs oracleRoot after every op.
func TestCompactFuzzAgainstOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	ct := newCompactTree()
	oracle := map[types.Hash][]byte{}

	// keys holds the set of keys inserted so far so we can pick them for updates/deletes.
	var keys []types.Hash

	compactRandPath := func() types.Hash {
		k := make([]byte, 9)
		k[0] = byte(3 + rng.Intn(7))
		binary.BigEndian.PutUint64(k[1:], rng.Uint64())
		return types.NewHash(k)
	}

	const ops = 2000
	for i := 0; i < ops; i++ {
		switch rng.Intn(3) {
		case 0, 1: // insert or update
			var path types.Hash
			if len(keys) > 0 && rng.Intn(2) == 0 {
				path = keys[rng.Intn(len(keys))]
			} else {
				path = compactRandPath()
				keys = append(keys, path)
			}
			value := make([]byte, 1+rng.Intn(40))
			rng.Read(value)
			ct.Insert(path, value)
			oracle[path] = value

		case 2: // delete
			if len(keys) == 0 {
				continue
			}
			idx := rng.Intn(len(keys))
			path := keys[idx]
			ct.Delete(path)
			delete(oracle, path)
			// Remove from keys slice (swap with last).
			keys[idx] = keys[len(keys)-1]
			keys = keys[:len(keys)-1]
		}

		got := ct.Root()
		want := oracleRoot(oracle)
		if got != want {
			t.Fatalf("op %d: root mismatch got=%v want=%v", i, got, want)
		}
	}

	// Delete all remaining keys and verify empty root.
	for _, path := range keys {
		ct.Delete(path)
		delete(oracle, path)
	}
	if ct.Root() != emptyHash {
		t.Fatalf("after delete-all, root is not default: %v", ct.Root())
	}
	if ct.Root() != oracleRoot(oracle) {
		t.Fatalf("after delete-all, root != oracle empty root")
	}
}

// TestCompactDeleteNonExistent verifies that deleting a key not in the tree is a no-op.
func TestCompactDeleteNonExistent(t *testing.T) {
	ct := newCompactTree()
	path := types.NewHash([]byte("present"))
	value := []byte("val")
	ct.Insert(path, value)
	before := ct.Root()

	absent := types.NewHash([]byte("absent"))
	ct.Delete(absent)
	after := ct.Root()

	if before != after {
		t.Fatalf("delete-nonexistent changed root: before=%v after=%v", before, after)
	}
}

// TestCompactUpdateValue verifies that re-inserting a key with a new value changes the root
// to match the oracle.
func TestCompactUpdateValue(t *testing.T) {
	ct := newCompactTree()
	path := types.NewHash([]byte("key"))
	ct.Insert(path, []byte("v1"))
	ct.Insert(path, []byte("v2"))

	m := map[types.Hash][]byte{path: []byte("v2")}
	got := ct.Root()
	want := oracleRoot(m)
	if got != want {
		t.Fatalf("update value: got %v want %v", got, want)
	}
}

// TestCompactThreeLeaves exercises a small tree where the branch structure is non-trivial.
func TestCompactThreeLeaves(t *testing.T) {
	// Use the same randKey shape as tree_test.go for consistency.
	rng := rand.New(rand.NewSource(99))
	ct := newCompactTree()
	oracle := map[types.Hash][]byte{}
	for i := 0; i < 3; i++ {
		k := make([]byte, 9)
		k[0] = byte(3 + rng.Intn(7))
		binary.BigEndian.PutUint64(k[1:], rng.Uint64())
		path := types.NewHash(k)
		val := make([]byte, 8)
		rng.Read(val)
		ct.Insert(path, val)
		oracle[path] = val
	}
	got := ct.Root()
	want := oracleRoot(oracle)
	if got != want {
		t.Fatalf("three-leaf root: got %v want %v", got, want)
	}
}
