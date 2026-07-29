package chain

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/pkg/errors"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"

	"github.com/zenon-network/go-zenon/chain/cache/storage"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/chain/store"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/trie"
	"github.com/zenon-network/go-zenon/common/types"
)

// getConsensusOpenFilesCacheCapacity mirrors common/db/leveldb.go and
// chain/cache/storage/db.go's per-package copy: leveldb's default open-files cache capacity is
// too small on darwin's default file-descriptor limits.
func getConsensusOpenFilesCacheCapacity() int {
	switch runtime.GOOS {
	case "darwin":
		return 20
	case "windows":
		return 200
	default:
		return 200
	}
}

// stateTree maintains the versioned Merkleized state tree (spec §7.2). Init reconciles a small
// gap (at most stateTreeBuildTailBatchSize heights) synchronously at startup; any larger gap is
// left for the background build goroutine launched from chain.Start, which advances the tree one
// bounded batch at a time, holding the chain's insert lock for the duration of each batch.
// UpdateStateTree becomes the tree's sole writer, folding one momentum per call, once the build's
// latch (ready) flips true; before then it is a no-op for any height the build has not reached
// yet, and TruncateStateTreeTo mirrors that split for reorgs. The tree is maintained for every
// momentum from genesis onward so the root is ready the moment StateRootSpork activates; the root
// is only produced/enforced once the spork is active (worker + verifier), not here.
// stateTreeRetentionMargin is kept beyond the reorg window so a pruned node can still serve
// recent proofs — comfortably past an off-chain challenge window (≈ MomentumsPerEpoch). ~2 days
// at 10s momentums. The reorg-safety floor is GetRollbackCacheSize() (§7.5); this is added on top.
const stateTreeRetentionMargin = uint64(17280)

// stateTreeBuildBulkBatchSize is the maximum number of momentum patches one build batch merges
// into a single CommitBulk. It caps the staged write-set and, through it, CommitBulk's peak
// memory; stateTreeBuildBatchBudget usually ends the batch first.
const stateTreeBuildBulkBatchSize = uint64(10000)

// stateTreeBuildTailBatchSize is the maximum number of momentums one build batch folds one
// version per height. A per-height fold costs far more than an accumulated one, so the tail cap is
// much smaller. It is also the largest gap Init closes synchronously.
const stateTreeBuildTailBatchSize = uint64(10)

// stateTreeBuildBatchBudget is how long a bounded batch keeps taking heights before it commits
// what it has and releases the chain's insert lock. It bounds the delay a live insert can suffer
// behind the build, independently of hardware speed and of how expensive a height turns out to be.
//
// The measured mainnet per-height fold rate is ~12 momentums/sec, i.e. ~83 ms per height,
// dominated by the per-version copy-on-write commit (~13.3 KB of node writes per momentum, ~4
// distinct keys per momentum, ~3.3 KB of node bytes per key touched). A bulk height skips that
// commit, so it is a small but unmeasured fraction of 83 ms; how many bulk heights fit in a second
// is therefore not predictable from the available data, which is exactly why the batch ends on a
// time budget rather than on a height count. With a 1s budget, a batch holds the insert lock for
// ~1s of accumulation plus its terminal CommitBulk, so worst-case ~2-3s, and the height caps only
// bind on hardware fast enough to reach them inside the budget. At the 10,000-height cap and
// mainnet-shaped data (~40k distinct paths, ~3.3 KB of node bytes each, held roughly three times
// over — pending, the leveldb batch, and the refcount delta, per
// common/trie/nodestore.go:340-342,371-374) a bulk commit peaks in the low hundreds of MB, far
// below the ~5-6 GB of a single unbatched genesis→frontier pass. prune() runs inside the same lock
// hold immediately after the accumulate loop and touches a comparable node set, so the actual
// peak duration and memory a batch holds the insert lock for are roughly double these figures.
// Every completed batch logs its height range and wall-clock duration at Info, so the real figure
// is observable on a mainnet node without new tooling.
const stateTreeBuildBatchBudget = time.Second

