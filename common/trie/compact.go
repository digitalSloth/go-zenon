package trie

import "github.com/zenon-network/go-zenon/common/types"

// CompactTree is an in-memory compact binary sparse Merkle tree (design §2/§3).
//
// It materialises only two kinds of nodes:
//   - cLeaf — a populated leaf (path, value).
//   - cInternal — a true branch: exactly two non-empty children, stored at the level where
//     the left/right split first occurs.
//
// Single-child chains between a branch and the child below it are implicit; their hashes are
// bridged on the fly by padSubtree (§4.1). The resulting root is byte-identical to
// rootOfLeaves / oracleRoot for every sequence of inserts and deletes (§4).
//
// This is step 1 of the on-disk node store design (§12): in-memory only, no disk, no versioning,
// no refcounts, no Prove/Truncate/Prune. Correctness (root identity vs the oracle) is the only
// goal.
type CompactTree struct {
	root cNode // nil = empty tree
}

// cNode is a compact subtree node; nil means empty.
type cNode interface {
	compactNode()
}

// cLeaf holds a single populated leaf. Its hash at any level L is padLeaf(leaf, L).
type cLeaf struct {
	path  types.Hash
	value []byte
}

func (*cLeaf) compactNode() {}

// cInternal is a true-branch internal node. Both children are non-empty (the compact invariant).
//
// Fields:
//   - level: the depth at which this branch exists (bit index MSB-first, 0 = root level).
//   - leftHash/rightHash: each child's subtree hash at level+1, computed via padSubtree at
//     insert time. These are what form this node's own hash: InternalHash(leftHash, rightHash),
//     which equals the subtree hash of this node at `level`.
//   - left/right: child node pointers (cLeaf or cInternal); nil means empty.
//   - repPath: the path of any leaf in this subtree (the left-most leaf by convention). Used
//     only by padSubtree to determine the prefix bits when folding a single-child chain above
//     this node up to a requested level.
type cInternal struct {
	level     int
	leftHash  types.Hash // child hash at level+1
	rightHash types.Hash // child hash at level+1
	left      cNode      // nil = empty left child
	right     cNode      // nil = empty right child
	repPath   types.Hash // representative path for padSubtree chain-folding
}

func (*cInternal) compactNode() {}

// repPathOf returns the representative path for any non-nil cNode.
func repPathOf(n cNode) types.Hash {
	switch v := n.(type) {
	case *cLeaf:
		return v.path
	case *cInternal:
		return v.repPath
	}
	panic("trie: repPathOf called on nil node")
}

// padSubtree returns the hash of a compact subtree n at toLevel (§4.1).
//
// If n is nil (empty) it returns emptyHash.
// If n is a leaf it returns padLeaf(leaf, toLevel) — identical to the compute.go padLeaf.
// If n is cInternal@M it starts from the node's own hash at level M and folds the implicit
// single-child chain upward to toLevel using the representative path's prefix bits.
//
// Correctness guarantee: padSubtree(n, L) == subtreeHash(n's leaves, L) for every n and L.
func padSubtree(n cNode, toLevel int) types.Hash {
	if n == nil {
		return emptyHash
	}

	var h types.Hash
	var fromLevel int
	var repPath types.Hash

	switch v := n.(type) {
	case *cLeaf:
		h = LeafHash(v.path, v.value)
		fromLevel = treeDepth // leaf hash is at level 256
		repPath = v.path
	case *cInternal:
		h = InternalHash(v.leftHash, v.rightHash) // hash at v.level
		fromLevel = v.level
		repPath = v.repPath
	}

	// Fold the single-child chain from fromLevel-1 down to toLevel.
	// At each chain level `lvl`, the off-path sibling is emptyHash.
	for lvl := fromLevel - 1; lvl >= toLevel; lvl-- {
		if pathBit(repPath, lvl) == 0 {
			h = InternalHash(h, emptyHash)
		} else {
			h = InternalHash(emptyHash, h)
		}
	}
	return h
}

// divergeLevel returns the first bit position >= startLevel where path a and path b differ.
// Callers guarantee that a != b, so this always finds a divergence before treeDepth.
func divergeLevel(a, b types.Hash, startLevel int) int {
	for lvl := startLevel; lvl < treeDepth; lvl++ {
		if pathBit(a, lvl) != pathBit(b, lvl) {
			return lvl
		}
	}
	// Unreachable for distinct 256-bit paths.
	panic("trie: identical paths in divergeLevel")
}

// divergeLevelBefore returns the first bit position in [startLevel, limit) where a and b
// differ, or `limit` if they agree on all bits in that range (meaning the path falls within
// the existing subtree rather than diverging from its prefix chain).
func divergeLevelBefore(a, b types.Hash, startLevel, limit int) int {
	for lvl := startLevel; lvl < limit; lvl++ {
		if pathBit(a, lvl) != pathBit(b, lvl) {
			return lvl
		}
	}
	return limit
}

