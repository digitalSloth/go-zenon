package trie

import (
	"bytes"
	"sort"

	"github.com/zenon-network/go-zenon/common/types"
)

// leaf is a populated entry: the key's path (= sha3(key)) and its raw value.
type leaf struct {
	path  types.Hash
	value []byte
}

// sortLeaves orders leaves by path ascending. Once sorted, the leaves on each side of a
// bit split at any level are contiguous, which is what lets subtreeHash/proveLeaves recurse
// with simple slice partitioning.
func sortLeaves(leaves []leaf) {
	sort.Slice(leaves, func(i, j int) bool {
		return bytes.Compare(leaves[i].path[:], leaves[j].path[:]) < 0
	})
}

// splitPoint returns the index of the first leaf whose bit at `level` is 1, within a
// path-sorted slice that shares the prefix above `level`.
func splitPoint(leaves []leaf, level int) int {
	return sort.Search(len(leaves), func(i int) bool {
		return pathBit(leaves[i].path, level) == 1
	})
}

// rootOfLeaves computes the full-depth sparse-Merkle root over a set of populated leaves.
// The input is copied and sorted; the empty set hashes to emptyHash (zero32).
func rootOfLeaves(leaves []leaf) types.Hash {
	if len(leaves) == 0 {
		return emptyHash
	}
	s := make([]leaf, len(leaves))
	copy(s, leaves)
	sortLeaves(s)
	return subtreeHash(s, 0)
}

// subtreeHash hashes the subtree rooted at `level` over `leaves` (assumed path-sorted and
// all sharing the prefix above `level`). A single leaf is hashed at full depth via padLeaf;
// an empty subtree is emptyHash (constant zero at every level).
func subtreeHash(leaves []leaf, level int) types.Hash {
	switch len(leaves) {
	case 0:
		return emptyHash
	case 1:
		return padLeaf(leaves[0], level)
	}
	// len >= 2: distinct paths guarantee level < treeDepth, so a real branch exists.
	mid := splitPoint(leaves, level)
	left := subtreeHash(leaves[:mid], level+1)
	right := subtreeHash(leaves[mid:], level+1)
	return InternalHash(left, right)
}

// padLeaf computes the hash of the subtree rooted at `level` that contains exactly the one
// leaf `l`. The leaf hash is formed at level 256 and folded upward to `level`, substituting
// emptyHash for the (empty) sibling at each step (§6.4 full-depth rule).
func padLeaf(l leaf, level int) types.Hash {
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

// proveLeaves walks `targetPath` down the (path-sorted) leaf set, recording the sibling
// subtree hash at every level. It returns whether the key is present (and its value if so)
// and the per-level sibling hashes. Levels not on a real branch carry emptyHash.
//
// In the full-depth model absence is always "the level-256 slot is empty" (§6.3): distinct
// sha3 paths cannot collide, so an absent key's leaf slot is empty even when another key
// shares a long prefix — the proof simply records that other key's subtree as a sibling and
// the target's own slot resolves to empty. There is therefore no colliding-leaf case.
func proveLeaves(leaves []leaf, targetPath types.Hash) (present bool, value []byte, sib [treeDepth]types.Hash) {
	// Initialize all sibling slots to emptyHash (constant zero).
	for l := 0; l < treeDepth; l++ {
		sib[l] = emptyHash
	}

	cur := leaves
	for level := 0; level < treeDepth; level++ {
		mid := splitPoint(cur, level)
		var onPath, offPath []leaf
		if pathBit(targetPath, level) == 0 {
			onPath, offPath = cur[:mid], cur[mid:]
		} else {
			onPath, offPath = cur[mid:], cur[:mid]
		}
		sib[level] = subtreeHash(offPath, level+1)

		cur = onPath
		if len(cur) == 0 {
			// Target's slot is empty below here: absence. Remaining siblings stay emptyHash.
			return false, nil, sib
		}
		if len(cur) == 1 && cur[0].path == targetPath {
			// Target is alone below here: inclusion. Remaining siblings stay emptyHash.
			return true, cur[0].value, sib
		}
		// len(cur) == 1 with a different path: keep descending until it diverges (where
		// its subtree becomes the recorded sibling and the target's slot goes empty).
	}
	// Unreachable for distinct 256-bit paths: the loop always terminates above.
	return false, nil, sib
}
