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

// Storage layout on the tree's own leveldb. The current (frontier) leaf set is held both in
// memory (authoritative, for O(1)-read root/proof computation) and persisted as a single
// keyspace updated by delta each commit — so a commit costs O(changed keys), not O(all keys).
// Each version also persists a reversible delta (the prior value of every key it changed),
// which lets a reorg undo back to any height in the rollback window and a historical read
// reconstruct an earlier leaf set.
var (
	keyFrontier    = []byte{0x00} // -> frontier HashHeight (hash ‖ height)
	keyVersionHash = []byte{0x01} // {0x01, height} -> version hash
	keyDelta       = []byte{0x02} // {0x02, height} -> reversible delta
	keyLeaf        = []byte{0x03} // {0x03, path} -> value (current frontier leaf set)
)

var (
	ErrNoVersion = errors.New("trie: no such version")
	ErrCorrupt   = errors.New("trie: corrupt tree database")
)

// Tree is the persisted, versioned state tree (spec §4.2). The consensus-visible root and
// proof format come entirely from the pure layer (hash.go / compute.go / proof.go); this type
// only manages versioned storage. The full on-disk node store with structural sharing — which
// would make updates and root computation O(changed keys · depth) and avoid holding the leaf
// set in memory — remains a later optimization that must reproduce these exact roots (§6.4).
type Tree struct {
	mu  sync.Mutex
	ldb *leveldb.DB

	frontier types.HashHeight
	leaves   map[types.Hash][]byte // current frontier leaf set
	staged   map[types.Hash]stagedOp
}

type stagedOp struct {
	del   bool
	value []byte
}

// NewTree opens a Tree over an existing leveldb handle, recovering the frontier pointer and
// loading the current leaf set into memory.
func NewTree(ldb *leveldb.DB) (*Tree, error) {
	t := &Tree{ldb: ldb, leaves: map[types.Hash][]byte{}}

	data, err := ldb.Get(keyFrontier, nil)
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

	iter := ldb.NewIterator(util.BytesPrefix(keyLeaf), nil)
	defer iter.Release()
	for iter.Next() {
		var path types.Hash
		copy(path[:], iter.Key()[len(keyLeaf):])
		value := make([]byte, len(iter.Value()))
		copy(value, iter.Value())
		t.leaves[path] = value
	}
	return t, iter.Error()
}

func heightKey(prefix []byte, height uint64) []byte {
	return common.JoinBytes(prefix, common.Uint64ToBytes(height))
}

func leafKey(path types.Hash) []byte {
	return common.JoinBytes(keyLeaf, path[:])
}

func leavesOf(m map[types.Hash][]byte) []leaf {
	leaves := make([]leaf, 0, len(m))
	for path, value := range m {
		leaves = append(leaves, leaf{path: path, value: value})
	}
	return leaves
}

// Update stages a write-set (fold rule §3.1.1) for the next Commit. It does not persist.
func (t *Tree) Update(changes db.Patch) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	staged := map[types.Hash]stagedOp{}
	if err := changes.Replay(&stagedApplier{staged: staged}); err != nil {
		return err
	}
	t.staged = staged
	return nil
}

// Commit applies the staged write-set as version identifier, persisting the leaf changes and a
// reversible delta in one leveldb batch, and advances the frontier. The root is NOT computed
// here — it is derived on demand by Root/ComputeRoot (O(N)), so momentums whose root is never
// read (every momentum before the state-root spork is active) cost only O(changed keys).
func (t *Tree) Commit(identifier types.HashHeight) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	batch := new(leveldb.Batch)
	var delta delta
	for path, op := range t.staged {
		old, hadOld := t.leaves[path]
		delta.add(path, old, hadOld)
		if op.del {
			delete(t.leaves, path)
			batch.Delete(leafKey(path))
		} else {
			t.leaves[path] = op.value
			batch.Put(leafKey(path), op.value)
		}
	}
	batch.Put(heightKey(keyDelta, identifier.Height), delta.bytes())
	batch.Put(heightKey(keyVersionHash, identifier.Height), identifier.Hash[:])
	batch.Put(keyFrontier, identifier.Bytes())
	if err := t.ldb.Write(batch, nil); err != nil {
		return err
	}

	t.frontier = identifier
	t.staged = nil
	return nil
}

