package implementation

import (
	"bytes"
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
