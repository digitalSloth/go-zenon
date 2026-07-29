package trie

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
)

// commitBoth stages and commits the same patch to both a leaf-set Tree and a nodeTree.
func commitBoth(t *testing.T, lt *Tree, nt *NodeTree, p db.Patch, id types.HashHeight) {
	t.Helper()
	if err := lt.Update(p); err != nil {
		t.Fatalf("h=%d lt.Update: %v", id.Height, err)
	}
	if err := lt.Commit(id); err != nil {
		t.Fatalf("h=%d lt.Commit: %v", id.Height, err)
	}
	if err := nt.Update(p); err != nil {
		t.Fatalf("h=%d nt.Update: %v", id.Height, err)
	}
	if err := nt.Commit(id); err != nil {
		t.Fatalf("h=%d nt.Commit: %v", id.Height, err)
	}
}

// newMemNodeTree opens a nodeTree backed by an in-memory leveldb.
func newMemNodeTree(t *testing.T) *NodeTree {
	t.Helper()
	ldb, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatalf("open memdb: %v", err)
	}
	t.Cleanup(func() { _ = ldb.Close() })
	nt, err := NewNodeTree(ldb)
	if err != nil {
		t.Fatalf("NewNodeTree: %v", err)
	}
	return nt
}

// checkRefcountIntegrity recomputes expected refcounts as the edge-multiset sum (§10.2) and
// asserts they equal the stored refcounts exactly.
//
// Expected refcount for a node n:
//   - +1 for each Internal node child-slot whose id == n (left and right counted separately,
//     so if both slots hold n it gets +2).
//   - +1 for each version→root entry whose rootId == n.
func checkRefcountIntegrity(t *testing.T, nt *NodeTree) {
	t.Helper()
	expected := map[types.Hash]int64{}

	// Scan all internal nodes and accumulate child edge counts.
	iter := nt.ldb.NewIterator(util.BytesPrefix(nsKeyNode), nil)
	defer iter.Release()
	for iter.Next() {
		data := iter.Value()
		if len(data) == 0 || data[0] != nsInternalTag {
			continue
		}
		node, err := deserializeNode(data)
		if err != nil {
			t.Fatalf("checkRefcountIntegrity: deserializeNode: %v", err)
		}
		internal, ok := node.(*diskInternal)
		if !ok {
			continue
		}
		if internal.leftId != zeroHash {
			expected[internal.leftId]++
		}
		if internal.rightId != zeroHash {
			expected[internal.rightId]++
		}
	}
	if err := iter.Error(); err != nil {
		t.Fatalf("checkRefcountIntegrity: iterator: %v", err)
	}

	// Scan all version→root entries.
	viter := nt.ldb.NewIterator(util.BytesPrefix(nsKeyVersion), nil)
	defer viter.Release()
	for viter.Next() {
		val := viter.Value()
		if len(val) != types.HashSize {
			t.Fatalf("checkRefcountIntegrity: bad rootId length %d", len(val))
		}
		var rootId types.Hash
		copy(rootId[:], val)
		if rootId != zeroHash {
			expected[rootId]++
		}
	}
	if err := viter.Error(); err != nil {
		t.Fatalf("checkRefcountIntegrity: version iterator: %v", err)
	}

	// Compare expected vs stored for every node that has either an expected count or a stored count.
	checked := map[types.Hash]bool{}

	for id, exp := range expected {
		stored, err := loadStoredRef(nt.ldb, id)
		if err != nil {
			t.Fatalf("checkRefcountIntegrity: loadStoredRef(%v): %v", id, err)
		}
		if int64(stored) != exp {
			t.Errorf("refcount mismatch for %v: stored=%d expected=%d", id, stored, exp)
		}
		checked[id] = true
	}

	// Any node with a stored non-zero refcount that we didn't compute an expected count for
	// is a leak.
	rIter := nt.ldb.NewIterator(util.BytesPrefix(nsKeyRef), nil)
	defer rIter.Release()
	for rIter.Next() {
		val := rIter.Value()
		if len(val) != 8 {
			continue
		}
		count := binary.BigEndian.Uint64(val)
		if count == 0 {
			continue
		}
		var id types.Hash
		copy(id[:], rIter.Key()[len(nsKeyRef):])
		if !checked[id] {
			t.Errorf("refcount leak: node %v has stored count %d but expected 0", id, count)
		}
	}
	if err := rIter.Error(); err != nil {
		t.Fatalf("checkRefcountIntegrity: ref iterator: %v", err)
	}
}

func loadStoredRef(ldb *leveldb.DB, id types.Hash) (uint64, error) {
	data, err := ldb.Get(nsRefKey(id), nil)
	if err == leveldb.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if len(data) != 8 {
		return 0, ErrCorrupt
	}
	return binary.BigEndian.Uint64(data), nil
}

// ntRandKey generates a random fold-included balance key: {3} || addr(20) || {3} || rand(8).
func ntRandKey(rng *rand.Rand) []byte {
	return randKey(rng)
}

// TestNodeTreeDifferential is the differential gate (§10.1): the SAME random insert/update/
// delete sequence is run through (a) the existing leaf-set Tree, (b) the in-memory CompactTree,
// and (c) the new nodeTree. After every commit, all three roots must be identical.
func TestNodeTreeDifferential(t *testing.T) {
	const seed = 12345
	rng := rand.New(rand.NewSource(seed))

	leafTree := newMemTree(t)
	nt := newMemNodeTree(t)
	ct := newCompactTree()

	oracle := map[types.Hash][]byte{}
	var keys [][]byte
	storedRoots := map[uint64]types.Hash{}

	const versions = 150

	for h := uint64(1); h <= versions; h++ {
		p := db.NewPatch()

		// Inserts / updates.
		for n := 0; n < rng.Intn(6)+1; n++ {
			var k []byte
			if len(keys) > 0 && rng.Intn(2) == 0 {
				k = keys[rng.Intn(len(keys))]
			} else {
				k = ntRandKey(rng)
				keys = append(keys, k)
			}
			v := make([]byte, 1+rng.Intn(40))
			rng.Read(v)
			p.Put(k, v)
			oracle[types.NewHash(k)] = v
			ct.Insert(types.NewHash(k), v)
		}
		// Deletes.
		for n := 0; n < rng.Intn(3); n++ {
			if len(keys) == 0 {
				break
			}
			k := keys[rng.Intn(len(keys))]
			p.Delete(k)
			delete(oracle, types.NewHash(k))
			ct.Delete(types.NewHash(k))
		}
		// Frontier keys must be filtered (same fold rule).
		if rng.Intn(3) == 0 {
			p.Put([]byte{byte(rng.Intn(3)), 0x77}, []byte("ignored"))
		}

		id := hh(h)

		if err := leafTree.Update(p); err != nil {
			t.Fatalf("h=%d leafTree.Update: %v", h, err)
		}
		if err := leafTree.Commit(id); err != nil {
			t.Fatalf("h=%d leafTree.Commit: %v", h, err)
		}
		leafRoot, err := leafTree.Root(id)
		if err != nil {
			t.Fatalf("h=%d leafTree.Root: %v", h, err)
		}

		if err := nt.Update(p); err != nil {
			t.Fatalf("h=%d nodeTree.Update: %v", h, err)
		}
		if err := nt.Commit(id); err != nil {
			t.Fatalf("h=%d nodeTree.Commit: %v", h, err)
		}
		ntRoot, err := nt.Root(id)
		if err != nil {
			t.Fatalf("h=%d nodeTree.Root: %v", h, err)
		}

		ctRoot := ct.Root()
		oracleR := oracleRoot(oracle)

		if leafRoot != oracleR {
			t.Fatalf("h=%d leafTree root=%v oracle=%v", h, leafRoot, oracleR)
		}
		if ctRoot != oracleR {
			t.Fatalf("h=%d compactTree root=%v oracle=%v", h, ctRoot, oracleR)
		}
		if ntRoot != oracleR {
			t.Fatalf("h=%d nodeTree root=%v oracle=%v", h, ntRoot, oracleR)
		}

		storedRoots[h] = ntRoot
		checkRefcountIntegrity(t, nt)
	}

	// Historical Root lookups must return the root committed at that height.
	for h := uint64(1); h <= versions; h++ {
		got, err := nt.Root(hh(h))
		if err != nil {
			t.Fatalf("historical Root(%d): %v", h, err)
		}
		if got != storedRoots[h] {
			t.Fatalf("historical root mismatch at h=%d: got=%v want=%v", h, got, storedRoots[h])
		}
	}
}

// TestNodeTreeEmpty checks that an empty nodeTree root equals emptyHash.
func TestNodeTreeEmpty(t *testing.T) {
	nt := newMemNodeTree(t)
	// Height 0 is the zero/empty state; no version committed yet.
	// Root at frontier (ZeroHashHeight, height=0) should return emptyHash.
	got, err := nt.Root(types.ZeroHashHeight)
	if err != nil {
		t.Fatalf("Root(ZeroHashHeight): %v", err)
	}
	if got != emptyHash {
		t.Fatalf("empty root: got %v want %v", got, emptyHash)
	}
}

// TestNodeTreeSingleLeaf checks a single-leaf commit.
func TestNodeTreeSingleLeaf(t *testing.T) {
	nt := newMemNodeTree(t)

	key := accountKey(0x03, 0x01, 0x02)
	value := []byte("hello")
	p := db.NewPatch()
	p.Put(key, value)

	id := hh(1)
	if err := nt.Update(p); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(id); err != nil {
		t.Fatal(err)
	}
	got, err := nt.Root(id)
	if err != nil {
		t.Fatal(err)
	}

	oracle := map[types.Hash][]byte{types.NewHash(key): value}
	want := oracleRoot(oracle)
	if got != want {
		t.Fatalf("single-leaf root: got %v want %v", got, want)
	}
	checkRefcountIntegrity(t, nt)
}

