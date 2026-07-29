package tests

import (
	"math/big"
	"testing"
	"time"

	"github.com/zenon-network/go-zenon/chain"
	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/zenon/mock"
)

// TestStateTree_LiveBuildCatchesUpWhileInserting is the production-configuration test: the
// node's own tree is left behind the chain frontier, the real background build owns it, and the
// real insert path writes to it concurrently.
func TestStateTree_LiveBuildCatchesUpWhileInserting(t *testing.T) {
	z, _ := newStateChangingChain(t)
	defer z.StopPanic()

	z.InsertMomentumsTo(60)

	behind, err := z.Chain().GetFrontierMomentumStore().GetMomentumByHeight(20)
	common.FailIfErr(t, err)
	behindId := behind.Identifier()

	insert := z.Chain().AcquireInsert("test rewind node's own tree")
	common.FailIfErr(t, z.Chain().TruncateStateTreeTo(insert, behindId))
	chain.RestartStateTreeBuild(z.Chain())
	common.ExpectTrue(t, !z.Chain().StateTreeReady())
	insert.Unlock()

	for i := 0; i < 40; i++ {
		z.InsertSendBlock(&nom.AccountBlock{
			Address:       g.User1.Address,
			ToAddress:     g.User2.Address,
			TokenStandard: types.ZnnTokenStandard,
			Amount:        big.NewInt(int64(i+1) * g.Zexp),
		}, nil, mock.SkipVmChanges)
		z.InsertNewMomentum()
	}

	deadline := time.Now().Add(30 * time.Second)
	for !z.Chain().StateTreeReady() {
		if time.Now().After(deadline) {
			t.Fatalf("state tree never caught up to the chain frontier")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The handoff: UpdateStateTree is now the sole writer, folding frontier+1 as normal.
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	frontier, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	id := frontier.Identifier()

	liveRoot, err := z.Chain().StateRoot(id)
	common.FailIfErr(t, err)

	rebuilt, err := chain.RebuildStateTree(z.Chain(), t.TempDir(), false, 0)
	common.FailIfErr(t, err)

	if rebuilt.Frontier != id {
		t.Fatalf("rebuilt frontier %v != chain frontier %v", rebuilt.Frontier, id)
	}
	if rebuilt.Roots[rebuilt.Frontier.Height] != liveRoot {
		t.Fatalf("build-caught-up root %v != independently rebuilt root %v", liveRoot, rebuilt.Roots[rebuilt.Frontier.Height])
	}
}

// TestStateTree_StopJoinsBuildInFlight drives the node's own background build mid-catch-up and
// asserts chain.Stop() (via z.StopPanic()) joins the build goroutine and returns rather than
// hanging.
func TestStateTree_StopJoinsBuildInFlight(t *testing.T) {
	z, _ := newStateChangingChain(t)
	defer z.StopPanic() // must return, not hang, with the build mid-catch-up

	z.InsertMomentumsTo(60)
	behind, err := z.Chain().GetFrontierMomentumStore().GetMomentumByHeight(1)
	common.FailIfErr(t, err)

	insert := z.Chain().AcquireInsert("test rewind for stop-mid-build")
	common.FailIfErr(t, z.Chain().TruncateStateTreeTo(insert, behind.Identifier()))
	chain.RestartStateTreeBuild(z.Chain())
	insert.Unlock()

	common.ExpectTrue(t, !z.Chain().StateTreeReady()) // still building when the defer fires
}

// TestStateTree_BuildRewindsOnReorg drives a detached build with Step(), deterministically, and
// exercises a real chain reorg mid-build: RollbackTo/RollbackCacheTo/TruncateStateTreeTo (the
// same sequence protocol.chainBridge.rollbackSideChain uses) applied under one insert hold to
// both the node's own tree and the detached build's tree.
func TestStateTree_BuildRewindsOnReorg(t *testing.T) {
	z, id := newStateChangingChain(t)
	defer z.StopPanic()
	if id.Height != 21 {
		t.Fatalf("expected newStateChangingChain's frontier at height 21, got %d", id.Height)
	}

	build, err := chain.NewStateTreeBuild(z.Chain(), t.TempDir(), 0, 0)
	common.FailIfErr(t, err)
	defer build.Stop()

	// Two tail batches of 10 land the build frontier at height 20, short of the chain frontier.
	for i := 0; i < 2; i++ {
		done, stepErr := build.Step()
		common.FailIfErr(t, stepErr)
		common.ExpectTrue(t, !done)
	}
	if got := build.Frontier().Height; got != 20 {
		t.Fatalf("build frontier after two tail batches = %d, want 20", got)
	}

	preReorgHashes := make(map[uint64]types.Hash)
	for h := uint64(16); h <= 21; h++ {
		m, mErr := z.Chain().GetFrontierMomentumStore().GetMomentumByHeight(h)
		common.FailIfErr(t, mErr)
		preReorgHashes[h] = m.Hash
	}

	forkMomentum, err := z.Chain().GetFrontierMomentumStore().GetMomentumByHeight(15)
	common.FailIfErr(t, err)
	forkId := forkMomentum.Identifier()

	insert := z.Chain().AcquireInsert("test reorg to fork")
	common.FailIfErr(t, z.Chain().RollbackTo(insert, forkId))
	common.FailIfErr(t, z.Chain().RollbackCacheTo(insert, forkId))
	common.FailIfErr(t, z.Chain().TruncateStateTreeTo(insert, forkId))
	common.FailIfErr(t, build.TruncateTo(insert, forkId))
	insert.Unlock()

	if got := build.Frontier(); got != forkId {
		t.Fatalf("build frontier after reorg = %v, want fork identifier %v", got, forkId)
	}
	common.ExpectTrue(t, !build.Ready())

	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     g.User2.Address,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(21 * g.Zexp),
	}, nil, mock.SkipVmChanges)
	z.InsertMomentumsTo(24)

	for h := uint64(16); h <= 21; h++ {
		m, mErr := z.Chain().GetFrontierMomentumStore().GetMomentumByHeight(h)
		common.FailIfErr(t, mErr)
		if m.Hash == preReorgHashes[h] {
			t.Fatalf("momentum hash at replayed height %d did not diverge from the pre-reorg chain", h)
		}
	}

	for {
		done, stepErr := build.Step()
		common.FailIfErr(t, stepErr)
		if done {
			break
		}
	}

	newFrontier, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	newFrontierId := newFrontier.Identifier()

	if build.Frontier() != newFrontierId {
		t.Fatalf("build frontier after catch-up = %v, want new chain frontier %v", build.Frontier(), newFrontierId)
	}
	common.ExpectTrue(t, build.Ready())

	buildRoot, err := build.Root(newFrontierId)
	common.FailIfErr(t, err)
	liveRoot, err := z.Chain().StateRoot(newFrontierId)
	common.FailIfErr(t, err)
	if buildRoot != liveRoot {
		t.Fatalf("build root %v != live root %v after reorg catch-up", buildRoot, liveRoot)
	}
}

