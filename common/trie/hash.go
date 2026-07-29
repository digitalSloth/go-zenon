// Package trie implements the consensus-visible Merkleized state commitment for go-zenon:
// a versioned, history-independent binary sparse Merkle tree whose root is recorded in the
// momentum header, plus a storage-free proof verifier usable by light clients and a future
// on-chain dispute referee.
//
// The hashing scheme uses constant-zero empty subtrees (emptyHash = 32 zero bytes at every
// level), raw-value leaf hashing (sha3(path‖value)), interior hashing (sha3(left‖right) with a
// both-zero short-circuit), and a deepest-first compressed-sparse bitmap proof layout — see spec
// §6.2 (node hashing), §6.3 (proof format), §6.4 (full-depth leaves), §3.1.1 (fold rule).
// The fold rule (FoldFilter), the full-depth leaf placement, and the proof byte layout are
// CONSENSUS-VISIBLE and frozen — a divergent hash halts the network, so these must never change
// once activated.
//
// v1 is a full-depth (256-level) binary sparse tree: every key occupies the leaf at
// sha3(key) (level 256). The 16-ary / divergence-depth storage compaction is deferred to a
// later release and MUST reproduce the identical full-depth root (§6.4).
//
// SHARED CORE vs L1 MOMENTUM ADAPTER: hash.go, compute.go, proof.go, and pathapi.go are the
// shared, frozen, consensus-visible SMT core used by both L1 and L2 — they never hash keys and
// never fold empties. tree.go (persistence/versioning) and momentum_fold.go (the fold rule and
// staged appliers) are the L1 momentum adapter: they hash arbitrary DB keys and fold empty→delete.
// L2 callers MUST use the path-native core (pathapi.go) and never the L1 adapter. (doc §3.)
package trie

import (
	"github.com/zenon-network/go-zenon/common/crypto"
	"github.com/zenon-network/go-zenon/common/types"
)

const (
	// treeDepth is the bit-length of a path (= sha3 output). Leaves live at this level.
	treeDepth = 256
)

var (
	// emptyHash is the empty-subtree hash at every level: a never-written key and a
	// written-then-deleted key share this value, giving the tree history-independence (§6.2).
	// Under the constant-zero profile, empty is the 32-byte zero at ALL levels — no per-level
	// computation is needed.
	emptyHash = types.Hash{}
)

// LeafHash is the consensus-visible hash of a populated leaf (§6.2):
// sha3(path ‖ value), where path = sha3(key) and value is the raw byte value.
// An empty value ([]byte{}) produces sha3(path), which is non-zero (P-LEAF present-empty).
func LeafHash(path types.Hash, value []byte) types.Hash {
	return types.BytesToHashPanic(crypto.Hash(path[:], value))
}

// InternalHash is the consensus-visible hash of an internal node (§6.2):
// sha3(left ‖ right), with a both-empty short-circuit (P-EMPTY): if both children are the
// constant zero hash the node is also zero (avoids hashing the all-zero leaf).
func InternalHash(left, right types.Hash) types.Hash {
	if left == emptyHash && right == emptyHash {
		return emptyHash
	}
	return types.BytesToHashPanic(crypto.Hash(left[:], right[:]))
}

// pathBit returns bit `level` of `path`, MSB-first (level 0 = most-significant bit).
// A 0 selects the left child, a 1 the right child, at that level.
func pathBit(path types.Hash, level int) int {
	return int((path[level>>3] >> (7 - uint(level&7))) & 1)
}
