package trie

import (
	"math/rand"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"

	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
)

func newMemTree(t *testing.T) *Tree {
	t.Helper()
	ldb, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatalf("open memdb: %v", err)
	}
	t.Cleanup(func() { _ = ldb.Close() })
	tr, err := NewTree(ldb)
	if err != nil {
		t.Fatalf("NewTree: %v", err)
	}
	return tr
}

// oracleSubtree is an independent reference root computation: it partitions by explicit
// filtering at each level (rather than the sorted-slice + binary-search path used by the
// production subtreeHash), so the two agreeing is a real cross-check of the structural logic.
func oracleSubtree(items []leaf, level int) types.Hash {
	switch len(items) {
	case 0:
		return emptyHash
	case 1:
		return oraclePad(items[0], level)
	}
	var left, right []leaf
	for _, it := range items {
		if pathBit(it.path, level) == 0 {
			left = append(left, it)
		} else {
			right = append(right, it)
		}
	}
	return InternalHash(oracleSubtree(left, level+1), oracleSubtree(right, level+1))
}

func oraclePad(l leaf, level int) types.Hash {
	h := LeafHash(l.path, l.value)
	for lvl := treeDepth - 1; lvl >= level; lvl-- {
		if pathBit(l.path, lvl) == 0 {
			h = InternalHash(h, emptyHash)
		} else {
			h = InternalHash(emptyHash, h)
		}
	}
	return h
}

func oracleRoot(m map[types.Hash][]byte) types.Hash {
	if len(m) == 0 {
		return emptyHash
	}
	items := make([]leaf, 0, len(m))
	for p, v := range m {
		items = append(items, leaf{path: p, value: v})
	}
	return oracleSubtree(items, 0)
}

// accountKey returns a fold-included key: {3} || addr(20) || sub || tail. sub is a balance
// (0x03) or contract-storage (0x04) sub-prefix. It is the shape the state-root fold commits to;
// tests use it wherever a written key must survive the fold. Distinct tails give distinct paths.
func accountKey(sub byte, tail ...byte) []byte {
	k := make([]byte, 1+types.AddressSize+1+len(tail))
	k[0] = 0x03
	k[1+types.AddressSize] = sub
	copy(k[1+types.AddressSize+1:], tail)
	return k
}

func randKey(rng *rand.Rand) []byte {
	// A fold-included balance key: {3} || addr(20) || {3} || rand(8). The random address and
	// tail give well-distributed sha3 paths; the fixed prefixes keep it inside the fold whitelist.
	k := make([]byte, 1+types.AddressSize+1+8)
	k[0] = 0x03
	k[1+types.AddressSize] = 0x03
	rng.Read(k[1 : 1+types.AddressSize])
	rng.Read(k[1+types.AddressSize+1:])
	return k
}

func hh(height uint64) types.HashHeight {
	return types.HashHeight{Hash: types.NewHash([]byte{byte(height), byte(height >> 8)}), Height: height}
}