// stateTreeBuildBatchPause is how long the background build waits between batches so inserts that
// queued on the insert lock are served before the next batch takes it.
const stateTreeBuildBatchPause = 10 * time.Millisecond

// ErrStateTreeNotReady is returned by the tree's read methods (ComputeStateRoot, StateRoot,
// GetProof) before the build has reached the chain frontier. While not ready, the read methods
// fail closed, the producer does not stamp v3, and the verifier cannot check a v3 root, so once
// StateRootSpork activates the node stops advancing until the build completes.
var ErrStateTreeNotReady = errors.New("state tree: not yet built to the chain frontier")

type stateTree struct {
	dir               string
	archive           bool
	retentionOverride uint64 // 0 = use retentionFloor()
	bulkBatchOverride uint64 // 0 = use stateTreeBuildBulkBatchSize (tests only, mirrors retentionOverride)
	log               log15.Logger
	changes           sync.Mutex

	// ready latches true once the build reaches the chain frontier. It is read by
	// ComputeStateRoot/StateRoot/GetProof without holding changes, so it's a separate atomic
	// rather than being guarded by that mutex.
	ready atomic.Bool

	// ancestryChecked guards the below-frontier ancestry check in catchUp so it runs at most once
	// per stateTree instance (the first catchUp call after open), not on every batch of a
	// background build. Always accessed under changes.
	ancestryChecked bool

	ldb  *leveldb.DB
	tree *trie.NodeTree

	quit        chan struct{} // created in newStateTree; closed by stop()
	buildExited chan struct{} // guarded by changes; the current build goroutine closes it on return
	stopOnce    sync.Once
	stopErr     error // result of the single stop(), replayed to later callers

	rootCacheMu sync.Mutex
	rootCache   *rootCacheEntry
}

// rootCacheEntry memoizes the last ComputeStateRoot call, keyed on the full (previous, changes)
// pair. Produce (packMomentum) and self-verify (verifyStateRoot) call ComputeStateRoot
// back-to-back with identical arguments; this avoids folding the same patch onto the same
// version twice.
type rootCacheEntry struct {
	previous    types.HashHeight
	changesHash types.Hash
	root        types.Hash
}

func newStateTree(dir string, archive bool) *stateTree {
	return &stateTree{
		dir:     dir,
		archive: archive,
		log:     common.ChainLogger.New("submodule", "state-tree"),
		quit:    make(chan struct{}),
	}
}

// retentionFloor is how many versions below the frontier are always kept: the chain rollback
// window (so any legal reorg can undo, §7.5) plus the proof-serving margin.
func retentionFloor() uint64 {
	return uint64(storage.GetRollbackCacheSize()) + stateTreeRetentionMargin
}

// retention is how many versions below the frontier this tree keeps; the retained set is those
// versions plus the frontier itself. It must be >= storage.GetRollbackCacheSize() for any tree
// that serves rollbacks, so that every height inside the rollback window has a version to
// truncate back to.
func (s *stateTree) retention() uint64 {
	if s.retentionOverride != 0 {
		return s.retentionOverride
	}
	return retentionFloor()
}

// bulkBatchSize is the bulk height cap, honouring bulkBatchOverride. Mirrors retention().
func (s *stateTree) bulkBatchSize() uint64 {
	if s.bulkBatchOverride != 0 {
		return s.bulkBatchOverride
	}
	return stateTreeBuildBulkBatchSize
}

