package trie

import (
	"encoding/binary"
	"errors"
	"sync"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
)

// NodeTree is a disk-backed, content-addressed, refcounted binary sparse Merkle tree
// (design §2/§3/§5/§6). It implements the same Update/Commit/Root/FrontierIdentifier API
// as Tree (tree.go) but stores compact nodes on disk rather than a flat leaf set.
//
// Storage keyspaces on its own leveldb (§5):
//
//	0x00             -> frontier HashHeight
//	0x01 ‖ id[32]   -> node bytes (content-addressed, shared across versions)
//	0x02 ‖ id[32]   -> refcount uint64
//	0x03 ‖ height[8]-> rootId[32] (version→root mapping)
//	0x05             -> format-version byte (migration sentinel, §9)
//
// Node bytes (§3):
//
//	Leaf:     0x00 ‖ path[32] ‖ value[...]           (variable length, ≥33 bytes)
//	Internal: 0x01 ‖ level[2] ‖ leftHash[32] ‖ leftId[32] ‖ rightHash[32] ‖ rightId[32]  (131 bytes)
//
// The leaf stores the raw value bytes. The id is LeafHash(path, value) — raw value,
// no pre-hash — so roots and proof bytes match the constant-zero profile.
//
// The zero hash is used as a sentinel for "empty child" in Internal nodes.
//
// Migration (§9): if the DB is non-empty but the format-version key is absent or holds a
// different version byte, NewNodeTree returns ErrFormatMismatch so the caller can wipe and
// rebuild from chain patches.
type NodeTree struct {
	mu  sync.Mutex
	ldb *leveldb.DB

	frontier types.HashHeight
	staged   map[types.Hash]stagedOp // reuses stagedOp from tree.go
}

// Storage key prefixes (§5). The frontier prefix mirrors tree.go's keyFrontier (0x00) but
// lives in a separate db, so there is no conflict.
var (
	nsKeyFrontier      = []byte{0x00}
	nsKeyNode          = []byte{0x01}
	nsKeyRef           = []byte{0x02}
	nsKeyVersion       = []byte{0x03}
	nsKeyFormatVersion = []byte{0x05} // format-version marker (§9 migration)
)

// nsCurrentFormatVersion is the format version this binary writes. A DB carrying a different
// byte (or no byte at all) is a foreign format; NewNodeTree returns ErrFormatMismatch.
const nsCurrentFormatVersion = byte(0x02)

// ErrFormatMismatch is returned by NewNodeTree when the DB is non-empty and carries a
// format-version key that does not match nsCurrentFormatVersion (e.g. a DB previously written
// by the leaf-set Tree, or a future format). The caller should wipe the DB and rebuild.
var ErrFormatMismatch = errors.New("trie: node store format mismatch — wipe and rebuild")

// ErrNotStaged is returned by NodeTree.Commit or NodeTree.CommitBulk when there is no staged
// write-set (Update/AccumulateFrom was never called, or a prior Commit/CommitBulk/Truncate
// already consumed it).
var ErrNotStaged = errors.New("trie: commit called without a staged write-set")

// ErrCommitOutOfOrder is returned by NodeTree.Commit when identifier.Height is not exactly one
// above the current frontier, guarding against building on the wrong base version.
var ErrCommitOutOfOrder = errors.New("trie: commit height must be exactly one above the frontier")

// ErrBulkCommitOutOfOrder is returned by NodeTree.CommitBulk when identifier.Height is not
// strictly above the current frontier.
var ErrBulkCommitOutOfOrder = errors.New("trie: bulk commit height must be above the frontier")

const (
	nsLeafTag     = 0x00
	nsInternalTag = 0x01

	// Serialized sizes.
	nsLeafMinSize  = 1 + 32                    // tag + path (value is variable length, may be 0 bytes)
	nsInternalSize = 1 + 2 + 32 + 32 + 32 + 32 // tag + level + leftHash + leftId + rightHash + rightId
)

// zeroHash is the "empty child" sentinel inside Internal nodes. Real node ids are sha3 outputs
// and are never zero, so this is unambiguous.
var zeroHash types.Hash

// ---- on-disk node representations ----

// diskLeaf is a deserialized leaf node. The raw value is stored in the node itself
// so that historical proofs return the correct versioned value.
type diskLeaf struct {
	path  types.Hash
	value []byte // raw value bytes
}

// id returns the leaf's storage id: LeafHash(path, value) — raw value, no pre-hash.
func (l *diskLeaf) id() types.Hash {
	return LeafHash(l.path, l.value)
}

// diskInternal is a deserialized internal node.
type diskInternal struct {
	level     int
	leftHash  types.Hash // child's subtree hash at level+1
	leftId    types.Hash // child's storage id, or zeroHash if empty
	rightHash types.Hash
	rightId   types.Hash
}

// id returns the internal node's storage id: InternalHash(leftHash, rightHash).
func (n *diskInternal) id() types.Hash {
	return InternalHash(n.leftHash, n.rightHash)
}

// ---- serialization / deserialization ----

func serializeLeaf(l *diskLeaf) []byte {
	b := make([]byte, 1+32+len(l.value))
	b[0] = nsLeafTag
	copy(b[1:33], l.path[:])
	copy(b[33:], l.value)
	return b
}

func serializeInternal(n *diskInternal) []byte {
	b := make([]byte, nsInternalSize)
	b[0] = nsInternalTag
	binary.BigEndian.PutUint16(b[1:3], uint16(n.level))
	copy(b[3:35], n.leftHash[:])
	copy(b[35:67], n.leftId[:])
	copy(b[67:99], n.rightHash[:])
	copy(b[99:131], n.rightId[:])
	return b
}

func deserializeNode(data []byte) (interface{}, error) {
	if len(data) == 0 {
		return nil, ErrCorrupt
	}
	switch data[0] {
	case nsLeafTag:
		if len(data) < nsLeafMinSize {
			return nil, ErrCorrupt
		}
		l := &diskLeaf{}
		copy(l.path[:], data[1:33])
		if len(data) > 33 {
			l.value = make([]byte, len(data)-33)
			copy(l.value, data[33:])
		}
		return l, nil
	case nsInternalTag:
		if len(data) != nsInternalSize {
			return nil, ErrCorrupt
		}
		n := &diskInternal{}
		n.level = int(binary.BigEndian.Uint16(data[1:3]))
		copy(n.leftHash[:], data[3:35])
		copy(n.leftId[:], data[35:67])
		copy(n.rightHash[:], data[67:99])
		copy(n.rightId[:], data[99:131])
		return n, nil
	default:
		return nil, ErrCorrupt
	}
}

