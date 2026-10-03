package implementation

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math"
	"math/big"
	"testing"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/vm/vm_context"
)

var feeEpochLen = uint64(constants.MomentumsPerEpoch)

func feeTestAddress(b byte) types.Address {
	var raw [types.AddressSize]byte
	raw[0] = types.UserAddrByte
	for i := 1; i < len(raw); i++ {
		raw[i] = b
	}
	addr, err := types.BytesToAddress(raw[:])
	if err != nil {
		panic(err)
	}
	return addr
}

func feeTestStorage() db.DB {
	return db.DisableNotFound(db.NewMemDB())
}

func feeTestPillars() []*definition.PillarInfo {
	return []*definition.PillarInfo{
		{Name: "pillar-a", RewardWithdrawAddress: feeTestAddress(0xA1)},
		{Name: "pillar-b", RewardWithdrawAddress: feeTestAddress(0xB1)},
	}
}

func seedPending(t *testing.T, storage db.DB, epoch uint64, name string, amount int64) {
	t.Helper()
	common.FailIfErr(t, (&definition.PendingFee{Epoch: epoch, PillarName: name, Amount: big.NewInt(amount)}).Save(storage))
}

func seedEpoch(t *testing.T, storage db.DB, epoch, finality uint64) {
	t.Helper()
	common.FailIfErr(t, (&definition.FeeEpoch{Epoch: epoch, FinalityHeight: finality}).Save(storage))
}

func seedCollected(t *testing.T, storage db.DB, collected int64) {
	t.Helper()
	counters, err := definition.GetFeeCounters(storage)
	common.FailIfErr(t, err)
	counters.TotalFeesCollected = big.NewInt(collected)
	common.FailIfErr(t, counters.Save(storage))
}

func depositOf(t *testing.T, storage db.DB, addr types.Address) *big.Int {
	t.Helper()
	deposit, err := definition.GetRewardDeposit(storage, &addr)
	common.FailIfErr(t, err)
	return deposit.Znn
}

func expectBig(t *testing.T, label string, got *big.Int, want int64) {
	t.Helper()
	if got == nil || got.Cmp(big.NewInt(want)) != 0 {
		t.Fatalf("%s: got %v want %d", label, got, want)
	}
}

func TestFeeEpochOf(t *testing.T) {
	if feeEpochOf(0) != 0 || feeEpochOf(feeEpochLen-1) != 0 {
		t.Fatal("expected heights inside the first epoch to map to epoch 0")
	}
	if feeEpochOf(feeEpochLen) != 1 || feeEpochOf(5*feeEpochLen+3) != 5 {
		t.Fatal("expected height / MomentumsPerEpoch")
	}
}

func TestAccrueBatchFees_SumsPassedRecordsOnly(t *testing.T) {
	storage := feeTestStorage()
	entry := &definition.BatchEntry{
		FirstInputSeq:     0,
		LastInputSeq:      1_000_000,
		ExecutorPillar:    "pillar-a",
		SubmittedAtHeight: 3*feeEpochLen + 5,
		FinalizeAtHeight:  3*feeEpochLen + 50,
	}
	inputs := []*definition.AssignedIndexRecord{
		{Fee: big.NewInt(40)},
		{Fee: big.NewInt(2)},
		{Fee: nil},
	}
	common.FailIfErr(t, accrueBatchFees(storage, entry, inputs))

	pending, err := definition.GetPendingFee(storage, 3, "pillar-a")
	common.FailIfErr(t, err)
	expectBig(t, "pending", pending.Amount, 42)

	queued, err := definition.GetFeeEpoch(storage, 3)
	common.FailIfErr(t, err)
	if queued.FinalityHeight != entry.FinalizeAtHeight {
		t.Fatalf("finality: got %d want %d", queued.FinalityHeight, entry.FinalizeAtHeight)
	}
}

func TestAccrueBatchFees_AccumulatesAndKeepsHighestFinality(t *testing.T) {
	storage := feeTestStorage()
	base := 2 * feeEpochLen
	first := &definition.BatchEntry{ExecutorPillar: "pillar-a", SubmittedAtHeight: base + 1, FinalizeAtHeight: base + 100}
	second := &definition.BatchEntry{ExecutorPillar: "pillar-a", SubmittedAtHeight: base + 2, FinalizeAtHeight: base + 60}
	other := &definition.BatchEntry{ExecutorPillar: "pillar-b", SubmittedAtHeight: base + 3, FinalizeAtHeight: base + 70}

	common.FailIfErr(t, accrueBatchFees(storage, first, []*definition.AssignedIndexRecord{{Fee: big.NewInt(10)}}))
	common.FailIfErr(t, accrueBatchFees(storage, second, []*definition.AssignedIndexRecord{{Fee: big.NewInt(5)}}))
	common.FailIfErr(t, accrueBatchFees(storage, other, []*definition.AssignedIndexRecord{{Fee: big.NewInt(7)}}))

	a, err := definition.GetPendingFee(storage, 2, "pillar-a")
	common.FailIfErr(t, err)
	expectBig(t, "pillar-a", a.Amount, 15)
	b, err := definition.GetPendingFee(storage, 2, "pillar-b")
	common.FailIfErr(t, err)
	expectBig(t, "pillar-b", b.Amount, 7)

	queued, err := definition.GetFeeEpoch(storage, 2)
	common.FailIfErr(t, err)
	if queued.FinalityHeight != base+100 {
		t.Fatalf("expected the highest finality %d, got %d", base+100, queued.FinalityHeight)
	}

	head, err := definition.GetFeeEpochQueueHead(storage)
	common.FailIfErr(t, err)
	if head.Epoch != 2 {
		t.Fatalf("expected one queued epoch 2, got head %d", head.Epoch)
	}
}

func TestAccrueBatchFees_ZeroFeeBatchQueuesEpoch(t *testing.T) {
	storage := feeTestStorage()
	entry := &definition.BatchEntry{ExecutorPillar: "pillar-a", SubmittedAtHeight: 4*feeEpochLen + 1, FinalizeAtHeight: 4*feeEpochLen + 9}
	common.FailIfErr(t, accrueBatchFees(storage, entry, nil))

	queued, err := definition.GetFeeEpoch(storage, 4)
	common.FailIfErr(t, err)
	if queued.FinalityHeight != entry.FinalizeAtHeight {
		t.Fatalf("finality: got %d want %d", queued.FinalityHeight, entry.FinalizeAtHeight)
	}
	pending, err := definition.GetPendingFeesForEpoch(storage, 4)
	common.FailIfErr(t, err)
	if len(pending) != 0 {
		t.Fatalf("expected no pending record for a zero-fee batch, got %d", len(pending))
	}
}

func TestSettleNextFeeEpoch_NothingQueued(t *testing.T) {
	storage := feeTestStorage()
	burn, err := settleNextFeeEpoch(storage, 10*feeEpochLen, feeTestPillars())
	common.FailIfErr(t, err)
	if burn != nil {
		t.Fatalf("expected nil, got %v", burn)
	}
}

func TestSettleNextFeeEpoch_WaitsForEpochEndAndFinality(t *testing.T) {
	storage := feeTestStorage()
	seedPending(t, storage, 1, "pillar-a", 101)
	seedEpoch(t, storage, 1, 2*feeEpochLen+500)
	seedCollected(t, storage, 101)

	// epoch 1 has not ended
	burn, err := settleNextFeeEpoch(storage, 2*feeEpochLen-1, feeTestPillars())
	common.FailIfErr(t, err)
	if burn != nil {
		t.Fatalf("expected nil before the epoch ends, got %v", burn)
	}
	// epoch ended, finality still ahead
	burn, err = settleNextFeeEpoch(storage, 2*feeEpochLen+499, feeTestPillars())
	common.FailIfErr(t, err)
	if burn != nil {
		t.Fatalf("expected nil before finality, got %v", burn)
	}
	if _, err := definition.GetFeeEpoch(storage, 1); err != nil {
		t.Fatalf("expected the head to stay queued: %v", err)
	}
	expectBig(t, "deposit", depositOf(t, storage, feeTestAddress(0xA1)), 0)

	// settles exactly at the finality height
	burn, err = settleNextFeeEpoch(storage, 2*feeEpochLen+500, feeTestPillars())
	common.FailIfErr(t, err)
	expectBig(t, "burn", burn, 30)
	expectBig(t, "deposit", depositOf(t, storage, feeTestAddress(0xA1)), 71)

	counters, err := definition.GetFeeCounters(storage)
	common.FailIfErr(t, err)
	expectBig(t, "burned", counters.TotalFeesBurned, 30)
	expectBig(t, "credited", counters.TotalFeesCredited, 71)

	if _, err := definition.GetFeeEpoch(storage, 1); err != constants.ErrDataNonExistent {
		t.Fatalf("expected the head removed, got %v", err)
	}
	remaining, err := definition.GetPendingFeesForEpoch(storage, 1)
	common.FailIfErr(t, err)
	if len(remaining) != 0 {
		t.Fatalf("expected pending records removed, got %d", len(remaining))
	}
}

func TestSettleNextFeeEpoch_SmallAmountBurnsZero(t *testing.T) {
	storage := feeTestStorage()
	seedPending(t, storage, 0, "pillar-a", 3)
	seedEpoch(t, storage, 0, 0)
	seedCollected(t, storage, 3)

	burn, err := settleNextFeeEpoch(storage, feeEpochLen, feeTestPillars())
	common.FailIfErr(t, err)
	expectBig(t, "burn", burn, 0)
	expectBig(t, "deposit", depositOf(t, storage, feeTestAddress(0xA1)), 3)
	if _, err := definition.GetFeeEpoch(storage, 0); err != constants.ErrDataNonExistent {
		t.Fatalf("expected the head removed, got %v", err)
	}
}

func TestSettleNextFeeEpoch_EmptyEpochOnlyDequeues(t *testing.T) {
	storage := feeTestStorage()
	seedEpoch(t, storage, 0, 0)

	burn, err := settleNextFeeEpoch(storage, feeEpochLen, feeTestPillars())
	common.FailIfErr(t, err)
	expectBig(t, "burn", burn, 0)
	if _, err := definition.GetFeeEpoch(storage, 0); err != constants.ErrDataNonExistent {
		t.Fatalf("expected the head removed, got %v", err)
	}
}

func TestSettleNextFeeEpoch_InactivePillarBurnsAll(t *testing.T) {
	storage := feeTestStorage()
	seedPending(t, storage, 0, "pillar-revoked", 100)
	seedEpoch(t, storage, 0, 0)
	seedCollected(t, storage, 100)

	burn, err := settleNextFeeEpoch(storage, feeEpochLen, feeTestPillars())
	common.FailIfErr(t, err)
	expectBig(t, "burn", burn, 100)
	counters, err := definition.GetFeeCounters(storage)
	common.FailIfErr(t, err)
	expectBig(t, "credited", counters.TotalFeesCredited, 0)
	expectBig(t, "burned", counters.TotalFeesBurned, 100)
}