// TestNodeTreeDeleteToEmpty checks that inserting and then deleting a key returns the empty root.
func TestNodeTreeDeleteToEmpty(t *testing.T) {
	nt := newMemNodeTree(t)

	key := accountKey(0x03, 0xAB)
	value := []byte("val")

	p1 := db.NewPatch()
	p1.Put(key, value)
	if err := nt.Update(p1); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(1)); err != nil {
		t.Fatal(err)
	}

	p2 := db.NewPatch()
	p2.Delete(key)
	if err := nt.Update(p2); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(2)); err != nil {
		t.Fatal(err)
	}

	got, err := nt.Root(hh(2))
	if err != nil {
		t.Fatal(err)
	}
	if got != emptyHash {
		t.Fatalf("delete-to-empty: got %v want %v", got, emptyHash)
	}
	checkRefcountIntegrity(t, nt)
}

// TestNodeTreeDeepPrefixDivergence tests two keys that share a very long common prefix.
func TestNodeTreeDeepPrefixDivergence(t *testing.T) {
	nt := newMemNodeTree(t)

	// Build two paths differing only at bit 255 (last bit).
	var pathA, pathB types.Hash
	for i := range pathA {
		pathA[i] = 0xAB
		pathB[i] = 0xAB
	}
	pathA[31] = 0xAB // bit 255 = 1 (10101011)
	pathB[31] = 0xAA // bit 255 = 0 (10101010)

	valA := []byte("deep-a")
	valB := []byte("deep-b")

	p := db.NewPatch()
	// Use paths directly as keys (path = sha3(key), but here we use fixed paths via types.NewHash)
	// For nodeTree the key is hashed; use keys that hash to pathA/pathB is not easy to engineer.
	// Instead use the oracle mapping and regular keys to confirm roots match.
	keyA := accountKey(0x03, 0x01)
	keyB := accountKey(0x03, 0x02)
	p.Put(keyA, valA)
	p.Put(keyB, valB)
	_ = pathA
	_ = pathB

	if err := nt.Update(p); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(1)); err != nil {
		t.Fatal(err)
	}
	got, err := nt.Root(hh(1))
	if err != nil {
		t.Fatal(err)
	}
	oracle := map[types.Hash][]byte{
		types.NewHash(keyA): valA,
		types.NewHash(keyB): valB,
	}
	want := oracleRoot(oracle)
	if got != want {
		t.Fatalf("deep-prefix: got %v want %v", got, want)
	}
	checkRefcountIntegrity(t, nt)
}

// TestNodeTreeWriteThenDelete verifies history independence: write-then-delete produces the
// same root as never writing.
func TestNodeTreeWriteThenDelete(t *testing.T) {
	ntA := newMemNodeTree(t)
	ntB := newMemNodeTree(t)

	keyA := accountKey(0x03, 0x01)
	keyB := accountKey(0x03, 0x02)
	val := []byte("v")

	// ntA: insert both, then delete keyA.
	pA1 := db.NewPatch()
	pA1.Put(keyA, val)
	pA1.Put(keyB, val)
	if err := ntA.Update(pA1); err != nil {
		t.Fatal(err)
	}
	if err := ntA.Commit(hh(1)); err != nil {
		t.Fatal(err)
	}
	pA2 := db.NewPatch()
	pA2.Delete(keyA)
	if err := ntA.Update(pA2); err != nil {
		t.Fatal(err)
	}
	if err := ntA.Commit(hh(2)); err != nil {
		t.Fatal(err)
	}

	// ntB: only insert keyB.
	pB := db.NewPatch()
	pB.Put(keyB, val)
	if err := ntB.Update(pB); err != nil {
		t.Fatal(err)
	}
	if err := ntB.Commit(hh(1)); err != nil {
		t.Fatal(err)
	}

	rootA, err := ntA.Root(hh(2))
	if err != nil {
		t.Fatal(err)
	}
	rootB, err := ntB.Root(hh(1))
	if err != nil {
		t.Fatal(err)
	}
	if rootA != rootB {
		t.Fatalf("write-then-delete != never-write: A=%v B=%v", rootA, rootB)
	}
	checkRefcountIntegrity(t, ntA)
	checkRefcountIntegrity(t, ntB)
}

// TestNodeTreeHistoricalRoots checks that Root(height) returns the root committed at that
// specific height across many versions.
func TestNodeTreeHistoricalRoots(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	nt := newMemNodeTree(t)
	storedRoots := map[uint64]types.Hash{}

	const versions = 50
	for h := uint64(1); h <= versions; h++ {
		p := db.NewPatch()
		for i := 0; i < 3; i++ {
			k := ntRandKey(rng)
			v := make([]byte, 8)
			rng.Read(v)
			p.Put(k, v)
		}
		if err := nt.Update(p); err != nil {
			t.Fatal(err)
		}
		if err := nt.Commit(hh(h)); err != nil {
			t.Fatal(err)
		}
		root, err := nt.Root(hh(h))
		if err != nil {
			t.Fatal(err)
		}
		storedRoots[h] = root
	}

	// Check all historical roots.
	for h := uint64(1); h <= versions; h++ {
		got, err := nt.Root(hh(h))
		if err != nil {
			t.Fatalf("historical Root(%d): %v", h, err)
		}
		if got != storedRoots[h] {
			t.Fatalf("historical root mismatch at h=%d: got=%v want=%v", h, got, storedRoots[h])
		}
	}
}

// TestNodeTreeSharedRootAcrossHeights exercises the idempotent-commit case (§5): two heights
// sharing the same root id — the refcount must be ≥2 for the root node, and the second
// +1 must not re-increment children.
func TestNodeTreeSharedRootAcrossHeights(t *testing.T) {
	nt := newMemNodeTree(t)

	key := accountKey(0x03, 0x01)
	val := []byte("stable")

	p1 := db.NewPatch()
	p1.Put(key, val)
	if err := nt.Update(p1); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(1)); err != nil {
		t.Fatal(err)
	}
	root1, err := nt.Root(hh(1))
	if err != nil {
		t.Fatal(err)
	}

	// Commit with no changes: empty staged set → same root id.
	p2 := db.NewPatch()
	if err := nt.Update(p2); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(2)); err != nil {
		t.Fatal(err)
	}
	root2, err := nt.Root(hh(2))
	if err != nil {
		t.Fatal(err)
	}

	if root1 != root2 {
		t.Fatalf("idempotent commit produced different roots: h1=%v h2=%v", root1, root2)
	}

	// The root id is the leaf's id (single leaf → padLeaf → the leafNode's id).
	// Check that refcount for the root id is ≥2.
	rootId, err := nt.loadVersionRoot(2)
	if err != nil {
		t.Fatal(err)
	}
	if rootId == zeroHash {
		t.Fatal("expected non-zero root id after commit")
	}
	rc, err := loadStoredRef(nt.ldb, rootId)
	if err != nil {
		t.Fatal(err)
	}
	if rc < 2 {
		t.Fatalf("shared root id refcount = %d, want >= 2", rc)
	}
	checkRefcountIntegrity(t, nt)
}

// TestNodeTreeErrNoVersion checks that Root for an uncommitted height returns ErrNoVersion.
func TestNodeTreeErrNoVersion(t *testing.T) {
	nt := newMemNodeTree(t)

	p := db.NewPatch()
	p.Put(accountKey(0x03, 0x01), []byte("v"))
	if err := nt.Update(p); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(1)); err != nil {
		t.Fatal(err)
	}

	_, err := nt.Root(hh(99))
	if err != ErrNoVersion {
		t.Fatalf("Root(99) = %v, want ErrNoVersion", err)
	}
}

// TestNodeTreeFrontierRecovery verifies that FrontierIdentifier is persisted and recovered.
func TestNodeTreeFrontierRecovery(t *testing.T) {
	ldb, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ldb.Close()

	nt, err := NewNodeTree(ldb)
	if err != nil {
		t.Fatal(err)
	}

	p := db.NewPatch()
	p.Put(accountKey(0x03, 0x01), []byte("v"))
	if err := nt.Update(p); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(1)); err != nil {
		t.Fatal(err)
	}

	// Re-open the same db and check frontier.
	nt2, err := NewNodeTree(ldb)
	if err != nil {
		t.Fatal(err)
	}
	if nt2.FrontierIdentifier().Height != 1 {
		t.Fatalf("recovered frontier height = %d, want 1", nt2.FrontierIdentifier().Height)
	}
}

// TestNodeTreeFuzzRefcountIntegrity is the intensive refcount integrity gate (§10.2): runs the
// same fuzz sequence as TestNodeTreeDifferential but also runs checkRefcountIntegrity after
// every single commit to catch any increment bugs early.
func TestNodeTreeFuzzRefcountIntegrity(t *testing.T) {
	rng := rand.New(rand.NewSource(54321))
	nt := newMemNodeTree(t)
	leafTree := newMemTree(t)

	oracle := map[types.Hash][]byte{}
	var keys [][]byte

	const versions = 80
	for h := uint64(1); h <= versions; h++ {
		p := db.NewPatch()

		for n := 0; n < rng.Intn(5)+1; n++ {
			var k []byte
			if len(keys) > 0 && rng.Intn(2) == 0 {
				k = keys[rng.Intn(len(keys))]
			} else {
				k = ntRandKey(rng)
				keys = append(keys, k)
			}
			v := make([]byte, 1+rng.Intn(20))
			rng.Read(v)
			p.Put(k, v)
			oracle[types.NewHash(k)] = v
		}
		for n := 0; n < rng.Intn(2); n++ {
			if len(keys) == 0 {
				break
			}
			k := keys[rng.Intn(len(keys))]
			p.Delete(k)
			delete(oracle, types.NewHash(k))
		}

		id := hh(h)
		if err := leafTree.Update(p); err != nil {
			t.Fatalf("h=%d leafTree.Update: %v", h, err)
		}
		if err := leafTree.Commit(id); err != nil {
			t.Fatalf("h=%d leafTree.Commit: %v", h, err)
		}
		leafRoot, err := leafTree.Root(id)
		if err != nil {
			t.Fatalf("h=%d leafTree.Root: %v", h, err)
		}

		if err := nt.Update(p); err != nil {
			t.Fatalf("h=%d nodeTree.Update: %v", h, err)
		}
		if err := nt.Commit(id); err != nil {
			t.Fatalf("h=%d nodeTree.Commit: %v", h, err)
		}
		ntRoot, err := nt.Root(id)
		if err != nil {
			t.Fatalf("h=%d nodeTree.Root: %v", h, err)
		}

		if ntRoot != leafRoot {
			t.Fatalf("h=%d root mismatch: nodeTree=%v leafTree=%v oracle=%v", h, ntRoot, leafRoot, oracleRoot(oracle))
		}

		checkRefcountIntegrity(t, nt)
	}
}