// open opens the tree's leveldb and NodeTree, wiping and recreating on ErrFormatMismatch. A no-op
// once already open, so Init can be called more than once (chain/tests/cache_test.go:279).
func (s *stateTree) open() error {
	if s.ldb != nil {
		return nil
	}
	opts := &opt.Options{OpenFilesCacheCapacity: getConsensusOpenFilesCacheCapacity()}
	ldb, err := leveldb.OpenFile(s.dir, opts)
	if err != nil {
		return err
	}
	s.ldb = ldb

	tree, err := trie.NewNodeTree(ldb)
	if err == trie.ErrFormatMismatch {
		// The DB was written by the old leaf-set Tree (or an incompatible format version).
		// Wipe all keys and rebuild from chain patches below (§9 migration).
		s.log.Info("state-tree DB format mismatch — wiping and rebuilding from chain patches")
		if wipeErr := wipeDB(ldb); wipeErr != nil {
			return wipeErr
		}
		tree, err = trie.NewNodeTree(ldb)
	}
	if err != nil {
		return err
	}
	s.tree = tree
	return nil
}

// Init opens the tree's own leveldb and reconciles it with the chain frontier, mirroring
// chainCache.Init (chain/cache.go:113-157). A gap of at most stateTreeBuildTailBatchSize heights
// is closed synchronously, so it blocks the caller only for a build small enough that blocking
// costs nothing; a larger gap is left for the background build (chain.Start) to close, and Init
// returns with ready still false.
func (s *stateTree) Init(chainManager db.Manager, momentumStore store.Momentum) error {
	if err := s.open(); err != nil {
		return err
	}

	s.changes.Lock()
	defer s.changes.Unlock()

	frontierDB := chainManager.Frontier()
	if frontierDB == nil {
		return errors.Errorf("can't initialize state tree: chain db is stopped")
	}
	chainFrontier := db.GetFrontierIdentifier(frontierDB)
	treeFrontier := s.tree.FrontierIdentifier()

	if treeFrontier.Height < chainFrontier.Height &&
		chainFrontier.Height-treeFrontier.Height > stateTreeBuildTailBatchSize {
		s.log.Info("state tree is behind the chain frontier; leaving the gap for the background build",
			"tree-frontier", treeFrontier, "chain-frontier", chainFrontier)
		fmt.Println("State tree is behind the chain frontier.")
		fmt.Println("State-root RPCs are unavailable until the background build catches up.")
		fmt.Println("Once the state-root spork activates, this node will not accept new momentums until the build completes.")
		return nil
	}

	_, err := s.catchUp(chainManager, momentumStore, 0, 0)
	return err
}

// StateTreeReady reports whether the build has reached the chain frontier. Before it does,
// ComputeStateRoot/StateRoot/GetProof fail closed with ErrStateTreeNotReady, and the producer
// must not stamp or enforce v3 (spec §7).
func (s *stateTree) StateTreeReady() bool {
	return s.ready.Load()
}

// tailStart is the first height the build commits its own version for; everything below it is
// folded into one accumulated commit because a non-archive node would prune it immediately.
func (s *stateTree) tailStart(treeFrontierHeight, chainFrontierHeight uint64) uint64 {
	tailStart := treeFrontierHeight + 1
	if !s.archive {
		retention := s.retention()
		if chainFrontierHeight > retention && chainFrontierHeight-retention+1 > tailStart {
			tailStart = chainFrontierHeight - retention + 1
		}
	}
	return tailStart
}