func TestSettleNextFeeEpoch_OldAndNewPillarBothCredited(t *testing.T) {
	storage := feeTestStorage()
	seedPending(t, storage, 0, "pillar-a", 100)
	seedPending(t, storage, 0, "pillar-b", 200)
	seedEpoch(t, storage, 0, 0)
	seedCollected(t, storage, 300)

	burn, err := settleNextFeeEpoch(storage, feeEpochLen, feeTestPillars())
	common.FailIfErr(t, err)
	expectBig(t, "burn", burn, 30+60)
	expectBig(t, "pillar-a deposit", depositOf(t, storage, feeTestAddress(0xA1)), 70)
	expectBig(t, "pillar-b deposit", depositOf(t, storage, feeTestAddress(0xB1)), 140)
}

func TestSettleNextFeeEpoch_SharedRewardAddressSumsCredits(t *testing.T) {
	storage := feeTestStorage()
	shared := feeTestAddress(0xC1)
	pillars := []*definition.PillarInfo{
		{Name: "pillar-a", RewardWithdrawAddress: shared},
		{Name: "pillar-b", RewardWithdrawAddress: shared},
	}
	seedPending(t, storage, 0, "pillar-a", 100)
	seedPending(t, storage, 0, "pillar-b", 200)
	seedEpoch(t, storage, 0, 0)
	seedCollected(t, storage, 300)

	_, err := settleNextFeeEpoch(storage, feeEpochLen, pillars)
	common.FailIfErr(t, err)
	expectBig(t, "shared deposit", depositOf(t, storage, shared), 70+140)
}

func TestSettleNextFeeEpoch_OneEpochPerCall(t *testing.T) {
	storage := feeTestStorage()
	seedPending(t, storage, 2, "pillar-a", 100)
	seedEpoch(t, storage, 2, 0)
	seedPending(t, storage, 9, "pillar-a", 200)
	seedEpoch(t, storage, 9, 0)
	seedCollected(t, storage, 300)
	height := 20 * feeEpochLen

	burn, err := settleNextFeeEpoch(storage, height, feeTestPillars())
	common.FailIfErr(t, err)
	expectBig(t, "first burn", burn, 30)
	head, err := definition.GetFeeEpochQueueHead(storage)
	common.FailIfErr(t, err)
	if head.Epoch != 9 {
		t.Fatalf("expected epoch 9 as the new head, got %d", head.Epoch)
	}
	expectBig(t, "deposit after first", depositOf(t, storage, feeTestAddress(0xA1)), 70)

	burn, err = settleNextFeeEpoch(storage, height, feeTestPillars())
	common.FailIfErr(t, err)
	expectBig(t, "second burn", burn, 60)
	expectBig(t, "deposit after second", depositOf(t, storage, feeTestAddress(0xA1)), 70+140)
	if _, err := definition.GetFeeEpochQueueHead(storage); err != constants.ErrDataNonExistent {
		t.Fatalf("expected an empty queue, got %v", err)
	}
}

func TestSettleNextFeeEpoch_RelayPauseBlocksThenReleases(t *testing.T) {
	storage := feeTestStorage()
	seedPending(t, storage, 0, "pillar-a", 101)
	seedEpoch(t, storage, 0, 0)
	seedCollected(t, storage, 101)
	common.FailIfErr(t, (&definition.PauseState{RelayPaused: true}).SaveDomain(storage, 6))

	burn, err := settleNextFeeEpoch(storage, feeEpochLen, feeTestPillars())
	common.FailIfErr(t, err)
	if burn != nil {
		t.Fatalf("expected nil while paused, got %v", burn)
	}
	pending, err := definition.GetPendingFee(storage, 0, "pillar-a")
	common.FailIfErr(t, err)
	expectBig(t, "pending while paused", pending.Amount, 101)
	if _, err := definition.GetFeeEpoch(storage, 0); err != nil {
		t.Fatalf("expected the head to stay queued: %v", err)
	}
	counters, err := definition.GetFeeCounters(storage)
	common.FailIfErr(t, err)
	expectBig(t, "burned while paused", counters.TotalFeesBurned, 0)
	expectBig(t, "credited while paused", counters.TotalFeesCredited, 0)
	expectBig(t, "deposit while paused", depositOf(t, storage, feeTestAddress(0xA1)), 0)

	common.FailIfErr(t, (&definition.PauseState{}).SaveDomain(storage, 6))
	burn, err = settleNextFeeEpoch(storage, feeEpochLen, feeTestPillars())
	common.FailIfErr(t, err)
	expectBig(t, "burn after unpause", burn, 30)
	expectBig(t, "deposit after unpause", depositOf(t, storage, feeTestAddress(0xA1)), 71)
}

func TestSettleNextFeeEpoch_GuardBlocksWhenCollectedBelowPending(t *testing.T) {
	storage := feeTestStorage()
	seedPending(t, storage, 0, "pillar-a", 101)
	seedEpoch(t, storage, 0, 0)
	seedCollected(t, storage, 100)

	burn, err := settleNextFeeEpoch(storage, feeEpochLen, feeTestPillars())
	common.FailIfErr(t, err)
	if burn != nil {
		t.Fatalf("expected nil when the guard breaks, got %v", burn)
	}
	pending, err := definition.GetPendingFee(storage, 0, "pillar-a")
	common.FailIfErr(t, err)
	expectBig(t, "pending", pending.Amount, 101)
	if _, err := definition.GetFeeEpoch(storage, 0); err != nil {
		t.Fatalf("expected the head to stay queued: %v", err)
	}
	counters, err := definition.GetFeeCounters(storage)
	common.FailIfErr(t, err)
	expectBig(t, "burned", counters.TotalFeesBurned, 0)
	expectBig(t, "credited", counters.TotalFeesCredited, 0)
	expectBig(t, "deposit", depositOf(t, storage, feeTestAddress(0xA1)), 0)
}

func TestFeeGuardHolds(t *testing.T) {
	counters := func(collected, burned, credited, rewards int64) *definition.FeeCounters {
		return &definition.FeeCounters{
			TotalFeesCollected:    big.NewInt(collected),
			TotalFeesBurned:       big.NewInt(burned),
			TotalFeesCredited:     big.NewInt(credited),
			TotalRewardsCollected: big.NewInt(rewards),
		}
	}
	if !feeGuardHolds(counters(0, 0, 0, 0)) || !feeGuardHolds(counters(100, 30, 70, 70)) {
		t.Fatal("expected the guard to hold on balanced counters")
	}
	if feeGuardHolds(counters(100, 30, 71, 0)) {
		t.Fatal("expected burned+credited above collected to fail")
	}
	if feeGuardHolds(counters(100, 30, 70, 71)) {
		t.Fatal("expected rewards above credited to fail")
	}
}

func TestCollectFeeReward_PaysDepositAsContractSend(t *testing.T) {
	storage := feeTestStorage()
	caller := feeTestAddress(0xA1)
	deposit, err := definition.GetRewardDeposit(storage, &caller)
	common.FailIfErr(t, err)
	deposit.Znn = big.NewInt(70)
	deposit.Qsr = big.NewInt(999)
	common.FailIfErr(t, deposit.Save(storage))
	counters, err := definition.GetFeeCounters(storage)
	common.FailIfErr(t, err)
	counters.TotalFeesCollected = big.NewInt(100)
	counters.TotalFeesBurned = big.NewInt(30)
	counters.TotalFeesCredited = big.NewInt(70)
	common.FailIfErr(t, counters.Save(storage))

	blocks, err := collectFeeReward(storage, caller)
	common.FailIfErr(t, err)
	if len(blocks) != 1 {
		t.Fatalf("expected one block, got %d", len(blocks))
	}
	block := blocks[0]
	if block.ToAddress != caller || block.ToAddress == types.TokenContract {
		t.Fatalf("expected a send to the caller, got %v", block.ToAddress)
	}
	if block.Address != types.SettlementContract || block.TokenStandard != types.ZnnTokenStandard {
		t.Fatalf("unexpected block origin or token: %+v", block)
	}
	if block.Amount.Cmp(big.NewInt(70)) != 0 {
		t.Fatalf("amount: got %v want 70", block.Amount)
	}
	expectBig(t, "znn deposit", depositOf(t, storage, caller), 0)
	after, err := definition.GetFeeCounters(storage)
	common.FailIfErr(t, err)
	expectBig(t, "rewards collected", after.TotalRewardsCollected, 70)
}

func TestCollectFeeReward_NothingToWithdraw(t *testing.T) {
	storage := feeTestStorage()
	caller := feeTestAddress(0xA1)

	_, err := collectFeeReward(storage, caller)
	common.ExpectError(t, err, constants.ErrNothingToWithdraw)

	// a QSR-only deposit is ignored
	deposit, err := definition.GetRewardDeposit(storage, &caller)
	common.FailIfErr(t, err)
	deposit.Qsr = big.NewInt(50)
	common.FailIfErr(t, deposit.Save(storage))
	_, err = collectFeeReward(storage, caller)
	common.ExpectError(t, err, constants.ErrNothingToWithdraw)
}

func TestCollectFeeReward_GuardFaultWritesNothing(t *testing.T) {
	storage := feeTestStorage()
	caller := feeTestAddress(0xA1)
	deposit, err := definition.GetRewardDeposit(storage, &caller)
	common.FailIfErr(t, err)
	deposit.Znn = big.NewInt(70)
	common.FailIfErr(t, deposit.Save(storage))
	// credited counter is below the deposit, so collecting breaks the guard
	counters, err := definition.GetFeeCounters(storage)
	common.FailIfErr(t, err)
	counters.TotalFeesCollected = big.NewInt(100)
	counters.TotalFeesCredited = big.NewInt(10)
	common.FailIfErr(t, counters.Save(storage))

	blocks, err := collectFeeReward(storage, caller)
	common.ExpectError(t, err, constants.ErrSettlementFeeGuard)
	if blocks != nil {
		t.Fatalf("expected no blocks, got %d", len(blocks))
	}
	expectBig(t, "znn deposit", depositOf(t, storage, caller), 70)
	after, err := definition.GetFeeCounters(storage)
	common.FailIfErr(t, err)
	expectBig(t, "rewards collected", after.TotalRewardsCollected, 0)
}