// leavesAt returns the leaf set at a committed height: the in-memory frontier set if height is
// the frontier, otherwise a copy with the intervening deltas undone (bounded by the reorg
// window). The caller must hold t.mu.
func (t *Tree) leavesAt(height uint64) (map[types.Hash][]byte, error) {
	if height == t.frontier.Height {
		return t.leaves, nil
	}
	if height > t.frontier.Height {
		return nil, ErrNoVersion
	}
	m := make(map[types.Hash][]byte, len(t.leaves))
	for k, v := range t.leaves {
		m[k] = v
	}
	for i := t.frontier.Height; i > height; i-- {
		raw, err := t.ldb.Get(heightKey(keyDelta, i), nil)
		if err == leveldb.ErrNotFound {
			return nil, ErrNoVersion
		}
		if err != nil {
			return nil, err
		}
		d, err := parseDelta(raw)
		if err != nil {
			return nil, err
		}
		d.undo(m)
	}
	return m, nil
}

func (t *Tree) hasVersion(height uint64) (bool, error) {
	if height == 0 || height == t.frontier.Height {
		return true, nil
	}
	_, err := t.ldb.Get(heightKey(keyVersionHash, height), nil)
	if err == leveldb.ErrNotFound {
		return false, nil
	}
	return err == nil, err
}

// Root returns the committed root at identifier's height.
func (t *Tree) Root(identifier types.HashHeight) (types.Hash, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ok, err := t.hasVersion(identifier.Height); err != nil {
		return types.Hash{}, err
	} else if !ok {
		return types.Hash{}, ErrNoVersion
	}
	m, err := t.leavesAt(identifier.Height)
	if err != nil {
		return types.Hash{}, err
	}
	return rootOfLeaves(leavesOf(m)), nil
}

// ComputeRoot folds changes (fold rule §3.1.1) onto the previous version WITHOUT committing —
// the verifier's hook (§3.3). In the consensus path previous is always the frontier.
func (t *Tree) ComputeRoot(previous types.HashHeight, changes db.Patch) (types.Hash, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ok, err := t.hasVersion(previous.Height); err != nil {
		return types.Hash{}, err
	} else if !ok {
		return types.Hash{}, ErrNoVersion
	}
	base, err := t.leavesAt(previous.Height)
	if err != nil {
		return types.Hash{}, err
	}
	m := make(map[types.Hash][]byte, len(base))
	for k, v := range base {
		m[k] = v
	}
	if err := changes.Replay(&stagedApplierMap{m: m}); err != nil {
		return types.Hash{}, err
	}
	return rootOfLeaves(leavesOf(m)), nil
}

// Prove returns the raw value (nil if absent) and a compressed-sparse proof (§6.3) for key at
// the committed version identifier.
func (t *Tree) Prove(identifier types.HashHeight, key []byte) (value []byte, proof []byte, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ok, err := t.hasVersion(identifier.Height); err != nil {
		return nil, nil, err
	} else if !ok {
		return nil, nil, ErrNoVersion
	}
	m, err := t.leavesAt(identifier.Height)
	if err != nil {
		return nil, nil, err
	}
	leaves := leavesOf(m)
	sortLeaves(leaves)
	path := types.NewHash(key)
	present, value, sib := proveLeaves(leaves, path)
	proof = encodeProof(present, value, path, sib)
	if present {
		return value, proof, nil
	}
	return nil, proof, nil
}

// Truncate removes every version above identifier (a reorg), undoing each version's delta to
// restore the leaf set, and restores identifier as the frontier (spec §4.2).
func (t *Tree) Truncate(identifier types.HashHeight) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if identifier.Height > t.frontier.Height {
		return ErrNoVersion
	}
	if ok, err := t.hasVersion(identifier.Height); err != nil {
		return err
	} else if !ok {
		return ErrNoVersion
	}

	batch := new(leveldb.Batch)
	for i := t.frontier.Height; i > identifier.Height; i-- {
		raw, err := t.ldb.Get(heightKey(keyDelta, i), nil)
		if err != nil {
			return err
		}
		d, err := parseDelta(raw)
		if err != nil {
			return err
		}
		for _, e := range d.entries {
			if e.hadOld {
				t.leaves[e.path] = e.oldValue
				batch.Put(leafKey(e.path), e.oldValue)
			} else {
				delete(t.leaves, e.path)
				batch.Delete(leafKey(e.path))
			}
		}
		batch.Delete(heightKey(keyDelta, i))
		batch.Delete(heightKey(keyVersionHash, i))
	}
	batch.Put(keyFrontier, identifier.Bytes())
	if err := t.ldb.Write(batch, nil); err != nil {
		return err
	}
	t.frontier = identifier
	t.staged = nil
	return nil
}