// ---- storage key helpers ----

func nsNodeKey(id types.Hash) []byte {
	return common.JoinBytes(nsKeyNode, id[:])
}

func nsRefKey(id types.Hash) []byte {
	return common.JoinBytes(nsKeyRef, id[:])
}

func nsVersionKey(height uint64) []byte {
	return common.JoinBytes(nsKeyVersion, common.Uint64ToBytes(height))
}

// ---- constructor / recovery ----

// NewNodeTree opens a NodeTree over an existing leveldb handle, recovering the frontier.
//
// Migration (§9): if the DB is non-empty but the format-version key is absent or holds a
// different version byte (e.g. a DB previously written by the leaf-set Tree), NewNodeTree
// returns ErrFormatMismatch. The caller should wipe the DB and rebuild from chain patches.
//
// On a fresh empty DB, NewNodeTree writes the format-version key before returning.
func NewNodeTree(ldb *leveldb.DB) (*NodeTree, error) {
	t := &NodeTree{ldb: ldb}

	// Check format-version marker for migration detection.
	fmtData, fmtErr := ldb.Get(nsKeyFormatVersion, nil)
	switch {
	case fmtErr == nil:
		// Marker present — verify it matches.
		if len(fmtData) != 1 || fmtData[0] != nsCurrentFormatVersion {
			return nil, ErrFormatMismatch
		}
	case fmtErr == leveldb.ErrNotFound:
		// No format-version key. Determine whether this is a fresh DB or a foreign one.
		// A DB written by the leaf-set Tree has keyFrontier (0x00) present (or keyLeaf 0x03
		// entries). Any non-empty DB without our format-version key is treated as foreign.
		iter := ldb.NewIterator(nil, nil)
		hasAny := iter.Next()
		iter.Release()
		if hasAny {
			// Non-empty DB, no format key → foreign format (e.g. old leaf-set Tree).
			return nil, ErrFormatMismatch
		}
		// Fresh empty DB: write the format-version key.
		if err := ldb.Put(nsKeyFormatVersion, []byte{nsCurrentFormatVersion}, nil); err != nil {
			return nil, err
		}
	default:
		return nil, fmtErr
	}

	data, err := ldb.Get(nsKeyFrontier, nil)
	if err == leveldb.ErrNotFound {
		t.frontier = types.ZeroHashHeight
		return t, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) != types.HashSize+8 {
		return nil, ErrCorrupt
	}
	var h types.Hash
	copy(h[:], data[:types.HashSize])
	t.frontier = types.HashHeight{Hash: h, Height: common.BytesToUint64(data[types.HashSize:])}
	return t, nil
}

// FrontierIdentifier returns the highest committed version.
func (t *NodeTree) FrontierIdentifier() types.HashHeight {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.frontier
}

// Update stages a write-set (fold rule §3.1.1) for the next Commit. It does not persist.
// Reuses the same stagedApplier fold logic as tree.go.
func (t *NodeTree) Update(changes db.Patch) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	staged := map[types.Hash]stagedOp{}
	if err := changes.Replay(&stagedApplier{staged: staged}); err != nil {
		return err
	}
	t.staged = staged
	return nil
}

// Commit applies the staged write-set as version identifier by copy-on-write from the current
// root (§6). It builds new branch/leaf nodes, serializes them per §3, and writes nodes +
// refcounts + version→root + frontier in one leveldb batch. The root is NOT returned (lazy).
func (t *NodeTree) Commit(identifier types.HashHeight) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.staged == nil {
		return ErrNotStaged
	}
	if identifier.Height != t.frontier.Height+1 {
		return ErrCommitOutOfOrder
	}

	return t.commitStagedLocked(identifier)
}

// commitStagedLocked applies t.staged as version identifier by copy-on-write from the current
// frontier's root (§6). Callers must hold t.mu and must have already validated identifier
// (Commit and CommitBulk each apply their own ordering guard before calling this).
func (t *NodeTree) commitStagedLocked(identifier types.HashHeight) error {
	currentRootId, err := t.loadVersionRoot(t.frontier.Height)
	if err != nil {
		return err
	}

	inserts := map[types.Hash][]byte{}
	deletes := map[types.Hash]bool{}
	for path, op := range t.staged {
		if op.del {
			deletes[path] = true
		} else {
			inserts[path] = op.value
		}
	}

	batch := new(leveldb.Batch)

	ctx := &commitCtx{
		ldb:     t.ldb,
		batch:   batch,
		pending: map[types.Hash][]byte{},
		rc:      &refcountAccum{ldb: t.ldb},
	}

	newRootId, _, err := ctx.applyChanges(currentRootId, 0, inserts, deletes)
	if err != nil {
		return err
	}

	// version→root entry + +1 root refcount (§5).
	batch.Put(nsVersionKey(identifier.Height), newRootId[:])
	if newRootId != zeroHash {
		ctx.rc.inc(newRootId)
	}

	if err := ctx.rc.flush(batch); err != nil {
		return err
	}

	batch.Put(nsKeyFrontier, identifier.Bytes())

	if err := t.ldb.Write(batch, nil); err != nil {
		return err
	}
	t.frontier = identifier
	t.staged = nil
	return nil
}