func TestActiveExecutorPillar(t *testing.T) {
	storage := feeTestStorage()
	executor := feeTestAddress(0xE1)
	common.FailIfErr(t, (&definition.ExecutorSetMember{
		PillarName: "pillar-a", ExecutorAddress: executor, Status: definition.ExecutorStatusActive,
	}).Save(storage))

	name, err := activeExecutorPillar(storage, feeTestPillars(), executor)
	common.FailIfErr(t, err)
	if name != "pillar-a" {
		t.Fatalf("got %q want pillar-a", name)
	}

	_, err = activeExecutorPillar(storage, feeTestPillars(), feeTestAddress(0xE2))
	common.ExpectError(t, err, constants.ErrPermissionDenied)

	_, err = activeExecutorPillar(storage, []*definition.PillarInfo{{Name: "pillar-b"}}, executor)
	common.ExpectError(t, err, constants.ErrPermissionDenied)
}

func TestActiveExecutorPillar_RejectsNonActiveStatus(t *testing.T) {
	storage := feeTestStorage()
	executor := feeTestAddress(0xE1)
	for _, status := range []uint8{definition.ExecutorStatusLapsed, definition.ExecutorStatusCooldown} {
		common.FailIfErr(t, (&definition.ExecutorSetMember{
			PillarName: "pillar-a", ExecutorAddress: executor, Status: status,
		}).Save(storage))
		_, err := activeExecutorPillar(storage, feeTestPillars(), executor)
		common.ExpectError(t, err, constants.ErrPermissionDenied)
	}
}

func TestBurnBlocks(t *testing.T) {
	if blocks := burnBlocks(nil); len(blocks) != 0 {
		t.Fatalf("expected no block for a nil burn, got %d", len(blocks))
	}
	if blocks := burnBlocks(big.NewInt(0)); len(blocks) != 0 {
		t.Fatalf("expected no block for a zero burn, got %d", len(blocks))
	}

	blocks := burnBlocks(big.NewInt(30))
	if len(blocks) != 1 {
		t.Fatalf("expected one block, got %d", len(blocks))
	}
	b := blocks[0]
	if b.Address != types.SettlementContract || b.ToAddress != types.TokenContract {
		t.Fatalf("expected Settlement to Token, got %v to %v", b.Address, b.ToAddress)
	}
	if b.BlockType != nom.BlockTypeContractSend || b.TokenStandard != types.ZnnTokenStandard {
		t.Fatalf("unexpected block type %d or token %v", b.BlockType, b.TokenStandard)
	}
	expectBig(t, "amount", b.Amount, 30)
	if !bytes.Equal(b.Data, definition.ABIToken.PackMethodPanic(definition.BurnMethodName)) {
		t.Fatal("expected the Burn call data")
	}
}

func znnAssetId() types.Hash { return definition.AssetIdFromZTS(types.ZnnTokenStandard) }
func qsrAssetId() types.Hash { return definition.AssetIdFromZTS(types.QsrTokenStandard) }

func TestAdmitInput_AssignsConsecutiveIndices(t *testing.T) {
	storage := feeTestStorage()
	znn, qsr := znnAssetId(), qsrAssetId()

	common.FailIfErr(t, admitInput(storage, 1, znn, big.NewInt(5), nil))
	common.FailIfErr(t, admitInput(storage, 1, types.ZeroHash, nil, big.NewInt(2)))
	common.FailIfErr(t, admitInput(storage, 2, qsr, big.NewInt(7), nil))
	common.FailIfErr(t, admitInput(storage, 1, znn, big.NewInt(3), nil))

	want := []struct {
		domain uint32
		index  uint64
		amount int64
	}{{1, 0, 5}, {1, 1, 0}, {2, 2, 7}, {1, 3, 3}}
	for _, w := range want {
		record, err := definition.GetAssignedIndexRecord(storage, w.domain, w.index)
		common.FailIfErr(t, err)
		if record.Amount.Cmp(big.NewInt(w.amount)) != 0 {
			t.Fatalf("index %d: expected amount %d, got %v", w.index, w.amount, record.Amount)
		}
	}
	if _, err := definition.GetAssignedIndexRecord(storage, 2, 0); err != constants.ErrDataNonExistent {
		t.Fatalf("expected no record for domain 2 index 0, got %v", err)
	}

	counter, err := definition.GetReceivedInputCounter(storage)
	common.FailIfErr(t, err)
	if counter.Count != 4 {
		t.Fatalf("expected counter 4, got %d", counter.Count)
	}
	for _, c := range []struct {
		domain uint32
		asset  types.Hash
		total  int64
	}{{1, znn, 8}, {2, qsr, 7}} {
		total, err := definition.GetCumulativeDepositTotal(storage, c.domain, c.asset)
		common.FailIfErr(t, err)
		if total.CumulativeDeposited.Cmp(big.NewInt(c.total)) != 0 {
			t.Fatalf("domain %d: expected cumulative %d, got %v", c.domain, c.total, total.CumulativeDeposited)
		}
	}
}

func TestAdmitInput_RejectsExhaustedCounter(t *testing.T) {
	storage := feeTestStorage()
	common.FailIfErr(t, (&definition.ReceivedInputCounter{Count: math.MaxUint64}).Save(storage))

	if err := admitInput(storage, 1, znnAssetId(), big.NewInt(5), nil); err != constants.ErrSettlementInputIndexExhausted {
		t.Fatalf("expected ErrSettlementInputIndexExhausted, got %v", err)
	}
	if _, err := definition.GetAssignedIndexRecord(storage, 1, math.MaxUint64); err != constants.ErrDataNonExistent {
		t.Fatalf("expected no record, got %v", err)
	}
	total, err := definition.GetCumulativeDepositTotal(storage, 1, znnAssetId())
	common.FailIfErr(t, err)
	if total.CumulativeDeposited.Sign() != 0 {
		t.Fatalf("expected no cumulative total, got %v", total.CumulativeDeposited)
	}
}

func TestSaveCustodyCounters_Invariant(t *testing.T) {
	storage := feeTestStorage()
	asset := znnAssetId()
	counters := func(deposited, reserve, released int64) *definition.ConservationCounters {
		return &definition.ConservationCounters{
			DomainId:                 1,
			AssetId:                  asset,
			TotalDeposited:           big.NewInt(deposited),
			PendingWithdrawalReserve: big.NewInt(reserve),
			TotalReleased:            big.NewInt(released),
		}
	}

	common.FailIfErr(t, saveCustodyCounters(storage, counters(100, 100, 0)))

	for _, c := range []*definition.ConservationCounters{
		counters(100, 101, 0),
		counters(100, 0, 101),
		counters(100, 41, 60),
	} {
		if err := saveCustodyCounters(storage, c); err != constants.ErrSettlementConservationViolated {
			t.Fatalf("expected ErrSettlementConservationViolated, got %v", err)
		}
		stored, err := definition.GetConservationCounters(storage, 1, asset)
		common.FailIfErr(t, err)
		if stored.PendingWithdrawalReserve.Cmp(big.NewInt(100)) != 0 || stored.TotalReleased.Sign() != 0 {
			t.Fatalf("expected stored counters unchanged, got %+v", stored)
		}
	}
}

const conformanceAssetFlowHex = "000000020000000000000000000000000000000000000000000004066318c6318c6318c6" +
	"000000000000000000000000000000000000000000000000000000000000000a" +
	"0000000000000000000000000000000000000000000000000000000000000001" +
	"0000000000000000000000000000000000000000000014e66318c6318c6318c6" +
	"0000000000000000000000000000000000000000000000000000000000000000" +
	"0000000000000000000000000000000000000000000000000000000000000002"

var conformanceAsset = types.HexToHashPanic("0000000000000000000000000000000000000000000004066318c6318c6318c6")

func flowEntry(asset types.Hash, credit, debit int64) []byte {
	out := append([]byte{}, asset[:]...)
	for _, v := range []int64{credit, debit} {
		var word [32]byte
		new(big.Int).SetInt64(v).FillBytes(word[:])
		out = append(out, word[:]...)
	}
	return out
}

func flowSummary(entries ...[]byte) []byte {
	out := []byte{0, 0, 0, byte(len(entries))}
	for _, e := range entries {
		out = append(out, e...)
	}
	return out
}

func depositRecords(t *testing.T, amounts map[types.Hash][]int64) []*definition.AssignedIndexRecord {
	t.Helper()
	var records []*definition.AssignedIndexRecord
	for asset, list := range amounts {
		for _, a := range list {
			records = append(records, &definition.AssignedIndexRecord{AssetId: asset, Amount: big.NewInt(a)})
		}
	}
	return records
}

func TestVerifyAssetFlowDeposits_AcceptsConformanceSummary(t *testing.T) {
	summary, err := hex.DecodeString(conformanceAssetFlowHex)
	common.FailIfErr(t, err)
	if len(summary) != 196 {
		t.Fatalf("expected 196 bytes, got %d", len(summary))
	}
	records := depositRecords(t, map[types.Hash][]int64{conformanceAsset: {4, 6}, types.ZeroHash: {0}})
	common.FailIfErr(t, verifyAssetFlowDeposits(summary, records))
}

func TestVerifyAssetFlowDeposits_EmptySummary(t *testing.T) {
	common.FailIfErr(t, verifyAssetFlowDeposits([]byte{0, 0, 0, 0}, []*definition.AssignedIndexRecord{
		{AssetId: types.ZeroHash, Amount: big.NewInt(0)},
	}))
}

func TestVerifyAssetFlowDeposits_Rejects(t *testing.T) {
	znn, qsr := znnAssetId(), qsrAssetId()
	tenQsr := depositRecords(t, map[types.Hash][]int64{qsr: {10}})
	znnAndQsr := depositRecords(t, map[types.Hash][]int64{qsr: {10}, znn: {2}})

	many := []byte{0, 0, 0, 17}
	for i := 0; i < 17; i++ {
		var asset types.Hash
		copy(asset[22:], []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, byte(i + 1)})
		many = append(many, flowEntry(asset, 1, 0)...)
	}
	var nonCanonical types.Hash
	nonCanonical[0] = 1
	copy(nonCanonical[22:], types.ZnnTokenStandard.Bytes())

	valid := flowSummary(flowEntry(qsr, 10, 0))
	cases := []struct {
		name    string
		summary []byte
		records []*definition.AssignedIndexRecord
	}{
		{"credit below deposits", flowSummary(flowEntry(qsr, 9, 0)), tenQsr},
		{"deposit absent from summary", flowSummary(flowEntry(qsr, 10, 0)), znnAndQsr},
		{"credit without deposit", flowSummary(flowEntry(qsr, 10, 0), flowEntry(znn, 1, 0)), tenQsr},
		{"entries out of order", flowSummary(flowEntry(znn, 2, 0), flowEntry(qsr, 10, 0)), znnAndQsr},
		{"duplicate asset", flowSummary(flowEntry(qsr, 10, 0), flowEntry(qsr, 10, 0)), tenQsr},
		{"too many entries", many, nil},
		{"entry with both amounts zero", flowSummary(flowEntry(qsr, 0, 0)), nil},
		{"non-canonical asset id", flowSummary(flowEntry(nonCanonical, 1, 0)), nil},
		{"trailing byte", append(append([]byte{}, valid...), 0), tenQsr},
		{"truncated entry", valid[:len(valid)-1], tenQsr},
		{"shorter than the count", []byte{0, 0}, nil},
	}
	for _, c := range cases {
		if err := verifyAssetFlowDeposits(c.summary, c.records); err != constants.ErrSettlementInvalidAssetFlow {
			t.Fatalf("%s: expected ErrSettlementInvalidAssetFlow, got %v", c.name, err)
		}
	}
}