// Prune deletes every version strictly below horizon — its reversible delta and version-hash
// records — so those heights become unservable (Root/Prove/ComputeRoot return ErrNoVersion).
// It is local-only and lossless to consensus: the current frontier leaf set and the root in
// each signed momentum header are untouched, and a fresh Init rebuilds any pruned version from
// the chain's patches. The caller must keep horizon at least the reorg window below the
// frontier so a legal reorg can still undo (Truncate needs the deltas it removes), §7.5/§8.
func (t *Tree) Prune(horizon uint64) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	batch := new(leveldb.Batch)
	for _, prefix := range [][]byte{keyDelta, keyVersionHash} {
		iter := t.ldb.NewIterator(util.BytesPrefix(prefix), nil)
		for iter.Next() {
			key := iter.Key()
			// Heights are big-endian, so iteration is height-ordered: once we reach a height
			// at or above the horizon, nothing below it remains.
			if common.BytesToUint64(key[len(prefix):len(prefix)+8]) >= horizon {
				break
			}
			batch.Delete(append([]byte{}, key...))
		}
		iter.Release()
		if err := iter.Error(); err != nil {
			return err
		}
	}
	return t.ldb.Write(batch, nil)
}

// FrontierIdentifier returns the highest committed version.
func (t *Tree) FrontierIdentifier() types.HashHeight {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.frontier
}

// ---- reversible per-version delta ----

type deltaEntry struct {
	path     types.Hash
	hadOld   bool
	oldValue []byte
}

type delta struct {
	entries []deltaEntry
}

func (d *delta) add(path types.Hash, old []byte, hadOld bool) {
	e := deltaEntry{path: path, hadOld: hadOld}
	if hadOld {
		e.oldValue = make([]byte, len(old))
		copy(e.oldValue, old)
	}
	d.entries = append(d.entries, e)
}

// undo restores the pre-version leaf values into m.
func (d *delta) undo(m map[types.Hash][]byte) {
	for _, e := range d.entries {
		if e.hadOld {
			m[e.path] = e.oldValue
		} else {
			delete(m, e.path)
		}
	}
}

func (d *delta) bytes() []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(d.entries)))
	for _, e := range d.entries {
		out = append(out, e.path[:]...)
		if e.hadOld {
			out = append(out, 1)
			out = binary.BigEndian.AppendUint32(out, uint32(len(e.oldValue)))
			out = append(out, e.oldValue...)
		} else {
			out = append(out, 0)
		}
	}
	return out
}

func parseDelta(raw []byte) (*delta, error) {
	if len(raw) < 4 {
		return nil, ErrCorrupt
	}
	count := int(binary.BigEndian.Uint32(raw[:4]))
	off := 4
	d := &delta{entries: make([]deltaEntry, 0, count)}
	for i := 0; i < count; i++ {
		if off+types.HashSize+1 > len(raw) {
			return nil, ErrCorrupt
		}
		var e deltaEntry
		copy(e.path[:], raw[off:off+types.HashSize])
		off += types.HashSize
		flag := raw[off]
		off++
		if flag == 1 {
			if off+4 > len(raw) {
				return nil, ErrCorrupt
			}
			n := int(binary.BigEndian.Uint32(raw[off : off+4]))
			off += 4
			if off+n > len(raw) {
				return nil, ErrCorrupt
			}
			e.hadOld = true
			e.oldValue = make([]byte, n)
			copy(e.oldValue, raw[off:off+n])
			off += n
		}
		d.entries = append(d.entries, e)
	}
	return d, nil
}