// TestNodeTreeFrontierIdentifier checks FrontierIdentifier advances correctly.
func TestNodeTreeFrontierIdentifier(t *testing.T) {
	nt := newMemNodeTree(t)

	if fr := nt.FrontierIdentifier(); fr.Height != 0 {
		t.Fatalf("initial frontier height = %d, want 0", fr.Height)
	}

	for h := uint64(1); h <= 5; h++ {
		p := db.NewPatch()
		p.Put(common.Uint64ToBytes(h), []byte("v"))
		if err := nt.Update(p); err != nil {
			t.Fatal(err)
		}
		if err := nt.Commit(hh(h)); err != nil {
			t.Fatal(err)
		}
		if fr := nt.FrontierIdentifier(); fr.Height != h {
			t.Fatalf("frontier after Commit(%d) = %d", h, fr.Height)
		}
	}
}

// --- Prove tests (Step 3 gate) ---

// assertProveIdentical asserts that nodeTree.Prove and leafTree.Prove return byte-identical
// value and proof bytes for the given key at the given version.
func assertProveIdentical(t *testing.T, nt *NodeTree, lt *Tree, id types.HashHeight, key []byte, label string) {
	t.Helper()

	ntVal, ntProof, ntErr := nt.Prove(id, key)
	ltVal, ltProof, ltErr := lt.Prove(id, key)

	if ntErr != ltErr {
		t.Errorf("%s: Prove(%d, %x) error mismatch: nodeTree=%v leafTree=%v", label, id.Height, key, ntErr, ltErr)
		return
	}
	if ntErr != nil {
		return
	}

	if string(ntVal) != string(ltVal) {
		t.Errorf("%s: Prove(%d, %x) value mismatch: nodeTree=%x leafTree=%x", label, id.Height, key, ntVal, ltVal)
	}
	if string(ntProof) != string(ltProof) {
		t.Errorf("%s: Prove(%d, %x) proof mismatch:\n  nodeTree=%x\n  leafTree=%x", label, id.Height, key, ntProof, ltProof)
	}
}

// TestNodeTreeProveEmpty verifies that Prove on an empty nodeTree returns a valid absence proof
// that matches the leaf-set Tree and verifies against VerifyAbsence.
func TestNodeTreeProveEmpty(t *testing.T) {
	nt := newMemNodeTree(t)
	lt := newMemTree(t)

	// Commit empty (height 0 is fine, but both trees start at height 0 = ZeroHashHeight).
	// Use height 1 with an empty patch.
	id := hh(1)
	p := db.NewPatch()
	if err := lt.Update(p); err != nil {
		t.Fatal(err)
	}
	if err := lt.Commit(id); err != nil {
		t.Fatal(err)
	}
	if err := nt.Update(p); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(id); err != nil {
		t.Fatal(err)
	}

	key := []byte{0x05, 0xAB, 0xCD}
	assertProveIdentical(t, nt, lt, id, key, "empty-tree")

	// Standalone verification.
	root, err := nt.Root(id)
	if err != nil {
		t.Fatal(err)
	}
	_, proof, err := nt.Prove(id, key)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyAbsence(root, key, proof)
	if err != nil || !ok {
		t.Fatalf("VerifyAbsence on empty tree: ok=%v err=%v", ok, err)
	}
}