// ============================================================================
// Deploy records and bytecode fee
// ============================================================================

func deployTestHash(b byte) types.Hash {
	var h types.Hash
	for i := range h {
		h[i] = b
	}
	return h
}

// seedDeployRecord stores a header with chunkCount chunks and one chunk
// entry for each index in filled.
func seedDeployRecord(t *testing.T, storage db.DB, deployer types.Address, startHeight uint64, chunkCount uint32, filled ...int) *definition.DeployRecord {
	t.Helper()
	record := &definition.DeployRecord{
		DeployRecordKey: definition.DeployRecordKey{DomainId: 1, Deployer: deployer, CodeHash: deployTestHash(0xC0)},
		MetadataHash:    deployTestHash(0xD0),
		TotalSize:       100,
		ChunkCount:      chunkCount,
		StartHeight:     startHeight,
		ReceivedCount:   uint32(len(filled)),
	}
	common.FailIfErr(t, record.Save(storage))
	for _, i := range filled {
		chunk := &definition.DeployChunk{
			DeployChunkKey: definition.DeployChunkKey{DeployRecordKey: record.DeployRecordKey, StartHeight: startHeight, ChunkIndex: uint32(i)},
			ChunkHash:      deployTestHash(byte(0x10 + i)),
		}
		common.FailIfErr(t, chunk.Save(storage))
	}
	return record
}

func expectErr(t *testing.T, label string, got, want error) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got error %v want %v", label, got, want)
	}
}

func TestGetLiveDeployRecord_AbsentAndOtherDeployer(t *testing.T) {
	storage := feeTestStorage()
	codeHash := deployTestHash(0xC0)
	_, err := getLiveDeployRecord(storage, 1, feeTestAddress(0x0A), codeHash, 10)
	expectErr(t, "absent", err, constants.ErrDataNonExistent)

	seedDeployRecord(t, storage, feeTestAddress(0x0A), 10, 1)
	record, err := getLiveDeployRecord(storage, 1, feeTestAddress(0x0A), codeHash, 10)
	common.FailIfErr(t, err)
	if record.StartHeight != 10 {
		t.Fatalf("unexpected record %+v", record)
	}
	_, err = getLiveDeployRecord(storage, 1, feeTestAddress(0x0B), codeHash, 10)
	expectErr(t, "other deployer", err, constants.ErrDataNonExistent)
	_, err = getLiveDeployRecord(storage, 2, feeTestAddress(0x0A), codeHash, 10)
	expectErr(t, "other domain", err, constants.ErrDataNonExistent)
}

func TestGetLiveDeployRecord_ExpiryBoundary(t *testing.T) {
	storage := feeTestStorage()
	deployer := feeTestAddress(0x0A)
	seedDeployRecord(t, storage, deployer, 100, 1)
	codeHash := deployTestHash(0xC0)

	_, err := getLiveDeployRecord(storage, 1, deployer, codeHash, 100+constants.DeploymentExpiryWindow-1)
	common.FailIfErr(t, err)
	_, err = getLiveDeployRecord(storage, 1, deployer, codeHash, 100+constants.DeploymentExpiryWindow)
	expectErr(t, "at expiry height", err, constants.ErrDataNonExistent)
	_, err = getLiveDeployRecord(storage, 1, deployer, codeHash, 100+constants.DeploymentExpiryWindow+5)
	expectErr(t, "after expiry height", err, constants.ErrDataNonExistent)
}

func TestConsumeDeployRecord_Rejections(t *testing.T) {
	storage := feeTestStorage()
	deployer := feeTestAddress(0x0A)
	codeHash := deployTestHash(0xC0)
	seedDeployRecord(t, storage, deployer, 10, 3, 0, 2)

	_, err := consumeDeployRecord(storage, 1, feeTestAddress(0x0B), codeHash, 11)
	expectErr(t, "other caller", err, constants.ErrDataNonExistent)
	_, err = consumeDeployRecord(storage, 1, deployer, codeHash, 11)
	expectErr(t, "missing chunk", err, constants.ErrForbiddenParam)
	_, err = consumeDeployRecord(storage, 1, deployer, codeHash, 10+constants.DeploymentExpiryWindow)
	expectErr(t, "expired", err, constants.ErrDataNonExistent)

	record, err := definition.GetDeployRecord(storage, 1, deployer, codeHash)
	common.FailIfErr(t, err)
	if record.StartHeight != 10 {
		t.Fatalf("expected the record to stay stored, got %+v", record)
	}
}

func TestConsumeDeployRecord_ReturnsAndDeletes(t *testing.T) {
	storage := feeTestStorage()
	deployer := feeTestAddress(0x0A)
	codeHash := deployTestHash(0xC0)
	seedDeployRecord(t, storage, deployer, 10, 3, 0, 1, 2)

	record, err := consumeDeployRecord(storage, 1, deployer, codeHash, 11)
	common.FailIfErr(t, err)
	if record.ChunkCount != 3 || record.TotalSize != 100 || record.Deployer != deployer {
		t.Fatalf("unexpected record %+v", record)
	}
	_, err = definition.GetDeployRecord(storage, 1, deployer, codeHash)
	expectErr(t, "after consume", err, constants.ErrDataNonExistent)
	_, err = consumeDeployRecord(storage, 1, deployer, codeHash, 11)
	expectErr(t, "second consume", err, constants.ErrDataNonExistent)
	for i := uint32(0); i < 3; i++ {
		_, err = definition.GetDeployChunk(storage, definition.DeployChunkKey{DeployRecordKey: record.DeployRecordKey, StartHeight: 10, ChunkIndex: i})
		expectErr(t, "chunk after consume", err, constants.ErrDataNonExistent)
	}
}

func enqueueSeed(t *testing.T, storage db.DB, record *definition.DeployRecord) {
	t.Helper()
	common.FailIfErr(t, definition.EnqueueDeployStart(storage, &definition.DeployStartQueueEntry{
		DomainId:    record.DomainId,
		Deployer:    record.Deployer,
		CodeHash:    record.CodeHash,
		StartHeight: record.StartHeight,
	}))
}

func hasDeployRecord(t *testing.T, storage db.DB, deployer types.Address) bool {
	t.Helper()
	_, err := definition.GetDeployRecord(storage, 1, deployer, deployTestHash(0xC0))
	if err == constants.ErrDataNonExistent {
		return false
	}
	common.FailIfErr(t, err)
	return true
}

func hasDeployChunk(t *testing.T, storage db.DB, deployer types.Address, startHeight uint64, index uint32) bool {
	t.Helper()
	key := definition.DeployChunkKey{
		DeployRecordKey: definition.DeployRecordKey{DomainId: 1, Deployer: deployer, CodeHash: deployTestHash(0xC0)},
		StartHeight:     startHeight,
		ChunkIndex:      index,
	}
	_, err := definition.GetDeployChunk(storage, key)
	if err == constants.ErrDataNonExistent {
		return false
	}
	common.FailIfErr(t, err)
	return true
}

func queueHead(t *testing.T, storage db.DB) (uint64, uint64) {
	t.Helper()
	meta, err := definition.GetDeployStartQueueMeta(storage)
	common.FailIfErr(t, err)
	return meta.Head, meta.Tail
}

func TestSweepExpiredDeployStarts_DrainsTwoPerStart(t *testing.T) {
	storage := feeTestStorage()
	w := uint64(constants.DeploymentExpiryWindow)
	for i := 0; i < 5; i++ {
		enqueueSeed(t, storage, seedDeployRecord(t, storage, feeTestAddress(byte(0x0A+i)), 10, 2, 0))
	}
	for n := 1; n <= 3; n++ {
		common.FailIfErr(t, sweepExpiredDeployStarts(storage, 10+w))
		head, _ := queueHead(t, storage)
		want := 2 * n
		if want > 5 {
			want = 5
		}
		if head != uint64(want) {
			t.Fatalf("call %d: head %d want %d", n, head, want)
		}
		for i := 0; i < 5; i++ {
			deployer := feeTestAddress(byte(0x0A + i))
			swept := i < want
			if hasDeployRecord(t, storage, deployer) == swept || hasDeployChunk(t, storage, deployer, 10, 0) == swept {
				t.Fatalf("call %d: deployer %d swept=%v mismatch", n, i, swept)
			}
		}
	}
	common.FailIfErr(t, sweepExpiredDeployStarts(storage, 10+w))
	head, tail := queueHead(t, storage)
	if head != 5 || tail != 5 {
		t.Fatalf("head %d tail %d, want 5 5", head, tail)
	}
	_, err := definition.PeekDeployStart(storage)
	expectErr(t, "peek drained", err, constants.ErrDataNonExistent)
}

func TestSweepExpiredDeployStarts_StopsAtFirstUnexpired(t *testing.T) {
	storage := feeTestStorage()
	w := uint64(constants.DeploymentExpiryWindow)
	a, b := feeTestAddress(0x0A), feeTestAddress(0x0B)
	enqueueSeed(t, storage, seedDeployRecord(t, storage, a, 10, 1))
	enqueueSeed(t, storage, seedDeployRecord(t, storage, b, 11, 1))

	common.FailIfErr(t, sweepExpiredDeployStarts(storage, 10+w-1))
	if head, _ := queueHead(t, storage); head != 0 || !hasDeployRecord(t, storage, a) {
		t.Fatalf("expected nothing removed, head %d", head)
	}
	common.FailIfErr(t, sweepExpiredDeployStarts(storage, 10+w))
	if head, _ := queueHead(t, storage); head != 1 {
		t.Fatalf("head %d want 1", head)
	}
	if hasDeployRecord(t, storage, a) || !hasDeployRecord(t, storage, b) {
		t.Fatal("expected only A removed")
	}
}

func TestSweepExpiredDeployStarts_ConsumedRecordLeavesNothing(t *testing.T) {
	storage := feeTestStorage()
	w := uint64(constants.DeploymentExpiryWindow)
	a := feeTestAddress(0x0A)
	enqueueSeed(t, storage, seedDeployRecord(t, storage, a, 10, 2, 0, 1))
	_, err := consumeDeployRecord(storage, 1, a, deployTestHash(0xC0), 11)
	common.FailIfErr(t, err)

	common.FailIfErr(t, sweepExpiredDeployStarts(storage, 10+w))
	if head, _ := queueHead(t, storage); head != 1 {
		t.Fatalf("head %d want 1", head)
	}
	if hasDeployRecord(t, storage, a) || hasDeployChunk(t, storage, a, 10, 0) || hasDeployChunk(t, storage, a, 10, 1) {
		t.Fatal("expected no header and no chunk entries")
	}
}

