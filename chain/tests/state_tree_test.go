package tests

import (
	"math/big"
	"testing"

	"github.com/zenon-network/go-zenon/chain"
	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/zenon/mock"
)

// stateTreeTestRetention is the retention override the rebuild tests use in place of
// retentionFloor(), small enough that a short chain reaches the bulk build path.
const stateTreeTestRetention = uint64(5)

// newStateChangingChain builds a chain in which every momentum carries account-block writes, so
// the state-tree root differs at every height, and returns it with its frontier identifier. The
// caller is responsible for `defer z.StopPanic()`.
func newStateChangingChain(t *testing.T) (mock.MockZenon, types.HashHeight) {
	t.Helper()
	z := mock.NewMockZenon(t)

	for i := 0; i < 20; i++ {
		z.InsertSendBlock(&nom.AccountBlock{
			Address:       g.User1.Address,
			ToAddress:     g.User2.Address,
			TokenStandard: types.ZnnTokenStandard,
			Amount:        big.NewInt(int64(i+1) * g.Zexp),
		}, nil, mock.SkipVmChanges)
		z.InsertNewMomentum()
	}

	frontier, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	id := frontier.Identifier()

	// Precondition: the live roots across the retained window around the frontier are pairwise
	// distinct. If this ever fails, restore one state-changing account block per momentum — do
	// not weaken this assertion or lengthen the chain instead.
	h := id.Height
	r := stateTreeTestRetention
	seen := make(map[types.Hash]uint64)
	for hh := h - r - 1; hh <= h; hh++ {
		momentum, err := z.Chain().GetFrontierMomentumStore().GetMomentumByHeight(hh)
		common.FailIfErr(t, err)
		root, err := z.Chain().StateRoot(momentum.Identifier())
		common.FailIfErr(t, err)
		if other, ok := seen[root]; ok {
			t.Fatalf("live state-tree roots at heights %d and %d collide: %v", other, hh, root)
		}
		seen[root] = hh
	}

	return z, id
}

// TestStateTree_InitRebuildEqualsLive exercises the synchronous Init catch-up build in
// chain/state_tree.go — the code path that runs on every node at activation, folding GetPatch
// results per height — against a real chain, and asserts it reproduces the live-maintained
// state-tree root (the build-vs-replay equivalence), for both the per-height path and the
// bulk-build path.
func TestStateTree_InitRebuildEqualsLive(t *testing.T) {
	z, id := newStateChangingChain(t)
	defer z.StopPanic()
	h := id.Height

	// The live state tree (maintained per momentum since genesis) root at the frontier.
	liveRoot, err := z.Chain().StateRoot(id)
	common.FailIfErr(t, err)
	if liveRoot.IsZero() {
		t.Fatalf("live state-tree root is zero")
	}

	// Rebuild the tree from scratch in a fresh directory by replaying the chain's patches via
	// stateTree.Init with the production retention floor, so the whole build stays on the
	// per-height path, and assert it produces the identical root at every height.
	rebuilt, err := chain.RebuildStateTree(z.Chain(), t.TempDir(), false, 0)
	common.FailIfErr(t, err)

	if rebuilt.Roots[rebuilt.Frontier.Height] != liveRoot {
		t.Fatalf("Init-rebuilt root %v != live-maintained root %v", rebuilt.Roots[rebuilt.Frontier.Height], liveRoot)
	}
	for hh := uint64(1); hh <= h; hh++ {
		if _, ok := rebuilt.Roots[hh]; !ok {
			t.Fatalf("per-height rebuild is missing a root at height %d", hh)
		}
	}

	// Rebuild again with a small retention override, so the build takes the bulk path for the
	// prunable prefix, and assert it still reaches the same frontier and root.
	bulkRebuilt, err := chain.RebuildStateTree(z.Chain(), t.TempDir(), false, stateTreeTestRetention)
	common.FailIfErr(t, err)

	if bulkRebuilt.Frontier != id {
		t.Fatalf("bulk-rebuilt frontier %v != chain frontier %v", bulkRebuilt.Frontier, id)
	}
	if bulkRebuilt.Roots[bulkRebuilt.Frontier.Height] != liveRoot {
		t.Fatalf("bulk-rebuilt root %v != live-maintained root %v", bulkRebuilt.Roots[bulkRebuilt.Frontier.Height], liveRoot)
	}
}