// catchUp advances the tree at most one batch toward the chain frontier and latches ready once the
// tree frontier equals the live chain frontier in height AND hash. bulkLimit/tailLimit of 0 mean
// "no bound" — the whole remaining gap in one call, no time budget — used by the startup and
// offline builds. A bounded call performs either a bulk sub-range or a tail sub-range, never both.
//
// The caller must hold s.changes for the whole call and, whenever the chain is live, the chain's
// insert lock around it.
func (s *stateTree) catchUp(chainManager db.Manager, momentumStore store.Momentum, bulkLimit, tailLimit uint64) (bool, error) {
	frontierDB := chainManager.Frontier()
	if frontierDB == nil {
		return false, errors.Errorf("can't build state tree: chain db is stopped")
	}
	chainFrontier := db.GetFrontierIdentifier(frontierDB)
	treeFrontier := s.tree.FrontierIdentifier()

	if !s.ancestryChecked {
		s.ancestryChecked = true
		if treeFrontier.Height > 0 && treeFrontier.Height < chainFrontier.Height {
			// A resumed tree whose frontier sits below the chain frontier is about to be folded
			// forward. Confirm its persisted frontier is actually on this chain before doing so —
			// a tree built from a forked chain, the wrong network, or a torn snapshot copy must
			// not be silently accepted.
			ancestor, err := momentumStore.GetMomentumByHeight(treeFrontier.Height)
			if err != nil {
				return false, err
			}
			if ancestor == nil || ancestor.Identifier() != treeFrontier {
				return false, errors.Errorf("the state tree's state is incorrect. " +
					"You can fix the problem by removing the state-tree database manually.")
			}
		}
	}

	if treeFrontier.Height >= chainFrontier.Height {
		if treeFrontier.Height > chainFrontier.Height {
			if err := s.rollbackTo(chainFrontier); err != nil {
				return false, err
			}
		} else if treeFrontier.Hash != chainFrontier.Hash {
			return false, errors.Errorf("the state tree's state is incorrect. " +
				"You can fix the problem by removing the state-tree database manually.")
		}
		s.ready.Store(true)
		return true, nil
	}

	var deadline time.Time // zero => unbounded call, no time budget
	if bulkLimit != 0 || tailLimit != 0 {
		deadline = time.Now().Add(stateTreeBuildBatchBudget)
	}

	from := treeFrontier.Height + 1
	tailStart := s.tailStart(treeFrontier.Height, chainFrontier.Height)

	if from < tailStart { // bulk sub-range
		bulkEnd := tailStart - 1
		if bulkLimit != 0 && from+bulkLimit-1 < bulkEnd {
			bulkEnd = from + bulkLimit - 1
		}
		if err := s.accumulateRange(chainManager, momentumStore, from, bulkEnd, chainFrontier.Height, deadline); err != nil {
			return false, err
		}
		from = s.tree.FrontierIdentifier().Height + 1
		if bulkLimit != 0 { // a bounded batch does bulk only
			if err := s.prune(); err != nil {
				return false, err
			}
			return false, nil
		}
	}

	tailEnd := chainFrontier.Height // tail sub-range
	if tailLimit != 0 && from+tailLimit-1 < tailEnd {
		tailEnd = from + tailLimit - 1
	}
	if err := s.foldEach(chainManager, momentumStore, from, tailEnd, chainFrontier.Height, deadline); err != nil {
		return false, err
	}
	if err := s.prune(); err != nil {
		return false, err
	}

	if s.tree.FrontierIdentifier() == chainFrontier {
		s.ready.Store(true)
		return true, nil
	}
	return false, nil
}

// accumulateRange merges the patches of [from..to] into one staged set and commits them as the
// single version at the last height it reaches. It stops early once deadline has passed (a zero
// deadline means no bound), but never before the first height, so the range always ends committed.
// The caller must hold s.changes for the whole call: the trie's accumulate→commit sequence is not
// internally atomic (common/trie/nodestore.go:344-350).
func (s *stateTree) accumulateRange(chainManager db.Manager, momentumStore store.Momentum, from, to, progressTotal uint64, deadline time.Time) error {
	var lastIdentifier types.HashHeight
	for i := from; i <= to; i++ {
		if i > from && !deadline.IsZero() && time.Now().After(deadline) {
			break
		}
		momentum, err := momentumStore.GetMomentumByHeight(i)
		if err != nil {
			return err
		}
		if momentum == nil {
			return errors.Errorf("state tree build: missing momentum at height %d", i)
		}
		// GetPatch returns the post-Replay patch carrying the momentum-frontier keys; the fold
		// rule inside the tree strips {0}/{1}/{2}, so this build folds the same write-set the
		// live maintainer folds from transaction.Changes (the build-vs-replay equivalence).
		changes := chainManager.GetPatch(momentum.Identifier())
		if err := s.tree.AccumulateFrom(changes); err != nil {
			return err
		}
		lastIdentifier = momentum.Identifier()
		if i%100000 == 0 {
			fmt.Printf("Initializing state tree: %d%%\n", i*100/progressTotal)
		}
	}
	return s.tree.CommitBulk(lastIdentifier)
}