// AccumulateFrom merges a write-set into the staged set without committing, so a range of
// per-height patches can be folded into a single version by a following CommitBulk. Later
// patches win per path (deletes included), matching the fold rule (§3.1.1) applied by Update.
// Callers stream one patch per height, so no patch history is retained — only the merged staged
// set, one entry per distinct path touched. That bound does NOT carry over to the following
// CommitBulk: its peak memory is O(nodes created) — commitCtx.pending, the leveldb batch, and
// refcountAccum.delta each hold roughly one entry per node touched, a multiple of the
// staged-set size, not equal to it. Callers must bound the accumulated range accordingly.
//
// The accumulate→CommitBulk sequence is NOT atomic: t.mu is held per call only. The caller must
// hold its own exclusive lock across the whole sequence. A concurrent Update, Commit or Truncate
// interleaved between two AccumulateFrom calls invalidates the accumulation without returning an
// error (Update replaces it wholesale, Commit consumes it, then accumulation resumes into a
// fresh map), except that a Truncate with no further AccumulateFrom after it clears t.staged, so
// the following CommitBulk returns ErrNotStaged; a Truncate followed by more AccumulateFrom
// calls is silent like the others.
func (t *NodeTree) AccumulateFrom(changes db.Patch) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.staged == nil {
		t.staged = map[types.Hash]stagedOp{}
	}
	return changes.Replay(&stagedApplier{staged: t.staged})
}

// CommitBulk commits the accumulated staged set as the single version identifier, seeding the
// frontier to identifier. Unlike Commit it does not require identifier.Height to be exactly one
// above the frontier — a bulk fold of a range of momentums lands at the range's boundary height —
// but the commit is still built copy-on-write from the current frontier's root, and the height
// must be above the current frontier. Intermediate heights in the folded range get no
// version→root entry.
//
// The caller must hold its own exclusive lock across the whole accumulate→commit sequence; see
// AccumulateFrom.
//
// Peak memory during the commit is O(nodes created) — the serialized bytes of every written node
// are held in commitCtx.pending, again in the leveldb batch, plus one refcount-delta entry per
// node — a multiple of the staged-set size, not equal to it. Callers must bound the accumulated
// range accordingly.
//
// Because intermediate heights get no version→root entry, this is the first operation that can
// leave gaps in the version keyspace. Two consequences: Truncate to a height inside a folded gap
// returns ErrNoVersion (it requires a committed version at the target height), and Truncate from
// a bulk frontier down across a gap performs one no-op loadVersionRoot read per skipped height.
func (t *NodeTree) CommitBulk(identifier types.HashHeight) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.staged == nil {
		return ErrNotStaged
	}
	if identifier.Height <= t.frontier.Height {
		return ErrBulkCommitOutOfOrder
	}

	return t.commitStagedLocked(identifier)
}

// Root returns the root hash at identifier's height.
func (t *NodeTree) Root(identifier types.HashHeight) (types.Hash, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rootAt(identifier.Height)
}

func (t *NodeTree) rootAt(height uint64) (types.Hash, error) {
	rootId, err := t.loadVersionRoot(height)
	if err != nil {
		return types.Hash{}, err
	}
	if rootId == zeroHash {
		return emptyHash, nil
	}
	// For an Internal@0: rootId == InternalHash(leftHash, rightHash) == hash at level 0. ✓
	// For a lone leaf: rootId == LeafHash(path, vh) == hash at level 256; pad to 0.
	// padSubtreeIdOnDisk handles both by loading the node and folding from its native level.
	rctx := &readCtx{ldb: t.ldb}
	return rctx.padSubtreeIdOnDisk(rootId, 0)
}

// loadVersionRoot returns the rootId stored for `height`, or zeroHash for height 0 (empty).
// Returns ErrNoVersion if height is non-zero and not found.
func (t *NodeTree) loadVersionRoot(height uint64) (types.Hash, error) {
	if height == 0 {
		return zeroHash, nil
	}
	data, err := t.ldb.Get(nsVersionKey(height), nil)
	if err == leveldb.ErrNotFound {
		return zeroHash, ErrNoVersion
	}
	if err != nil {
		return zeroHash, err
	}
	if len(data) != types.HashSize {
		return zeroHash, ErrCorrupt
	}
	var id types.Hash
	copy(id[:], data)
	return id, nil
}

// ComputeRoot folds changes (fold rule §3.1.1) onto the version at `previous` and returns
// the resulting root WITHOUT writing anything to disk or mutating refcounts/frontier (the
// verifier's hook, §6). In the consensus path `previous` is always the frontier.
//
// The computation is done entirely in memory using a transient commitCtx whose batch and
// pending cache are discarded after the root hash is derived; no nodes are written to ldb.
// Returns ErrNoVersion if `previous.Height` is not retained.
func (t *NodeTree) ComputeRoot(previous types.HashHeight, changes db.Patch) (types.Hash, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	currentRootId, err := t.loadVersionRoot(previous.Height)
	if err != nil {
		return types.Hash{}, err
	}

	// Stage the filtered write-set.
	staged := map[types.Hash]stagedOp{}
	if err := changes.Replay(&stagedApplier{staged: staged}); err != nil {
		return types.Hash{}, err
	}

	inserts := map[types.Hash][]byte{}
	deletes := map[types.Hash]bool{}
	for path, op := range staged {
		if op.del {
			deletes[path] = true
		} else {
			inserts[path] = op.value
		}
	}

	// Use a transient commitCtx: batch/pending are discarded; ldb is read-only here.
	// We pass a nil batch because we never call rc.flush or ldb.Write.
	ctx := &commitCtx{
		ldb:     t.ldb,
		batch:   new(leveldb.Batch), // discarded
		pending: map[types.Hash][]byte{},
		rc:      &refcountAccum{ldb: t.ldb}, // accumulator discarded
	}

	_, hashAtLevel0, err := ctx.applyChanges(currentRootId, 0, inserts, deletes)
	if err != nil {
		return types.Hash{}, err
	}
	return hashAtLevel0, nil
}

// ---- commit context ----

// commitCtx holds the state for a single Commit: a leveldb batch, a pending-node cache
// (nodes written to batch but not yet in ldb), and a refcount accumulator.
// The pending cache is essential: new nodes are added to batch during CoW traversal; if a
// later step needs to read back a newly-written node (e.g. collectLeaves on a chain-div
// restructure), it must find it in pending, not ldb.
type commitCtx struct {
	ldb     *leveldb.DB
	batch   *leveldb.Batch
	pending map[types.Hash][]byte // id -> serialized node bytes, for batch-written nodes
	rc      *refcountAccum
}