// TestStateTree_BulkBuildRetainedTailMatchesLive exercises the bulk build path's retained tail:
// a rebuild with a small retention override must retain exactly the same window a live tree
// retains after pruning with that retention, and the root at every retained height — including
// the boundary height where the accumulated bulk fold lands — must match the live-maintained root
// at that height.
func TestStateTree_BulkBuildRetainedTailMatchesLive(t *testing.T) {
	z, id := newStateChangingChain(t)
	defer z.StopPanic()
	h := id.Height
	r := stateTreeTestRetention

	rebuilt, err := chain.RebuildStateTree(z.Chain(), t.TempDir(), false, r)
	common.FailIfErr(t, err)

	if uint64(len(rebuilt.Roots)) != r+1 {
		t.Fatalf("expected %d retained roots, got %d: %v", r+1, len(rebuilt.Roots), rebuilt.Roots)
	}
	for hh := h - r; hh <= h; hh++ {
		if _, ok := rebuilt.Roots[hh]; !ok {
			t.Fatalf("expected a retained root at height %d, got %v", hh, rebuilt.Roots)
		}
	}
	if _, ok := rebuilt.Roots[h-r-1]; ok {
		t.Fatalf("did not expect a retained root below the retention window, at height %d", h-r-1)
	}

	// The live tree of this chain never prunes (its frontier is far below retentionFloor()), so
	// parity is asserted over the rebuilt tree's tail window only.
	for hh := h - r; hh <= h; hh++ {
		momentum, err := z.Chain().GetFrontierMomentumStore().GetMomentumByHeight(hh)
		common.FailIfErr(t, err)
		liveRoot, err := z.Chain().StateRoot(momentum.Identifier())
		common.FailIfErr(t, err)
		if rebuilt.Roots[hh] != liveRoot {
			t.Fatalf("rebuilt root at height %d = %v, want live root %v", hh, rebuilt.Roots[hh], liveRoot)
		}
	}
}

// TestStateTree_ArchiveBuildRetainsEveryVersion pins the !s.archive guard on the bulk build path:
// an archive node must never take the bulk branch and must not lose history, even with a small
// retention override.
func TestStateTree_ArchiveBuildRetainsEveryVersion(t *testing.T) {
	z, id := newStateChangingChain(t)
	defer z.StopPanic()
	h := id.Height

	liveRoot, err := z.Chain().StateRoot(id)
	common.FailIfErr(t, err)

	rebuilt, err := chain.RebuildStateTree(z.Chain(), t.TempDir(), true, stateTreeTestRetention)
	common.FailIfErr(t, err)

	for hh := uint64(1); hh <= h; hh++ {
		if _, ok := rebuilt.Roots[hh]; !ok {
			t.Fatalf("archive rebuild is missing a root at height %d", hh)
		}
	}
	if rebuilt.Roots[rebuilt.Frontier.Height] != liveRoot {
		t.Fatalf("archive-rebuilt root %v != live-maintained root %v", rebuilt.Roots[rebuilt.Frontier.Height], liveRoot)
	}
}

// TestStateTree_TruncateAfterSpeculativeUpdateRestoresFrontier covers the invariant that
// protocol.InsertChain's failed-momentum-insertion path relies on: pairing a state-tree
// TruncateStateTreeTo with a cache RollbackCacheTo after UpdateStateTree ran ahead of a
// momentum that was never committed to the momentum store. UpdateStateTree/AddMomentumTransaction
// are driven independently by protocol.chainBridge.InsertChain, whose only chain dependency is
// the chain.Chain interface (no fake seam for AddMomentumTransaction failures exists in the
// protocol package without a real vm.Supervisor), so the property is exercised here directly
// against a real chain.Chain instead.
func TestStateTree_TruncateAfterSpeculativeUpdateRestoresFrontier(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()

	z.InsertMomentumsTo(5)

	frontier, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	id := frontier.Identifier()

	// Speculatively fold a momentum on top of the frontier, mirroring UpdateStateTree running
	// before AddMomentumTransaction in the insert path.
	insert := z.Chain().AcquireInsert("test speculative state-tree update")
	speculative := &nom.Momentum{Height: id.Height + 1, PreviousHash: id.Hash}
	speculative.Hash = speculative.ComputeHash()
	speculativeId := speculative.Identifier()

	common.FailIfErr(t, z.Chain().UpdateStateTree(insert, &nom.DetailedMomentum{Momentum: speculative}, db.NewPatch()))

	if _, err := z.Chain().StateRoot(speculativeId); err != nil {
		t.Fatalf("state tree did not advance to the speculative height: %v", err)
	}

	// The momentum was never added to the momentum store (AddMomentumTransaction failed in the
	// real path); truncate the state tree back to the frontier, mirroring the reorg path.
	common.FailIfErr(t, z.Chain().TruncateStateTreeTo(insert, id))
	insert.Unlock()

	if _, err := z.Chain().StateRoot(speculativeId); err == nil {
		t.Fatalf("state tree still has the speculative version after truncate")
	}
	restoredRoot, err := z.Chain().StateRoot(id)
	common.FailIfErr(t, err)
	if restoredRoot != liveRootAt(t, z, id) {
		t.Fatalf("state tree root at %v changed after truncate", id)
	}

	// The state tree is back in lock-step with the (unmoved) chain frontier, so folding the
	// next real momentum sequentially still succeeds.
	z.InsertNewMomentum()
}