// insertNode inserts newLeaf into compact subtree n (living at `level` from the caller's
// perspective) and returns the new node and its hash at `level`.
func insertNode(n cNode, level int, newLeaf *cLeaf) (cNode, types.Hash) {
	if n == nil {
		return newLeaf, padSubtree(newLeaf, level)
	}

	switch node := n.(type) {
	case *cLeaf:
		if node.path == newLeaf.path {
			// Same key: replace value.
			return newLeaf, padSubtree(newLeaf, level)
		}
		// Different key: create a branch at the divergence level.
		D := divergeLevel(node.path, newLeaf.path, level)
		return makeBranch(node, newLeaf, D, level)

	case *cInternal:
		M := node.level
		if M > level {
			// There is a single-child chain from `level` to M-1. Check whether newLeaf
			// diverges from the existing subtree within that chain.
			D := divergeLevelBefore(node.repPath, newLeaf.path, level, M)
			if D < M {
				// Diverges in the chain: split here, existing subtree is one child.
				return makeBranch(node, newLeaf, D, level)
			}
			// No divergence in chain: newLeaf enters the existing branch at level M.
			// Fall through to the M == level case below.
		}
		// Insert into the appropriate child of this branch at level M.
		bit := pathBit(newLeaf.path, M)
		updated := *node // shallow copy
		if bit == 0 {
			newLeft, leftHash := insertNode(node.left, M+1, newLeaf)
			updated.left = newLeft
			updated.leftHash = leftHash
		} else {
			newRight, rightHash := insertNode(node.right, M+1, newLeaf)
			updated.right = newRight
			updated.rightHash = rightHash
		}
		// Refresh repPath: always the left child's representative (it is non-nil; if we just
		// inserted into the left it is definitely non-nil; if not it was already non-nil by
		// the compact invariant).
		updated.repPath = repPathOf(updated.left)
		result := &updated
		return result, padSubtree(result, level)
	}
	panic("trie: unknown node type in insertNode")
}

// makeBranch creates a cInternal@D branching between nodes a and b (which diverge at bit D),
// and returns it along with its hash at `level` (level <= D).
func makeBranch(a, b cNode, D, level int) (cNode, types.Hash) {
	aPath := repPathOf(a)
	node := &cInternal{level: D}
	if pathBit(aPath, D) == 0 {
		// a goes left, b goes right.
		node.left = a
		node.right = b
		node.leftHash = padSubtree(a, D+1)
		node.rightHash = padSubtree(b, D+1)
		node.repPath = aPath
	} else {
		// a goes right, b goes left.
		node.left = b
		node.right = a
		node.leftHash = padSubtree(b, D+1)
		node.rightHash = padSubtree(a, D+1)
		node.repPath = repPathOf(b)
	}
	return node, padSubtree(node, level)
}

// deleteNode removes the leaf with `path` from compact subtree n (living at `level`) and
// returns the new node and its hash at `level`.
func deleteNode(n cNode, level int, path types.Hash) (cNode, types.Hash) {
	if n == nil {
		return nil, emptyHash
	}

	switch node := n.(type) {
	case *cLeaf:
		if node.path == path {
			return nil, emptyHash
		}
		// Path not in this subtree: no-op.
		return node, padSubtree(node, level)

	case *cInternal:
		M := node.level
		if M > level {
			// Single-child chain from level to M-1: check if `path` shares the chain prefix.
			D := divergeLevelBefore(node.repPath, path, level, M)
			if D < M {
				// `path` diverges from the chain — it is not in this subtree.
				return node, padSubtree(node, level)
			}
			// Path shares the chain prefix; descend into the branch at M.
		}
		bit := pathBit(path, M)
		updated := *node
		if bit == 0 {
			newLeft, leftHash := deleteNode(node.left, M+1, path)
			if newLeft == nil {
				// Left child gone: collapse to right child only.
				return node.right, padSubtree(node.right, level)
			}
			updated.left = newLeft
			updated.leftHash = leftHash
			updated.repPath = repPathOf(newLeft)
		} else {
			newRight, rightHash := deleteNode(node.right, M+1, path)
			if newRight == nil {
				// Right child gone: collapse to left child only.
				return node.left, padSubtree(node.left, level)
			}
			updated.right = newRight
			updated.rightHash = rightHash
			updated.repPath = repPathOf(updated.left)
		}
		result := &updated
		return result, padSubtree(result, level)
	}
	panic("trie: unknown node type in deleteNode")
}

// Insert inserts or replaces the leaf at path with the given value.
// path is the raw 32-byte path (= sha3(key)); value is the raw value.
func (t *CompactTree) Insert(path types.Hash, value []byte) {
	newLeaf := &cLeaf{path: path, value: value}
	newRoot, _ := insertNode(t.root, 0, newLeaf)
	t.root = newRoot
}

// Delete removes the leaf at path. It is a no-op if the path is not present.
func (t *CompactTree) Delete(path types.Hash) {
	newRoot, _ := deleteNode(t.root, 0, path)
	t.root = newRoot
}

// Root returns the tree's root hash at level 0.
//   - Empty tree → emptyHash (zero32)
//   - Single leaf → padLeaf(leaf, 0) (== padSubtree(leaf, 0))
//   - Branch at root → InternalHash(leftHash, rightHash) (== padSubtree(branch, 0))
func (t *CompactTree) Root() types.Hash {
	if t.root == nil {
		return emptyHash
	}
	return padSubtree(t.root, 0)
}