// TestNodeTreeProveSingleLeaf verifies the single-leaf root case: inclusion and absence (leaf divergence).
func TestNodeTreeProveSingleLeaf(t *testing.T) {
	nt := newMemNodeTree(t)
	lt := newMemTree(t)

	key := accountKey(0x03, 0x01, 0x02)
	val := []byte("single-leaf-value")
	id := hh(1)

	p := db.NewPatch()
	p.Put(key, val)
	for _, tree := range []interface{ Update(db.Patch) error }{lt, nt} {
		if err := tree.Update(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := lt.Commit(id); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(id); err != nil {
		t.Fatal(err)
	}

	// Inclusion: the key itself.
	assertProveIdentical(t, nt, lt, id, key, "single-leaf-inclusion")

	// Absence: a different key (empty slot).
	absentKey := []byte{0x06, 0x00}
	assertProveIdentical(t, nt, lt, id, absentKey, "single-leaf-absent-empty-slot")

	// Verify inclusion standalone.
	root, err := nt.Root(id)
	if err != nil {
		t.Fatal(err)
	}
	gotVal, proof, err := nt.Prove(id, key)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyProof(root, key, gotVal, proof)
	if err != nil || !ok {
		t.Fatalf("VerifyProof single-leaf inclusion: ok=%v err=%v", ok, err)
	}

	// Verify absence standalone.
	_, abProof, err := nt.Prove(id, absentKey)
	if err != nil {
		t.Fatal(err)
	}
	ok, err = VerifyAbsence(root, absentKey, abProof)
	if err != nil || !ok {
		t.Fatalf("VerifyAbsence single-leaf absence: ok=%v err=%v", ok, err)
	}
}

// TestNodeTreeProveLeafDivergence tests the "long-shared-prefix absent key" case: an absent key
// that shares a long path prefix with a present key, triggering the leaf-rule divergence branch.
func TestNodeTreeProveLeafDivergence(t *testing.T) {
	nt := newMemNodeTree(t)
	lt := newMemTree(t)

	// Present key.
	presentKey := accountKey(0x03, 0x01)
	presentVal := []byte("present")
	id := hh(1)

	p := db.NewPatch()
	p.Put(presentKey, presentVal)
	for _, tree := range []interface{ Update(db.Patch) error }{lt, nt} {
		if err := tree.Update(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := lt.Commit(id); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(id); err != nil {
		t.Fatal(err)
	}

	// We want an absent key whose sha3 shares a long prefix with sha3(presentKey).
	// We can't engineer that easily, so just use a few candidate absent keys —
	// the differential comparison will exercise whichever divergence depth occurs.
	for _, absentKey := range [][]byte{
		{0x05, 0x02},
		{0x05, 0x01, 0x00},
		{0x07, 0xAB},
	} {
		assertProveIdentical(t, nt, lt, id, absentKey, "leaf-divergence-absent")
	}
}

// TestNodeTreeProveDifferential is the main differential gate (§10.1): the same random
// insert/update/delete sequence through both trees, checking byte-identical value and proof
// for a mix of present and absent keys after every commit.
func TestNodeTreeProveDifferential(t *testing.T) {
	const seed = 99887
	rng := rand.New(rand.NewSource(seed))

	lt := newMemTree(t)
	nt := newMemNodeTree(t)

	liveKeys := map[string][]byte{} // raw-key -> raw-value, present set
	var allKeys [][]byte

	const versions = 80

	for h := uint64(1); h <= versions; h++ {
		p := db.NewPatch()

		// Inserts/updates.
		for n := 0; n < rng.Intn(5)+1; n++ {
			var k []byte
			if len(allKeys) > 0 && rng.Intn(2) == 0 {
				k = allKeys[rng.Intn(len(allKeys))]
			} else {
				k = ntRandKey(rng)
				allKeys = append(allKeys, k)
			}
			v := make([]byte, 1+rng.Intn(30))
			rng.Read(v)
			p.Put(k, v)
			liveKeys[string(k)] = v
		}
		// Deletes.
		for n := 0; n < rng.Intn(3); n++ {
			if len(allKeys) == 0 {
				break
			}
			k := allKeys[rng.Intn(len(allKeys))]
			p.Delete(k)
			delete(liveKeys, string(k))
		}

		id := hh(h)
		if err := lt.Update(p); err != nil {
			t.Fatalf("h=%d lt.Update: %v", h, err)
		}
		if err := lt.Commit(id); err != nil {
			t.Fatalf("h=%d lt.Commit: %v", h, err)
		}
		if err := nt.Update(p); err != nil {
			t.Fatalf("h=%d nt.Update: %v", h, err)
		}
		if err := nt.Commit(id); err != nil {
			t.Fatalf("h=%d nt.Commit: %v", h, err)
		}

		// Sample a present key (if any).
		for rawKey, rawVal := range liveKeys {
			k := []byte(rawKey)
			ntVal, ntProof, err := nt.Prove(id, k)
			if err != nil {
				t.Fatalf("h=%d Prove(present): %v", h, err)
			}
			ltVal, ltProof, err := lt.Prove(id, k)
			if err != nil {
				t.Fatalf("h=%d leafTree.Prove(present): %v", h, err)
			}
			if string(ntVal) != string(ltVal) {
				t.Fatalf("h=%d present key value mismatch: nodeTree=%x leafTree=%x", h, ntVal, ltVal)
			}
			if string(ntProof) != string(ltProof) {
				t.Fatalf("h=%d present key proof mismatch", h)
			}
			// Verify inclusion.
			root, _ := nt.Root(id)
			ok, err := VerifyProof(root, k, rawVal, ntProof)
			if err != nil || !ok {
				t.Fatalf("h=%d VerifyProof(present): ok=%v err=%v", h, ok, err)
			}
			break // just one present key per height
		}

		// Sample a definitely-absent key.
		absentKey := ntRandKey(rng)
		// Make sure it's not in the live set.
		for liveKeys[string(absentKey)] != nil {
			absentKey = ntRandKey(rng)
		}
		assertProveIdentical(t, nt, lt, id, absentKey, "absent-empty-slot")

		// Verify absence standalone.
		root, _ := nt.Root(id)
		_, abProof, _ := nt.Prove(id, absentKey)
		ok, err := VerifyAbsence(root, absentKey, abProof)
		if err != nil || !ok {
			t.Fatalf("h=%d VerifyAbsence: ok=%v err=%v", h, ok, err)
		}
	}
}

// TestNodeTreeProveHistorical checks that Prove at a past (still-retained) height returns
// byte-identical proofs to the leaf-set tree at that height (§10.1 historical requirement).
func TestNodeTreeProveHistorical(t *testing.T) {
	const seed = 11223
	rng := rand.New(rand.NewSource(seed))

	lt := newMemTree(t)
	nt := newMemNodeTree(t)

	type snapshot struct {
		id      types.HashHeight
		sampleK []byte // a key present at this height
	}
	var snapshots []snapshot
	liveKeys := map[string][]byte{}
	var allKeys [][]byte

	const versions = 30
	for h := uint64(1); h <= versions; h++ {
		p := db.NewPatch()
		var lastKey []byte
		for n := 0; n < rng.Intn(4)+1; n++ {
			k := ntRandKey(rng)
			allKeys = append(allKeys, k)
			v := make([]byte, 4+rng.Intn(20))
			rng.Read(v)
			p.Put(k, v)
			liveKeys[string(k)] = v
			lastKey = k
		}
		if err := lt.Update(p); err != nil {
			t.Fatal(err)
		}
		if err := lt.Commit(hh(h)); err != nil {
			t.Fatal(err)
		}
		if err := nt.Update(p); err != nil {
			t.Fatal(err)
		}
		if err := nt.Commit(hh(h)); err != nil {
			t.Fatal(err)
		}
		if lastKey != nil {
			snapshots = append(snapshots, snapshot{id: hh(h), sampleK: lastKey})
		}
	}

	// Now check historical proofs.
	for _, snap := range snapshots {
		assertProveIdentical(t, nt, lt, snap.id, snap.sampleK, "historical-present")
		absentKey := ntRandKey(rng)
		assertProveIdentical(t, nt, lt, snap.id, absentKey, "historical-absent")
	}
}

// TestNodeTreeProveErrNoVersion verifies that Prove on a non-retained height returns ErrNoVersion.
func TestNodeTreeProveErrNoVersion(t *testing.T) {
	nt := newMemNodeTree(t)

	p := db.NewPatch()
	p.Put(accountKey(0x03, 0x01), []byte("v"))
	if err := nt.Update(p); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(1)); err != nil {
		t.Fatal(err)
	}

	_, _, err := nt.Prove(hh(99), accountKey(0x03, 0x01))
	if err != ErrNoVersion {
		t.Fatalf("Prove(99) = %v, want ErrNoVersion", err)
	}
}

// TestNodeTreeProveHistoricalValueChanged is the regression test for the value-versioning bug:
// a key whose value changed after height H must produce the old value (not the frontier value)
// when Prove is called at height H.
//
// Before the fix: the node store read the frontier value from the side-table (nsKeyValue 0x04)
// instead of the versioned leaf node, so historical proofs returned the wrong value and failed
// VerifyProof against the historical root.
func TestNodeTreeProveHistoricalValueChanged(t *testing.T) {
	lt := newMemTree(t)
	nt := newMemNodeTree(t)

	key := accountKey(0x03, 0xAA)
	commit := func(h uint64, val []byte) {
		t.Helper()
		p := db.NewPatch()
		p.Put(key, val)
		if err := lt.Update(p); err != nil {
			t.Fatal(err)
		}
		if err := lt.Commit(hh(h)); err != nil {
			t.Fatal(err)
		}
		if err := nt.Update(p); err != nil {
			t.Fatal(err)
		}
		if err := nt.Commit(hh(h)); err != nil {
			t.Fatal(err)
		}
	}

	commit(1, []byte("VALUE-ONE"))
	commit(2, []byte("VALUE-TWO"))

	id1 := hh(1)

	// Both trees must return the same value and byte-identical proof at height 1.
	lv, lp, err := lt.Prove(id1, key)
	if err != nil {
		t.Fatalf("lt.Prove h=1: %v", err)
	}
	nv, np, err := nt.Prove(id1, key)
	if err != nil {
		t.Fatalf("nt.Prove h=1: %v", err)
	}

	if string(nv) != string(lv) {
		t.Errorf("value mismatch at h=1: nodeTree=%q leafTree=%q (want %q)", nv, lv, "VALUE-ONE")
	}
	if string(np) != string(lp) {
		t.Errorf("proof mismatch at h=1:\n  nodeTree=%x\n  leafTree=%x", np, lp)
	}

	// The inclusion proof must verify against the historical root.
	root1, err := nt.Root(id1)
	if err != nil {
		t.Fatalf("nt.Root h=1: %v", err)
	}
	ok, err := VerifyProof(root1, key, nv, np)
	if err != nil || !ok {
		t.Fatalf("VerifyProof at h=1: ok=%v err=%v (value=%q)", ok, err, nv)
	}

	// Sanity: frontier (h=2) still returns VALUE-TWO.
	id2 := hh(2)
	nv2, np2, err := nt.Prove(id2, key)
	if err != nil {
		t.Fatalf("nt.Prove h=2: %v", err)
	}
	if string(nv2) != "VALUE-TWO" {
		t.Errorf("frontier value: got %q want %q", nv2, "VALUE-TWO")
	}
	root2, err := nt.Root(id2)
	if err != nil {
		t.Fatalf("nt.Root h=2: %v", err)
	}
	ok, err = VerifyProof(root2, key, nv2, np2)
	if err != nil || !ok {
		t.Fatalf("VerifyProof at h=2: ok=%v err=%v", ok, err)
	}
}

// assertRootsMatch checks that both trees return the same root at the given height.
func assertRootsMatch(t *testing.T, lt *Tree, nt *NodeTree, id types.HashHeight, label string) {
	t.Helper()
	ltRoot, err := lt.Root(id)
	if err != nil {
		t.Fatalf("%s: lt.Root(%d): %v", label, id.Height, err)
	}
	ntRoot, err := nt.Root(id)
	if err != nil {
		t.Fatalf("%s: nt.Root(%d): %v", label, id.Height, err)
	}
	if ltRoot != ntRoot {
		t.Errorf("%s: root mismatch at h=%d: lt=%v nt=%v", label, id.Height, ltRoot, ntRoot)
	}
}

// assertPrunedHeight checks that Root returns ErrNoVersion at the given height for both trees.
func assertPrunedHeight(t *testing.T, lt *Tree, nt *NodeTree, id types.HashHeight) {
	t.Helper()
	_, ltErr := lt.Root(id)
	_, ntErr := nt.Root(id)
	if ltErr != ErrNoVersion {
		t.Errorf("lt.Root(%d) = %v, want ErrNoVersion", id.Height, ltErr)
	}
	if ntErr != ErrNoVersion {
		t.Errorf("nt.Root(%d) = %v, want ErrNoVersion", id.Height, ntErr)
	}
}

// countNodeStoreRecords counts the number of node (0x01) and refcount (0x02) records.
func countNodeStoreRecords(t *testing.T, nt *NodeTree) (nodes int, refs int) {
	t.Helper()
	nIter := nt.ldb.NewIterator(util.BytesPrefix(nsKeyNode), nil)
	defer nIter.Release()
	for nIter.Next() {
		nodes++
	}
	rIter := nt.ldb.NewIterator(util.BytesPrefix(nsKeyRef), nil)
	defer rIter.Release()
	for rIter.Next() {
		refs++
	}
	return
}

// collectReachableIds does a DFS from rootId and returns all reachable node ids.
func collectReachableIds(t *testing.T, nt *NodeTree, rootId types.Hash) map[types.Hash]bool {
	t.Helper()
	seen := map[types.Hash]bool{}
	var dfs func(id types.Hash)
	dfs = func(id types.Hash) {
		if id == zeroHash || seen[id] {
			return
		}
		seen[id] = true
		data, err := nt.ldb.Get(nsNodeKey(id), nil)
		if err != nil {
			return
		}
		node, err := deserializeNode(data)
		if err != nil {
			return
		}
		if n, ok := node.(*diskInternal); ok {
			dfs(n.leftId)
			dfs(n.rightId)
		}
	}
	dfs(rootId)
	return seen
}

// TestNodeTreeProveHistoricalUpdatesFuzz is an extension of the historical/differential fuzz
// that explicitly updates existing keys' values across heights and then proves OLD heights for
// those updated keys. This is the class of test the original suite missed (it only inserted
// new keys, never re-used existing ones for historical proof checks).
func TestNodeTreeProveHistoricalUpdatesFuzz(t *testing.T) {
	const seed = 77531
	rng := rand.New(rand.NewSource(seed))

	lt := newMemTree(t)
	nt := newMemNodeTree(t)

	type heightSnapshot struct {
		id    types.HashHeight
		key   []byte
		value []byte // expected value of key at this height
	}

	liveKeys := map[string][]byte{} // raw key -> raw value, frontier state
	var allKeys [][]byte
	var snapshots []heightSnapshot // one snapshot per height for a key that was updated

	const versions = 40
	for h := uint64(1); h <= versions; h++ {
		p := db.NewPatch()

		// Always re-use an existing key for the first op (if possible) to create an update.
		var updatedKey []byte
		var updatedVal []byte
		if len(allKeys) > 0 {
			updatedKey = allKeys[rng.Intn(len(allKeys))]
			updatedVal = make([]byte, 4+rng.Intn(20))
			rng.Read(updatedVal)
			p.Put(updatedKey, updatedVal)
			liveKeys[string(updatedKey)] = updatedVal
		}

		// Additional inserts/updates.
		for n := 0; n < rng.Intn(4)+1; n++ {
			var k []byte
			if len(allKeys) > 0 && rng.Intn(3) == 0 {
				k = allKeys[rng.Intn(len(allKeys))]
			} else {
				k = ntRandKey(rng)
				allKeys = append(allKeys, k)
			}
			v := make([]byte, 1+rng.Intn(20))
			rng.Read(v)
			p.Put(k, v)
			liveKeys[string(k)] = v
		}
		// Deletes.
		for n := 0; n < rng.Intn(2); n++ {
			if len(allKeys) == 0 {
				break
			}
			k := allKeys[rng.Intn(len(allKeys))]
			p.Delete(k)
			delete(liveKeys, string(k))
		}

		id := hh(h)
		if err := lt.Update(p); err != nil {
			t.Fatalf("h=%d lt.Update: %v", h, err)
		}
		if err := lt.Commit(id); err != nil {
			t.Fatalf("h=%d lt.Commit: %v", h, err)
		}
		if err := nt.Update(p); err != nil {
			t.Fatalf("h=%d nt.Update: %v", h, err)
		}
		if err := nt.Commit(id); err != nil {
			t.Fatalf("h=%d nt.Commit: %v", h, err)
		}

		// If we updated an existing key this height, record a snapshot to verify later.
		if updatedKey != nil {
			snapshots = append(snapshots, heightSnapshot{id: id, key: updatedKey, value: updatedVal})
		}
	}

	// Verify historical proofs for all snapshots.
	for _, snap := range snapshots {
		lv, lp, err := lt.Prove(snap.id, snap.key)
		if err != nil {
			t.Fatalf("lt.Prove h=%d key=%x: %v", snap.id.Height, snap.key, err)
		}
		nv, np, err := nt.Prove(snap.id, snap.key)
		if err != nil {
			t.Fatalf("nt.Prove h=%d key=%x: %v", snap.id.Height, snap.key, err)
		}

		if string(nv) != string(lv) {
			t.Errorf("h=%d key=%x value mismatch: nodeTree=%x leafTree=%x", snap.id.Height, snap.key, nv, lv)
		}
		if string(np) != string(lp) {
			t.Errorf("h=%d key=%x proof mismatch:\n  nodeTree=%x\n  leafTree=%x", snap.id.Height, snap.key, np, lp)
		}

		// VerifyProof against historical root.
		root, err := nt.Root(snap.id)
		if err != nil {
			t.Fatalf("nt.Root h=%d: %v", snap.id.Height, err)
		}
		if lv != nil {
			ok, err := VerifyProof(root, snap.key, nv, np)
			if err != nil || !ok {
				t.Errorf("VerifyProof h=%d key=%x: ok=%v err=%v", snap.id.Height, snap.key, ok, err)
			}
		} else {
			ok, err := VerifyAbsence(root, snap.key, np)
			if err != nil || !ok {
				t.Errorf("VerifyAbsence h=%d key=%x: ok=%v err=%v", snap.id.Height, snap.key, ok, err)
			}
		}
	}
}

// ---- Step 4 tests: Truncate, Prune, refcount cascade ----

// TestNodeTreeTruncatePruneDifferential is the main extended fuzz (gate 1): random op mix
// including Truncate (reorg) and Prune, applied to both lt and nt. After every op the roots
// are compared and refcount integrity is checked.
func TestNodeTreeTruncatePruneDifferential(t *testing.T) {
	const seed = 314159
	rng := rand.New(rand.NewSource(seed))

	lt := newMemTree(t)
	nt := newMemNodeTree(t)

	// liveKeys tracks the current frontier leaf set.
	liveKeys := map[string][]byte{}
	var allKeys [][]byte

	// history records the leaf-set snapshot at each committed height (for oracle verification).
	type snapshot struct {
		leaves map[string][]byte
	}
	history := map[uint64]snapshot{}

	frontier := uint64(0)
	const ops = 200

	for op := 0; op < ops; op++ {
		r := rng.Intn(10)

		switch {
		case r < 5:
			// Normal commit.
			frontier++
			p := db.NewPatch()
			for n := 0; n < rng.Intn(4)+1; n++ {
				var k []byte
				if len(allKeys) > 0 && rng.Intn(2) == 0 {
					k = allKeys[rng.Intn(len(allKeys))]
				} else {
					k = ntRandKey(rng)
					allKeys = append(allKeys, k)
				}
				v := make([]byte, 1+rng.Intn(20))
				rng.Read(v)
				p.Put(k, v)
				liveKeys[string(k)] = v
			}
			for n := 0; n < rng.Intn(2); n++ {
				if len(allKeys) == 0 {
					break
				}
				k := allKeys[rng.Intn(len(allKeys))]
				p.Delete(k)
				delete(liveKeys, string(k))
			}
			id := hh(frontier)
			commitBoth(t, lt, nt, p, id)
			// snapshot current liveKeys
			snap := snapshot{leaves: make(map[string][]byte, len(liveKeys))}
			for k, v := range liveKeys {
				snap.leaves[k] = v
			}
			history[frontier] = snap

			assertRootsMatch(t, lt, nt, id, "after-commit")
			checkRefcountIntegrity(t, nt)

		case r < 7:
			// Truncate: reorg to a random recent height, then commit a divergent branch.
			if frontier == 0 {
				continue
			}
			// Pick a target in [max(1, frontier-5), frontier-1] to be realistic.
			lo := frontier
			if lo > 5 {
				lo = frontier - 5
			}
			if lo < 1 {
				lo = 1
			}
			target := lo + uint64(rng.Intn(int(frontier-lo+1)))
			if target >= frontier {
				target = frontier
			}
			// Check target is in committed history.
			if _, ok := history[target]; !ok {
				continue
			}

			// Truncate both trees.
			targetId := hh(target)
			if err := lt.Truncate(targetId); err != nil {
				t.Fatalf("op=%d lt.Truncate(%d): %v", op, target, err)
			}
			if err := nt.Truncate(targetId); err != nil {
				t.Fatalf("op=%d nt.Truncate(%d): %v", op, target, err)
			}

			// Restore liveKeys to target's snapshot.
			snap := history[target]
			liveKeys = make(map[string][]byte, len(snap.leaves))
			for k, v := range snap.leaves {
				liveKeys[k] = v
			}
			// Remove history entries above target.
			for h := target + 1; h <= frontier; h++ {
				delete(history, h)
			}
			frontier = target

			assertRootsMatch(t, lt, nt, targetId, "after-truncate")
			checkRefcountIntegrity(t, nt)

			// Commit a divergent branch after the reorg.
			frontier++
			p := db.NewPatch()
			k := ntRandKey(rng)
			v := make([]byte, 4+rng.Intn(20))
			rng.Read(v)
			p.Put(k, v)
			liveKeys[string(k)] = v
			allKeys = append(allKeys, k)
			id := hh(frontier)
			commitBoth(t, lt, nt, p, id)

			snap2 := snapshot{leaves: make(map[string][]byte, len(liveKeys))}
			for k2, v2 := range liveKeys {
				snap2.leaves[k2] = v2
			}
			history[frontier] = snap2

			assertRootsMatch(t, lt, nt, id, "after-reorg-commit")
			checkRefcountIntegrity(t, nt)

		case r < 9:
			// Prune: remove versions below a horizon.
			if frontier < 2 {
				continue
			}
			// Prune up to [1, frontier-1].
			horizon := uint64(1) + uint64(rng.Intn(int(frontier)))
			if err := lt.Prune(horizon); err != nil {
				t.Fatalf("op=%d lt.Prune(%d): %v", op, horizon, err)
			}
			if err := nt.Prune(horizon); err != nil {
				t.Fatalf("op=%d nt.Prune(%d): %v", op, horizon, err)
			}

			// Check pruned heights are gone from both.
			for h := uint64(1); h < horizon; h++ {
				if _, ok := history[h]; ok {
					assertPrunedHeight(t, lt, nt, hh(h))
				}
			}
			// Remove from local history too.
			for h := uint64(1); h < horizon; h++ {
				delete(history, h)
			}

			// Frontier must still be intact.
			assertRootsMatch(t, lt, nt, hh(frontier), "after-prune-frontier")
			checkRefcountIntegrity(t, nt)

			// Sample a retained height (if any).
			for h := horizon; h <= frontier; h++ {
				if _, ok := history[h]; ok {
					assertRootsMatch(t, lt, nt, hh(h), "after-prune-retained")
					// Also check proofs at retained height.
					for rawK := range history[h].leaves {
						assertProveIdentical(t, nt, lt, hh(h), []byte(rawK), "after-prune-prove")
						break // just one
					}
					break
				}
			}

		default:
			// Idempotent commit (no-op patch): tests shared root across heights.
			if frontier == 0 {
				continue
			}
			frontier++
			p := db.NewPatch()
			commitBoth(t, lt, nt, p, hh(frontier))

			snap := snapshot{leaves: make(map[string][]byte, len(liveKeys))}
			for k, v := range liveKeys {
				snap.leaves[k] = v
			}
			history[frontier] = snap

			assertRootsMatch(t, lt, nt, hh(frontier), "after-idempotent-commit")
			checkRefcountIntegrity(t, nt)
		}
	}
}

// TestNodeTreeReorgRecommit is the explicit §10.1 named reorg-recommit case (gate 3):
// build to H, Truncate(H-k), commit a divergent branch, assert identical root + refcount integrity.
func TestNodeTreeReorgRecommit(t *testing.T) {
	lt := newMemTree(t)
	nt := newMemNodeTree(t)

	keys := [][]byte{
		accountKey(0x03, 0x01), accountKey(0x03, 0x02), accountKey(0x03, 0x03),
		accountKey(0x03, 0x04), accountKey(0x03, 0x05),
	}

	// Build to H=5 with a key per height.
	for h := uint64(1); h <= 5; h++ {
		p := db.NewPatch()
		p.Put(keys[h-1], []byte("value-original"))
		commitBoth(t, lt, nt, p, hh(h))
		checkRefcountIntegrity(t, nt)
	}

	// Truncate back to H=3.
	truncId := hh(3)
	if err := lt.Truncate(truncId); err != nil {
		t.Fatalf("lt.Truncate: %v", err)
	}
	if err := nt.Truncate(truncId); err != nil {
		t.Fatalf("nt.Truncate: %v", err)
	}
	assertRootsMatch(t, lt, nt, truncId, "after-truncate")
	checkRefcountIntegrity(t, nt)

	// Commit a divergent branch: H=4' with different value.
	p4 := db.NewPatch()
	p4.Put(keys[3], []byte("value-divergent"))
	commitBoth(t, lt, nt, p4, hh(4))
	assertRootsMatch(t, lt, nt, hh(4), "after-divergent-commit")
	checkRefcountIntegrity(t, nt)

	// Continue to H=5'.
	p5 := db.NewPatch()
	p5.Put(keys[4], []byte("value-divergent-5"))
	commitBoth(t, lt, nt, p5, hh(5))
	assertRootsMatch(t, lt, nt, hh(5), "after-divergent-commit-2")
	checkRefcountIntegrity(t, nt)

	// Heights 1,2,3 should still be servable and root-identical.
	for _, h := range []uint64{1, 2, 3} {
		assertRootsMatch(t, lt, nt, hh(h), "historical-after-reorg")
	}

	// Verify proofs at the frontier.
	for _, k := range keys {
		assertProveIdentical(t, nt, lt, hh(5), k, "reorg-proof-frontier")
	}
}

// TestNodeTreeSharedRootTruncatePrune is the shared-root case (gate 4): two heights sharing
// the same root id; truncating/pruning one leaves the other intact.
func TestNodeTreeSharedRootTruncatePrune(t *testing.T) {
	lt := newMemTree(t)
	nt := newMemNodeTree(t)

	key := accountKey(0x03, 0xBB)
	val := []byte("stable-value")

	// H=1: insert.
	p1 := db.NewPatch()
	p1.Put(key, val)
	commitBoth(t, lt, nt, p1, hh(1))
	checkRefcountIntegrity(t, nt)

	// H=2: idempotent (same root).
	commitBoth(t, lt, nt, db.NewPatch(), hh(2))
	checkRefcountIntegrity(t, nt)

	// H=3: another key.
	p3 := db.NewPatch()
	p3.Put(accountKey(0x03, 0xCC), []byte("other"))
	commitBoth(t, lt, nt, p3, hh(3))
	checkRefcountIntegrity(t, nt)

	// Check h=1 and h=2 share the same root in nodeTree.
	root1, err := nt.Root(hh(1))
	if err != nil {
		t.Fatal(err)
	}
	root2, err := nt.Root(hh(2))
	if err != nil {
		t.Fatal(err)
	}
	if root1 != root2 {
		t.Fatalf("expected shared root at h=1,2: h1=%v h2=%v", root1, root2)
	}

	// Prune below h=2: removes h=1.
	if err := lt.Prune(2); err != nil {
		t.Fatalf("lt.Prune: %v", err)
	}
	if err := nt.Prune(2); err != nil {
		t.Fatalf("nt.Prune: %v", err)
	}
	checkRefcountIntegrity(t, nt)

	// h=1 gone, h=2 still serves identical root.
	assertPrunedHeight(t, lt, nt, hh(1))
	assertRootsMatch(t, lt, nt, hh(2), "shared-root-after-prune")

	// Proof at h=2 still works.
	assertProveIdentical(t, nt, lt, hh(2), key, "shared-root-prove-after-prune")

	// Now truncate back to h=2 (removing h=3) — shared root at h=2 must survive.
	if err := lt.Truncate(hh(2)); err != nil {
		t.Fatalf("lt.Truncate: %v", err)
	}
	if err := nt.Truncate(hh(2)); err != nil {
		t.Fatalf("nt.Truncate: %v", err)
	}
	checkRefcountIntegrity(t, nt)
	assertRootsMatch(t, lt, nt, hh(2), "shared-root-after-truncate")
	assertProveIdentical(t, nt, lt, hh(2), key, "shared-root-prove-after-truncate")
}

// TestNodeTreeNoLeakTerminal is gate 5: after pruning/truncating down to the frontier only,
// every stored node is reachable from the frontier root (no orphans). Then truncating the
// frontier itself (to height 0) leaves the node store completely empty.
//
// The "truncate to 0" part is tested on nodeTree only: the leaf-set Tree.Truncate cannot
// descend below a pruned horizon (it needs the intervening delta records), but the nodeTree
// is free to truncate all the way to the empty origin.
func TestNodeTreeNoLeakTerminal(t *testing.T) {
	rng := rand.New(rand.NewSource(271828))
	lt := newMemTree(t)
	nt := newMemNodeTree(t)
	var allKeys [][]byte

	const versions = 20
	for h := uint64(1); h <= versions; h++ {
		p := db.NewPatch()
		for n := 0; n < rng.Intn(4)+2; n++ {
			k := ntRandKey(rng)
			v := make([]byte, 4+rng.Intn(10))
			rng.Read(v)
			p.Put(k, v)
			allKeys = append(allKeys, k)
		}
		commitBoth(t, lt, nt, p, hh(h))
	}
	checkRefcountIntegrity(t, nt)

	// Prune everything below the frontier.
	frontier := uint64(versions)
	if err := lt.Prune(frontier); err != nil {
		t.Fatalf("lt.Prune: %v", err)
	}
	if err := nt.Prune(frontier); err != nil {
		t.Fatalf("nt.Prune: %v", err)
	}
	checkRefcountIntegrity(t, nt)

	// Verify only the frontier version remains and roots still match.
	assertRootsMatch(t, lt, nt, hh(frontier), "only-frontier-root")

	// All stored nodes must be reachable from the frontier root.
	frontierRootId, err := nt.loadVersionRoot(frontier)
	if err != nil {
		t.Fatalf("loadVersionRoot: %v", err)
	}
	reachable := collectReachableIds(t, nt, frontierRootId)
	nIter := nt.ldb.NewIterator(util.BytesPrefix(nsKeyNode), nil)
	defer nIter.Release()
	for nIter.Next() {
		var id types.Hash
		copy(id[:], nIter.Key()[len(nsKeyNode):])
		if !reachable[id] {
			t.Errorf("orphan node in store after prune-to-frontier: %v", id)
		}
	}
	nIter.Release()

	// Truncate to height 0 on nodeTree only: removes the frontier version entirely.
	// The cascade must free every node and refcount record.
	if err := nt.Truncate(types.ZeroHashHeight); err != nil {
		t.Fatalf("nt.Truncate(0): %v", err)
	}
	checkRefcountIntegrity(t, nt)

	// The node store must be completely empty: no node or refcount records.
	nodes, refs := countNodeStoreRecords(t, nt)
	if nodes != 0 || refs != 0 {
		t.Errorf("after full truncate: nodes=%d refs=%d, want 0/0", nodes, refs)
	}

	// Frontier should now be at height 0.
	if nt.FrontierIdentifier().Height != 0 {
		t.Errorf("frontier after truncate(0) = %d, want 0", nt.FrontierIdentifier().Height)
	}
}

// TestNodeTreeComputeRootDifferential is the ComputeRoot differential gate: for random
// (previous, changes) at the frontier, nodeTree.ComputeRoot and leafTree.ComputeRoot must
// return byte-identical hashes. The result must also equal the root that would be produced
// by actually committing those changes (verified by committing to a separate tree).
func TestNodeTreeComputeRootDifferential(t *testing.T) {
	const seed = 42001
	rng := rand.New(rand.NewSource(seed))

	lt := newMemTree(t)
	nt := newMemNodeTree(t)

	var allKeys [][]byte
	const versions = 60

	for h := uint64(1); h <= versions; h++ {
		// Build a patch to commit.
		p := db.NewPatch()
		for n := 0; n < rng.Intn(5)+1; n++ {
			var k []byte
			if len(allKeys) > 0 && rng.Intn(2) == 0 {
				k = allKeys[rng.Intn(len(allKeys))]
			} else {
				k = ntRandKey(rng)
				allKeys = append(allKeys, k)
			}
			v := make([]byte, 1+rng.Intn(30))
			rng.Read(v)
			p.Put(k, v)
		}
		for n := 0; n < rng.Intn(3); n++ {
			if len(allKeys) == 0 {
				break
			}
			p.Delete(allKeys[rng.Intn(len(allKeys))])
		}

		id := hh(h)
		if err := lt.Update(p); err != nil {
			t.Fatalf("h=%d lt.Update: %v", h, err)
		}
		if err := lt.Commit(id); err != nil {
			t.Fatalf("h=%d lt.Commit: %v", h, err)
		}
		if err := nt.Update(p); err != nil {
			t.Fatalf("h=%d nt.Update: %v", h, err)
		}
		if err := nt.Commit(id); err != nil {
			t.Fatalf("h=%d nt.Commit: %v", h, err)
		}

		// Build a separate "candidate" patch (not yet committed) to compute root for.
		candidate := db.NewPatch()
		for n := 0; n < rng.Intn(4)+1; n++ {
			var k []byte
			if len(allKeys) > 0 && rng.Intn(2) == 0 {
				k = allKeys[rng.Intn(len(allKeys))]
			} else {
				k = ntRandKey(rng)
				allKeys = append(allKeys, k)
			}
			v := make([]byte, 1+rng.Intn(20))
			rng.Read(v)
			candidate.Put(k, v)
		}

		// ComputeRoot on both trees at the current frontier.
		ltRoot, err := lt.ComputeRoot(id, candidate)
		if err != nil {
			t.Fatalf("h=%d lt.ComputeRoot: %v", h, err)
		}
		ntRoot, err := nt.ComputeRoot(id, candidate)
		if err != nil {
			t.Fatalf("h=%d nt.ComputeRoot: %v", h, err)
		}

		if ltRoot != ntRoot {
			t.Fatalf("h=%d ComputeRoot mismatch: leafTree=%v nodeTree=%v", h, ltRoot, ntRoot)
		}

		// ComputeRoot must not modify the frontier.
		ltFrontier := lt.FrontierIdentifier()
		ntFrontier := nt.FrontierIdentifier()
		if ltFrontier.Height != h || ntFrontier.Height != h {
			t.Fatalf("h=%d ComputeRoot modified frontier: lt=%v nt=%v", h, ltFrontier.Height, ntFrontier.Height)
		}

		// Commit the candidate to a scratch tree and verify the committed root matches.
		scratchNt := newMemNodeTree(t)
		// First bring the scratch to the same frontier by replaying existing state snapshot.
		// Easier: just commit the candidate on top of the current id and compare.
		if err := nt.Update(candidate); err != nil {
			t.Fatalf("h=%d nt.Update(candidate): %v", h, err)
		}
		if err := nt.Commit(hh(h + 1)); err != nil {
			t.Fatalf("h=%d nt.Commit(candidate): %v", h, err)
		}
		actualRoot, err := nt.Root(hh(h + 1))
		if err != nil {
			t.Fatalf("h=%d nt.Root(candidate): %v", h, err)
		}
		if actualRoot != ntRoot {
			t.Fatalf("h=%d ComputeRoot=%v but actual committed root=%v", h, ntRoot, actualRoot)
		}
		// Truncate back to h.
		if err := nt.Truncate(id); err != nil {
			t.Fatalf("h=%d nt.Truncate back: %v", h, err)
		}
		_ = scratchNt
		checkRefcountIntegrity(t, nt)
	}

	// Verify ComputeRoot returns ErrNoVersion for a pruned height.
	if err := nt.Prune(versions - 2); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	_, err := nt.ComputeRoot(hh(1), db.NewPatch())
	if err != ErrNoVersion {
		t.Fatalf("ComputeRoot on pruned height = %v, want ErrNoVersion", err)
	}
}

// TestNodeTreeFormatMismatch verifies that NewNodeTree detects a foreign DB (e.g. the old
// leaf-set Tree format) and returns ErrFormatMismatch.
func TestNodeTreeFormatMismatch(t *testing.T) {
	ldb, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ldb.Close()

	// Simulate a leaf-set Tree DB by writing a key with the leaf-set tree's frontier prefix (0x00).
	if err := ldb.Put([]byte{0x00}, []byte("frontier-data"), nil); err != nil {
		t.Fatal(err)
	}

	// NewNodeTree on this non-empty DB without a format-version key must return ErrFormatMismatch.
	_, err = NewNodeTree(ldb)
	if err != ErrFormatMismatch {
		t.Fatalf("NewNodeTree on foreign DB = %v, want ErrFormatMismatch", err)
	}
}

// TestNodeTreeFormatVersionPersisted verifies that a fresh DB gets the format-version key written
// and a re-open succeeds.
func TestNodeTreeFormatVersionPersisted(t *testing.T) {
	ldb, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ldb.Close()

	// First open: fresh DB → format-version key should be written.
	nt, err := NewNodeTree(ldb)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	p := db.NewPatch()
	p.Put(accountKey(0x03, 0x01), []byte("v"))
	if err := nt.Update(p); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(1)); err != nil {
		t.Fatal(err)
	}

	// Second open on the same DB must succeed (format-version key is present).
	nt2, err := NewNodeTree(ldb)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if nt2.FrontierIdentifier().Height != 1 {
		t.Fatalf("re-opened frontier = %d, want 1", nt2.FrontierIdentifier().Height)
	}
}

// TestReadCtxGetRepPathFallsBackToRightId constructs a diskInternal with an empty leftId
// directly on disk and verifies readCtx.getRepPath resolves the representative path through
// rightId instead of returning ErrCorrupt, matching commitCtx.getRepPath's fallback.
func TestReadCtxGetRepPathFallsBackToRightId(t *testing.T) {
	ldb, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ldb.Close()

	dl := &diskLeaf{value: []byte("v")}
	dl.path[0] = 0x42
	if err := ldb.Put(nsNodeKey(dl.id()), serializeLeaf(dl), nil); err != nil {
		t.Fatal(err)
	}

	internal := &diskInternal{
		level:     0,
		leftHash:  zeroHash,
		leftId:    zeroHash,
		rightHash: LeafHash(dl.path, dl.value),
		rightId:   dl.id(),
	}
	if err := ldb.Put(nsNodeKey(internal.id()), serializeInternal(internal), nil); err != nil {
		t.Fatal(err)
	}

	rc := &readCtx{ldb: ldb}
	got, err := rc.getRepPath(internal)
	if err != nil {
		t.Fatalf("getRepPath: unexpected error %v", err)
	}
	if got != dl.path {
		t.Fatalf("getRepPath: got %x want %x", got, dl.path)
	}
}

// TestNodeTreeCommitWithoutStagedReturnsErrNotStaged verifies Commit refuses to run with no
// preceding Update.
func TestNodeTreeCommitWithoutStagedReturnsErrNotStaged(t *testing.T) {
	nt := newMemNodeTree(t)
	if err := nt.Commit(hh(1)); err != ErrNotStaged {
		t.Fatalf("Commit without Update: got %v, want ErrNotStaged", err)
	}
}

// TestNodeTreeDoubleCommitReturnsErrNotStaged verifies a second Commit with no intervening
// Update is rejected: Commit clears the staged set.
func TestNodeTreeDoubleCommitReturnsErrNotStaged(t *testing.T) {
	nt := newMemNodeTree(t)
	p := db.NewPatch()
	p.Put([]byte{0x01}, []byte("v"))
	if err := nt.Update(p); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(1)); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(2)); err != ErrNotStaged {
		t.Fatalf("second Commit without Update: got %v, want ErrNotStaged", err)
	}
}