// TestStateTree_PrebuildThenDeltaCatchUpEqualsLive exercises the state-tree prebuild subcommand's
// underlying build path (chain.RebuildStateTree, the same engine `znnd state-tree prebuild` runs
// against a frozen copy of the chain database) followed by a real node resuming that prebuilt
// database and folding only the delta produced since the snapshot (chain.NewStateTreeBuild, the
// same driver Init/the background build use). The result must match a node that built its state
// tree entirely live.
func TestStateTree_PrebuildThenDeltaCatchUpEqualsLive(t *testing.T) {
	z, _ := newStateChangingChain(t)
	defer z.StopPanic()

	// Simulates `znnd state-tree prebuild`: build a statetree database against the chain as it
	// stands at the snapshot point, with the old binary (this live chain) still running.
	dir := t.TempDir()
	prebuilt, err := chain.RebuildStateTree(z.Chain(), dir, false, 0)
	common.FailIfErr(t, err)
	snapshotFrontier := prebuilt.Frontier

	// Momentums produced between the snapshot and the upgrade.
	for i := 0; i < 10; i++ {
		z.InsertSendBlock(&nom.AccountBlock{
			Address:       g.User1.Address,
			ToAddress:     g.User2.Address,
			TokenStandard: types.ZnnTokenStandard,
			Amount:        big.NewInt(int64(i+1) * g.Zexp),
		}, nil, mock.SkipVmChanges)
		z.InsertNewMomentum()
	}

	frontier, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	id := frontier.Identifier()
	liveRoot, err := z.Chain().StateRoot(id)
	common.FailIfErr(t, err)

	// Simulates the upgraded node's startup: it resumes the prebuilt statetree database from its
	// persisted (snapshot) frontier and folds only the delta, mirroring stateTree.Init's tail
	// catch-up and the background build.
	build, err := chain.NewStateTreeBuild(z.Chain(), dir, 0, 0)
	common.FailIfErr(t, err)
	defer build.Stop()

	if got := build.Frontier(); got != snapshotFrontier {
		t.Fatalf("resumed build frontier = %v, want the prebuild's snapshot frontier %v", got, snapshotFrontier)
	}
	common.ExpectTrue(t, !build.Ready())

	for {
		done, stepErr := build.Step()
		common.FailIfErr(t, stepErr)
		if done {
			break
		}
	}

	if build.Frontier() != id {
		t.Fatalf("delta-caught-up build frontier %v != chain frontier %v", build.Frontier(), id)
	}
	common.ExpectTrue(t, build.Ready())

	deltaRoot, err := build.Root(id)
	common.FailIfErr(t, err)
	if deltaRoot != liveRoot {
		t.Fatalf("prebuild-then-delta root %v != live-maintained root %v", deltaRoot, liveRoot)
	}
}

// TestStateTree_ResumeSameHeightHashMismatchFails covers the "stale/corrupt prebuild" failure case
// at equal heights: resuming a prebuilt statetree database (chain.NewStateTreeBuild, the same
// driver `znnd state-tree prebuild`'s output feeds into on upgrade) against a chain whose momentum
// at the prebuilt frontier's height has a different hash — e.g. the snapshot the prebuild ran
// against was not actually an ancestor of the chain the upgraded node is running — must surface an
// explicit error rather than silently folding the delta onto a mismatched tree.
func TestStateTree_ResumeSameHeightHashMismatchFails(t *testing.T) {
	z, id := newStateChangingChain(t)
	defer z.StopPanic()

	dir := t.TempDir()
	prebuilt, err := chain.RebuildStateTree(z.Chain(), dir, false, 0)
	common.FailIfErr(t, err)
	if prebuilt.Frontier != id {
		t.Fatalf("prebuilt frontier %v != chain frontier %v", prebuilt.Frontier, id)
	}

	// A second, independent chain reaching the same height with no state-changing blocks: its
	// momentum at id.Height has different content, hence a different hash, than the chain the
	// prebuild above ran against.
	other := mock.NewMockZenon(t)
	defer other.StopPanic()
	other.InsertMomentumsTo(id.Height)

	otherFrontier, err := other.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	if otherFrontier.Height != id.Height {
		t.Fatalf("expected the second chain to reach height %d, got %d", id.Height, otherFrontier.Height)
	}
	if otherFrontier.Hash == id.Hash {
		t.Fatalf("expected the second chain's momentum at height %d to differ from the prebuilt chain's", id.Height)
	}

	build, err := chain.NewStateTreeBuild(other.Chain(), dir, 0, 0)
	common.FailIfErr(t, err)
	defer build.Stop()

	if _, stepErr := build.Step(); stepErr == nil {
		t.Fatalf("expected resuming a prebuilt tree against a mismatched chain to fail, got no error")
	}
}