// TestStateTree_BuildLatchFlipsOnlyAtFrontier steps a detached build against a chain that stays
// still, and asserts the latch is false at every intermediate frontier and true only once the
// build frontier reaches the (fixed) live frontier.
func TestStateTree_BuildLatchFlipsOnlyAtFrontier(t *testing.T) {
	z, liveFrontier := newStateChangingChain(t)
	defer z.StopPanic()

	build, err := chain.NewStateTreeBuild(z.Chain(), t.TempDir(), 0, 0)
	common.FailIfErr(t, err)
	defer build.Stop()

	for {
		done, stepErr := build.Step()
		common.FailIfErr(t, stepErr)
		want := build.Frontier() == liveFrontier
		if build.Ready() != want {
			t.Fatalf("Ready() = %v at frontier %v, want %v (live frontier %v)", build.Ready(), build.Frontier(), want, liveFrontier)
		}
		if done {
			break
		}
	}
	common.ExpectTrue(t, build.Ready())
}

// TestStateTree_BuildResumesAfterStop asserts a detached build persists its progress across
// stop(), that stop() is idempotent, and that a fresh build over the same directory resumes from
// the persisted frontier with ready false, then reaches the same root as the live tree.
func TestStateTree_BuildResumesAfterStop(t *testing.T) {
	z, id := newStateChangingChain(t)
	defer z.StopPanic()

	dir := t.TempDir()
	build, err := chain.NewStateTreeBuild(z.Chain(), dir, 0, 0)
	common.FailIfErr(t, err)

	for i := 0; i < 2; i++ {
		_, stepErr := build.Step()
		common.FailIfErr(t, stepErr)
	}
	persistedFrontier := build.Frontier()

	common.FailIfErr(t, build.Stop())
	common.FailIfErr(t, build.Stop()) // idempotent: same nil result the second time

	resumed, err := chain.NewStateTreeBuild(z.Chain(), dir, 0, 0)
	common.FailIfErr(t, err)
	defer resumed.Stop()

	if got := resumed.Frontier(); got != persistedFrontier {
		t.Fatalf("resumed frontier = %v, want persisted frontier %v", got, persistedFrontier)
	}
	common.ExpectTrue(t, !resumed.Ready())

	for {
		done, stepErr := resumed.Step()
		common.FailIfErr(t, stepErr)
		if done {
			break
		}
	}

	liveRoot, err := z.Chain().StateRoot(id)
	common.FailIfErr(t, err)
	resumedRoot, err := resumed.Root(id)
	common.FailIfErr(t, err)
	if resumedRoot != liveRoot {
		t.Fatalf("resumed root %v != live root %v", resumedRoot, liveRoot)
	}
}

// TestStateTree_BulkBatchedBuildEqualsLive drives a detached build whose bulk range spans several
// CommitBulk boundaries (a small retention override forces a bulk prefix, a small bulk-batch
// override forces several batches through it) before the tail, and asserts the build root equals
// the live-maintained root at the frontier. No reorg here: a retention override below the
// rollback window intentionally prunes inside the reorg window, which is only safe because this
// tree is inspected and discarded, never used to serve a rollback.
func TestStateTree_BulkBatchedBuildEqualsLive(t *testing.T) {
	z, id := newStateChangingChain(t)
	defer z.StopPanic()

	liveRoot, err := z.Chain().StateRoot(id)
	common.FailIfErr(t, err)

	build, err := chain.NewStateTreeBuild(z.Chain(), t.TempDir(), 5, 4)
	common.FailIfErr(t, err)
	defer build.Stop()

	for {
		done, stepErr := build.Step()
		common.FailIfErr(t, stepErr)
		if done {
			break
		}
	}

	if build.Frontier() != id {
		t.Fatalf("bulk-batched build frontier %v != chain frontier %v", build.Frontier(), id)
	}
	buildRoot, err := build.Root(id)
	common.FailIfErr(t, err)
	if buildRoot != liveRoot {
		t.Fatalf("bulk-batched build root %v != live root %v", buildRoot, liveRoot)
	}
}