func TestSweepExpiredDeployStarts_KeepsRestartedRecord(t *testing.T) {
	storage := feeTestStorage()
	w := uint64(constants.DeploymentExpiryWindow)
	a := feeTestAddress(0x0A)
	enqueueSeed(t, storage, seedDeployRecord(t, storage, a, 10, 2, 0))
	enqueueSeed(t, storage, seedDeployRecord(t, storage, a, 10+w, 2, 0))

	common.FailIfErr(t, sweepExpiredDeployStarts(storage, 10+w))
	if head, _ := queueHead(t, storage); head != 1 {
		t.Fatalf("head %d want 1", head)
	}
	record, err := definition.GetDeployRecord(storage, 1, a, deployTestHash(0xC0))
	common.FailIfErr(t, err)
	if record.StartHeight != 10+w || record.ReceivedCount != 1 {
		t.Fatalf("unexpected record %+v", record)
	}
	if !hasDeployChunk(t, storage, a, 10+w, 0) || hasDeployChunk(t, storage, a, 10, 0) {
		t.Fatal("expected the restart's chunk kept and the predecessor's chunk gone")
	}
}

// deployStartContext serves a deploy start receive from a storage and a
// frontier height; the receive reads nothing else from the context.
type deployStartContext struct {
	vm_context.AccountVmContext
	storage db.DB
	height  uint64
}

func (c *deployStartContext) Storage() db.DB { return c.storage }
func (c *deployStartContext) GetFrontierMomentum() (*nom.Momentum, error) {
	return &nom.Momentum{Height: c.height}, nil
}

// A start whose input index is exhausted fails after its sweep has run.
// The receive runs on a snapshot, which the caller drops when the receive
// fails, so none of the sweep's writes reach the base storage.
func TestDeployContractStart_ExhaustedInputIndexLeavesSweepUnapplied(t *testing.T) {
	base := feeTestStorage()
	w := uint64(constants.DeploymentExpiryWindow)
	deployers := []types.Address{feeTestAddress(0x0A), feeTestAddress(0x0B), feeTestAddress(0x0C)}
	for _, deployer := range deployers {
		enqueueSeed(t, base, seedDeployRecord(t, base, deployer, 10, 2, 0, 1))
	}
	domain := &definition.DomainRecord{DomainRecordKey: definition.DomainRecordKey{DomainId: 1}, DomainClass: 1}
	common.FailIfErr(t, domain.Save(base))
	common.FailIfErr(t, (&definition.ReceivedInputCounter{Count: math.MaxUint64}).Save(base))

	starter := feeTestAddress(0x0D)
	method := &SettlementDeployContractStartMethod{MethodName: definition.SettlementDeployContractStartMethodName}
	data, err := definition.ABISettlement.PackMethod(method.MethodName,
		uint32(1), deployTestHash(0xC0), types.NewHash(nil), uint64(100), uint32(1), []byte(nil))
	common.FailIfErr(t, err)
	send := &nom.AccountBlock{Address: starter, Amount: big.NewInt(0), Data: data}

	attempt := db.DisableNotFound(base.Snapshot())
	_, err = method.ReceiveBlock(&deployStartContext{storage: attempt, height: 10 + w}, send)
	expectErr(t, "exhausted start", err, constants.ErrSettlementInputIndexExhausted)

	if head, tail := queueHead(t, attempt); head != 2 || tail != 4 {
		t.Fatalf("expected the attempt to have swept two entries and queued its own (head 2 tail 4), got head %d tail %d", head, tail)
	}
	if hasDeployRecord(t, attempt, deployers[0]) || hasDeployRecord(t, attempt, deployers[1]) {
		t.Fatal("expected the attempt to have swept the two oldest headers")
	}

	if head, tail := queueHead(t, base); head != 0 || tail != 3 {
		t.Fatalf("expected the queue meta unchanged (head 0 tail 3), got head %d tail %d", head, tail)
	}
	for i, deployer := range deployers {
		if !hasDeployRecord(t, base, deployer) {
			t.Fatalf("deployer %d: header missing after the rejected start", i)
		}
		for chunk := uint32(0); chunk < 2; chunk++ {
			if !hasDeployChunk(t, base, deployer, 10, chunk) {
				t.Fatalf("deployer %d: chunk %d missing after the rejected start", i, chunk)
			}
		}
	}
	probe := db.DisableNotFound(base.Snapshot())
	for index, deployer := range deployers {
		entry, err := definition.DequeueDeployStart(probe)
		common.FailIfErr(t, err)
		if entry.Deployer != deployer || entry.StartHeight != 10 {
			t.Fatalf("queue entry %d changed: %+v", index, entry)
		}
	}
	if hasDeployRecord(t, base, starter) {
		t.Fatal("rejected start left a header behind")
	}
	counter, err := definition.GetReceivedInputCounter(base)
	common.FailIfErr(t, err)
	if counter.Count != math.MaxUint64 {
		t.Fatalf("expected the counter unchanged, got %d", counter.Count)
	}
}

func feeBlock(zts types.ZenonTokenStandard, amount int64) *nom.AccountBlock {
	return &nom.AccountBlock{TokenStandard: zts, Amount: big.NewInt(amount)}
}

func collectedOf(t *testing.T, storage db.DB) *big.Int {
	t.Helper()
	counters, err := definition.GetFeeCounters(storage)
	common.FailIfErr(t, err)
	return counters.TotalFeesCollected
}

func TestChargeBytecodeFee_NoPeripheryConfig(t *testing.T) {
	storage := feeTestStorage()
	_, err := chargeBytecodeFee(storage, 100, feeBlock(types.ZnnTokenStandard, 100))
	expectErr(t, "no config", err, constants.ErrDataNonExistent)
	expectBig(t, "collected", collectedOf(t, storage), 0)
}

func TestChargeBytecodeFee_RejectsWrongTokenOrAmount(t *testing.T) {
	storage := feeTestStorage()
	common.FailIfErr(t, (&definition.PeripheryConfig{ZnnPerByteOfBytecode: big.NewInt(3), ZnnPerByteOfState: big.NewInt(1)}).Save(storage))
	// fee = 100 * 3 = 300
	for _, tc := range []struct {
		name string
		blk  *nom.AccountBlock
	}{
		{"qsr right amount", feeBlock(types.QsrTokenStandard, 300)},
		{"znn one short", feeBlock(types.ZnnTokenStandard, 299)},
		{"znn one over", feeBlock(types.ZnnTokenStandard, 301)},
		{"znn zero", feeBlock(types.ZnnTokenStandard, 0)},
	} {
		_, err := chargeBytecodeFee(storage, 100, tc.blk)
		expectErr(t, tc.name, err, constants.ErrInvalidTokenOrAmount)
		expectBig(t, tc.name+" collected", collectedOf(t, storage), 0)
	}
}

func TestChargeBytecodeFee_ExactFeeIsCollected(t *testing.T) {
	storage := feeTestStorage()
	common.FailIfErr(t, (&definition.PeripheryConfig{ZnnPerByteOfBytecode: big.NewInt(3), ZnnPerByteOfState: big.NewInt(1)}).Save(storage))
	seedCollected(t, storage, 50)

	fee, err := chargeBytecodeFee(storage, 100, feeBlock(types.ZnnTokenStandard, 300))
	common.FailIfErr(t, err)
	expectBig(t, "fee", fee, 300)
	expectBig(t, "collected", collectedOf(t, storage), 350)

	fee, err = chargeBytecodeFee(storage, 1, feeBlock(types.ZnnTokenStandard, 3))
	common.FailIfErr(t, err)
	expectBig(t, "second fee", fee, 3)
	expectBig(t, "second collected", collectedOf(t, storage), 353)
}

// ============================================================================
// SubmitBatch
// ============================================================================

const (
	conformanceCommitmentHex         = "00010001000100010001000100020001000100000001000000000000006400000000000000000000000000000005010101010101010101010101010101010101010101010101010101010101010102020202020202020202020202020202020202020202020202020202020202020303030303030303030303030303030303030303030303030303030303030303040404040404040404040404040404040404040404040404040404040404040405050505050505050505050505050505050505050505050505050505050505050606060606060606060606060606060606060606060606060606060606060606070707070707070707070707070707070707070707070707070707070707070700eeee0000000000000000000000000000000000000000000000003039000000020000000000000000000000000000000000000000000004066318c6318c6318c6000000000000000000000000000000000000000000000000000000000000000a00000000000000000000000000000000000000000000000000000000000000010000000000000000000000000000000000000000000014e66318c6318c6318c60000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000200000000"
	conformanceCommitmentTrailingHex = conformanceCommitmentHex + "00"
	conformanceCommitmentProofHex    = "00010001000100010001000100020001000100000001000000000000006400000000000000000000000000000005010101010101010101010101010101010101010101010101010101010101010102020202020202020202020202020202020202020202020202020202020202020303030303030303030303030303030303030303030303030303030303030303040404040404040404040404040404040404040404040404040404040404040405050505050505050505050505050505050505050505050505050505050505050606060606060606060606060606060606060606060606060606060606060606070707070707070707070707070707070707070707070707070707070707070700eeee0000000000000000000000000000000000000000000000003039000000020000000000000000000000000000000000000000000004066318c6318c6318c6000000000000000000000000000000000000000000000000000000000000000a00000000000000000000000000000000000000000000000000000000000000010000000000000000000000000000000000000000000014e66318c6318c6318c6000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000020000000001ab"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	common.FailIfErr(t, err)
	return b
}

// pinnedVersions returns the commitment bytes with the version block set
// to the pinned values.
func pinnedVersions(t *testing.T, s string) []byte {
	b := mustHex(t, s)
	b[12], b[13] = 0, 1
	return b
}

func TestDecodeBatchCommitment_RejectsUnpinnedReceiptVersion(t *testing.T) {
	_, _, _, err := decodeBatchCommitment(mustHex(t, conformanceCommitmentHex))
	expectErr(t, "receipt version 2", err, constants.ErrForbiddenParam)
}