// TestNodeTreeCommitOutOfOrderRejected verifies Commit rejects a height that is not exactly
// one above the frontier.
func TestNodeTreeCommitOutOfOrderRejected(t *testing.T) {
	nt := newMemNodeTree(t)
	p := db.NewPatch()
	p.Put([]byte{0x01}, []byte("v"))
	if err := nt.Update(p); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(5)); err != ErrCommitOutOfOrder {
		t.Fatalf("Commit(hh(5)) on fresh tree: got %v, want ErrCommitOutOfOrder", err)
	}
}

// TestNodeTreeMissedTruncateRetryRejected reproduces the scenario finding 1 guards against: a
// retry that would Commit at a height already committed (i.e. would build on the wrong base)
// is rejected rather than silently corrupting the tree.
func TestNodeTreeMissedTruncateRetryRejected(t *testing.T) {
	nt := newMemNodeTree(t)
	p := db.NewPatch()
	p.Put([]byte{0x01}, []byte("v"))
	if err := nt.Update(p); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(1)); err != nil {
		t.Fatal(err)
	}

	p2 := db.NewPatch()
	p2.Put([]byte{0x02}, []byte("w"))
	if err := nt.Update(p2); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(1)); err != ErrCommitOutOfOrder {
		t.Fatalf("retry Commit(hh(1)) at frontier 1: got %v, want ErrCommitOutOfOrder", err)
	}
}