// foldEach folds [from..to] as one version per height, stopping early once deadline has passed
// (zero = no bound), never before the first height.
func (s *stateTree) foldEach(chainManager db.Manager, momentumStore store.Momentum, from, to, progressTotal uint64, deadline time.Time) error {
	for i := from; i <= to; i++ {
		if i > from && !deadline.IsZero() && time.Now().After(deadline) {
			break
		}
		momentum, err := momentumStore.GetMomentumByHeight(i)
		if err != nil {
			return err
		}
		if momentum == nil {
			return errors.Errorf("state tree build: missing momentum at height %d", i)
		}
		changes := chainManager.GetPatch(momentum.Identifier())
		if err := s.fold(momentum.Identifier(), changes); err != nil {
			return err
		}
		if i%100000 == 0 {
			fmt.Printf("Initializing state tree: %d%%\n", i*100/progressTotal)
		}
	}
	return nil
}

// fold applies one momentum's write-set and commits it as the next version. The caller must
// hold s.changes.
func (s *stateTree) fold(identifier types.HashHeight, changes db.Patch) error {
	if err := s.tree.Update(changes); err != nil {
		return err
	}
	return s.tree.Commit(identifier)
}

// prune drops versions that have fallen out of the retention window. Local-only; never changes a
// root. No-op on archive nodes. The caller must hold s.changes.
func (s *stateTree) prune() error {
	if s.archive {
		return nil
	}
	frontier := s.tree.FrontierIdentifier().Height
	retention := s.retention()
	if frontier <= retention {
		return nil
	}
	return s.tree.Prune(frontier - retention)
}

func (s *stateTree) UpdateStateTree(insertLocker sync.Locker, detailed *nom.DetailedMomentum, changes db.Patch) error {
	if insertLocker == nil {
		return errors.Errorf("insertLocker can't be nil")
	}
	if changes == nil {
		return errors.Errorf("changes can't be nil")
	}
	s.changes.Lock()
	defer s.changes.Unlock()

	identifier := detailed.Momentum.Identifier()
	frontier := s.tree.FrontierIdentifier()
	if identifier.Height != frontier.Height+1 {
		if !s.ready.Load() {
			// Already folded, or the build has not reached this height yet — it owns every height
			// up to the chain frontier while it runs. Folding out of order would duplicate a
			// version or build on the wrong base.
			s.log.Debug("skipping state-tree update while the build owns the tree", "momentum", identifier, "tree-frontier", frontier)
			return nil
		}
		// Ready: fall through and let the commit report the out-of-order fold.
	} else if detailed.Momentum.Previous() != frontier {
		return errors.Errorf("can't fold %v into the state tree. It does not build on the tree frontier %v", identifier, frontier)
	}
	if err := s.fold(identifier, changes); err != nil {
		return err
	}
	return s.prune()
}

func (s *stateTree) TruncateStateTreeTo(insertLocker sync.Locker, identifier types.HashHeight) error {
	if insertLocker == nil {
		return errors.Errorf("insertLocker can't be nil")
	}
	s.changes.Lock()
	defer s.changes.Unlock()
	if !s.ready.Load() && identifier.Height >= s.tree.FrontierIdentifier().Height {
		// Nothing above the target is committed: the build has not reached this height, or the
		// fold being undone was skipped.
		s.log.Debug("skipping state-tree truncate while the build owns the tree", "target", identifier, "tree-frontier", s.tree.FrontierIdentifier())
		return nil
	}
	return s.rollbackTo(identifier)
}

