package trie

import (
	"testing"

	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
)

// TestFoldKeep is the table test for the whitelist predicate: one case per category named in
// the fold's edge cases, asserting include/exclude.
func TestFoldKeep(t *testing.T) {
	cases := []struct {
		name string
		key  []byte
		want bool
	}{
		{"balance", accountKey(0x03, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09), true},
		{"storage", accountKey(0x04, 0xAA, 0xBB), true},
		{"balance exactly 22 bytes", accountKey(0x03), true},
		{"storage exactly 22 bytes", accountKey(0x04), true},
		{"empty key", []byte{}, false},
		{"account prefix alone", []byte{0x03}, false},
		{"partial address", append([]byte{0x03}, make([]byte, 10)...), false},
		{"excluded sub-prefix 0", accountKey(0x00, 0x01), false},
		{"excluded sub-prefix 1", accountKey(0x01, 0x01), false},
		{"excluded sub-prefix 2", accountKey(0x02, 0x01), false},
		{"excluded sub-prefix 6", accountKey(0x06, 0x01), false},
		{"excluded sub-prefix 7", accountKey(0x07, 0x01), false},
		{"momentum history 0", []byte{0x00, 0x01}, false},
		{"momentum history 1", []byte{0x01, 0x01}, false},
		{"momentum history 2", []byte{0x02, 0x01}, false},
		{"momentum history 5", []byte{0x05, 0x01}, false},
		{"momentum history 9", []byte{0x09, 0x01}, false},
		{"mailbox", []byte{0x04, 0x01, 0x02}, false},
		{"znn cache", []byte{0x08, 0x01, 0x02}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := foldKeep(tc.key); got != tc.want {
				t.Fatalf("foldKeep(%x) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

// TestFoldFilterCategories builds one db.Patch containing one representative of every category
// the fold predicate distinguishes (as both Puts and Deletes) and asserts FoldFilter's output
// contains exactly the two included keys and nothing else.
func TestFoldFilterCategories(t *testing.T) {
	balancePutKey := accountKey(0x03, 0x01)
	storageDeleteKey := accountKey(0x04, 0x02)

	excluded := [][]byte{
		{0x00, 0x01},           // momentum history
		{0x01, 0x01},           // momentum history
		{0x02, 0x01},           // momentum history
		{0x05, 0x01},           // momentum history
		{0x09, 0x01},           // momentum history
		{0x04, 0x01, 0x02},     // mailbox
		{0x08, 0x01, 0x02},     // znn cache
		{0x03, 0x01},           // short account-store key, len < 22
		accountKey(0x06, 0x01), // account-store key, excluded sub-prefix
	}

	p := db.NewPatch()
	p.Put(balancePutKey, []byte("balance"))
	p.Delete(storageDeleteKey)
	for i, k := range excluded {
		p.Put(k, []byte{byte(i)})
		p.Delete(k)
	}

	kept := map[string][]byte{}
	deleted := map[string]bool{}
	out := FoldFilter(p)
	if err := out.Replay(&recorder{put: kept, del: deleted}); err != nil {
		t.Fatal(err)
	}

	if len(kept) != 1 {
		t.Fatalf("expected 1 kept put, got %d (%v)", len(kept), kept)
	}
	if _, ok := kept[string(balancePutKey)]; !ok {
		t.Fatalf("balance key dropped from kept puts")
	}
	if len(deleted) != 1 {
		t.Fatalf("expected 1 kept delete, got %d (%v)", len(deleted), deleted)
	}
	if !deleted[string(storageDeleteKey)] {
		t.Fatalf("storage key delete dropped")
	}
}

// TestFoldInvarianceThroughComputeRoot exercises stagedApplier end-to-end through the commit
// engine: folding a mixed patch (included keys plus every excluded category) must produce the
// same root as folding a pre-filtered patch containing only the included keys.
func TestFoldInvarianceThroughComputeRoot(t *testing.T) {
	balanceKey := accountKey(0x03, 0x01)
	storageKey := accountKey(0x04, 0x02)

	includedOnly := db.NewPatch()
	includedOnly.Put(balanceKey, []byte("balance"))
	includedOnly.Put(storageKey, []byte("storage"))

	mixed := db.NewPatch()
	mixed.Put(balanceKey, []byte("balance"))
	mixed.Put(storageKey, []byte("storage"))
	mixed.Put([]byte{0x00, 0x01}, []byte("frontier-identifier"))
	mixed.Put([]byte{0x01, 0x01}, []byte("height-by-hash"))
	mixed.Put([]byte{0x02, 0x01}, []byte("entry-by-height"))
	mixed.Put([]byte{0x05, 0x01}, []byte("index"))
	mixed.Put([]byte{0x09, 0x01}, []byte("index"))
	mixed.Put([]byte{0x04, 0x01, 0x02}, []byte("mailbox"))
	mixed.Put([]byte{0x08, 0x01, 0x02}, []byte("znn-cache"))
	mixed.Put([]byte{0x03, 0x01}, []byte("short-account-key"))
	mixed.Put(accountKey(0x06, 0x01), []byte("excluded-sub-prefix"))

	nt := newMemNodeTree(t)

	mixedRoot, err := nt.ComputeRoot(types.ZeroHashHeight, mixed)
	if err != nil {
		t.Fatalf("ComputeRoot(mixed): %v", err)
	}
	includedOnlyRoot, err := nt.ComputeRoot(types.ZeroHashHeight, includedOnly)
	if err != nil {
		t.Fatalf("ComputeRoot(includedOnly): %v", err)
	}

	if mixedRoot != includedOnlyRoot {
		t.Fatalf("fold invariance broken: mixed=%v includedOnly=%v", mixedRoot, includedOnlyRoot)
	}
	if mixedRoot == (types.Hash{}) {
		t.Fatalf("expected a non-zero root")
	}
}