// TestFuzzAgainstOracle drives random insert/update/delete sequences across versions and
// checks the tree's root equals an independently-computed reference after every commit. It
// covers history-independence (write-then-delete == never-write) and the per-version storage.
func TestFuzzAgainstOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	tr := newMemTree(t)

	oracle := map[types.Hash][]byte{}
	keys := [][]byte{}
	roots := map[uint64]types.Hash{}

	const versions = 200
	for h := uint64(1); h <= versions; h++ {
		p := db.NewPatch()

		// Inserts / updates.
		for n := 0; n < rng.Intn(6); n++ {
			var k []byte
			if len(keys) > 0 && rng.Intn(2) == 0 {
				k = keys[rng.Intn(len(keys))] // update existing
			} else {
				k = randKey(rng)
				keys = append(keys, k)
			}
			v := make([]byte, 1+rng.Intn(40))
			rng.Read(v)
			p.Put(k, v)
			oracle[types.NewHash(k)] = v
		}
		// Deletes.
		for n := 0; n < rng.Intn(3); n++ {
			if len(keys) == 0 {
				break
			}
			k := keys[rng.Intn(len(keys))]
			p.Delete(k)
			delete(oracle, types.NewHash(k))
		}
		// Occasionally include a frontier-metadata key, which must not affect the root.
		if rng.Intn(3) == 0 {
			p.Put([]byte{byte(rng.Intn(3)), 0x77}, []byte("ignored"))
		}

		// ComputeRoot (verifier path) must predict the committed root.
		predicted, err := tr.ComputeRoot(tr.FrontierIdentifier(), p)
		if err != nil {
			t.Fatalf("h=%d ComputeRoot: %v", h, err)
		}

		if err := tr.Update(p); err != nil {
			t.Fatalf("h=%d Update: %v", h, err)
		}
		if err := tr.Commit(hh(h)); err != nil {
			t.Fatalf("h=%d Commit: %v", h, err)
		}
		got, err := tr.Root(hh(h))
		if err != nil {
			t.Fatalf("h=%d Root: %v", h, err)
		}

		want := oracleRoot(oracle)
		if got != want {
			t.Fatalf("h=%d root mismatch: tree=%v oracle=%v", h, got, want)
		}
		if predicted != want {
			t.Fatalf("h=%d ComputeRoot predicted %v, committed %v", h, predicted, want)
		}
		roots[h] = got

		// Spot-check proofs for present and absent keys.
		if len(keys) > 0 {
			k := keys[rng.Intn(len(keys))]
			value, proof, err := tr.Prove(hh(h), k)
			if err != nil {
				t.Fatalf("h=%d Prove: %v", h, err)
			}
			if _, present := oracle[types.NewHash(k)]; present {
				ok, err := VerifyProof(got, k, value, proof)
				if err != nil || !ok {
					t.Fatalf("h=%d inclusion verify failed: ok=%v err=%v", h, ok, err)
				}
			} else {
				ok, err := VerifyAbsence(got, k, proof)
				if err != nil || !ok {
					t.Fatalf("h=%d absence verify failed: ok=%v err=%v", h, ok, err)
				}
			}
		}
		// An always-absent key.
		absent := []byte{0x09, 0xde, 0xad, 0xbe, 0xef}
		if _, present := oracle[types.NewHash(absent)]; !present {
			_, proof, err := tr.Prove(hh(h), absent)
			if err != nil {
				t.Fatalf("h=%d Prove(absent): %v", h, err)
			}
			ok, err := VerifyAbsence(got, absent, proof)
			if err != nil || !ok {
				t.Fatalf("h=%d absent verify failed: ok=%v err=%v", h, ok, err)
			}
		}
	}

	// Historical roots remain readable.
	for h := uint64(1); h <= versions; h++ {
		got, err := tr.Root(hh(h))
		if err != nil {
			t.Fatalf("Root(%d): %v", h, err)
		}
		if got != roots[h] {
			t.Fatalf("historical root %d changed", h)
		}
	}
}

func TestTruncate(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	tr := newMemTree(t)
	roots := map[uint64]types.Hash{}

	for h := uint64(1); h <= 50; h++ {
		p := db.NewPatch()
		k := randKey(rng)
		v := make([]byte, 8)
		rng.Read(v)
		p.Put(k, v)
		if err := tr.Update(p); err != nil {
			t.Fatal(err)
		}
		if err := tr.Commit(hh(h)); err != nil {
			t.Fatal(err)
		}
		root, err := tr.Root(hh(h))
		if err != nil {
			t.Fatal(err)
		}
		roots[h] = root
	}

	if err := tr.Truncate(hh(30)); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if fr := tr.FrontierIdentifier(); fr.Height != 30 {
		t.Fatalf("frontier after truncate = %d, want 30", fr.Height)
	}
	if got, err := tr.Root(hh(30)); err != nil || got != roots[30] {
		t.Fatalf("root at 30 after truncate: got %v err %v", got, err)
	}
	if _, err := tr.Root(hh(31)); err != ErrNoVersion {
		t.Fatalf("version 31 should be gone, got err %v", err)
	}

	// Re-commit a divergent branch on top of the truncated frontier.
	p := db.NewPatch()
	p.Put(accountKey(0x03, 0x31), []byte("new-branch"))
	if err := tr.Update(p); err != nil {
		t.Fatal(err)
	}
	if err := tr.Commit(hh(31)); err != nil {
		t.Fatalf("re-commit 31: %v", err)
	}
}

// buildSeq builds a tree by replaying a deterministic (seed-driven) sequence of patches and
// returns it plus the root committed at every height.
func buildSeq(t *testing.T, seed int64, n uint64) (*Tree, map[uint64]types.Hash) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	tr := newMemTree(t)
	roots := map[uint64]types.Hash{}
	for h := uint64(1); h <= n; h++ {
		p := db.NewPatch()
		for i := 0; i < 3; i++ {
			k := randKey(rng)
			v := make([]byte, 8)
			rng.Read(v)
			p.Put(k, v)
		}
		if err := tr.Update(p); err != nil {
			t.Fatal(err)
		}
		if err := tr.Commit(hh(h)); err != nil {
			t.Fatal(err)
		}
		root, err := tr.Root(hh(h))
		if err != nil {
			t.Fatal(err)
		}
		roots[h] = root
	}
	return tr, roots
}