// rollbackTo truncates every version above identifier. The caller must hold s.changes.
func (s *stateTree) rollbackTo(identifier types.HashHeight) error {
	frontier := s.tree.FrontierIdentifier()
	if identifier.Height > frontier.Height {
		return errors.Errorf("can't rollback state tree. Target %v is greater than frontier %v", identifier, frontier)
	}
	if frontier.Height-identifier.Height > uint64(storage.GetRollbackCacheSize()) {
		return errors.Errorf("can't rollback state tree. Target %v is outside the rollback window", identifier)
	}
	return s.tree.Truncate(identifier)
}

// StateRoot returns the committed root at identifier (spec §9 RPC hook).
func (s *stateTree) StateRoot(identifier types.HashHeight) (types.Hash, error) {
	if !s.ready.Load() {
		return types.Hash{}, ErrStateTreeNotReady
	}
	return s.tree.Root(identifier)
}

// ComputeStateRoot folds changes onto the previous version without committing (the verifier's
// hook, §3.3). In the consensus path previous is always the frontier.
//
// Memoizes the last (previous, changes) call: producer packMomentum and self-verify
// verifyStateRoot call this back-to-back with identical arguments. The key includes the full
// previous HashHeight (hash + height) and the changes hash, and committed versions are
// immutable/content-addressed, so a cache hit can only return the root a recompute would
// produce.
func (s *stateTree) ComputeStateRoot(previous types.HashHeight, changes db.Patch) (types.Hash, error) {
	if !s.ready.Load() {
		return types.Hash{}, ErrStateTreeNotReady
	}
	changesHash := db.PatchHash(changes)

	s.rootCacheMu.Lock()
	if s.rootCache != nil && s.rootCache.previous == previous && s.rootCache.changesHash == changesHash {
		root := s.rootCache.root
		s.rootCacheMu.Unlock()
		return root, nil
	}
	s.rootCacheMu.Unlock()

	root, err := s.tree.ComputeRoot(previous, changes)
	if err != nil {
		return types.Hash{}, err
	}

	s.rootCacheMu.Lock()
	s.rootCache = &rootCacheEntry{previous: previous, changesHash: changesHash, root: root}
	s.rootCacheMu.Unlock()
	return root, nil
}

// GetProof returns the value (nil if absent) and a proof for key at identifier (spec §9 RPC).
func (s *stateTree) GetProof(identifier types.HashHeight, key []byte) (value []byte, proof []byte, err error) {
	if !s.ready.Load() {
		return nil, nil, ErrStateTreeNotReady
	}
	return s.tree.Prove(identifier, key)
}

// buildBatch runs one bounded catchUp holding the chain's insert lock for the whole batch.
func (s *stateTree) buildBatch(c *chain) (bool, error) {
	insert := c.AcquireInsert("state-tree background build")
	defer insert.Unlock()
	momentumStore := c.GetFrontierMomentumStore() // re-derived under the lock, every batch
	if momentumStore == nil {
		return false, errors.Errorf("can't build state tree: chain db is stopped")
	}
	s.changes.Lock()
	defer s.changes.Unlock()
	return s.catchUp(c.chainManager, momentumStore, s.bulkBatchSize(), stateTreeBuildTailBatchSize)
}

// runBuild is the background build goroutine body. It never retries on error: an error here is a
// dead end (leveldb failure, a missing momentum, a stopped db), not something a backoff loop
// fixes, and a silent retry would hide a failing disk. The node is left permanently not-ready
// until restart.
func (s *stateTree) runBuild(c *chain) {
	for {
		select {
		case <-s.quit:
			return
		default:
		}

		started := time.Now()
		fromHeight := s.tree.FrontierIdentifier().Height
		done, err := s.buildBatch(c)
		if err != nil {
			s.log.Error("background state-tree build stopped", "reason", err)
			fmt.Printf("===== Error =====\n")
			fmt.Printf("The background state-tree build stopped: %v\n", err)
			fmt.Printf("znnd keeps running without a state tree: state-root RPCs are unavailable, and this node will not accept new momentums once the state-root spork activates.\n")
			fmt.Printf("Restarting znnd resumes the build from the persisted frontier.\n")
			return
		}
		s.log.Info("state-tree build batch", "from", fromHeight+1, "to", s.tree.FrontierIdentifier().Height, "elapsed", time.Since(started))
		if done {
			s.log.Info("state tree reached the chain frontier", "frontier", s.tree.FrontierIdentifier())
			return
		}

		select {
		case <-s.quit:
			return
		case <-time.After(stateTreeBuildBatchPause):
		}
	}
}

