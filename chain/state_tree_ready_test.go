package chain

import (
	"testing"

	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
)

// emptyFrontierManager is a db.Manager whose Frontier is an empty memDB, so
// db.GetFrontierIdentifier reports the zero HashHeight (chain frontier at genesis).
type emptyFrontierManager struct{}

func (emptyFrontierManager) Frontier() db.DB                    { return db.NewMemDB() }
func (emptyFrontierManager) Get(types.HashHeight) db.DB         { return nil }
func (emptyFrontierManager) GetPatch(types.HashHeight) db.Patch { return nil }
func (emptyFrontierManager) Add(db.Transaction) error           { return nil }
func (emptyFrontierManager) Pop() error                         { return nil }
func (emptyFrontierManager) Stop() error                        { return nil }
func (emptyFrontierManager) Location() string                   { return "empty-frontier" }

// TestStateTree_ReadsFailClosedBeforeReady asserts the three read methods fail closed with
// ErrStateTreeNotReady before Init has reached the chain frontier, matching an un-upgraded
// node's behavior.
func TestStateTree_ReadsFailClosedBeforeReady(t *testing.T) {
	tree := newStateTree(t.TempDir(), false)

	common.ExpectTrue(t, !tree.StateTreeReady())

	if _, err := tree.StateRoot(types.ZeroHashHeight); err != ErrStateTreeNotReady {
		t.Fatalf("StateRoot before ready = %v, want %v", err, ErrStateTreeNotReady)
	}
	if _, err := tree.ComputeStateRoot(types.ZeroHashHeight, db.NewPatch()); err != ErrStateTreeNotReady {
		t.Fatalf("ComputeStateRoot before ready = %v, want %v", err, ErrStateTreeNotReady)
	}
	if _, _, err := tree.GetProof(types.ZeroHashHeight, []byte("key")); err != ErrStateTreeNotReady {
		t.Fatalf("GetProof before ready = %v, want %v", err, ErrStateTreeNotReady)
	}
}

// fixedFrontierManager is a db.Manager whose Frontier reports a fixed HashHeight, so Init can be
// driven against a chosen chain frontier without a real momentum store.
type fixedFrontierManager struct {
	frontier types.HashHeight
}

func (m fixedFrontierManager) Frontier() db.DB {
	d := db.NewMemDB()
	common.DealWithErr(db.SetFrontier(d, m.frontier, nil))
	return d
}
func (fixedFrontierManager) Get(types.HashHeight) db.DB         { return nil }
func (fixedFrontierManager) GetPatch(types.HashHeight) db.Patch { return nil }
func (fixedFrontierManager) Add(db.Transaction) error           { return nil }
func (fixedFrontierManager) Pop() error                         { return nil }
func (fixedFrontierManager) Stop() error                        { return nil }
func (fixedFrontierManager) Location() string                   { return "fixed-frontier" }

// TestStateTree_InitFlipsReadyOnReachingFrontier asserts Init latches ready true once the tree
// reaches the chain frontier (here, the trivial genesis case: both frontiers at height 0), after
// which the three read methods work normally.
func TestStateTree_InitFlipsReadyOnReachingFrontier(t *testing.T) {
	tree := newStateTree(t.TempDir(), false)
	defer tree.stop()

	common.FailIfErr(t, tree.Init(emptyFrontierManager{}, nil))
	common.ExpectTrue(t, tree.StateTreeReady())

	if _, err := tree.StateRoot(types.ZeroHashHeight); err != nil {
		t.Fatalf("StateRoot after ready returned an error: %v", err)
	}
	if _, err := tree.ComputeStateRoot(types.ZeroHashHeight, db.NewPatch()); err != nil {
		t.Fatalf("ComputeStateRoot after ready returned an error: %v", err)
	}
	if _, _, err := tree.GetProof(types.ZeroHashHeight, []byte("key")); err != nil {
		t.Fatalf("GetProof after ready returned an error: %v", err)
	}
}

// TestStateTree_InitFlipsReadyOnRollbackBranch asserts Init latches ready true when the tree's
// on-disk frontier is ahead of the chain frontier, so Init takes the rollback branch (rollbackTo)
// rather than the at-frontier or build-loop branches.
func TestStateTree_InitFlipsReadyOnRollbackBranch(t *testing.T) {
	dir := t.TempDir()

	build := newStateTree(dir, false)
	common.FailIfErr(t, build.Init(emptyFrontierManager{}, nil))

	// Commit a few versions directly onto the tree, past the chain frontier it will be re-Init'd
	// against below.
	build.changes.Lock()
	var rollbackTarget types.HashHeight
	for h := uint64(1); h <= 3; h++ {
		id := types.HashHeight{Hash: types.NewHash([]byte{byte(h)}), Height: h}
		common.FailIfErr(t, build.tree.Update(db.NewPatch()))
		common.FailIfErr(t, build.tree.Commit(id))
		if h == 1 {
			rollbackTarget = id
		}
	}
	build.changes.Unlock()
	common.FailIfErr(t, build.stop())

	// Re-open the same directory against a chain manager whose frontier is behind the tree's
	// on-disk frontier (but within the rollback window), so Init takes the rollback branch.
	restarted := newStateTree(dir, false)
	defer restarted.stop()
	common.FailIfErr(t, restarted.Init(fixedFrontierManager{frontier: rollbackTarget}, nil))

	common.ExpectTrue(t, restarted.StateTreeReady())

	if got := restarted.tree.FrontierIdentifier(); got != rollbackTarget {
		t.Fatalf("after rollback branch, tree frontier = %v, want %v", got, rollbackTarget)
	}
	if _, err := restarted.StateRoot(rollbackTarget); err != nil {
		t.Fatalf("StateRoot after rollback branch returned an error: %v", err)
	}
}