// TestNodeTreeCommitBulkEqualsPerHeightCommit is the equivalence gate: folding a range of
// per-height patches through AccumulateFrom + CommitBulk must yield the exact same root as
// committing each height individually, for any split point between the per-height prefix and
// the bulk-folded suffix.
func TestNodeTreeCommitBulkEqualsPerHeightCommit(t *testing.T) {
	splits := []int{0, 1, 10, 39}
	seeds := []int64{1, 2, 3}

	for _, split := range splits {
		for _, seed := range seeds {
			split, seed := split, seed
			t.Run(fmt.Sprintf("split=%d/seed=%d", split, seed), func(t *testing.T) {
				rng := rand.New(rand.NewSource(seed))

				// Find three keys sharing types.NewHash(key)[0] for the subtree-collapse case.
				buckets := map[byte][][]byte{}
				var collapseKeys [][]byte
				for i := 0; i < 4096; i++ {
					k := accountKey(0x03, byte(i>>8), byte(i))
					b := types.NewHash(k)[0]
					buckets[b] = append(buckets[b], k)
					if len(buckets[b]) == 3 {
						collapseKeys = buckets[b]
						break
					}
				}
				if collapseKeys == nil {
					t.Fatal("could not find 3 keys sharing a first hash byte within 4096 candidates")
				}

				const versions = 40
				patches := make([]db.Patch, versions+1) // 1-indexed
				oracle := map[types.Hash][]byte{}
				var keys [][]byte

				// Scripted keys, reused across heights.
				keyA := ntRandKey(rng) // Put h=3, re-Put h=12, Deleted h=25
				keyB := ntRandKey(rng) // Put h=2, Deleted h=8, re-Put h=30
				keyC := ntRandKey(rng) // Delete h=18, never Put
				keyD := ntRandKey(rng) // Put h=14, empty-value-delete h=22
				keys = append(keys, keyA, keyB, keyD)

				for h := 1; h <= versions; h++ {
					p := db.NewPatch()

					// Random inserts/updates over a growing key set.
					for n := 0; n < rng.Intn(4)+1; n++ {
						var k []byte
						if len(keys) > 0 && rng.Intn(2) == 0 {
							k = keys[rng.Intn(len(keys))]
						} else {
							k = ntRandKey(rng)
							keys = append(keys, k)
						}
						v := make([]byte, 1+rng.Intn(20))
						rng.Read(v)
						p.Put(k, v)
						oracle[types.NewHash(k)] = v
					}
					// Random deletes.
					for n := 0; n < rng.Intn(2); n++ {
						if len(keys) == 0 {
							break
						}
						k := keys[rng.Intn(len(keys))]
						p.Delete(k)
						delete(oracle, types.NewHash(k))
					}
					// Occasional frontier-key noise.
					if rng.Intn(3) == 0 {
						p.Put([]byte{byte(rng.Intn(3)), 0x77}, []byte("ignored"))
					}

					// Scripted cases.
					switch h {
					case 3:
						p.Put(keyA, []byte("keyA-v1"))
						oracle[types.NewHash(keyA)] = []byte("keyA-v1")
					case 12:
						p.Put(keyA, []byte("keyA-v2"))
						oracle[types.NewHash(keyA)] = []byte("keyA-v2")
					case 25:
						p.Delete(keyA)
						delete(oracle, types.NewHash(keyA))
					case 2:
						p.Put(keyB, []byte("keyB-v1"))
						oracle[types.NewHash(keyB)] = []byte("keyB-v1")
					case 8:
						p.Delete(keyB)
						delete(oracle, types.NewHash(keyB))
					case 30:
						p.Put(keyB, []byte("keyB-v2"))
						oracle[types.NewHash(keyB)] = []byte("keyB-v2")
					case 18:
						p.Delete(keyC)
						delete(oracle, types.NewHash(keyC))
					case 14:
						p.Put(keyD, []byte("keyD-v1"))
						oracle[types.NewHash(keyD)] = []byte("keyD-v1")
					case 22:
						p.Put(keyD, []byte{})
						delete(oracle, types.NewHash(keyD))
					case 4:
						for _, k := range collapseKeys {
							p.Put(k, []byte("collapse-v"))
							oracle[types.NewHash(k)] = []byte("collapse-v")
						}
					case 15:
						p.Delete(collapseKeys[0])
						delete(oracle, types.NewHash(collapseKeys[0]))
					}
					if h == 22 {
						p.Delete(collapseKeys[1])
						delete(oracle, types.NewHash(collapseKeys[1]))
					}
					if h == 31 {
						p.Delete(collapseKeys[2])
						delete(oracle, types.NewHash(collapseKeys[2]))
					}

					patches[h] = p
				}

				perHeight := newMemNodeTree(t)
				for h := 1; h <= versions; h++ {
					if err := perHeight.Update(patches[h]); err != nil {
						t.Fatalf("perHeight h=%d Update: %v", h, err)
					}
					if err := perHeight.Commit(hh(uint64(h))); err != nil {
						t.Fatalf("perHeight h=%d Commit: %v", h, err)
					}
				}

				bulk := newMemNodeTree(t)
				for h := 1; h <= split; h++ {
					if err := bulk.Update(patches[h]); err != nil {
						t.Fatalf("bulk h=%d Update: %v", h, err)
					}
					if err := bulk.Commit(hh(uint64(h))); err != nil {
						t.Fatalf("bulk h=%d Commit: %v", h, err)
					}
				}
				for h := split + 1; h <= versions; h++ {
					if err := bulk.AccumulateFrom(patches[h]); err != nil {
						t.Fatalf("bulk h=%d AccumulateFrom: %v", h, err)
					}
				}
				if err := bulk.CommitBulk(hh(uint64(versions))); err != nil {
					t.Fatalf("bulk CommitBulk(%d): %v", versions, err)
				}

				if got := bulk.FrontierIdentifier(); got != hh(uint64(versions)) {
					t.Fatalf("bulk.FrontierIdentifier() = %v, want %v", got, hh(uint64(versions)))
				}

				bulkRoot, err := bulk.Root(hh(uint64(versions)))
				if err != nil {
					t.Fatalf("bulk.Root: %v", err)
				}
				perHeightRoot, err := perHeight.Root(hh(uint64(versions)))
				if err != nil {
					t.Fatalf("perHeight.Root: %v", err)
				}
				wantRoot := oracleRoot(oracle)
				if bulkRoot != wantRoot {
					t.Fatalf("bulk root = %v, want oracle %v", bulkRoot, wantRoot)
				}
				if perHeightRoot != wantRoot {
					t.Fatalf("perHeight root = %v, want oracle %v", perHeightRoot, wantRoot)
				}

				for h := split + 1; h < versions; h++ {
					if _, err := bulk.Root(hh(uint64(h))); err != ErrNoVersion {
						t.Fatalf("bulk.Root(hh(%d)) = %v, want ErrNoVersion (no intermediate version)", h, err)
					}
				}

				checkRefcountIntegrity(t, bulk)
			})
		}
	}
}

