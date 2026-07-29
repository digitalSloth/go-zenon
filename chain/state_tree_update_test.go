package chain

import (
	"sync"
	"testing"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
)

// buildDirectTreeToHeight commits height versions 1..height directly onto a fresh tree via
// tree.Update/tree.Commit (bypassing catchUp/Init), so ready stays false regardless of the
// caller's chain-frontier story. Each version gets a distinct identifier hash so a mismatched
// PreviousHash can be told apart from the real one.
func buildDirectTreeToHeight(t *testing.T, height uint64) (*stateTree, []types.HashHeight) {
	t.Helper()
	tree := newStateTree(t.TempDir(), false)
	common.FailIfErr(t, tree.open())

	identifiers := make([]types.HashHeight, height+1) // index by height, index 0 unused
	tree.changes.Lock()
	for h := uint64(1); h <= height; h++ {
		id := types.HashHeight{Hash: types.NewHash([]byte{byte(h)}), Height: h}
		common.FailIfErr(t, tree.tree.Update(db.NewPatch()))
		common.FailIfErr(t, tree.tree.Commit(id))
		identifiers[h] = id
	}
	tree.changes.Unlock()
	return tree, identifiers
}

func detailedMomentumAt(height uint64, previous types.HashHeight) *nom.DetailedMomentum {
	m := &nom.Momentum{Height: height, PreviousHash: previous.Hash}
	m.Hash = m.ComputeHash()
	return &nom.DetailedMomentum{Momentum: m}
}

// TestStateTree_UpdateStateTreeSkipsOutOfOrderWhileNotReady asserts that while the build owns the
// tree (ready == false), an out-of-order UpdateStateTree call is a silent no-op, and the tree
// still folds the height that actually builds on its frontier.
func TestStateTree_UpdateStateTreeSkipsOutOfOrderWhileNotReady(t *testing.T) {
	tree, ids := buildDirectTreeToHeight(t, 3)
	defer tree.stop()
	insert := &sync.Mutex{}

	// Ahead of the frontier: skipped.
	ahead := detailedMomentumAt(7, ids[3])
	common.FailIfErr(t, tree.UpdateStateTree(insert, ahead, db.NewPatch()))
	if got := tree.tree.FrontierIdentifier(); got != ids[3] {
		t.Fatalf("frontier after ahead-of-frontier update = %v, want %v", got, ids[3])
	}

	// A repeat of an already-folded height: skipped.
	repeat := detailedMomentumAt(3, ids[2])
	common.FailIfErr(t, tree.UpdateStateTree(insert, repeat, db.NewPatch()))
	if got := tree.tree.FrontierIdentifier(); got != ids[3] {
		t.Fatalf("frontier after repeat update = %v, want %v", got, ids[3])
	}

	// Exactly frontier+1: folds normally.
	next := detailedMomentumAt(4, ids[3])
	common.FailIfErr(t, tree.UpdateStateTree(insert, next, db.NewPatch()))
	if got := tree.tree.FrontierIdentifier(); got != next.Momentum.Identifier() {
		t.Fatalf("frontier after in-order update = %v, want %v", got, next.Momentum.Identifier())
	}
}

// TestStateTree_UpdateStateTreeFailsOutOfOrderWhenReady asserts the not-ready skip rule does not
// apply once ready: an out-of-order fold errors and leaves the frontier unchanged.
func TestStateTree_UpdateStateTreeFailsOutOfOrderWhenReady(t *testing.T) {
	tree, ids := buildDirectTreeToHeight(t, 3)
	defer tree.stop()
	tree.ready.Store(true)
	insert := &sync.Mutex{}

	ahead := detailedMomentumAt(7, ids[3])
	if err := tree.UpdateStateTree(insert, ahead, db.NewPatch()); err == nil {
		t.Fatalf("expected an error folding an out-of-order height while ready")
	}
	if got := tree.tree.FrontierIdentifier(); got != ids[3] {
		t.Fatalf("frontier after failed update = %v, want %v", got, ids[3])
	}
}

// TestStateTree_UpdateStateTreeRejectsMismatchedPrevious asserts that a height lining up with
// frontier+1 but carrying the wrong PreviousHash always errors, in both the ready and not-ready
// states.
func TestStateTree_UpdateStateTreeRejectsMismatchedPrevious(t *testing.T) {
	for _, ready := range []bool{false, true} {
		tree, ids := buildDirectTreeToHeight(t, 3)
		tree.ready.Store(ready)
		insert := &sync.Mutex{}

		wrongPrevious := types.HashHeight{Hash: types.NewHash([]byte("not-the-frontier-hash")), Height: 3}
		bad := detailedMomentumAt(4, wrongPrevious)
		if err := tree.UpdateStateTree(insert, bad, db.NewPatch()); err == nil {
			t.Fatalf("ready=%v: expected an error folding a mismatched-previous height", ready)
		}
		if got := tree.tree.FrontierIdentifier(); got != ids[3] {
			t.Fatalf("ready=%v: frontier after mismatched-previous update = %v, want %v", ready, got, ids[3])
		}
		common.FailIfErr(t, tree.stop())
	}
}

// TestStateTree_TruncateStateTreeToSkipsAtOrAboveFrontierWhileNotReady asserts that while the
// build owns the tree (ready == false), truncating at or above the tree's own frontier is a
// silent no-op, while truncating below it still rewinds.
func TestStateTree_TruncateStateTreeToSkipsAtOrAboveFrontierWhileNotReady(t *testing.T) {
	tree, ids := buildDirectTreeToHeight(t, 3)
	defer tree.stop()
	insert := &sync.Mutex{}

	common.FailIfErr(t, tree.TruncateStateTreeTo(insert, ids[3]))
	if got := tree.tree.FrontierIdentifier(); got != ids[3] {
		t.Fatalf("frontier after truncate-to-frontier = %v, want %v", got, ids[3])
	}

	above := types.HashHeight{Hash: types.NewHash([]byte("above")), Height: 7}
	common.FailIfErr(t, tree.TruncateStateTreeTo(insert, above))
	if got := tree.tree.FrontierIdentifier(); got != ids[3] {
		t.Fatalf("frontier after truncate-above-frontier = %v, want %v", got, ids[3])
	}

	common.FailIfErr(t, tree.TruncateStateTreeTo(insert, ids[1]))
	if got := tree.tree.FrontierIdentifier(); got != ids[1] {
		t.Fatalf("frontier after truncate-below-frontier = %v, want %v", got, ids[1])
	}
}

// TestStateTree_TruncateStateTreeToFailsAboveFrontierWhenReady asserts the not-ready skip rule
// does not apply once ready: truncating above the frontier errors, matching today's behaviour.
func TestStateTree_TruncateStateTreeToFailsAboveFrontierWhenReady(t *testing.T) {
	tree, ids := buildDirectTreeToHeight(t, 3)
	defer tree.stop()
	tree.ready.Store(true)
	insert := &sync.Mutex{}

	above := types.HashHeight{Hash: types.NewHash([]byte("above")), Height: 7}
	if err := tree.TruncateStateTreeTo(insert, above); err == nil {
		t.Fatalf("expected an error truncating above the frontier while ready")
	}
	if got := tree.tree.FrontierIdentifier(); got != ids[3] {
		t.Fatalf("frontier after failed truncate = %v, want %v", got, ids[3])
	}
}