// TestPruneLosslessAndNotRetained is the Phase 4 gate (§8/§11.8): pruning makes old heights
// unservable but never changes a retained root, still allows a reorg within the rollback
// window, and is lossless — a rebuild reproduces every root, including pruned heights.
func TestPruneLosslessAndNotRetained(t *testing.T) {
	const n = 120
	const horizon = 100
	tr, roots := buildSeq(t, 42, n)

	if err := tr.Prune(horizon); err != nil {
		t.Fatal(err)
	}

	// Pruned heights are not retained.
	if _, err := tr.Root(hh(50)); err != ErrNoVersion {
		t.Fatalf("pruned Root(50) = %v, want ErrNoVersion", err)
	}
	if _, _, err := tr.Prove(hh(50), []byte{0x03, 0x01}); err != ErrNoVersion {
		t.Fatalf("pruned Prove(50) = %v, want ErrNoVersion", err)
	}
	// Retained heights are unchanged; the frontier root is untouched by pruning.
	if got, err := tr.Root(hh(110)); err != nil || got != roots[110] {
		t.Fatalf("retained Root(110): got %v err %v", got, err)
	}
	if got, err := tr.Root(hh(n)); err != nil || got != roots[n] {
		t.Fatalf("frontier root changed by prune")
	}

	// A reorg within the rollback window still succeeds after pruning.
	if err := tr.Truncate(hh(115)); err != nil {
		t.Fatalf("Truncate(115) after prune: %v", err)
	}
	if got, err := tr.Root(hh(115)); err != nil || got != roots[115] {
		t.Fatalf("root after post-prune truncate mismatch")
	}

	// Lossless: a fresh rebuild of the same sequence reproduces every root, pruned or not.
	_, roots2 := buildSeq(t, 42, n)
	for h := uint64(1); h <= n; h++ {
		if roots2[h] != roots[h] {
			t.Fatalf("rebuild root mismatch at height %d", h)
		}
	}
}

// TestFoldEquivalence is the fold-equivalence gate: the SAME logical write-set folded as
// a clean transaction.Changes object (only fold-included account-store keys) and as a synthetic
// post-Replay/GetPatch object that DOES carry every excluded category must produce identical
// roots.
func TestFoldEquivalence(t *testing.T) {
	apps := map[string][]byte{
		string(accountKey(0x03, 0x01)): []byte("balance"),
		string(accountKey(0x04, 0x02)): []byte("storage"),
	}

	clean := db.NewPatch()
	for k, v := range apps {
		clean.Put([]byte(k), v)
	}

	dirty := db.NewPatch()
	for k, v := range apps {
		dirty.Put([]byte(k), v)
	}
	// Every excluded category: momentum history, mailbox, the ZNN cache, an index, and a
	// short account-store key too short to carry a sub-prefix.
	dirty.Put([]byte{0x00}, []byte("frontier-identifier"))
	dirty.Put([]byte{0x01, 0xab, 0xcd}, []byte("height-by-hash"))
	dirty.Put([]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}, []byte("entry-by-height"))
	dirty.Put([]byte{0x04, 0x02}, []byte("mailbox"))
	dirty.Put([]byte{0x08, 0x03}, []byte("znn-cache"))
	dirty.Put([]byte{0x09, 0x03}, []byte("index"))
	dirty.Put([]byte{0x03, 0x01}, []byte("short-account-key"))

	cleanTree := newMemTree(t)
	if err := cleanTree.Update(clean); err != nil {
		t.Fatal(err)
	}
	if err := cleanTree.Commit(hh(1)); err != nil {
		t.Fatal(err)
	}
	cleanRoot, err := cleanTree.Root(hh(1))
	if err != nil {
		t.Fatal(err)
	}

	dirtyTree := newMemTree(t)
	if err := dirtyTree.Update(dirty); err != nil {
		t.Fatal(err)
	}
	if err := dirtyTree.Commit(hh(1)); err != nil {
		t.Fatal(err)
	}
	dirtyRoot, err := dirtyTree.Root(hh(1))
	if err != nil {
		t.Fatal(err)
	}

	if cleanRoot != dirtyRoot {
		t.Fatalf("fold equivalence broken: clean=%v dirty=%v", cleanRoot, dirtyRoot)
	}

	// And the verifier path agrees with both.
	cr, err := newMemTree(t).ComputeRoot(types.ZeroHashHeight, clean)
	if err != nil {
		t.Fatal(err)
	}
	if cr != cleanRoot {
		t.Fatalf("ComputeRoot(clean)=%v != committed %v", cr, cleanRoot)
	}
}