func TestDecodeBatchCommitment_DecodesFields(t *testing.T) {
	entry, executor, height, err := decodeBatchCommitment(pinnedVersions(t, conformanceCommitmentHex))
	common.FailIfErr(t, err)

	if entry.DomainId != 1 || entry.BatchId != 100 || entry.FirstInputSeq != 0 || entry.LastInputSeq != 5 {
		t.Fatalf("unexpected key or bounds %+v", entry)
	}
	if entry.ProtocolVersion != 1 || entry.DaMode != 0 {
		t.Fatalf("unexpected version %d or mode %d", entry.ProtocolVersion, entry.DaMode)
	}
	for i, h := range []types.Hash{entry.PreStateRoot, entry.PostStateRoot, entry.InputRoot, entry.ReceiptRoot, entry.EventRoot, entry.OutboxRoot, entry.DaHash} {
		if h != deployTestHash(byte(i+1)) {
			t.Fatalf("hash %d: got %v", i, h)
		}
	}
	var wantExecutor types.Address
	wantExecutor[0], wantExecutor[1] = 0xee, 0xee
	if executor != wantExecutor || height != 12345 {
		t.Fatalf("unexpected executor %v or height %d", executor, height)
	}
	if len(entry.AssetFlowSummary) != 4+2*assetFlowEntrySize || len(entry.ProofData) != 0 || entry.ProofData == nil {
		t.Fatalf("unexpected summary length %d or proofData %v", len(entry.AssetFlowSummary), entry.ProofData)
	}
}

func TestDecodeBatchCommitment_RejectsEachUnpinnedVersion(t *testing.T) {
	for i := 0; i < 9; i++ {
		b := pinnedVersions(t, conformanceCommitmentHex)
		b[2*i], b[2*i+1] = 0, 2
		_, _, _, err := decodeBatchCommitment(b)
		expectErr(t, "version field "+string(rune('0'+i)), err, constants.ErrForbiddenParam)
	}
}

func TestDecodeBatchCommitment_RejectsMalformed(t *testing.T) {
	valid := pinnedVersions(t, conformanceCommitmentHex)
	tooManyAssets := append([]byte{}, valid...)
	tooManyAssets[299], tooManyAssets[300], tooManyAssets[301], tooManyAssets[302] = 0, 0, 0, 17

	for _, c := range []struct {
		name string
		data []byte
		want error
	}{
		{"non-empty proofData", pinnedVersions(t, conformanceCommitmentProofHex), constants.ErrForbiddenParam},
		{"trailing byte", pinnedVersions(t, conformanceCommitmentTrailingHex), constants.ErrForbiddenParam},
		{"17 bytes", valid[:17], constants.ErrForbiddenParam},
		{"18 bytes", valid[:18], constants.ErrForbiddenParam},
		{"298 bytes", valid[:298], constants.ErrForbiddenParam},
		{"302 bytes", valid[:302], constants.ErrForbiddenParam},
		{"missing last byte", valid[:len(valid)-1], constants.ErrForbiddenParam},
		{"17 assets", tooManyAssets, constants.ErrSettlementInvalidAssetFlow},
	} {
		_, _, _, err := decodeBatchCommitment(c.data)
		expectErr(t, c.name, err, c.want)
	}
}

// batchSpec describes a commitment for encodeCommitment; the versions are
// pinned and the wire height is 0.
type batchSpec struct {
	domain        uint32
	batchId       uint64
	first, last   uint64
	pre, post     types.Hash
	daMode        uint8
	executor      types.Address
	summary       []byte
	wireHeight    uint64
	proofDataSize uint32
}

func encodeCommitment(s batchSpec) []byte {
	var out []byte
	for i := 0; i < 9; i++ {
		out = binary.BigEndian.AppendUint16(out, 1)
	}
	out = binary.BigEndian.AppendUint32(out, s.domain)
	out = binary.BigEndian.AppendUint64(out, s.batchId)
	out = binary.BigEndian.AppendUint64(out, s.first)
	out = binary.BigEndian.AppendUint64(out, s.last)
	out = append(out, s.pre[:]...)
	out = append(out, s.post[:]...)
	for i := 0; i < 5; i++ {
		out = append(out, deployTestHash(byte(0x30+i)).Bytes()...)
	}
	out = append(out, s.daMode)
	out = append(out, s.executor[:]...)
	out = binary.BigEndian.AppendUint64(out, s.wireHeight)
	summary := s.summary
	if summary == nil {
		summary = flowSummary()
	}
	out = append(out, summary...)
	return binary.BigEndian.AppendUint32(out, s.proofDataSize)
}

func submitBlock(t *testing.T, method *SettlementSubmitBatchMethod, sender types.Address, commitment []byte) *nom.AccountBlock {
	t.Helper()
	data, err := definition.ABISettlement.PackMethod(method.MethodName, commitment)
	common.FailIfErr(t, err)
	return &nom.AccountBlock{Address: sender, Amount: big.NewInt(0), Data: data}
}

func TestSubmitBatchValidateSendBlock(t *testing.T) {
	method := &SettlementSubmitBatchMethod{MethodName: definition.SettlementSubmitBatchMethodName}
	executor := feeTestAddress(0xE1)
	good := batchSpec{domain: 1, executor: executor}

	common.FailIfErr(t, method.ValidateSendBlock(submitBlock(t, method, executor, encodeCommitment(good))))

	oversize := make([]byte, constants.MaxOnChainCommitmentSize+1)
	expectErr(t, "oversize commitment", method.ValidateSendBlock(submitBlock(t, method, executor, oversize)), constants.ErrForbiddenParam)

	expectErr(t, "other sender", method.ValidateSendBlock(submitBlock(t, method, feeTestAddress(0xE2), encodeCommitment(good))), constants.ErrPermissionDenied)

	withHeight := good
	withHeight.wireHeight = 7
	expectErr(t, "wire height", method.ValidateSendBlock(submitBlock(t, method, executor, encodeCommitment(withHeight))), constants.ErrForbiddenParam)

	paid := submitBlock(t, method, executor, encodeCommitment(good))
	paid.Amount = big.NewInt(1)
	expectErr(t, "attached amount", method.ValidateSendBlock(paid), constants.ErrInvalidTokenOrAmount)

	expectErr(t, "bad data", method.ValidateSendBlock(&nom.AccountBlock{Address: executor, Amount: big.NewInt(0), Data: []byte{1, 2}}), constants.ErrUnpackError)
}

func TestAdmitInput_AssignsDomainOrdinals(t *testing.T) {
	storage := feeTestStorage()
	for _, domain := range []uint32{1, 2, 1, 2, 1} {
		common.FailIfErr(t, admitInput(storage, domain, types.ZeroHash, nil, nil))
	}

	for _, w := range []struct {
		domain  uint32
		ordinal uint64
		global  uint64
	}{{1, 0, 0}, {1, 1, 2}, {1, 2, 4}, {2, 0, 1}, {2, 1, 3}} {
		o, err := definition.GetDomainInputOrdinal(storage, w.domain, w.ordinal)
		common.FailIfErr(t, err)
		if o.GlobalInputIndex != w.global {
			t.Fatalf("domain %d ordinal %d: got global %d want %d", w.domain, w.ordinal, o.GlobalInputIndex, w.global)
		}
	}
	for domain, want := range map[uint32]uint64{1: 3, 2: 2} {
		counter, err := definition.GetDomainInputCounter(storage, domain)
		common.FailIfErr(t, err)
		if counter.Count != want {
			t.Fatalf("domain %d: counter %d want %d", domain, counter.Count, want)
		}
	}
}

const (
	batchDelay    = uint64(10)
	batchDomain   = uint32(1)
	otherDomain   = uint32(2)
	submitHeight0 = uint64(100)
)

var batchExecutor = feeTestAddress(0xE1)

// batchStorage returns a storage with registered domains 1 and 2, an
// approved DA mode 0, and an active executor for pillar-a.
func batchStorage(t *testing.T) db.DB {
	storage := feeTestStorage()
	for _, id := range []uint32{batchDomain, otherDomain} {
		common.FailIfErr(t, (&definition.DomainRecord{DomainRecordKey: definition.DomainRecordKey{DomainId: id}, DomainClass: 1}).Save(storage))
		common.FailIfErr(t, (&definition.DomainConfig{DomainId: id, WithdrawalDelay: batchDelay}).Save(storage))
		common.FailIfErr(t, (&definition.ApprovedDAModeSet{DomainId: id, Modes: []byte{0}}).Save(storage))
	}
	common.FailIfErr(t, (&definition.ExecutorSetMember{
		PillarName: "pillar-a", ExecutorAddress: batchExecutor, Status: definition.ExecutorStatusActive,
	}).Save(storage))
	return storage
}

func admitN(t *testing.T, storage db.DB, domain uint32, n int, fee int64) {
	t.Helper()
	for i := 0; i < n; i++ {
		common.FailIfErr(t, admitInput(storage, domain, types.ZeroHash, nil, big.NewInt(fee)))
	}
}

func rootOf(b byte) types.Hash { return deployTestHash(b) }

// submitBatch decodes spec through the wire format and accepts it at height.
func submitBatch(t *testing.T, storage db.DB, height uint64, spec batchSpec) error {
	t.Helper()
	spec.executor = batchExecutor
	entry, _, _, err := decodeBatchCommitment(encodeCommitment(spec))
	common.FailIfErr(t, err)
	return acceptBatch(storage, height, feeTestPillars(), batchExecutor, entry)
}

func cursorOf(t *testing.T, storage db.DB, domain uint32) definition.InputSequenceCursor {
	t.Helper()
	cursor, err := definition.GetInputSequenceCursor(storage, domain)
	common.FailIfErr(t, err)
	return *cursor
}

func hasInput(t *testing.T, storage db.DB, domain uint32, ordinal, global uint64) (record, entry bool) {
	t.Helper()
	_, err := definition.GetAssignedIndexRecord(storage, domain, global)
	if err != nil && err != constants.ErrDataNonExistent {
		t.Fatal(err)
	}
	record = err == nil
	_, err = definition.GetDomainInputOrdinal(storage, domain, ordinal)
	if err != nil && err != constants.ErrDataNonExistent {
		t.Fatal(err)
	}
	return record, err == nil
}

func TestAcceptBatch_AcceptsFirstAndChainedBatches(t *testing.T) {
	storage := batchStorage(t)
	admitN(t, storage, batchDomain, 5, 3)

	common.FailIfErr(t, submitBatch(t, storage, submitHeight0, batchSpec{domain: batchDomain, batchId: 0, first: 0, last: 1, pre: types.ZeroHash, post: rootOf(0x51)}))
	entry, err := definition.GetBatchEntry(storage, batchDomain, 0)
	common.FailIfErr(t, err)
	if entry.ExecutorPillar != "pillar-a" || entry.SubmittedAtHeight != submitHeight0 || entry.FinalizeAtHeight != submitHeight0+batchDelay || entry.State != definition.BatchStateSubmitted {
		t.Fatalf("unexpected entry %+v", entry)
	}
	if got := cursorOf(t, storage, batchDomain); got.ConsumedInputCount != 2 || got.NextBatchId != 1 {
		t.Fatalf("unexpected cursor %+v", got)
	}
	pending, err := definition.GetPendingFee(storage, feeEpochOf(submitHeight0), "pillar-a")
	common.FailIfErr(t, err)
	expectBig(t, "pending fee", pending.Amount, 6)

	common.FailIfErr(t, submitBatch(t, storage, submitHeight0+1, batchSpec{domain: batchDomain, batchId: 1, first: 2, last: 4, pre: rootOf(0x51), post: rootOf(0x52)}))
	if got := cursorOf(t, storage, batchDomain); got.ConsumedInputCount != 5 || got.NextBatchId != 2 {
		t.Fatalf("unexpected cursor %+v", got)
	}
}