// startBuild launches runBuild. chain.Start calls it once; the exported test driver may call it
// again after a build has exited. Each launch owns a fresh exit channel, published under changes
// so stop() joins whichever build is current.
func (s *stateTree) startBuild(c *chain) {
	s.changes.Lock()
	previous := s.buildExited
	s.changes.Unlock()
	if previous != nil {
		// A live build goroutine from an earlier launch must finish (and close its exit channel)
		// before this launch overwrites buildExited, or it would be orphaned: stop() would never
		// join it.
		<-previous
	}

	exited := make(chan struct{})
	s.changes.Lock()
	s.buildExited = exited
	s.changes.Unlock()

	go func() {
		defer close(exited)
		s.runBuild(c)
	}()
}

func (s *stateTree) stop() error {
	s.stopOnce.Do(func() {
		close(s.quit)
		s.changes.Lock()
		exited := s.buildExited
		s.changes.Unlock()
		if exited != nil {
			<-exited // never while holding changes: the build takes it per batch
		}
		if s.ldb != nil {
			s.stopErr = s.ldb.Close()
		}
	})
	return s.stopErr
}

// RebuiltStateTree is the result of a from-scratch state-tree build: the frontier it reached
// and the root of every version the tree retains, keyed by height.
type RebuiltStateTree struct {
	Frontier types.HashHeight
	Roots    map[uint64]types.Hash
}

// catchUpStateTree opens (or resumes) a state tree in dir and drives its unbounded catchUp until
// it reaches the chain frontier, returning the open tree. The caller owns the returned tree and
// must st.stop() it. Shared by RebuildStateTree (which additionally materializes a Roots map for
// tests) and PrebuildStateTree (the offline CLI entry point, which only needs the frontier).
func catchUpStateTree(c Chain, dir string, archive bool, retentionOverride uint64) (*stateTree, error) {
	ch := c.(*chain)
	st := newStateTree(dir, archive)
	st.retentionOverride = retentionOverride
	if err := st.open(); err != nil {
		return nil, err
	}

	for {
		frontierStore := ch.GetFrontierMomentumStore()
		if frontierStore == nil {
			st.stop()
			return nil, errors.Errorf("can't rebuild state tree: chain db is stopped")
		}
		st.changes.Lock()
		done, err := st.catchUp(ch.chainManager, frontierStore, 0, 0)
		st.changes.Unlock()
		if err != nil {
			st.stop()
			return nil, err
		}
		if done {
			break
		}
	}
	return st, nil
}

// RebuildStateTree rebuilds the state tree from scratch (or resumes a previously built one) in dir
// by replaying the chain's patches through stateTree.catchUp, unbounded, and additionally
// materializes the root of every retained height into Roots. retentionOverride, when non-zero,
// replaces retentionFloor() so a short chain can exercise the bulk build path; a value below
// storage.GetRollbackCacheSize() yields a tree whose retained tail is shorter than the rollback
// window, which is only safe because the returned tree is inspected and discarded, never used to
// serve a rollback. It is exported only so cross-package tests can verify build-vs-replay
// equivalence and the retained-version window; it shares its build loop with PrebuildStateTree,
// the entry point the `znnd state-tree prebuild` offline CLI uses in production, which skips the
// Roots materialization it does not need.
func RebuildStateTree(c Chain, dir string, archive bool, retentionOverride uint64) (*RebuiltStateTree, error) {
	st, err := catchUpStateTree(c, dir, archive, retentionOverride)
	if err != nil {
		return nil, err
	}
	defer st.stop()

	frontier := st.tree.FrontierIdentifier()
	roots := make(map[uint64]types.Hash)
	for h := uint64(1); h <= frontier.Height; h++ {
		root, err := st.tree.Root(types.HashHeight{Height: h})
		if err != nil {
			if err == trie.ErrNoVersion {
				continue
			}
			return nil, err
		}
		roots[h] = root
	}
	return &RebuiltStateTree{Frontier: frontier, Roots: roots}, nil
}