// TestStateTree_ResumeBelowFrontierAncestryMismatchFails covers the realistic stale-prebuild shape:
// a prebuilt tree whose frontier sits below the chain frontier it resumes against (the entire point
// of the prebuild feature), where the chain's momentum at the tree's frontier height is not the one
// the tree was built against. Without an ancestry check this would be folded forward silently;
// resuming it must fail loudly instead, on the very first build step.
func TestStateTree_ResumeBelowFrontierAncestryMismatchFails(t *testing.T) {
	z, id := newStateChangingChain(t)
	defer z.StopPanic()

	dir := t.TempDir()
	prebuilt, err := chain.RebuildStateTree(z.Chain(), dir, false, 0)
	common.FailIfErr(t, err)
	if prebuilt.Frontier != id {
		t.Fatalf("prebuilt frontier %v != chain frontier %v", prebuilt.Frontier, id)
	}

	// A second, independent chain that diverges from the prebuilt chain's history (its momentum at
	// id.Height has a different hash) and then continues on past id.Height, so its frontier ends up
	// above the prebuilt tree's frontier — the shape a resumed prebuild actually has in production.
	other := mock.NewMockZenon(t)
	defer other.StopPanic()
	other.InsertMomentumsTo(id.Height)
	for i := 0; i < 5; i++ {
		other.InsertNewMomentum()
	}

	otherAtId, err := other.Chain().GetFrontierMomentumStore().GetMomentumByHeight(id.Height)
	common.FailIfErr(t, err)
	if otherAtId.Identifier().Hash == id.Hash {
		t.Fatalf("expected the second chain's momentum at height %d to differ from the prebuilt chain's", id.Height)
	}
	otherFrontier, err := other.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	if otherFrontier.Height <= id.Height {
		t.Fatalf("expected the second chain's frontier (%d) to be above the prebuilt tree's frontier (%d)", otherFrontier.Height, id.Height)
	}

	build, err := chain.NewStateTreeBuild(other.Chain(), dir, 0, 0)
	common.FailIfErr(t, err)
	defer build.Stop()

	if _, stepErr := build.Step(); stepErr == nil {
		t.Fatalf("expected resuming a below-frontier prebuilt tree against a mismatched chain to fail, got no error")
	}
}

func liveRootAt(t *testing.T, z mock.MockZenon, id types.HashHeight) types.Hash {
	t.Helper()
	root, err := z.Chain().StateRoot(id)
	common.FailIfErr(t, err)
	return root
}

// TestStateTree_ComputeStateRootRepeatedCallsAgree exercises ComputeStateRoot's memoized fast
// path: calling it twice with identical (previous, changes) must return the same non-zero root
// both times. Folding an empty patch onto the frontier must reproduce the frontier's own
// committed root, giving a seam to also check the memoized result against StateRoot(id).
func TestStateTree_ComputeStateRootRepeatedCallsAgree(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()

	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     g.User2.Address,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(g.Zexp),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum() // receive
	z.InsertNewMomentum() // settle

	frontier, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	id := frontier.Identifier()

	committedRoot, err := z.Chain().StateRoot(id)
	common.FailIfErr(t, err)
	if committedRoot.IsZero() {
		t.Fatalf("committed state-tree root is zero")
	}

	emptyChanges := db.NewPatch()
	root1, err := z.Chain().ComputeStateRoot(id, emptyChanges)
	common.FailIfErr(t, err)
	root2, err := z.Chain().ComputeStateRoot(id, emptyChanges)
	common.FailIfErr(t, err)

	if root1 != root2 {
		t.Fatalf("repeated ComputeStateRoot calls disagree: %v != %v", root1, root2)
	}
	if root1 != committedRoot {
		t.Fatalf("ComputeStateRoot(id, no-op changes) = %v, want committed root %v", root1, committedRoot)
	}
}