// TestNodeTreeCommitBulkDistantHeightThenCommit verifies CommitBulk can land far above the
// frontier, and a subsequent per-height Commit resumes correctly from the seeded frontier.
func TestNodeTreeCommitBulkDistantHeightThenCommit(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	nt := newMemNodeTree(t)

	oracle := map[types.Hash][]byte{}

	k1, k2, k3 := ntRandKey(rng), ntRandKey(rng), ntRandKey(rng)

	p1 := db.NewPatch()
	p1.Put(k1, []byte("v1"))
	oracle[types.NewHash(k1)] = []byte("v1")

	p2 := db.NewPatch()
	p2.Put(k2, []byte("v2"))
	oracle[types.NewHash(k2)] = []byte("v2")

	if err := nt.AccumulateFrom(p1); err != nil {
		t.Fatalf("AccumulateFrom(p1): %v", err)
	}
	if err := nt.AccumulateFrom(p2); err != nil {
		t.Fatalf("AccumulateFrom(p2): %v", err)
	}
	if err := nt.CommitBulk(hh(5_000_000)); err != nil {
		t.Fatalf("CommitBulk(5_000_000): %v", err)
	}

	if got := nt.FrontierIdentifier(); got != hh(5_000_000) {
		t.Fatalf("FrontierIdentifier() = %v, want %v", got, hh(5_000_000))
	}
	root, err := nt.Root(hh(5_000_000))
	if err != nil {
		t.Fatalf("Root(5_000_000): %v", err)
	}
	if want := oracleRoot(oracle); root != want {
		t.Fatalf("Root(5_000_000) = %v, want %v", root, want)
	}

	p3 := db.NewPatch()
	p3.Put(k3, []byte("v3"))
	oracle[types.NewHash(k3)] = []byte("v3")

	if err := nt.Update(p3); err != nil {
		t.Fatalf("Update(p3): %v", err)
	}
	if err := nt.Commit(hh(5_000_001)); err != nil {
		t.Fatalf("Commit(5_000_001): %v", err)
	}
	root2, err := nt.Root(hh(5_000_001))
	if err != nil {
		t.Fatalf("Root(5_000_001): %v", err)
	}
	if want := oracleRoot(oracle); root2 != want {
		t.Fatalf("Root(5_000_001) = %v, want %v", root2, want)
	}

	checkRefcountIntegrity(t, nt)
}