func TestAcceptBatch_AcceptsDepositSummary(t *testing.T) {
	storage := batchStorage(t)
	common.FailIfErr(t, admitInput(storage, batchDomain, znnAssetId(), big.NewInt(5), nil))
	summary := flowSummary(flowEntry(znnAssetId(), 5, 0))
	common.FailIfErr(t, submitBatch(t, storage, submitHeight0, batchSpec{domain: batchDomain, first: 0, last: 0, summary: summary}))

	bad := batchStorage(t)
	common.FailIfErr(t, admitInput(bad, batchDomain, znnAssetId(), big.NewInt(5), nil))
	err := submitBatch(t, bad, submitHeight0, batchSpec{domain: batchDomain, first: 0, last: 0, summary: flowSummary(flowEntry(znnAssetId(), 4, 0))})
	expectErr(t, "wrong credit", err, constants.ErrSettlementInvalidAssetFlow)
}

func TestAcceptBatch_InterleavedDomains(t *testing.T) {
	storage := batchStorage(t)
	for _, domain := range []uint32{batchDomain, otherDomain, batchDomain, otherDomain, batchDomain} {
		admitN(t, storage, domain, 1, 0)
	}
	common.FailIfErr(t, submitBatch(t, storage, submitHeight0, batchSpec{domain: batchDomain, first: 0, last: 2}))

	err := submitBatch(t, storage, submitHeight0, batchSpec{domain: batchDomain, batchId: 1, first: 2, last: 4, pre: types.ZeroHash})
	expectErr(t, "overlap", err, constants.ErrForbiddenParam)
	common.FailIfErr(t, submitBatch(t, storage, submitHeight0, batchSpec{domain: batchDomain, batchId: 1, first: 4, last: 4}))
}

// rejection pairs a submission with the error it must return.
func TestAcceptBatch_RejectsAndWritesNothing(t *testing.T) {
	setup := func() db.DB {
		storage := batchStorage(t)
		admitN(t, storage, batchDomain, 2, 3)
		admitN(t, storage, otherDomain, 1, 0)
		common.FailIfErr(t, submitBatch(t, storage, submitHeight0, batchSpec{domain: batchDomain, first: 0, last: 0, post: rootOf(0x51)}))
		return storage
	}
	// the domain-1 batch starts at global index 1, since index 0 is consumed.
	valid := batchSpec{domain: batchDomain, batchId: 1, first: 1, last: 1, pre: rootOf(0x51)}
	with := func(f func(*batchSpec)) batchSpec { s := valid; f(&s); return s }

	for _, c := range []struct {
		name string
		spec batchSpec
		want error
	}{
		{"replayed batchId", with(func(s *batchSpec) { s.batchId = 0 }), constants.ErrForbiddenParam},
		{"skipped batchId", with(func(s *batchSpec) { s.batchId = 2 }), constants.ErrForbiddenParam},
		{"wrong preStateRoot", with(func(s *batchSpec) { s.pre = rootOf(0x99) }), constants.ErrForbiddenParam},
		{"unregistered domain", with(func(s *batchSpec) { s.domain = 9 }), constants.ErrForbiddenParam},
		{"unregistered domain 0", with(func(s *batchSpec) { s.domain = 0 }), constants.ErrForbiddenParam},
		{"unapproved DA mode", with(func(s *batchSpec) { s.daMode = 1 }), constants.ErrForbiddenParam},
		{"gap", with(func(s *batchSpec) { s.first = 2 }), constants.ErrForbiddenParam},
		{"last not yet assigned", with(func(s *batchSpec) { s.last = 5 }), constants.ErrForbiddenParam},
		{"last below first", with(func(s *batchSpec) { s.last = 0 }), constants.ErrForbiddenParam},
		{"last in another domain", with(func(s *batchSpec) { s.last = 2 }), constants.ErrForbiddenParam},
		{"non-canonical asset", with(func(s *batchSpec) {
			s.summary = flowSummary(flowEntry(types.HexToHashPanic("0000000000000000000000000000000000000000000000000000000000000001"), 1, 0))
		}), constants.ErrSettlementInvalidAssetFlow},
	} {
		storage := setup()
		beforeCursor := cursorOf(t, storage, batchDomain)
		err := submitBatch(t, storage, submitHeight0+1, c.spec)
		expectErr(t, c.name, err, c.want)

		if got := cursorOf(t, storage, batchDomain); got != beforeCursor {
			t.Fatalf("%s: cursor changed to %+v", c.name, got)
		}
		if _, err := definition.GetBatchEntry(storage, batchDomain, 1); err != constants.ErrDataNonExistent {
			t.Fatalf("%s: expected no batch 1, got %v", c.name, err)
		}
		pending, err := definition.GetPendingFee(storage, feeEpochOf(submitHeight0), "pillar-a")
		common.FailIfErr(t, err)
		expectBig(t, c.name+" pending fee", pending.Amount, 3)
		for _, in := range []struct {
			domain  uint32
			ordinal uint64
			global  uint64
		}{{batchDomain, 0, 0}, {batchDomain, 1, 1}, {otherDomain, 0, 2}} {
			if record, entry := hasInput(t, storage, in.domain, in.ordinal, in.global); !record || !entry {
				t.Fatalf("%s: input %d missing", c.name, in.global)
			}
		}
	}
}

func TestAcceptBatch_InputCountBound(t *testing.T) {
	storage := batchStorage(t)
	admitN(t, storage, batchDomain, constants.MaxBatchInputs+1, 0)

	err := submitBatch(t, storage, submitHeight0, batchSpec{domain: batchDomain, first: 0, last: constants.MaxBatchInputs})
	expectErr(t, "65 inputs", err, constants.ErrForbiddenParam)
	common.FailIfErr(t, submitBatch(t, storage, submitHeight0, batchSpec{domain: batchDomain, first: 0, last: constants.MaxBatchInputs - 1}))
	if got := cursorOf(t, storage, batchDomain); got.ConsumedInputCount != constants.MaxBatchInputs {
		t.Fatalf("unexpected cursor %+v", got)
	}
}

func TestAcceptBatch_FirstBatchNeedsZeroPreStateRoot(t *testing.T) {
	storage := batchStorage(t)
	admitN(t, storage, batchDomain, 1, 0)
	err := submitBatch(t, storage, submitHeight0, batchSpec{domain: batchDomain, first: 0, last: 0, pre: rootOf(0x01)})
	expectErr(t, "non-zero first preStateRoot", err, constants.ErrForbiddenParam)
}

func TestAcceptBatch_NoApprovedDAModes(t *testing.T) {
	storage := batchStorage(t)
	common.FailIfErr(t, (&definition.ApprovedDAModeSet{DomainId: batchDomain}).Save(storage))
	admitN(t, storage, batchDomain, 1, 0)
	err := submitBatch(t, storage, submitHeight0, batchSpec{domain: batchDomain, first: 0, last: 0})
	expectErr(t, "no approved modes", err, constants.ErrForbiddenParam)
}

func TestAcceptBatch_PauseScopes(t *testing.T) {
	spec := batchSpec{domain: batchDomain, first: 0, last: 0}
	for _, c := range []struct {
		name  string
		pause func(storage db.DB)
		want  error
	}{
		{"global submit", func(s db.DB) { common.FailIfErr(t, (&definition.PauseState{SubmitPaused: true}).SaveGlobal(s)) }, constants.ErrSettlementSubmitPaused},
		{"domain submit", func(s db.DB) {
			common.FailIfErr(t, (&definition.PauseState{SubmitPaused: true}).SaveDomain(s, batchDomain))
		}, constants.ErrSettlementSubmitPaused},
		{"global relay", func(s db.DB) { common.FailIfErr(t, (&definition.PauseState{RelayPaused: true}).SaveGlobal(s)) }, nil},
	} {
		storage := batchStorage(t)
		admitN(t, storage, batchDomain, 1, 0)
		c.pause(storage)
		err := submitBatch(t, storage, submitHeight0, spec)
		if c.want == nil {
			common.FailIfErr(t, err)
		} else {
			expectErr(t, c.name, err, c.want)
		}
	}
}

func TestAcceptBatch_PausedDomainStillBatches(t *testing.T) {
	storage := batchStorage(t)
	admitN(t, storage, batchDomain, 1, 0)
	common.FailIfErr(t, (&definition.DomainRecord{DomainRecordKey: definition.DomainRecordKey{DomainId: batchDomain}, DomainClass: 1, Status: definition.DomainStatusPaused}).Save(storage))
	common.FailIfErr(t, submitBatch(t, storage, submitHeight0, batchSpec{domain: batchDomain, first: 0, last: 0}))
}

func TestAcceptBatch_RejectsUnentitledCaller(t *testing.T) {
	storage := batchStorage(t)
	admitN(t, storage, batchDomain, 1, 0)
	entry, _, _, err := decodeBatchCommitment(encodeCommitment(batchSpec{domain: batchDomain, first: 0, last: 0}))
	common.FailIfErr(t, err)

	expectErr(t, "other caller", acceptBatch(storage, submitHeight0, feeTestPillars(), feeTestAddress(0xE2), entry), constants.ErrPermissionDenied)
	expectErr(t, "pillar not active", acceptBatch(storage, submitHeight0, []*definition.PillarInfo{{Name: "pillar-b"}}, batchExecutor, entry), constants.ErrPermissionDenied)
}

func TestAcceptBatch_ReplacedExecutorChainsFromLastRoot(t *testing.T) {
	storage := batchStorage(t)
	admitN(t, storage, batchDomain, 3, 0)
	common.FailIfErr(t, submitBatch(t, storage, submitHeight0, batchSpec{domain: batchDomain, first: 0, last: 0, post: rootOf(0x51)}))

	newExecutor := feeTestAddress(0xE3)
	common.FailIfErr(t, (&definition.ExecutorSetMember{PillarName: "pillar-a", ExecutorAddress: batchExecutor, Status: definition.ExecutorStatusLapsed}).Save(storage))
	common.FailIfErr(t, (&definition.ExecutorSetMember{PillarName: "pillar-b", ExecutorAddress: newExecutor, Status: definition.ExecutorStatusActive}).Save(storage))

	next := batchSpec{domain: batchDomain, batchId: 1, first: 1, last: 1, pre: rootOf(0x51), executor: newExecutor}
	entry, _, _, err := decodeBatchCommitment(encodeCommitment(next))
	common.FailIfErr(t, err)
	expectErr(t, "old executor", acceptBatch(storage, submitHeight0+1, feeTestPillars(), batchExecutor, entry), constants.ErrPermissionDenied)

	next.pre = rootOf(0x99)
	wrong, _, _, err := decodeBatchCommitment(encodeCommitment(next))
	common.FailIfErr(t, err)
	expectErr(t, "wrong pre root", acceptBatch(storage, submitHeight0+1, feeTestPillars(), newExecutor, wrong), constants.ErrForbiddenParam)

	common.FailIfErr(t, acceptBatch(storage, submitHeight0+1, feeTestPillars(), newExecutor, entry))
	stored, err := definition.GetBatchEntry(storage, batchDomain, 1)
	common.FailIfErr(t, err)
	if stored.ExecutorPillar != "pillar-b" {
		t.Fatalf("got pillar %q want pillar-b", stored.ExecutorPillar)
	}
}