// PrebuildStateTree builds the state tree from scratch (or resumes a previously prebuilt one) in
// dir by replaying the chain's patches through stateTree.catchUp, unbounded, and returns only the
// frontier reached, skipping RebuildStateTree's per-height Roots materialization. It is the entry
// point `znnd state-tree prebuild` runs against a frozen copy of the chain database ahead of an
// upgrade.
func PrebuildStateTree(c Chain, dir string, archive bool) (types.HashHeight, error) {
	st, err := catchUpStateTree(c, dir, archive, 0)
	if err != nil {
		return types.HashHeight{}, err
	}
	defer st.stop()
	return st.tree.FrontierIdentifier(), nil
}

// StateTreeBuild drives a detached state tree's catch-up build against a live chain, one bounded
// batch at a time. Exported only so cross-package tests can interleave batches with live inserts
// and reorgs under non-production retention and batch sizes; the node runs the same driver from
// chain.Start.
type StateTreeBuild struct {
	st *stateTree
	ch *chain
}

// NewStateTreeBuild opens (or resumes) a state tree in dir bound to c. retentionOverride and
// bulkBatchOverride are 0 for production defaults.
func NewStateTreeBuild(c Chain, dir string, retentionOverride, bulkBatchOverride uint64) (*StateTreeBuild, error) {
	ch := c.(*chain)
	st := newStateTree(dir, false)
	st.retentionOverride = retentionOverride
	st.bulkBatchOverride = bulkBatchOverride
	if err := st.open(); err != nil {
		return nil, err
	}
	return &StateTreeBuild{st: st, ch: ch}, nil
}

// Step runs one bounded batch, holding the chain's insert lock for the call.
func (b *StateTreeBuild) Step() (bool, error) {
	return b.st.buildBatch(b.ch)
}

func (b *StateTreeBuild) Ready() bool {
	return b.st.StateTreeReady()
}

func (b *StateTreeBuild) Frontier() types.HashHeight {
	return b.st.tree.FrontierIdentifier()
}

// Root returns the committed root at identifier, bypassing the readiness latch so a mid-build
// tree can be inspected.
func (b *StateTreeBuild) Root(identifier types.HashHeight) (types.Hash, error) {
	return b.st.tree.Root(identifier)
}

func (b *StateTreeBuild) TruncateTo(insertLocker sync.Locker, identifier types.HashHeight) error {
	return b.st.TruncateStateTreeTo(insertLocker, identifier)
}

func (b *StateTreeBuild) Stop() error {
	return b.st.stop()
}

// RestartStateTreeBuild clears the readiness latch and runs the production background build
// against the chain's own state tree again. Exported only so tests can leave a live node's tree
// behind its chain frontier and exercise the build against real inserts; not used in production.
func RestartStateTreeBuild(c Chain) {
	ch := c.(*chain)
	ch.stateTree.ready.Store(false)
	ch.stateTree.startBuild(ch)
}

// wipeDB deletes every key in ldb so it becomes a fresh empty database. Used during the
// §9 format-mismatch migration to clear an old leaf-set Tree DB before rebuilding it as a
// NodeTree.
func wipeDB(ldb *leveldb.DB) error {
	iter := ldb.NewIterator(nil, nil)
	batch := new(leveldb.Batch)
	for iter.Next() {
		batch.Delete(append([]byte{}, iter.Key()...))
	}
	iter.Release()
	if err := iter.Error(); err != nil {
		return err
	}
	return ldb.Write(batch, nil)
}