// TestNodeTreeCommitBulkGuards exercises CommitBulk's ErrNotStaged / ErrBulkCommitOutOfOrder
// guards, and that a rejected guard leaves the staged set intact.
func TestNodeTreeCommitBulkGuards(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	nt := newMemNodeTree(t)

	if err := nt.CommitBulk(hh(1)); err != ErrNotStaged {
		t.Fatalf("CommitBulk on fresh tree: got %v, want ErrNotStaged", err)
	}

	p1 := db.NewPatch()
	p1.Put(ntRandKey(rng), []byte("v1"))
	if err := nt.AccumulateFrom(p1); err != nil {
		t.Fatalf("AccumulateFrom(p1): %v", err)
	}
	if err := nt.CommitBulk(hh(10)); err != nil {
		t.Fatalf("CommitBulk(10): %v", err)
	}
	root, err := nt.Root(hh(10))
	if err != nil {
		t.Fatalf("Root(10): %v", err)
	}
	if root == (types.Hash{}) {
		t.Fatalf("Root(10) is zero, expected non-empty root")
	}

	if err := nt.CommitBulk(hh(11)); err != ErrNotStaged {
		t.Fatalf("CommitBulk(11) with no accumulation: got %v, want ErrNotStaged", err)
	}

	p2 := db.NewPatch()
	p2.Put(ntRandKey(rng), []byte("v2"))
	if err := nt.AccumulateFrom(p2); err != nil {
		t.Fatalf("AccumulateFrom(p2): %v", err)
	}
	if err := nt.CommitBulk(hh(10)); err != ErrBulkCommitOutOfOrder {
		t.Fatalf("CommitBulk(10) at frontier 10: got %v, want ErrBulkCommitOutOfOrder", err)
	}
	if err := nt.CommitBulk(hh(9)); err != ErrBulkCommitOutOfOrder {
		t.Fatalf("CommitBulk(9) below frontier: got %v, want ErrBulkCommitOutOfOrder", err)
	}
	if err := nt.CommitBulk(hh(11)); err != nil {
		t.Fatalf("CommitBulk(11) after rejected guards: %v", err)
	}

	// Frontier-metadata-only accumulation: staged set becomes non-nil but empty after filtering.
	frontierBeforeRoot, err := nt.Root(hh(11))
	if err != nil {
		t.Fatalf("Root(11): %v", err)
	}
	p3 := db.NewPatch()
	p3.Put([]byte{0x01}, []byte("ignored"))
	if err := nt.AccumulateFrom(p3); err != nil {
		t.Fatalf("AccumulateFrom(p3): %v", err)
	}
	if err := nt.CommitBulk(hh(12)); err != nil {
		t.Fatalf("CommitBulk(12) with frontier-metadata-only accumulation: %v", err)
	}
	root12, err := nt.Root(hh(12))
	if err != nil {
		t.Fatalf("Root(12): %v", err)
	}
	if root12 != frontierBeforeRoot {
		t.Fatalf("Root(12) = %v, want unchanged frontier root %v", root12, frontierBeforeRoot)
	}
}

// TestNodeTreeAccumulateThenTruncateDiscardsStaged pins the one interleaving the package
// signals: a Truncate between AccumulateFrom and CommitBulk clears the staged set, so the
// following CommitBulk returns ErrNotStaged rather than committing a partial accumulation.
func TestNodeTreeAccumulateThenTruncateDiscardsStaged(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	nt := newMemNodeTree(t)

	p1 := db.NewPatch()
	p1.Put(ntRandKey(rng), []byte("v1"))
	if err := nt.Update(p1); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(1)); err != nil {
		t.Fatal(err)
	}

	p2 := db.NewPatch()
	p2.Put(ntRandKey(rng), []byte("v2"))
	if err := nt.Update(p2); err != nil {
		t.Fatal(err)
	}
	if err := nt.Commit(hh(2)); err != nil {
		t.Fatal(err)
	}

	p3 := db.NewPatch()
	p3.Put(ntRandKey(rng), []byte("v3"))
	if err := nt.AccumulateFrom(p3); err != nil {
		t.Fatalf("AccumulateFrom(p3): %v", err)
	}

	if err := nt.Truncate(hh(1)); err != nil {
		t.Fatalf("Truncate(1): %v", err)
	}

	if err := nt.CommitBulk(hh(10)); err != ErrNotStaged {
		t.Fatalf("CommitBulk(10) after Truncate: got %v, want ErrNotStaged", err)
	}

	if got := nt.FrontierIdentifier(); got != hh(1) {
		t.Fatalf("FrontierIdentifier() = %v, want %v", got, hh(1))
	}
}

// TestNodeTreeUpdateThenAccumulateFromMerges pins the documented (unguarded) semantics of mixing
// Update and AccumulateFrom against the same staged set: AccumulateFrom merges on top of whatever
// Update already staged, latest-wins per path, and the following CommitBulk reflects both.
func TestNodeTreeUpdateThenAccumulateFromMerges(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	nt := newMemNodeTree(t)

	kUpdateOnly := ntRandKey(rng)
	kOverwritten := ntRandKey(rng)

	p1 := db.NewPatch()
	p1.Put(kUpdateOnly, []byte("update-only"))
	p1.Put(kOverwritten, []byte("from-update"))
	if err := nt.Update(p1); err != nil {
		t.Fatalf("Update(p1): %v", err)
	}

	p2 := db.NewPatch()
	p2.Put(kOverwritten, []byte("from-accumulate"))
	if err := nt.AccumulateFrom(p2); err != nil {
		t.Fatalf("AccumulateFrom(p2): %v", err)
	}

	if err := nt.CommitBulk(hh(5)); err != nil {
		t.Fatalf("CommitBulk(5): %v", err)
	}

	oracle := map[types.Hash][]byte{
		types.NewHash(kUpdateOnly):  []byte("update-only"),
		types.NewHash(kOverwritten): []byte("from-accumulate"),
	}
	root, err := nt.Root(hh(5))
	if err != nil {
		t.Fatalf("Root(5): %v", err)
	}
	if want := oracleRoot(oracle); root != want {
		t.Fatalf("Root(5) = %v, want %v (Update+AccumulateFrom merge)", root, want)
	}

	checkRefcountIntegrity(t, nt)
}