func TestAcceptBatch_ConfigChangeKeepsFinalizeAtHeight(t *testing.T) {
	storage := batchStorage(t)
	admitN(t, storage, batchDomain, 2, 0)
	common.FailIfErr(t, submitBatch(t, storage, submitHeight0, batchSpec{domain: batchDomain, first: 0, last: 0}))
	common.FailIfErr(t, (&definition.DomainConfig{DomainId: batchDomain, WithdrawalDelay: 500}).Save(storage))
	entry, err := definition.GetBatchEntry(storage, batchDomain, 0)
	common.FailIfErr(t, err)
	if entry.FinalizeAtHeight != submitHeight0+batchDelay {
		t.Fatalf("got FinalizeAtHeight %d", entry.FinalizeAtHeight)
	}
}

// submitSingles accepts n one-input batches for domain, batchId from first.
func submitSingles(t *testing.T, storage db.DB, domain uint32, n int, height uint64) {
	t.Helper()
	cursor := cursorOf(t, storage, domain)
	for i := 0; i < n; i++ {
		b := cursor.NextBatchId
		pre := types.ZeroHash
		if b > 0 {
			pre = rootOf(byte(0x50 + b - 1))
		}
		first := cursor.ConsumedInputCount
		o, err := definition.GetDomainInputOrdinal(storage, domain, first)
		common.FailIfErr(t, err)
		common.FailIfErr(t, submitBatch(t, storage, height, batchSpec{domain: domain, batchId: b, first: o.GlobalInputIndex, last: o.GlobalInputIndex, pre: pre, post: rootOf(byte(0x50 + b))}))
		cursor = cursorOf(t, storage, domain)
	}
}

func TestSweep_DeletesTwoFinalizedBatchesPerSubmit(t *testing.T) {
	storage := batchStorage(t)
	admitN(t, storage, batchDomain, 5, 0)
	admitN(t, storage, otherDomain, 1, 0)
	submitSingles(t, storage, batchDomain, 3, submitHeight0)

	submitSingles(t, storage, batchDomain, 1, submitHeight0+batchDelay)
	got := cursorOf(t, storage, batchDomain)
	if got.RecordsSweptThrough != 2 || got.SweptInputCount != 2 || got.NextBatchId != 4 {
		t.Fatalf("unexpected cursor %+v", got)
	}
	for ordinal := uint64(0); ordinal < 2; ordinal++ {
		if record, entry := hasInput(t, storage, batchDomain, ordinal, ordinal); record || entry {
			t.Fatalf("ordinal %d not swept", ordinal)
		}
	}
	for ordinal := uint64(2); ordinal < 4; ordinal++ {
		if record, entry := hasInput(t, storage, batchDomain, ordinal, ordinal); !record || !entry {
			t.Fatalf("ordinal %d swept", ordinal)
		}
	}
	if record, entry := hasInput(t, storage, otherDomain, 0, 5); !record || !entry {
		t.Fatal("other domain's input swept")
	}

	admitN(t, storage, batchDomain, 1, 0)
	submitSingles(t, storage, batchDomain, 1, submitHeight0+batchDelay)
	got = cursorOf(t, storage, batchDomain)
	if got.RecordsSweptThrough != 3 || got.SweptInputCount != 3 {
		t.Fatalf("expected batch 2 swept and batch 3 kept, got %+v", got)
	}
}

func TestSweep_WaitsForFinality(t *testing.T) {
	storage := batchStorage(t)
	admitN(t, storage, batchDomain, 3, 0)
	submitSingles(t, storage, batchDomain, 2, submitHeight0)

	submitSingles(t, storage, batchDomain, 1, submitHeight0+batchDelay-1)
	got := cursorOf(t, storage, batchDomain)
	if got.RecordsSweptThrough != 0 || got.SweptInputCount != 0 {
		t.Fatalf("expected nothing swept, got %+v", got)
	}
	if record, entry := hasInput(t, storage, batchDomain, 0, 0); !record || !entry {
		t.Fatal("input 0 deleted before its batch finalized")
	}
}

func TestSweep_StopsAtUnfinalizedHead(t *testing.T) {
	storage := batchStorage(t)
	admitN(t, storage, batchDomain, 3, 0)
	submitSingles(t, storage, batchDomain, 1, submitHeight0)
	common.FailIfErr(t, (&definition.DomainConfig{DomainId: batchDomain, WithdrawalDelay: 1}).Save(storage))
	submitSingles(t, storage, batchDomain, 1, submitHeight0+2)

	// batch 1 finalizes at submitHeight0+3 while batch 0 finalizes at +10
	common.FailIfErr(t, (&definition.DomainConfig{DomainId: batchDomain, WithdrawalDelay: batchDelay}).Save(storage))
	submitSingles(t, storage, batchDomain, 1, submitHeight0+5)
	got := cursorOf(t, storage, batchDomain)
	if got.RecordsSweptThrough != 0 || got.SweptInputCount != 0 {
		t.Fatalf("expected nothing swept, got %+v", got)
	}
	if record, entry := hasInput(t, storage, batchDomain, 1, 1); !record || !entry {
		t.Fatal("input 1 deleted while batch 0 was unfinalized")
	}
}

func TestSweep_RejectedSubmitSweepsNothing(t *testing.T) {
	storage := batchStorage(t)
	admitN(t, storage, batchDomain, 2, 0)
	submitSingles(t, storage, batchDomain, 1, submitHeight0)

	err := submitBatch(t, storage, submitHeight0+batchDelay, batchSpec{domain: batchDomain, batchId: 1, first: 1, last: 1, pre: rootOf(0x99)})
	expectErr(t, "wrong pre root", err, constants.ErrForbiddenParam)
	if got := cursorOf(t, storage, batchDomain); got.RecordsSweptThrough != 0 {
		t.Fatalf("unexpected cursor %+v", got)
	}
	if record, entry := hasInput(t, storage, batchDomain, 0, 0); !record || !entry {
		t.Fatal("input 0 deleted by a rejected submit")
	}
}

func TestSweep_MultiInputBatchesAmongInterleavedDomains(t *testing.T) {
	storage := batchStorage(t)
	// globals: A0 B1 A2 A3 B4 A5 A6
	for _, domain := range []uint32{batchDomain, otherDomain, batchDomain, batchDomain, otherDomain, batchDomain, batchDomain} {
		admitN(t, storage, domain, 1, 0)
	}
	common.FailIfErr(t, submitBatch(t, storage, submitHeight0, batchSpec{domain: batchDomain, first: 0, last: 2, post: rootOf(0x50)}))
	common.FailIfErr(t, submitBatch(t, storage, submitHeight0, batchSpec{domain: batchDomain, batchId: 1, first: 3, last: 5, pre: rootOf(0x50), post: rootOf(0x51)}))

	// both batches finalize at submitHeight0+batchDelay, so the sweep deletes their four inputs.
	common.FailIfErr(t, submitBatch(t, storage, submitHeight0+batchDelay, batchSpec{domain: batchDomain, batchId: 2, first: 6, last: 6, pre: rootOf(0x51)}))
	got := cursorOf(t, storage, batchDomain)
	if got.RecordsSweptThrough != 2 || got.SweptInputCount != 4 {
		t.Fatalf("unexpected cursor %+v", got)
	}
	for ordinal, global := range []uint64{0, 2, 3, 5, 6} {
		record, entry := hasInput(t, storage, batchDomain, uint64(ordinal), global)
		if ordinal < 4 && (record || entry) {
			t.Fatalf("ordinal %d (global %d) not swept", ordinal, global)
		}
		if ordinal == 4 && (!record || !entry) {
			t.Fatalf("ordinal %d (global %d) swept", ordinal, global)
		}
	}
	for ordinal, global := range []uint64{1, 4} {
		if record, entry := hasInput(t, storage, otherDomain, uint64(ordinal), global); !record || !entry {
			t.Fatalf("other domain input %d swept", global)
		}
	}
}

func TestAcceptBatch_RejectionsLeaveSweepableStateUntouched(t *testing.T) {
	setup := func() db.DB {
		storage := batchStorage(t)
		admitN(t, storage, batchDomain, 3, 0)
		submitSingles(t, storage, batchDomain, 2, submitHeight0)
		return storage
	}
	valid := batchSpec{domain: batchDomain, batchId: 2, first: 2, last: 2, pre: rootOf(0x51)}
	height := submitHeight0 + batchDelay

	for _, c := range []struct {
		name string
		run  func(storage db.DB) error
		want error
	}{
		{"submit paused", func(s db.DB) error {
			common.FailIfErr(t, (&definition.PauseState{SubmitPaused: true}).SaveDomain(s, batchDomain))
			return submitBatch(t, s, height, valid)
		}, constants.ErrSettlementSubmitPaused},
		{"unentitled caller", func(s db.DB) error {
			entry, _, _, err := decodeBatchCommitment(encodeCommitment(valid))
			common.FailIfErr(t, err)
			return acceptBatch(s, height, feeTestPillars(), feeTestAddress(0xE2), entry)
		}, constants.ErrPermissionDenied},
		{"unapproved DA mode", func(s db.DB) error {
			spec := valid
			spec.daMode = 1
			return submitBatch(t, s, height, spec)
		}, constants.ErrForbiddenParam},
		{"overlapping inputs", func(s db.DB) error {
			spec := valid
			spec.first = 1
			return submitBatch(t, s, height, spec)
		}, constants.ErrForbiddenParam},
	} {
		storage := setup()
		before := cursorOf(t, storage, batchDomain)
		expectErr(t, c.name, c.run(storage), c.want)
		if got := cursorOf(t, storage, batchDomain); got != before {
			t.Fatalf("%s: cursor changed to %+v", c.name, got)
		}
		if record, entry := hasInput(t, storage, batchDomain, 0, 0); !record || !entry {
			t.Fatalf("%s: input 0 swept", c.name)
		}
	}
}