// loadNode loads a node by id, checking the pending cache first, then disk.
func (ctx *commitCtx) loadNode(id types.Hash) (interface{}, error) {
	if id == zeroHash {
		return nil, nil
	}
	if data, ok := ctx.pending[id]; ok {
		return deserializeNode(data)
	}
	data, err := ctx.ldb.Get(nsNodeKey(id), nil)
	if err == leveldb.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return deserializeNode(data)
}

// nodeExists checks whether a node is already known (pending or on disk).
func (ctx *commitCtx) nodeExists(id types.Hash) (bool, error) {
	if _, ok := ctx.pending[id]; ok {
		return true, nil
	}
	_, err := ctx.ldb.Get(nsNodeKey(id), nil)
	if err == leveldb.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// writeNode writes a node to batch and pending cache.
func (ctx *commitCtx) writeNode(id types.Hash, data []byte) {
	ctx.batch.Put(nsNodeKey(id), data)
	ctx.pending[id] = data
}

// ---- CoW commit engine ----

// applyChanges applies inserts and deletes to the subtree rooted at `nodeId` (living at
// `level`) by copy-on-write. Returns (newNodeId, hashAtLevel, error).
//
// hashAtLevel is the subtree hash at `level` — the value a parent node stores in its
// leftHash/rightHash field for this child. No disk reads are needed for newly-created
// nodes because hashes are computed in the return path and pending cache serves re-reads.
func (ctx *commitCtx) applyChanges(
	nodeId types.Hash,
	level int,
	inserts map[types.Hash][]byte,
	deletes map[types.Hash]bool,
) (types.Hash, types.Hash, error) {
	empty := emptyHash

	if len(inserts) == 0 && len(deletes) == 0 {
		if nodeId == zeroHash {
			return zeroHash, empty, nil
		}
		// Unchanged subtree: compute its hash at `level` from disk.
		h, err := ctx.padSubtreeIdOnDisk(nodeId, level)
		if err != nil {
			return zeroHash, empty, err
		}
		return nodeId, h, nil
	}

	if nodeId == zeroHash {
		if len(inserts) == 0 {
			return zeroHash, empty, nil
		}
		return ctx.buildSubtree(level, inserts)
	}

	raw, err := ctx.loadNode(nodeId)
	if err != nil {
		return zeroHash, empty, err
	}
	if raw == nil {
		if len(inserts) == 0 {
			return zeroHash, empty, nil
		}
		return ctx.buildSubtree(level, inserts)
	}

	switch node := raw.(type) {
	case *diskLeaf:
		return ctx.applyToLeaf(node, level, inserts, deletes)
	case *diskInternal:
		return ctx.applyToInternal(node, level, inserts, deletes)
	default:
		return zeroHash, empty, ErrCorrupt
	}
}

// applyToLeaf applies changes to a subtree currently consisting of a single leaf.
func (ctx *commitCtx) applyToLeaf(
	existing *diskLeaf,
	level int,
	inserts map[types.Hash][]byte,
	deletes map[types.Hash]bool,
) (types.Hash, types.Hash, error) {
	working := map[types.Hash][]byte{}

	if deletes[existing.path] {
		if v, ok := inserts[existing.path]; ok {
			working[existing.path] = v
		}
	} else if v, ok := inserts[existing.path]; ok {
		working[existing.path] = v
	} else {
		working[existing.path] = existing.value
	}

	for path, v := range inserts {
		if path != existing.path {
			working[path] = v
		}
	}

	if len(working) == 0 {
		return zeroHash, emptyHash, nil
	}
	return ctx.buildSubtree(level, working)
}

// applyToInternal applies changes to a subtree rooted at an internal node.
func (ctx *commitCtx) applyToInternal(
	node *diskInternal,
	level int,
	inserts map[types.Hash][]byte,
	deletes map[types.Hash]bool,
) (types.Hash, types.Hash, error) {
	M := node.level

	if M > level {
		// Single-child chain from `level` to M-1. Get a representative path.
		repPath, err := ctx.getRepPath(node)
		if err != nil {
			return zeroHash, emptyHash, err
		}

		// Partition changes: those diverging in the chain vs those entering the branch at M.
		chainDivInserts := map[types.Hash][]byte{}
		branchInserts := map[types.Hash][]byte{}
		chainDivDeletes := map[types.Hash]bool{}
		branchDeletes := map[types.Hash]bool{}

		for path, v := range inserts {
			if divergeLevelBefore(repPath, path, level, M) < M {
				chainDivInserts[path] = v
			} else {
				branchInserts[path] = v
			}
		}
		for path := range deletes {
			if divergeLevelBefore(repPath, path, level, M) < M {
				chainDivDeletes[path] = true
			} else {
				branchDeletes[path] = true
			}
		}

		if len(chainDivInserts) > 0 {
			// Some inserts diverge in the chain: restructure.
			// First apply branch-level changes to get the updated existing subtree.
			var existingId types.Hash
			if len(branchInserts) > 0 || len(branchDeletes) > 0 {
				updatedId, _, err := ctx.applyToInternalAtLevel(node, branchInserts, branchDeletes)
				if err != nil {
					return zeroHash, emptyHash, err
				}
				existingId = updatedId
			} else {
				existingId = node.id()
			}

			// Collect all leaves from the updated existing subtree.
			existingLeaves, err := ctx.collectLeaves(existingId)
			if err != nil {
				return zeroHash, emptyHash, err
			}
			// Merge with chain-diverging inserts; chain-diverging deletes are no-ops.
			for path, v := range chainDivInserts {
				existingLeaves[path] = v
			}
			if len(existingLeaves) == 0 {
				return zeroHash, emptyHash, nil
			}
			return ctx.buildSubtree(level, existingLeaves)
		}

		// All changes enter the branch at M.
		newId, hashAtM, err := ctx.applyToInternalAtLevel(node, branchInserts, branchDeletes)
		if err != nil {
			return zeroHash, emptyHash, err
		}
		if newId == zeroHash {
			return zeroHash, emptyHash, nil
		}
		// Fold hashAtM from M-1 down to `level` using repPath bits.
		h := hashAtM
		for lvl := M - 1; lvl >= level; lvl-- {
			if pathBit(repPath, lvl) == 0 {
				h = InternalHash(h, emptyHash)
			} else {
				h = InternalHash(emptyHash, h)
			}
		}
		return newId, h, nil
	}

	// level == M.
	return ctx.applyToInternalAtLevel(node, inserts, deletes)
}

// applyToInternalAtLevel processes changes at an internal node where level == node.level.
// Returns (newId, hashAtM, error) where hashAtM is the subtree hash at node.level.
func (ctx *commitCtx) applyToInternalAtLevel(
	node *diskInternal,
	inserts map[types.Hash][]byte,
	deletes map[types.Hash]bool,
) (types.Hash, types.Hash, error) {
	M := node.level

	leftInserts := map[types.Hash][]byte{}
	rightInserts := map[types.Hash][]byte{}
	leftDeletes := map[types.Hash]bool{}
	rightDeletes := map[types.Hash]bool{}
	for path, v := range inserts {
		if pathBit(path, M) == 0 {
			leftInserts[path] = v
		} else {
			rightInserts[path] = v
		}
	}
	for path := range deletes {
		if pathBit(path, M) == 0 {
			leftDeletes[path] = true
		} else {
			rightDeletes[path] = true
		}
	}

	newLeftId, newLeftHash, err := ctx.applyChanges(node.leftId, M+1, leftInserts, leftDeletes)
	if err != nil {
		return zeroHash, emptyHash, err
	}
	newRightId, newRightHash, err := ctx.applyChanges(node.rightId, M+1, rightInserts, rightDeletes)
	if err != nil {
		return zeroHash, emptyHash, err
	}

	if newLeftId == zeroHash && newRightId == zeroHash {
		return zeroHash, emptyHash, nil
	}
	if newLeftId == zeroHash {
		// Right survives. Right side has pathBit(.,M)==1 by definition.
		h := InternalHash(emptyHash, newRightHash)
		return newRightId, h, nil
	}
	if newRightId == zeroHash {
		// Left survives. Left side has pathBit(.,M)==0 by definition.
		h := InternalHash(newLeftHash, emptyHash)
		return newLeftId, h, nil
	}

	id, hash, err := ctx.makeInternalNode(M, newLeftId, newLeftHash, newRightId, newRightHash)
	if err != nil {
		return zeroHash, emptyHash, err
	}
	return id, hash, nil
}

// makeInternalNode creates (or reuses) a diskInternal node at `level`.
// leftHash and rightHash are the child hashes at level+1 (already computed by the caller).
// Returns (id, hashAtLevel, error). For an Internal node, id == hashAtLevel.
// Handles content-addressed dedup and child refcount increments (§5).
func (ctx *commitCtx) makeInternalNode(
	level int,
	leftId, leftHash, rightId, rightHash types.Hash,
) (types.Hash, types.Hash, error) {
	n := &diskInternal{
		level:     level,
		leftHash:  leftHash,
		leftId:    leftId,
		rightHash: rightHash,
		rightId:   rightId,
	}
	id := n.id() // InternalHash(leftHash, rightHash) == subtree hash at level

	exists, err := ctx.nodeExists(id)
	if err != nil {
		return zeroHash, emptyHash, err
	}
	if !exists {
		ctx.writeNode(id, serializeInternal(n))
		// +1 each non-empty child for the edges this new node creates (§5).
		if leftId != zeroHash {
			ctx.rc.inc(leftId)
		}
		if rightId != zeroHash {
			ctx.rc.inc(rightId)
		}
	}
	return id, id, nil
}

// buildSubtree builds a compact subtree from scratch from a map of (path, rawValue) pairs
// using the §7 recursive algorithm. Returns (id, hashAtLevel, error).
// All hash values flow through return values; no re-reads of pending nodes are needed.
func (ctx *commitCtx) buildSubtree(
	level int,
	leaves map[types.Hash][]byte,
) (types.Hash, types.Hash, error) {
	if len(leaves) == 0 {
		return zeroHash, emptyHash, nil
	}
	if len(leaves) == 1 {
		for path, v := range leaves {
			l := &diskLeaf{path: path, value: v}
			id := l.id()
			exists, err := ctx.nodeExists(id)
			if err != nil {
				return zeroHash, emptyHash, err
			}
			if !exists {
				ctx.writeNode(id, serializeLeaf(l))
			}
			// Hash at `level` = padSubtree(leaf, level) (§4.1).
			h := padSubtree(&cLeaf{path: path, value: l.value}, level)
			return id, h, nil
		}
	}

	leftLeaves := map[types.Hash][]byte{}
	rightLeaves := map[types.Hash][]byte{}
	for path, v := range leaves {
		if pathBit(path, level) == 0 {
			leftLeaves[path] = v
		} else {
			rightLeaves[path] = v
		}
	}

	if len(leftLeaves) == 0 {
		// All leaves go right (bit=1). Recurse to level+1 and pad: right = InternalHash(default, h).
		id, childHash, err := ctx.buildSubtree(level+1, rightLeaves)
		if err != nil {
			return zeroHash, emptyHash, err
		}
		// Fold: all leaves have pathBit(.,level)==1 → right side.
		h := InternalHash(emptyHash, childHash)
		return id, h, nil
	}
	if len(rightLeaves) == 0 {
		// All leaves go left (bit=0). Recurse and pad: left = InternalHash(h, default).
		id, childHash, err := ctx.buildSubtree(level+1, leftLeaves)
		if err != nil {
			return zeroHash, emptyHash, err
		}
		h := InternalHash(childHash, emptyHash)
		return id, h, nil
	}

	leftId, leftHash, err := ctx.buildSubtree(level+1, leftLeaves)
	if err != nil {
		return zeroHash, emptyHash, err
	}
	rightId, rightHash, err := ctx.buildSubtree(level+1, rightLeaves)
	if err != nil {
		return zeroHash, emptyHash, err
	}

	return ctx.makeInternalNode(level, leftId, leftHash, rightId, rightHash)
}

// ---- hash helpers (read-only, using pending cache) ----

// padSubtreeIdOnDisk computes the subtree hash of node `id` at `toLevel` (§4.1).
// Loads via pending cache first, then disk. Folds the single-child chain from the node's
// native level up to toLevel using the representative path's bits.
func (ctx *commitCtx) padSubtreeIdOnDisk(id types.Hash, toLevel int) (types.Hash, error) {
	if id == zeroHash {
		return emptyHash, nil
	}
	raw, err := ctx.loadNode(id)
	if err != nil {
		return types.Hash{}, err
	}
	if raw == nil {
		return emptyHash, nil
	}

	var h types.Hash
	var fromLevel int
	var repPath types.Hash

	switch node := raw.(type) {
	case *diskLeaf:
		h = LeafHash(node.path, node.value)
		fromLevel = treeDepth
		repPath = node.path
	case *diskInternal:
		h = node.id()
		fromLevel = node.level
		rp, err := ctx.getRepPath(node)
		if err != nil {
			return types.Hash{}, err
		}
		repPath = rp
	default:
		return types.Hash{}, ErrCorrupt
	}

	for lvl := fromLevel - 1; lvl >= toLevel; lvl-- {
		if pathBit(repPath, lvl) == 0 {
			h = InternalHash(h, emptyHash)
		} else {
			h = InternalHash(emptyHash, h)
		}
	}
	return h, nil
}

// getRepPath returns a representative leaf path from a diskInternal's subtree.
func (ctx *commitCtx) getRepPath(node *diskInternal) (types.Hash, error) {
	cur := node.leftId
	if cur == zeroHash {
		cur = node.rightId
	}
	return ctx.getRepPathById(cur)
}

// getRepPathById walks the left-most child chain until a leaf is found.
func (ctx *commitCtx) getRepPathById(id types.Hash) (types.Hash, error) {
	cur := id
	for {
		if cur == zeroHash {
			return types.Hash{}, ErrCorrupt
		}
		raw, err := ctx.loadNode(cur)
		if err != nil {
			return types.Hash{}, err
		}
		if raw == nil {
			return types.Hash{}, ErrCorrupt
		}
		switch n := raw.(type) {
		case *diskLeaf:
			return n.path, nil
		case *diskInternal:
			cur = n.leftId
			if cur == zeroHash {
				cur = n.rightId
			}
		}
	}
}

// collectLeaves returns all leaves in the subtree rooted at `id` as path→rawValue.
func (ctx *commitCtx) collectLeaves(id types.Hash) (map[types.Hash][]byte, error) {
	result := map[types.Hash][]byte{}
	if err := ctx.collectLeavesInto(id, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (ctx *commitCtx) collectLeavesInto(id types.Hash, out map[types.Hash][]byte) error {
	if id == zeroHash {
		return nil
	}
	raw, err := ctx.loadNode(id)
	if err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	switch node := raw.(type) {
	case *diskLeaf:
		out[node.path] = node.value
	case *diskInternal:
		if err := ctx.collectLeavesInto(node.leftId, out); err != nil {
			return err
		}
		if err := ctx.collectLeavesInto(node.rightId, out); err != nil {
			return err
		}
	}
	return nil
}

// Prove returns the raw value (nil if absent) and a compressed-sparse proof (§6.3) for key at
// the committed version identifier. It reproduces proveLeaves' sibling set by walking the
// compact on-disk node tree (§6 of the design doc). Returns ErrNoVersion if the height is
// not retained.
func (t *NodeTree) Prove(identifier types.HashHeight, key []byte) (value []byte, proof []byte, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	rootId, err := t.loadVersionRoot(identifier.Height)
	if err != nil {
		return nil, nil, err
	}

	path := types.NewHash(key)

	// Initialize sib to defaults, exactly as proveLeaves does.
	var sib [treeDepth]types.Hash
	for l := 0; l < treeDepth; l++ {
		sib[l] = emptyHash
	}

	rctx := &readCtx{ldb: t.ldb}
	present, value, err := rctx.proveFromRoot(rootId, path, &sib)
	if err != nil {
		return nil, nil, err
	}

	proof = encodeProof(present, value, path, sib)
	if present {
		return value, proof, nil
	}
	return nil, proof, nil
}

// ---- read context (for Root, not in Commit) ----

// readCtx is a thin wrapper for read-only operations that only use committed disk state.
type readCtx struct {
	ldb *leveldb.DB
}

func (rc *readCtx) loadNode(id types.Hash) (interface{}, error) {
	if id == zeroHash {
		return nil, nil
	}
	data, err := rc.ldb.Get(nsNodeKey(id), nil)
	if err == leveldb.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return deserializeNode(data)
}

func (rc *readCtx) padSubtreeIdOnDisk(id types.Hash, toLevel int) (types.Hash, error) {
	if id == zeroHash {
		return emptyHash, nil
	}
	raw, err := rc.loadNode(id)
	if err != nil {
		return types.Hash{}, err
	}
	if raw == nil {
		return emptyHash, nil
	}

	var h types.Hash
	var fromLevel int
	var repPath types.Hash

	switch node := raw.(type) {
	case *diskLeaf:
		h = LeafHash(node.path, node.value)
		fromLevel = treeDepth
		repPath = node.path
	case *diskInternal:
		h = node.id()
		fromLevel = node.level
		rp, err := rc.getRepPath(node)
		if err != nil {
			return types.Hash{}, err
		}
		repPath = rp
	default:
		return types.Hash{}, ErrCorrupt
	}

	for lvl := fromLevel - 1; lvl >= toLevel; lvl-- {
		if pathBit(repPath, lvl) == 0 {
			h = InternalHash(h, emptyHash)
		} else {
			h = InternalHash(emptyHash, h)
		}
	}
	return h, nil
}

// getRepPath returns a representative leaf path from a diskInternal's subtree, falling back
// to rightId when leftId is empty, mirroring commitCtx.getRepPath.
func (rc *readCtx) getRepPath(node *diskInternal) (types.Hash, error) {
	cur := node.leftId
	if cur == zeroHash {
		cur = node.rightId
	}
	return rc.getRepPathById(cur)
}

func (rc *readCtx) getRepPathById(id types.Hash) (types.Hash, error) {
	cur := id
	for {
		if cur == zeroHash {
			return types.Hash{}, ErrCorrupt
		}
		raw, err := rc.loadNode(cur)
		if err != nil {
			return types.Hash{}, err
		}
		if raw == nil {
			return types.Hash{}, ErrCorrupt
		}
		switch n := raw.(type) {
		case *diskLeaf:
			return n.path, nil
		case *diskInternal:
			cur = n.leftId
			if cur == zeroHash {
				cur = n.rightId
			}
		}
	}
}

// padDiskLeaf computes the subtree hash of a single diskLeaf at `level` (equivalent to
// padLeaf in compute.go, but operating on diskLeaf).
func padDiskLeaf(l *diskLeaf, level int) types.Hash {
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

// proveFromRoot walks from rootId, filling sib[0..255] as proveLeaves does.
// Returns (present, value, error). sib must already be initialized to defaults.
//
// The stored rootId may be an Internal@L with L > 0 (a compact tree whose leaves all share
// bits 0..L-1). Those chain levels are NOT stored as Internal nodes; they are single-child
// levels. proveFromRoot handles them before descending into the actual Internal.
func (rc *readCtx) proveFromRoot(rootId types.Hash, targetPath types.Hash, sib *[treeDepth]types.Hash) (bool, []byte, error) {
	// Base case: empty tree.
	if rootId == zeroHash {
		return false, nil, nil
	}

	raw, err := rc.loadNode(rootId)
	if err != nil {
		return false, nil, err
	}
	if raw == nil {
		return false, nil, nil
	}

	switch node := raw.(type) {
	case *diskLeaf:
		// Single-leaf root: the leaf rule handles all chain levels 0..255 automatically.
		return rc.proveLeafRule(node, 0, targetPath, sib)
	case *diskInternal:
		L := node.level
		if L > 0 {
			// Chain levels 0..L-1 are single-child. Walk them to detect if targetPath
			// diverges before reaching the branch at L.
			// Get a representative path to determine which side the chain goes.
			repPath, err := rc.getRepPath(node)
			if err != nil {
				return false, nil, err
			}
			for l := 0; l < L; l++ {
				if pathBit(targetPath, l) != pathBit(repPath, l) {
					// Target diverges at chain level l: off-path side is the entire subtree.
					// sib[l] = subtree hash at l+1 = padSubtreeIdOnDisk(rootId, l+1).
					h, err := rc.padSubtreeIdOnDisk(rootId, l+1)
					if err != nil {
						return false, nil, err
					}
					sib[l] = h
					// sib[l+1..255] stay default (target's slot is empty).
					return false, nil, nil
				}
				// sib[l] stays default (off-path is empty at this chain level).
			}
		}
		return rc.proveInternal(node, targetPath, sib)
	default:
		return false, nil, ErrCorrupt
	}
}

// proveInternal handles the walk from an Internal@L node.
func (rc *readCtx) proveInternal(node *diskInternal, targetPath types.Hash, sib *[treeDepth]types.Hash) (bool, []byte, error) {
	L := node.level

	// Record the off-path child's stored hash as sib[L].
	var onPathId types.Hash
	if pathBit(targetPath, L) == 0 {
		sib[L] = node.rightHash
		onPathId = node.leftId
	} else {
		sib[L] = node.leftHash
		onPathId = node.rightId
	}

	// On-path child is empty.
	if onPathId == zeroHash {
		// sib[L+1..255] stay default.
		return false, nil, nil
	}

	// Load the on-path child.
	raw, err := rc.loadNode(onPathId)
	if err != nil {
		return false, nil, err
	}
	if raw == nil {
		return false, nil, nil
	}

	switch child := raw.(type) {
	case *diskLeaf:
		return rc.proveLeafRule(child, L+1, targetPath, sib)
	case *diskInternal:
		M := child.level
		if M > L+1 {
			// Skipped-chain levels L+1..M-1: single-child levels. Check if targetPath
			// diverges from the subtree before reaching level M.
			repPath, repErr := rc.getRepPath(child)
			if repErr != nil {
				return false, nil, repErr
			}
			for l := L + 1; l < M; l++ {
				if pathBit(targetPath, l) != pathBit(repPath, l) {
					// Target diverges in the chain: off-path is the child subtree at l+1.
					h, err := rc.padSubtreeIdOnDisk(onPathId, l+1)
					if err != nil {
						return false, nil, err
					}
					sib[l] = h
					return false, nil, nil
				}
			}
		}
		return rc.proveInternal(child, targetPath, sib)
	default:
		return false, nil, ErrCorrupt
	}
}

// proveLeafRule handles "on-path child is a Leaf", starting comparison from `fromLevel`.
// It walks targetPath vs leaf.path from fromLevel downward, setting sib at the divergence.
func (rc *readCtx) proveLeafRule(leaf *diskLeaf, fromLevel int, targetPath types.Hash, sib *[treeDepth]types.Hash) (bool, []byte, error) {
	for D := fromLevel; D < treeDepth; D++ {
		if pathBit(targetPath, D) != pathBit(leaf.path, D) {
			// Divergence at D: leaf is the sibling subtree at D+1 on its side.
			sib[D] = padDiskLeaf(leaf, D+1)
			// Target's side is empty; remaining sib[D+1..255] stay default.
			return false, nil, nil
		}
	}
	// No divergence: the target IS this leaf (inclusion). The value is stored in the node.
	return true, leaf.value, nil
}

// Truncate removes every version above identifier (a reorg) and restores identifier as the
// frontier. It mirrors tree.go.Truncate but operates on the node store's version→root keyspace
// and refcount cascade instead of leaf deltas.
//
// Guards (matching tree.go): returns ErrNoVersion if identifier is above the current frontier
// or if identifier's height is not a committed version (and is non-zero).
func (t *NodeTree) Truncate(identifier types.HashHeight) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if identifier.Height > t.frontier.Height {
		return ErrNoVersion
	}
	// Height 0 is always "the empty origin" — no version→root entry needed for it.
	if identifier.Height != 0 {
		if _, err := t.loadVersionRoot(identifier.Height); err != nil {
			return err
		}
	}

	batch := new(leveldb.Batch)
	rc := &refcountAccum{ldb: t.ldb}

	for h := t.frontier.Height; h > identifier.Height; h-- {
		rootId, err := t.loadVersionRoot(h)
		if err != nil {
			if err == ErrNoVersion {
				// Already pruned — skip.
				continue
			}
			return err
		}
		batch.Delete(nsVersionKey(h))
		if rootId != zeroHash {
			if err := decrementCascade(t.ldb, batch, rc, rootId, 1); err != nil {
				return err
			}
		}
	}

	if err := rc.flush(batch); err != nil {
		return err
	}

	batch.Put(nsKeyFrontier, identifier.Bytes())
	if err := t.ldb.Write(batch, nil); err != nil {
		return err
	}
	t.frontier = identifier
	t.staged = nil
	return nil
}

// Prune deletes every version→root entry for height < horizon, decrementing refcounts via
// cascade. Heights below horizon will return ErrNoVersion from Root/Prove after this call.
func (t *NodeTree) Prune(horizon uint64) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	batch := new(leveldb.Batch)
	rc := &refcountAccum{ldb: t.ldb}

	iter := t.ldb.NewIterator(util.BytesPrefix(nsKeyVersion), nil)
	for iter.Next() {
		key := iter.Key()
		h := common.BytesToUint64(key[len(nsKeyVersion):])
		if h >= horizon {
			break // version keys are big-endian → height-ordered
		}
		var rootId types.Hash
		copy(rootId[:], iter.Value())
		if rootId != zeroHash {
			if err := decrementCascade(t.ldb, batch, rc, rootId, 1); err != nil {
				iter.Release()
				return err
			}
		}
		batch.Delete(append([]byte{}, key...))
	}
	iter.Release()
	if err := iter.Error(); err != nil {
		return err
	}

	if err := rc.flush(batch); err != nil {
		return err
	}

	return t.ldb.Write(batch, nil)
}

// decrementCascade decrements node id's refcount by `by`. If the refcount reaches 0, the
// node bytes and refcount record are deleted from the batch, and if the node is an Internal,
// each non-empty child is recursively decremented (−2 if the same id is in both child slots,
// per the edge-multiset definition in §5).
//
// The refcountAccum `rc` accumulates deltas; the caller must call rc.flush(batch) once after
// all cascade calls are done. Node deletes are written directly into `batch`.
//
// Re-entrant safety: if the same node is decremented to 0 by a previous call in the same
// batch (already marked deleted), we still accumulate the decrement in rc.delta (for flush to
// skip) but do not recurse into children again — the first cascade already queued their frees.
func decrementCascade(ldb *leveldb.DB, batch *leveldb.Batch, rc *refcountAccum, id types.Hash, by int64) error {
	// Always accumulate the decrement so flush has the full picture.
	rc.dec(id, by)

	// If this node was already freed by an earlier cascade in this batch, do not recurse again.
	if rc.deleted[id] {
		return nil
	}

	// Compute what the refcount will be after all accumulated changes.
	cur, err := rc.loadRef(id)
	if err != nil {
		return err
	}
	pending := rc.delta[id] // full accumulated delta (negative)
	newVal := int64(cur) + pending
	if newVal > 0 {
		// Node still referenced by other retained versions — stop here.
		return nil
	}

	// Refcount hits 0: delete the node and its refcount record, and cascade to children.
	// Mark deleted before recursing so re-entrant calls (shared children) skip the cascade.
	rc.markDeleted(id)
	batch.Delete(nsNodeKey(id))
	batch.Delete(nsRefKey(id))

	// Load the node to find children (if Internal).
	data, err := ldb.Get(nsNodeKey(id), nil)
	if err == leveldb.ErrNotFound {
		// Node already absent — nothing to cascade into.
		return nil
	}
	if err != nil {
		return err
	}
	node, err := deserializeNode(data)
	if err != nil {
		return err
	}
	internal, ok := node.(*diskInternal)
	if !ok {
		// Leaf: no children.
		return nil
	}

	// Cascade into children. If both slots hold the same id, decrement by 2.
	if internal.leftId != zeroHash && internal.leftId == internal.rightId {
		return decrementCascade(ldb, batch, rc, internal.leftId, 2)
	}
	if internal.leftId != zeroHash {
		if err := decrementCascade(ldb, batch, rc, internal.leftId, 1); err != nil {
			return err
		}
	}
	if internal.rightId != zeroHash {
		if err := decrementCascade(ldb, batch, rc, internal.rightId, 1); err != nil {
			return err
		}
	}
	return nil
}

// ---- refcount accumulator ----

// refcountAccum accumulates per-node refcount increments and flushes them to a batch.
type refcountAccum struct {
	ldb     *leveldb.DB
	delta   map[types.Hash]int64
	deleted map[types.Hash]bool // ids whose refcount records will be deleted, not written
}

func (rc *refcountAccum) inc(id types.Hash) {
	if rc.delta == nil {
		rc.delta = map[types.Hash]int64{}
	}
	rc.delta[id]++
}

func (rc *refcountAccum) dec(id types.Hash, by int64) {
	if rc.delta == nil {
		rc.delta = map[types.Hash]int64{}
	}
	rc.delta[id] -= by
}

// markDeleted records that the node's refcount record will be deleted from the DB (rather
// than written back as zero). flush skips deleted ids to avoid writing a zero-count record
// after the Delete has already been queued in the batch.
func (rc *refcountAccum) markDeleted(id types.Hash) {
	if rc.deleted == nil {
		rc.deleted = map[types.Hash]bool{}
	}
	rc.deleted[id] = true
}

func (rc *refcountAccum) flush(batch *leveldb.Batch) error {
	for id, d := range rc.delta {
		if d == 0 {
			continue
		}
		if rc.deleted[id] {
			// The node and its refcount record are already queued for deletion.
			continue
		}
		cur, err := rc.loadRef(id)
		if err != nil {
			return err
		}
		newVal := int64(cur) + d
		if newVal < 0 {
			newVal = 0
		}
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, uint64(newVal))
		batch.Put(nsRefKey(id), buf)
	}
	return nil
}

func (rc *refcountAccum) loadRef(id types.Hash) (uint64, error) {
	data, err := rc.ldb.Get(nsRefKey(id), nil)
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
