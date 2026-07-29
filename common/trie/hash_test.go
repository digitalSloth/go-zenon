package trie

import (
	"testing"

	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
)

func TestEmptyRoot(t *testing.T) {
	// Under the constant-zero profile, the empty tree root is the 32-byte zero hash.
	if got := rootOfLeaves(nil); got != (types.Hash{}) {
		t.Fatalf("empty root = %v, want zero hash", got)
	}
}

// keyWithTopBit returns a key whose sha3 path begins with the given bit.
func keyWithTopBit(bit int) []byte {
	for i := 0; ; i++ {
		k := []byte{0x03, byte(i), byte(i >> 8)}
		if pathBit(types.NewHash(k), 0) == bit {
			return k
		}
	}
}

// TestTwoLeafRootStructure independently reconstructs the root of a two-leaf tree whose keys
// diverge at the top bit, validating the branch + full-depth padding logic.
func TestTwoLeafRootStructure(t *testing.T) {
	left := leaf{path: types.NewHash(keyWithTopBit(0)), value: []byte("alpha")}
	right := leaf{path: types.NewHash(keyWithTopBit(1)), value: []byte("beta")}

	want := InternalHash(padLeaf(left, 1), padLeaf(right, 1))
	got := rootOfLeaves([]leaf{right, left}) // unsorted input on purpose
	if got != want {
		t.Fatalf("two-leaf root = %v, want %v", got, want)
	}
}

func TestSingleLeafRootIsPadded(t *testing.T) {
	l := leaf{path: types.NewHash([]byte{0x05, 0x42}), value: []byte("v")}
	if got := rootOfLeaves([]leaf{l}); got != padLeaf(l, 0) {
		t.Fatalf("single-leaf root must equal padLeaf(.,0)")
	}
}

func TestFoldFilter(t *testing.T) {
	balanceKey := accountKey(0x03, 0x01)
	storageKey := accountKey(0x04, 0x02)
	excludedSubKey := accountKey(0x06, 0x03) // account-store key with an excluded sub-prefix

	p := db.NewPatch()
	p.Put(balanceKey, []byte("balance"))
	p.Put(storageKey, []byte("storage"))
	p.Put([]byte{0x00, 0x01}, []byte("frontier-id"))
	p.Put([]byte{0x01, 0xaa}, []byte("height-by-hash"))
	p.Put([]byte{0x02, 0xbb}, []byte("entry-by-height"))
	p.Put([]byte{0x09, 0xdd}, []byte("index"))
	p.Put([]byte{0x04, 0xff}, []byte("mailbox"))
	p.Put([]byte{0x08, 0x11}, []byte("znn-cache"))
	p.Put([]byte{0x03, 0x22}, []byte("short-account-key")) // len < 22, can't index the sub-prefix
	p.Put(excludedSubKey, []byte("excluded-sub-prefix"))
	p.Delete([]byte{0x02, 0xee}) // excluded delete, must be dropped
	p.Delete(balanceKey)         // kept delete, must be retained

	kept := map[string][]byte{}
	deleted := map[string]bool{}
	out := FoldFilter(p)
	_ = out.Replay(&recorder{put: kept, del: deleted})

	if len(kept) != 2 {
		t.Fatalf("expected 2 kept puts, got %d (%v)", len(kept), kept)
	}
	if _, ok := kept[string(balanceKey)]; !ok {
		t.Fatalf("balance key dropped")
	}
	if _, ok := kept[string(storageKey)]; !ok {
		t.Fatalf("storage key dropped")
	}
	if len(deleted) != 1 {
		t.Fatalf("expected 1 kept delete, got %d (%v)", len(deleted), deleted)
	}
	if !deleted[string(balanceKey)] {
		t.Fatalf("balance key delete dropped")
	}
	if deleted[string([]byte{0x02, 0xee})] {
		t.Fatalf("excluded delete should have been filtered out")
	}
}

type recorder struct {
	put map[string][]byte
	del map[string]bool
}

func (r *recorder) Put(key, value []byte) { r.put[string(key)] = append([]byte{}, value...) }
func (r *recorder) Delete(key []byte)     { r.del[string(key)] = true }
