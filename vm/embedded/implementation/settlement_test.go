package implementation

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/crypto"
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

// ============================================================================
// RelayMessage registration and withdrawal release
// ============================================================================

const (
	relayDomain = uint32(1)
	relayBatch  = uint64(0)
	// relayFinalizeAt is the batch's FinalizeAtHeight in the fixtures
	// below.
	relayFinalizeAt = uint64(100)
)

var (
	relayRecipientA = feeTestAddress(0xC1)
	relayRecipientB = feeTestAddress(0xC2)
)

// wmsg describes one outbox message. A nil kind is encoded as 1.
type wmsg struct {
	source     types.Hash
	input      uint64
	outbox     uint32
	kind       *byte
	recipient  types.Address
	asset      types.Hash
	amount     *big.Int
	payloadLen uint32
}

func kindOf(k byte) *byte { return &k }

func (m wmsg) encode() []byte {
	out := append([]byte{}, m.source[:]...)
	out = binary.BigEndian.AppendUint64(out, m.input)
	out = binary.BigEndian.AppendUint32(out, m.outbox)
	kind := byte(1)
	if m.kind != nil {
		kind = *m.kind
	}
	out = append(out, kind)
	out = append(out, m.recipient[:]...)
	asset := m.asset
	if asset == types.ZeroHash {
		asset = znnAssetId()
	}
	out = append(out, asset[:]...)
	var word [32]byte
	amount := m.amount
	if amount == nil {
		amount = big.NewInt(1)
	}
	amount.FillBytes(word[:])
	out = append(out, word[:]...)
	return binary.BigEndian.AppendUint32(out, m.payloadLen)
}

// contractMsg is a contract-emitted withdrawal of amount ZNN.
func contractMsg(input uint64, outbox uint32, to types.Address, amount int64) wmsg {
	return wmsg{source: deployTestHash(0x77), input: input, outbox: outbox, recipient: to, amount: big.NewInt(amount)}
}

// refundMsg is a runtime-emitted refund (zero source contract).
func refundMsg(input uint64, outbox uint32, to types.Address, amount int64) wmsg {
	return wmsg{input: input, outbox: outbox, recipient: to, amount: big.NewInt(amount)}
}

// testTree builds the SPEC 27.3 tree level by level: leaf
// SHA3-256(0x00 || element), node SHA3-256(0x01 || left || right), an
// odd trailing node promoted unchanged. It shares no code with
// outboxProofRoot.
type testTree struct{ levels [][][]byte }

func buildTestTree(elements [][]byte) *testTree {
	level := make([][]byte, len(elements))
	for i, e := range elements {
		level[i] = crypto.Hash([]byte{0x00}, e)
	}
	tree := &testTree{levels: [][][]byte{level}}
	for len(level) > 1 {
		var next [][]byte
		i := 0
		for ; i+1 < len(level); i += 2 {
			next = append(next, crypto.Hash([]byte{0x01}, level[i], level[i+1]))
		}
		if i < len(level) {
			next = append(next, level[i])
		}
		tree.levels = append(tree.levels, next)
		level = next
	}
	return tree
}

func (tr *testTree) root() types.Hash {
	return types.BytesToHashPanic(tr.levels[len(tr.levels)-1][0])
}

// proof returns the encoded proof of leaf i: a promoted node adds no
// step.
func (tr *testTree) proof(i int) []byte {
	var steps []byte
	count := uint32(0)
	idx := i
	for _, level := range tr.levels[:len(tr.levels)-1] {
		if idx%2 == 1 {
			steps = append(steps, 0)
			steps = append(steps, level[idx-1]...)
			count++
		} else if idx+1 < len(level) {
			steps = append(steps, 1)
			steps = append(steps, level[idx+1]...)
			count++
		}
		idx /= 2
	}
	return append(binary.BigEndian.AppendUint32(nil, count), steps...)
}

func encodeAll(msgs ...wmsg) [][]byte {
	out := make([][]byte, len(msgs))
	for i, m := range msgs {
		out[i] = m.encode()
	}
	return out
}

func proofOf(count uint32, steps ...[]byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, count)
	for _, st := range steps {
		out = append(out, st...)
	}
	return out
}

func step(side byte, sibling types.Hash) []byte { return append([]byte{side}, sibling[:]...) }

var conformanceInputRootElements = []string{
	"001010101010101010101010101010101010101010101010101010101010101010111111111111111111111111111111111111111111111111111111111111111100000000000000640000000000000000000000000000000000000000000014e66318c6318c6318c600000000000000000000000000000000000000000000000000000000000000010000000463616c6c",
	"011212121212121212121212121212121212121212121212121212121212121212131313131313131313131313131313131313131313131313131313131313131300000000000000c80000000000000000000000000000000000000000000004066318c6318c6318c60000000000000000000000000000000000000000000000000000000000000002",
	"02141414141414141414141414141414141414141414141414141414141414141415151515151515151515151515151515151515151515151515151515151515153b552fdc9b613ed4cc2668f65e6ede473caa412b1506deb0706848e7613ffa4f000000000000100000000002000000170001000000000000000000000000000000000000000000",
	"0317171717171717171717171717171717171717171717171717171717171717171818181818181818181818181818181818181818181818181818181818181818000000001919191919191919191919191919191919191919191919191919191919191919",
	"041a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b",
	"051c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e",
	"061f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f20202020202020202020202020202020202020202020202020202020202020202121212121212121212121212121212121212121",
	"07222222222222222222222222222222222222222222222222222222222222222223232323232323232323232323232323232323232323232323232323232323230000000000001000",
}

const conformanceInputRoot = "47f19e1e068fa6f6ba7f62688536a8956f4d8d865a4e161e5e024e0f5ec3ad09"

// conformanceOutboxRejected is a withdrawal with a one-byte payload.
const conformanceOutboxRejected = "30303030303030303030303030303030303030303030303030303030303030300000000000000000000000000101000000000000000000000000000000000000000000000000000000000000000000000000000000000014e66318c6318c6318c6000000000000000000000000000000000000000000000000000000000000000100000001ff"

func conformanceElements(t *testing.T) [][]byte {
	t.Helper()
	out := make([][]byte, len(conformanceInputRootElements))
	for i, h := range conformanceInputRootElements {
		out[i] = mustHex(t, h)
	}
	return out
}

func TestOutboxTree_ReproducesConformanceRoot(t *testing.T) {
	elements := conformanceElements(t)
	tree := buildTestTree(elements)
	if got := hex.EncodeToString(tree.root().Bytes()); got != conformanceInputRoot {
		t.Fatalf("tree builder root: got %s want %s", got, conformanceInputRoot)
	}
	want := types.HexToHashPanic(conformanceInputRoot)
	for i, e := range elements {
		got, err := outboxProofRoot(e, tree.proof(i))
		common.FailIfErr(t, err)
		if got != want {
			t.Fatalf("leaf %d: proof root %s want %s", i, got, want)
		}
	}
}

func TestOutboxProofRoot_AllLeavesOfSmallTrees(t *testing.T) {
	for _, n := range []int{1, 2, 3, 4, 5, 7, 9} {
		msgs := make([]wmsg, n)
		for i := range msgs {
			msgs[i] = contractMsg(uint64(i), 0, relayRecipientA, int64(i+1))
		}
		elements := encodeAll(msgs...)
		tree := buildTestTree(elements)
		for i := range elements {
			got, err := outboxProofRoot(elements[i], tree.proof(i))
			common.FailIfErr(t, err)
			if got != tree.root() {
				t.Fatalf("%d leaves, leaf %d: wrong root", n, i)
			}
		}
	}
}

func TestOutboxProofRoot_SingleLeafHasNoSteps(t *testing.T) {
	leaf := contractMsg(0, 0, relayRecipientA, 5).encode()
	tree := buildTestTree([][]byte{leaf})
	if !bytes.Equal(tree.proof(0), []byte{0, 0, 0, 0}) {
		t.Fatalf("expected a zero-step proof, got %x", tree.proof(0))
	}
	got, err := outboxProofRoot(leaf, tree.proof(0))
	common.FailIfErr(t, err)
	if !bytes.Equal(got.Bytes(), crypto.Hash([]byte{0x00}, leaf)) {
		t.Fatal("a single-leaf root must equal the leaf hash")
	}
}

func TestOutboxTree_PromotedNodesContributeNoStep(t *testing.T) {
	// 3 leaves: leaf 2 is promoted past level 0 and has one step.
	three := buildTestTree(encodeAll(
		contractMsg(0, 0, relayRecipientA, 1), contractMsg(1, 0, relayRecipientA, 1), contractMsg(2, 0, relayRecipientA, 1)))
	for i, want := range []uint32{2, 2, 1} {
		if got := binary.BigEndian.Uint32(three.proof(i)[:4]); got != want {
			t.Fatalf("3 leaves, leaf %d: step count %d want %d", i, got, want)
		}
	}
	// 5 leaves: leaf 4 is promoted at levels 0 and 1 and has one step.
	msgs := make([]wmsg, 5)
	for i := range msgs {
		msgs[i] = contractMsg(uint64(i), 0, relayRecipientA, 1)
	}
	five := buildTestTree(encodeAll(msgs...))
	for i, want := range []uint32{3, 3, 3, 3, 1} {
		if got := binary.BigEndian.Uint32(five.proof(i)[:4]); got != want {
			t.Fatalf("5 leaves, leaf %d: step count %d want %d", i, got, want)
		}
	}
}

func TestOutboxProofRoot_Rejects(t *testing.T) {
	msgs := make([]wmsg, 5)
	for i := range msgs {
		msgs[i] = contractMsg(uint64(i), 0, relayRecipientA, int64(i+1))
	}
	elements := encodeAll(msgs...)
	tree := buildTestTree(elements)
	good := tree.proof(1)

	flipSide := append([]byte{}, good...)
	flipSide[4] ^= 1
	wrongSibling := append([]byte{}, good...)
	wrongSibling[5] ^= 0xFF
	badSide := append([]byte{}, good...)
	badSide[4] = 2
	manySteps := make([][]byte, 33)
	for i := range manySteps {
		manySteps[i] = step(0, deployTestHash(byte(i)))
	}

	for _, c := range []struct {
		name  string
		proof []byte
	}{
		{"empty", nil},
		{"short count", []byte{0, 0, 0}},
		{"trailing byte", append(append([]byte{}, good...), 0)},
		{"truncated step", good[:len(good)-1]},
		{"count above length", proofOf(5, step(0, rootOf(1)))},
		{"count below length", proofOf(1, step(0, rootOf(1)), step(0, rootOf(2)))},
		{"side 2", badSide},
		{"count 33 with 33 steps", proofOf(33, manySteps...)},
		{"max count with no steps", proofOf(math.MaxUint32)},
	} {
		if _, err := outboxProofRoot(elements[1], c.proof); err != constants.ErrForbiddenParam {
			t.Fatalf("%s: got %v want ErrForbiddenParam", c.name, err)
		}
	}

	for _, c := range []struct {
		name  string
		leaf  []byte
		proof []byte
	}{
		{"flipped side", elements[1], flipSide},
		{"wrong sibling", elements[1], wrongSibling},
		{"proof of another leaf", elements[1], tree.proof(2)},
		{"other message", elements[2], good},
		{"leaf hash in place of the message", crypto.Hash([]byte{0x00}, elements[1]), good},
	} {
		got, err := outboxProofRoot(c.leaf, c.proof)
		common.FailIfErr(t, err)
		if got == tree.root() {
			t.Fatalf("%s: verified against the real root", c.name)
		}
	}
}

func TestOutboxProofRoot_CountBoundaries(t *testing.T) {
	steps := make([][]byte, 32)
	for i := range steps {
		steps[i] = step(byte(i%2), deployTestHash(byte(i)))
	}
	if _, err := outboxProofRoot([]byte("leaf"), proofOf(32, steps...)); err != nil {
		t.Fatalf("32 steps are read: %v", err)
	}
	// The count is rejected before any step is read, so the error is the
	// same whether or not 33 well-formed steps follow.
	if _, err := outboxProofRoot([]byte("leaf"), proofOf(33, append(steps, step(0, rootOf(9)))...)); err != constants.ErrForbiddenParam {
		t.Fatalf("33 steps: got %v want ErrForbiddenParam", err)
	}
}

func TestOutboxId(t *testing.T) {
	var pre []byte
	pre = binary.BigEndian.AppendUint32(pre, 7)
	pre = binary.BigEndian.AppendUint64(pre, 1234)
	pre = binary.BigEndian.AppendUint64(pre, 5678)
	pre = binary.BigEndian.AppendUint32(pre, 9)
	if len(pre) != 24 {
		t.Fatalf("preimage length %d", len(pre))
	}
	if got := outboxId(7, 1234, 5678, 9); !bytes.Equal(got.Bytes(), crypto.Hash(pre)) {
		t.Fatalf("outboxId mismatch: %s", got)
	}
	seen := map[types.Hash]bool{}
	for _, id := range []types.Hash{outboxId(7, 1234, 5678, 9), outboxId(8, 1234, 5678, 9), outboxId(7, 1235, 5678, 9), outboxId(7, 1234, 5679, 9), outboxId(7, 1234, 5678, 10)} {
		if seen[id] {
			t.Fatal("outboxId must change with each field")
		}
		seen[id] = true
	}
}

func TestDecodeOutboxWithdrawal_DecodesFields(t *testing.T) {
	m := wmsg{
		source: deployTestHash(0x55), input: 0x0102030405060708, outbox: 0x0A0B0C0D,
		recipient: relayRecipientA, asset: qsrAssetId(),
		amount: new(big.Int).Lsh(big.NewInt(3), 130),
	}
	raw := m.encode()
	if len(raw) != 133 {
		t.Fatalf("encoded length %d", len(raw))
	}
	w, err := decodeOutboxWithdrawal(raw)
	common.FailIfErr(t, err)
	if w.SourceContract != m.source || w.InputIndex != m.input || w.OutboxIndex != m.outbox ||
		w.Recipient != m.recipient || w.AssetId != m.asset || w.Amount.Cmp(m.amount) != 0 {
		t.Fatalf("decoded fields mismatch: %+v", w)
	}
}

func TestDecodeOutboxWithdrawal_Rejects(t *testing.T) {
	good := contractMsg(1, 2, relayRecipientA, 5)
	nonCanonical := znnAssetId()
	nonCanonical[0] = 1
	zeroAmount := good
	zeroAmount.amount = big.NewInt(0)
	nonCanonicalMsg := good
	nonCanonicalMsg.asset = nonCanonical
	payload := good
	payload.payloadLen = 1
	toSettlement := good
	toSettlement.recipient = types.SettlementContract
	toToken := good
	toToken.recipient = types.TokenContract
	toPlasma := good
	toPlasma.recipient = types.PlasmaContract
	short := good.encode()[:132]
	long := append(good.encode(), 0)
	kind0, kind2 := good, good
	kind0.kind, kind2.kind = kindOf(0), kindOf(2)

	for _, c := range []struct {
		name string
		raw  []byte
	}{
		{"132 bytes", short},
		{"134 bytes", long},
		{"empty", nil},
		{"kind 0", kind0.encode()},
		{"kind 2", kind2.encode()},
		{"payload length 1", payload.encode()},
		{"non-canonical asset", nonCanonicalMsg.encode()},
		{"zero amount", zeroAmount.encode()},
		{"settlement recipient", toSettlement.encode()},
		{"token recipient", toToken.encode()},
		{"plasma recipient", toPlasma.encode()},
		{"conformance vector with payload", mustHex(t, conformanceOutboxRejected)},
	} {
		if _, err := decodeOutboxWithdrawal(c.raw); err != constants.ErrForbiddenParam {
			t.Fatalf("%s: got %v want ErrForbiddenParam", c.name, err)
		}
	}
}

func TestDecodeOutboxWithdrawal_ConformanceVectorFields(t *testing.T) {
	raw := mustHex(t, conformanceOutboxRejected)
	raw = append(raw[:129:129], 0, 0, 0, 0)
	if len(raw) != 133 {
		t.Fatalf("length %d", len(raw))
	}
	// The vector's recipient starts with the embedded-contract byte, so
	// the payload fix alone still leaves the message unregistrable.
	if _, err := decodeOutboxWithdrawal(raw); err != constants.ErrForbiddenParam {
		t.Fatalf("embedded recipient: got %v want ErrForbiddenParam", err)
	}
	raw[45] = types.UserAddrByte
	w, err := decodeOutboxWithdrawal(raw)
	common.FailIfErr(t, err)
	wantRecipient := types.Address{}
	if w.SourceContract != types.HexToHashPanic(strings.Repeat("30", 32)) || w.InputIndex != 0 || w.OutboxIndex != 0 ||
		w.Recipient != wantRecipient || w.AssetId != znnAssetId() || w.Amount.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("decoded fields mismatch: %+v", w)
	}
}

func TestDepositCreditOf(t *testing.T) {
	summary := flowSummary(flowEntry(conformanceAsset, 7, 0), flowEntry(znnAssetId(), 150, 140))
	got, err := depositCreditOf(summary, znnAssetId())
	common.FailIfErr(t, err)
	expectBig(t, "znn credit", got, 150)
	got, err = depositCreditOf(summary, conformanceAsset)
	common.FailIfErr(t, err)
	expectBig(t, "second asset credit", got, 7)
	got, err = depositCreditOf(summary, rootOf(0x09))
	common.FailIfErr(t, err)
	expectBig(t, "unlisted asset", got, 0)
	got, err = depositCreditOf(flowSummary(), znnAssetId())
	common.FailIfErr(t, err)
	expectBig(t, "empty summary", got, 0)
	if _, err := depositCreditOf([]byte{1}, znnAssetId()); err != constants.ErrSettlementInvalidAssetFlow {
		t.Fatalf("malformed summary: got %v", err)
	}
}

// relayFixture holds a finalized batch whose outbox is the given
// messages.
type relayFixture struct {
	storage  db.DB
	elements [][]byte
	tree     *testTree
}

// newRelayFixture stores batch (relayDomain, relayBatch), finalized at
// relayFinalizeAt, with deposit credit 150 and withdrawal debit 140 for
// ZNN. ZNN has a per-batch cap of maxBatch and totalDeposited in
// custody.
func newRelayFixture(t *testing.T, maxBatch, totalDeposited int64, msgs ...wmsg) *relayFixture {
	t.Helper()
	storage := feeTestStorage()
	elements := encodeAll(msgs...)
	tree := buildTestTree(elements)
	common.FailIfErr(t, (&definition.BatchEntry{
		BatchEntryKey:    definition.BatchEntryKey{DomainId: relayDomain, BatchId: relayBatch},
		OutboxRoot:       tree.root(),
		FinalizeAtHeight: relayFinalizeAt,
		AssetFlowSummary: flowSummary(flowEntry(znnAssetId(), 150, 140)),
	}).Save(storage))
	common.FailIfErr(t, (&definition.AssetConfig{DomainId: relayDomain, AssetId: znnAssetId(), MaxBatchWithdrawal: big.NewInt(maxBatch)}).Save(storage))
	seedCounters(t, storage, relayDomain, znnAssetId(), totalDeposited, 0, 0)
	return &relayFixture{storage: storage, elements: elements, tree: tree}
}

func seedCounters(t *testing.T, storage db.DB, domain uint32, asset types.Hash, deposited, reserve, released int64) {
	t.Helper()
	common.FailIfErr(t, (&definition.ConservationCounters{
		DomainId: domain, AssetId: asset,
		TotalDeposited: big.NewInt(deposited), PendingWithdrawalReserve: big.NewInt(reserve), TotalReleased: big.NewInt(released),
	}).Save(storage))
}

func (f *relayFixture) relay(t *testing.T, height uint64, i int) error {
	t.Helper()
	return registerWithdrawal(f.storage, height, relayDomain, relayBatch, f.elements[i], f.tree.proof(i))
}

func countersOf(t *testing.T, storage db.DB, domain uint32, asset types.Hash) (deposited, reserve, released int64) {
	t.Helper()
	c, err := definition.GetConservationCounters(storage, domain, asset)
	common.FailIfErr(t, err)
	return c.TotalDeposited.Int64(), c.PendingWithdrawalReserve.Int64(), c.TotalReleased.Int64()
}

// relayState snapshots everything a registration can write.
func (f *relayFixture) relayState(t *testing.T) string {
	t.Helper()
	d, r, rel := countersOf(t, f.storage, relayDomain, znnAssetId())
	meta, err := definition.GetReleaseQueueMeta(f.storage, relayDomain)
	common.FailIfErr(t, err)
	total, err := definition.GetBatchWithdrawalTotal(f.storage, relayDomain, relayBatch, znnAssetId())
	common.FailIfErr(t, err)
	processed := ""
	for _, m := range f.elements {
		w, err := decodeOutboxWithdrawal(m)
		if err != nil {
			continue
		}
		ok, err := definition.IsOutboxProcessed(f.storage, outboxId(relayDomain, relayBatch, w.InputIndex, w.OutboxIndex))
		common.FailIfErr(t, err)
		if ok {
			processed += "1"
		} else {
			processed += "0"
		}
	}
	return fmt.Sprintf("%d/%d/%d head=%d tail=%d contract=%v refund=%v processed=%s",
		d, r, rel, meta.Head, meta.Tail, total.ContractAmount, total.RefundAmount, processed)
}

func (f *relayFixture) expectRejected(t *testing.T, label string, height uint64, i int, want error) {
	t.Helper()
	before := f.relayState(t)
	expectErr(t, label, f.relay(t, height, i), want)
	if after := f.relayState(t); after != before {
		t.Fatalf("%s: rejected relay changed state: %s -> %s", label, before, after)
	}
}

func (f *relayFixture) totals(t *testing.T) (contract, refund int64) {
	t.Helper()
	total, err := definition.GetBatchWithdrawalTotal(f.storage, relayDomain, relayBatch, znnAssetId())
	common.FailIfErr(t, err)
	return total.ContractAmount.Int64(), total.RefundAmount.Int64()
}

func TestRegisterWithdrawal_RegistersAndEnqueues(t *testing.T) {
	f := newRelayFixture(t, 1000, 500, contractMsg(3, 4, relayRecipientA, 40))
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 0))

	_, reserve, released := countersOf(t, f.storage, relayDomain, znnAssetId())
	if reserve != 40 || released != 0 {
		t.Fatalf("counters: reserve %d released %d", reserve, released)
	}
	if contract, refund := f.totals(t); contract != 40 || refund != 0 {
		t.Fatalf("totals: %d/%d", contract, refund)
	}
	id := outboxId(relayDomain, relayBatch, 3, 4)
	processed, err := definition.IsOutboxProcessed(f.storage, id)
	common.FailIfErr(t, err)
	if !processed {
		t.Fatal("outboxId not recorded")
	}
	entry, err := definition.DequeueRelease(f.storage, relayDomain)
	common.FailIfErr(t, err)
	if entry.Recipient != relayRecipientA || entry.AssetId != znnAssetId() || entry.Amount.Int64() != 40 || entry.OutboxId != id {
		t.Fatalf("queue entry mismatch: %+v", entry)
	}
}

func TestRegisterWithdrawal_FinalityIsTheOnlyTimeGate(t *testing.T) {
	f := newRelayFixture(t, 1000, 500, contractMsg(0, 0, relayRecipientA, 1))
	f.expectRejected(t, "before finality", relayFinalizeAt-1, 0, constants.ErrForbiddenParam)
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 0))
}

func TestRegisterWithdrawal_RejectsMalformedMessage(t *testing.T) {
	f := newRelayFixture(t, 1000, 500, contractMsg(0, 0, relayRecipientA, 1))
	short := f.elements[0][:132]
	err := registerWithdrawal(f.storage, relayFinalizeAt, relayDomain, relayBatch, short, f.tree.proof(0))
	expectErr(t, "short message", err, constants.ErrForbiddenParam)
}

func TestRegisterWithdrawal_MissingBatch(t *testing.T) {
	f := newRelayFixture(t, 1000, 500, contractMsg(0, 0, relayRecipientA, 1))
	for _, c := range []struct {
		domain uint32
		batch  uint64
	}{{relayDomain, 1}, {2, relayBatch}, {99, 99}} {
		err := registerWithdrawal(f.storage, relayFinalizeAt, c.domain, c.batch, f.elements[0], f.tree.proof(0))
		expectErr(t, "missing batch", err, constants.ErrForbiddenParam)
	}
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 0))
}

func TestRegisterWithdrawal_ProofBoundToDomainAndBatch(t *testing.T) {
	f := newRelayFixture(t, 1000, 500, contractMsg(0, 0, relayRecipientA, 1))
	// Batches (2, 0) and (1, 1) exist with other roots.
	for _, key := range []definition.BatchEntryKey{{DomainId: 2, BatchId: 0}, {DomainId: relayDomain, BatchId: 1}} {
		common.FailIfErr(t, (&definition.BatchEntry{BatchEntryKey: key, OutboxRoot: rootOf(0x99), FinalizeAtHeight: 1}).Save(f.storage))
	}
	before := f.relayState(t)
	for _, key := range []definition.BatchEntryKey{{DomainId: 2, BatchId: 0}, {DomainId: relayDomain, BatchId: 1}} {
		err := registerWithdrawal(f.storage, relayFinalizeAt, key.DomainId, key.BatchId, f.elements[0], f.tree.proof(0))
		expectErr(t, "mismatched domain or batch", err, constants.ErrForbiddenParam)
	}
	if f.relayState(t) != before {
		t.Fatal("mismatched relay changed state")
	}
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 0))
}

func TestRegisterWithdrawal_RejectsBadProofs(t *testing.T) {
	f := newRelayFixture(t, 1000, 500,
		contractMsg(0, 0, relayRecipientA, 1), contractMsg(1, 0, relayRecipientA, 2), contractMsg(2, 0, relayRecipientA, 3))
	good := f.tree.proof(0)
	wrongSibling := append([]byte{}, good...)
	wrongSibling[10] ^= 1
	for _, c := range []struct {
		name  string
		proof []byte
	}{
		{"empty", nil},
		{"trailing byte", append(append([]byte{}, good...), 0)},
		{"wrong sibling", wrongSibling},
		{"proof of another leaf", f.tree.proof(1)},
		{"count 33", proofOf(33)},
		{"side 2", proofOf(1, step(2, rootOf(1)))},
	} {
		before := f.relayState(t)
		err := registerWithdrawal(f.storage, relayFinalizeAt, relayDomain, relayBatch, f.elements[0], c.proof)
		expectErr(t, c.name, err, constants.ErrForbiddenParam)
		if f.relayState(t) != before {
			t.Fatalf("%s: state changed", c.name)
		}
	}
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 0))
}

func TestRegisterWithdrawal_Replay(t *testing.T) {
	f := newRelayFixture(t, 1000, 500, contractMsg(0, 0, relayRecipientA, 40))
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 0))
	f.expectRejected(t, "replay", relayFinalizeAt+5, 0, constants.ErrSettlementOutboxProcessed)
	meta, err := definition.GetReleaseQueueMeta(f.storage, relayDomain)
	common.FailIfErr(t, err)
	if meta.Tail-meta.Head != 1 {
		t.Fatalf("expected one queued entry, got %d", meta.Tail-meta.Head)
	}
}

func TestRegisterWithdrawal_InertMessagesRecordNothing(t *testing.T) {
	embedded := contractMsg(0, 0, types.SettlementContract, 5)
	zero := contractMsg(1, 0, relayRecipientA, 0)
	kind0 := contractMsg(2, 0, relayRecipientA, 5)
	kind0.kind = kindOf(0)
	f := newRelayFixture(t, 1000, 500, embedded, zero, kind0)
	// The tree commits to the inert messages, so each proof is valid and
	// only the message rules reject them.
	for i, name := range []string{"embedded recipient", "zero amount", "kind 0"} {
		err := f.relay(t, relayFinalizeAt, i)
		expectErr(t, name, err, constants.ErrForbiddenParam)
	}
	if got := f.relayState(t); got != "500/0/0 head=0 tail=0 contract=0 refund=0 processed=" {
		t.Fatalf("inert relays changed state: %s", got)
	}
	for _, id := range []types.Hash{outboxId(relayDomain, relayBatch, 0, 0), outboxId(relayDomain, relayBatch, 1, 0), outboxId(relayDomain, relayBatch, 2, 0)} {
		processed, err := definition.IsOutboxProcessed(f.storage, id)
		common.FailIfErr(t, err)
		if processed {
			t.Fatal("an inert message recorded its outboxId")
		}
	}
}

func TestRegisterWithdrawal_ContractCap(t *testing.T) {
	f := newRelayFixture(t, 100, 500,
		contractMsg(0, 0, relayRecipientA, 60), contractMsg(1, 0, relayRecipientA, 40), contractMsg(2, 0, relayRecipientA, 1))
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 0))
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 1)) // exactly at the cap
	f.expectRejected(t, "one unit above the cap", relayFinalizeAt, 2, constants.ErrSettlementWithdrawalCapExceeded)
	if contract, _ := f.totals(t); contract != 100 {
		t.Fatalf("contract total %d", contract)
	}
}

func TestRegisterWithdrawal_RefundCapIsDepositCredit(t *testing.T) {
	// deposit_credit 150, withdrawal_debit 140. The refund cap is the
	// credit; the contract cap is far below it.
	f := newRelayFixture(t, 10, 500,
		refundMsg(0, 0, relayRecipientA, 100), refundMsg(1, 0, relayRecipientB, 50), refundMsg(2, 0, relayRecipientA, 1))
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 0)) // 100 > MaxBatchWithdrawal 10, still accepted
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 1)) // exactly 150
	f.expectRejected(t, "one unit above the credit", relayFinalizeAt, 2, constants.ErrSettlementWithdrawalCapExceeded)
	if contract, refund := f.totals(t); contract != 0 || refund != 150 {
		t.Fatalf("totals: %d/%d", contract, refund)
	}
}

func TestRegisterWithdrawal_RefundInUnlistedAssetHasZeroCap(t *testing.T) {
	m := refundMsg(0, 0, relayRecipientA, 1)
	m.asset = qsrAssetId()
	f := newRelayFixture(t, 1000, 500, m)
	seedCounters(t, f.storage, relayDomain, qsrAssetId(), 500, 0, 0)
	before := f.relayState(t)
	expectErr(t, "unlisted asset refund", f.relay(t, relayFinalizeAt, 0), constants.ErrSettlementWithdrawalCapExceeded)
	if f.relayState(t) != before {
		t.Fatal("state changed")
	}
}

func TestRegisterWithdrawal_ContractWithdrawalNeedsAssetConfig(t *testing.T) {
	m := contractMsg(0, 0, relayRecipientA, 1)
	m.asset = qsrAssetId()
	f := newRelayFixture(t, 1000, 500, m)
	f.expectRejected(t, "no asset config", relayFinalizeAt, 0, constants.ErrForbiddenParam)
}

func TestRegisterWithdrawal_RefundAndContractTotalsAreSeparate(t *testing.T) {
	f := newRelayFixture(t, 100, 500,
		contractMsg(0, 0, relayRecipientA, 100), refundMsg(1, 0, relayRecipientB, 150), contractMsg(2, 0, relayRecipientA, 1), refundMsg(3, 0, relayRecipientB, 1))
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 0))
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 1))
	if contract, refund := f.totals(t); contract != 100 || refund != 150 {
		t.Fatalf("totals: %d/%d", contract, refund)
	}
	f.expectRejected(t, "contract cap full", relayFinalizeAt, 2, constants.ErrSettlementWithdrawalCapExceeded)
	f.expectRejected(t, "refund cap full", relayFinalizeAt, 3, constants.ErrSettlementWithdrawalCapExceeded)
}

func TestRegisterWithdrawal_TotalsArePerBatchAndAsset(t *testing.T) {
	zeroCredit := wmsg{source: deployTestHash(0x77), input: 0, outbox: 0, recipient: relayRecipientA, amount: big.NewInt(100)}
	f := newRelayFixture(t, 100, 500, zeroCredit)
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 0))

	// A second batch with the same message shape starts from zero.
	second := encodeAll(contractMsg(5, 0, relayRecipientA, 100))
	tree2 := buildTestTree(second)
	common.FailIfErr(t, (&definition.BatchEntry{
		BatchEntryKey: definition.BatchEntryKey{DomainId: relayDomain, BatchId: 1},
		OutboxRoot:    tree2.root(), FinalizeAtHeight: 1, AssetFlowSummary: flowSummary(),
	}).Save(f.storage))
	common.FailIfErr(t, registerWithdrawal(f.storage, relayFinalizeAt, relayDomain, 1, second[0], tree2.proof(0)))

	// A second asset starts from zero too.
	qsr := contractMsg(6, 0, relayRecipientA, 100)
	qsr.asset = qsrAssetId()
	third := encodeAll(qsr)
	tree3 := buildTestTree(third)
	common.FailIfErr(t, (&definition.BatchEntry{
		BatchEntryKey: definition.BatchEntryKey{DomainId: relayDomain, BatchId: 2},
		OutboxRoot:    tree3.root(), FinalizeAtHeight: 1, AssetFlowSummary: flowSummary(),
	}).Save(f.storage))
	common.FailIfErr(t, (&definition.AssetConfig{DomainId: relayDomain, AssetId: qsrAssetId(), MaxBatchWithdrawal: big.NewInt(100)}).Save(f.storage))
	seedCounters(t, f.storage, relayDomain, qsrAssetId(), 500, 0, 0)
	common.FailIfErr(t, registerWithdrawal(f.storage, relayFinalizeAt, relayDomain, 2, third[0], tree3.proof(0)))
}

func TestRegisterWithdrawal_InputIndexOutsideBatchRangeIsAccepted(t *testing.T) {
	f := newRelayFixture(t, 1000, 500, contractMsg(1<<40, 7, relayRecipientA, 1))
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 0))
}

func TestRegisterWithdrawal_ConservationViolation(t *testing.T) {
	// The cap has room (1000), custody does not (30 deposited).
	f := newRelayFixture(t, 1000, 30, contractMsg(0, 0, relayRecipientA, 20), contractMsg(1, 0, relayRecipientA, 11))
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 0))
	f.expectRejected(t, "reserve above custody", relayFinalizeAt, 1, constants.ErrSettlementConservationViolated)
	// Released funds count against custody too.
	seedCounters(t, f.storage, relayDomain, znnAssetId(), 30, 20, 10)
	f.expectRejected(t, "released plus reserved above custody", relayFinalizeAt, 1, constants.ErrSettlementConservationViolated)
	// The id stays free: more custody lets the same relay through.
	seedCounters(t, f.storage, relayDomain, znnAssetId(), 41, 20, 0)
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 1))
}

func TestRegisterWithdrawal_RelayPause(t *testing.T) {
	for _, c := range []struct {
		name    string
		pause   func(db.DB)
		blocked bool
	}{
		{"global relay", func(s db.DB) { common.FailIfErr(t, (&definition.PauseState{RelayPaused: true}).SaveGlobal(s)) }, true},
		{"domain relay", func(s db.DB) {
			common.FailIfErr(t, (&definition.PauseState{RelayPaused: true}).SaveDomain(s, relayDomain))
		}, true},
		{"other domain relay", func(s db.DB) { common.FailIfErr(t, (&definition.PauseState{RelayPaused: true}).SaveDomain(s, 2)) }, false},
		{"global submit", func(s db.DB) { common.FailIfErr(t, (&definition.PauseState{SubmitPaused: true}).SaveGlobal(s)) }, false},
		{"domain submit", func(s db.DB) {
			common.FailIfErr(t, (&definition.PauseState{SubmitPaused: true}).SaveDomain(s, relayDomain))
		}, false},
	} {
		f := newRelayFixture(t, 1000, 500, contractMsg(0, 0, relayRecipientA, 5))
		c.pause(f.storage)
		if c.blocked {
			f.expectRejected(t, c.name, relayFinalizeAt, 0, constants.ErrSettlementRelayPaused)
		} else if err := f.relay(t, relayFinalizeAt, 0); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
}

func TestRegisterWithdrawal_PausedDomainStatusStillRegisters(t *testing.T) {
	f := newRelayFixture(t, 1000, 500, contractMsg(0, 0, relayRecipientA, 5))
	common.FailIfErr(t, (&definition.DomainRecord{DomainRecordKey: definition.DomainRecordKey{DomainId: relayDomain}, DomainClass: 1, Status: definition.DomainStatusPaused}).Save(f.storage))
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 0))
}

func TestRegisterWithdrawal_MessagesOfOneBatchRegisterIndependently(t *testing.T) {
	msgs := make([]wmsg, 5)
	for i := range msgs {
		msgs[i] = contractMsg(uint64(i), 0, relayRecipientA, int64(i+1))
	}
	f := newRelayFixture(t, 1000, 500, msgs...)
	for i := range msgs {
		common.FailIfErr(t, f.relay(t, relayFinalizeAt, i))
	}
	if _, reserve, _ := countersOf(t, f.storage, relayDomain, znnAssetId()); reserve != 15 {
		t.Fatalf("reserve %d", reserve)
	}
}

// releaseFixture queues n ZNN withdrawals of amounts 1 to n,
// alternating recipients, through registerWithdrawal.
func releaseFixture(t *testing.T, n int) *relayFixture {
	t.Helper()
	msgs := make([]wmsg, n)
	for i := range msgs {
		to := relayRecipientA
		if i%2 == 1 {
			to = relayRecipientB
		}
		msgs[i] = contractMsg(uint64(i), 0, to, int64(i+1))
	}
	f := newRelayFixture(t, 1000, 1000, msgs...)
	for i := range msgs {
		common.FailIfErr(t, f.relay(t, relayFinalizeAt, i))
	}
	return f
}

func sendsSummary(sends []*nom.AccountBlock) string {
	out := ""
	for _, s := range sends {
		out += fmt.Sprintf("%s:%v:%s;", s.ToAddress, s.Amount, s.TokenStandard)
	}
	return out
}

func TestReleaseWithdrawals_SeventeenEntriesTwoCalls(t *testing.T) {
	f := releaseFixture(t, 17)
	const first = uint64(1000)

	sends, err := releaseWithdrawals(f.storage, first, relayDomain)
	common.FailIfErr(t, err)
	if len(sends) != constants.MaxWithdrawalsPerUpdate {
		t.Fatalf("expected %d sends, got %d", constants.MaxWithdrawalsPerUpdate, len(sends))
	}
	for i, s := range sends {
		to := relayRecipientA
		if i%2 == 1 {
			to = relayRecipientB
		}
		if s.Address != types.SettlementContract || s.ToAddress != to || s.BlockType != nom.BlockTypeContractSend ||
			s.Amount.Int64() != int64(i+1) || s.TokenStandard != types.ZnnTokenStandard || len(s.Data) != 0 {
			t.Fatalf("send %d mismatch: %+v", i, s)
		}
	}
	// Amounts 1..16 released, amount 17 pending.
	_, reserve, released := countersOf(t, f.storage, relayDomain, znnAssetId())
	if released != 136 || reserve != 17 {
		t.Fatalf("after first call: released %d reserve %d", released, reserve)
	}

	_, err = releaseWithdrawals(f.storage, first, relayDomain)
	expectErr(t, "inside the rate limit", err, constants.ErrUpdateTooRecent)
	_, reserve, released = countersOf(t, f.storage, relayDomain, znnAssetId())
	if released != 136 || reserve != 17 {
		t.Fatalf("rejected call changed counters: released %d reserve %d", released, reserve)
	}

	sends, err = releaseWithdrawals(f.storage, first+1, relayDomain)
	common.FailIfErr(t, err)
	if len(sends) != 1 || sends[0].Amount.Int64() != 17 || sends[0].ToAddress != relayRecipientA {
		t.Fatalf("second call: %s", sendsSummary(sends))
	}
	_, reserve, released = countersOf(t, f.storage, relayDomain, znnAssetId())
	if released != 153 || reserve != 0 {
		t.Fatalf("after second call: released %d reserve %d", released, reserve)
	}
}

func TestReleaseWithdrawals_ExactlyOneBatchOfSixteen(t *testing.T) {
	f := releaseFixture(t, 16)
	sends, err := releaseWithdrawals(f.storage, 500, relayDomain)
	common.FailIfErr(t, err)
	if len(sends) != 16 {
		t.Fatalf("got %d sends", len(sends))
	}
	_, err = releaseWithdrawals(f.storage, 500+constants.ReleaseIntervalMomentums, relayDomain)
	expectErr(t, "drained queue", err, constants.ErrNothingToWithdraw)
}

func TestReleaseWithdrawals_EmptyQueueLeavesRateLimitUntouched(t *testing.T) {
	f := newRelayFixture(t, 1000, 1000, contractMsg(0, 0, relayRecipientA, 5))
	_, err := releaseWithdrawals(f.storage, 700, relayDomain)
	expectErr(t, "empty queue", err, constants.ErrNothingToWithdraw)
	limit, err := definition.GetReleaseRateLimit(f.storage, relayDomain)
	common.FailIfErr(t, err)
	if limit.Height != 0 {
		t.Fatalf("empty call stamped the rate limit at %d", limit.Height)
	}
	// The next release is not delayed by the failed call.
	common.FailIfErr(t, f.relay(t, relayFinalizeAt, 0))
	sends, err := releaseWithdrawals(f.storage, 701, relayDomain)
	common.FailIfErr(t, err)
	if len(sends) != 1 {
		t.Fatalf("got %d sends", len(sends))
	}
}

func TestReleaseWithdrawals_FirstCallPassesAtAnyHeight(t *testing.T) {
	f := releaseFixture(t, 1)
	_, err := releaseWithdrawals(f.storage, 1, relayDomain)
	common.FailIfErr(t, err)
	limit, err := definition.GetReleaseRateLimit(f.storage, relayDomain)
	common.FailIfErr(t, err)
	if limit.Height != 1 {
		t.Fatalf("rate limit height %d", limit.Height)
	}
}

func TestReleaseWithdrawals_RateLimitIsPerDomain(t *testing.T) {
	f := releaseFixture(t, 17)
	// Domain 2 has its own queued withdrawal.
	common.FailIfErr(t, definition.EnqueueRelease(f.storage, 2, &definition.ReleaseQueueEntry{
		Recipient: relayRecipientB, AssetId: znnAssetId(), Amount: big.NewInt(3), OutboxId: rootOf(0x61),
	}))
	seedCounters(t, f.storage, 2, znnAssetId(), 3, 3, 0)

	_, err := releaseWithdrawals(f.storage, 1000, relayDomain)
	common.FailIfErr(t, err)
	sends, err := releaseWithdrawals(f.storage, 1000, 2)
	common.FailIfErr(t, err)
	if len(sends) != 1 || sends[0].Amount.Int64() != 3 {
		t.Fatalf("domain 2 release: %s", sendsSummary(sends))
	}
	_, err = releaseWithdrawals(f.storage, 1000, relayDomain)
	expectErr(t, "domain 1 stays limited", err, constants.ErrUpdateTooRecent)
}

func TestReleaseWithdrawals_MixedAssetsInOrder(t *testing.T) {
	qsrMsg := contractMsg(1, 0, relayRecipientB, 7)
	qsrMsg.asset = qsrAssetId()
	f := newRelayFixture(t, 1000, 1000, contractMsg(0, 0, relayRecipientA, 5), qsrMsg, contractMsg(2, 0, relayRecipientA, 6))
	common.FailIfErr(t, (&definition.AssetConfig{DomainId: relayDomain, AssetId: qsrAssetId(), MaxBatchWithdrawal: big.NewInt(100)}).Save(f.storage))
	seedCounters(t, f.storage, relayDomain, qsrAssetId(), 50, 0, 0)
	for i := 0; i < 3; i++ {
		common.FailIfErr(t, f.relay(t, relayFinalizeAt, i))
	}

	sends, err := releaseWithdrawals(f.storage, 10, relayDomain)
	common.FailIfErr(t, err)
	want := fmt.Sprintf("%s:5:%s;%s:7:%s;%s:6:%s;", relayRecipientA, types.ZnnTokenStandard, relayRecipientB, types.QsrTokenStandard, relayRecipientA, types.ZnnTokenStandard)
	if got := sendsSummary(sends); got != want {
		t.Fatalf("sends: got %s want %s", got, want)
	}
	_, reserve, released := countersOf(t, f.storage, relayDomain, znnAssetId())
	if reserve != 0 || released != 11 {
		t.Fatalf("znn counters: reserve %d released %d", reserve, released)
	}
	_, reserve, released = countersOf(t, f.storage, relayDomain, qsrAssetId())
	if reserve != 0 || released != 7 {
		t.Fatalf("qsr counters: reserve %d released %d", reserve, released)
	}
}

func TestReleaseWithdrawals_KeepsCustodyInvariant(t *testing.T) {
	f := releaseFixture(t, 3)
	before := func() int64 {
		_, reserve, released := countersOf(t, f.storage, relayDomain, znnAssetId())
		return reserve + released
	}
	total := before()
	_, err := releaseWithdrawals(f.storage, 5, relayDomain)
	common.FailIfErr(t, err)
	if after := before(); after != total {
		t.Fatalf("released plus reserved changed: %d -> %d", total, after)
	}
}

func TestReleaseWithdrawals_RelayPauseBlocksThenReleasesInOrder(t *testing.T) {
	f := releaseFixture(t, 3)
	common.FailIfErr(t, (&definition.PauseState{RelayPaused: true}).SaveDomain(f.storage, relayDomain))
	before := f.relayState(t)
	_, err := releaseWithdrawals(f.storage, 10, relayDomain)
	expectErr(t, "paused", err, constants.ErrSettlementRelayPaused)
	if f.relayState(t) != before {
		t.Fatal("paused release changed state")
	}
	if limit, _ := definition.GetReleaseRateLimit(f.storage, relayDomain); limit.Height != 0 {
		t.Fatal("paused release stamped the rate limit")
	}

	common.FailIfErr(t, (&definition.PauseState{}).SaveDomain(f.storage, relayDomain))
	sends, err := releaseWithdrawals(f.storage, 11, relayDomain)
	common.FailIfErr(t, err)
	amounts := ""
	for _, s := range sends {
		amounts += s.Amount.String() + ","
	}
	if amounts != "1,2,3," {
		t.Fatalf("release order after unpause: %s", amounts)
	}
}

func TestReleaseWithdrawals_PauseScopes(t *testing.T) {
	for _, c := range []struct {
		name    string
		pause   func(db.DB)
		blocked bool
	}{
		{"global relay", func(s db.DB) { common.FailIfErr(t, (&definition.PauseState{RelayPaused: true}).SaveGlobal(s)) }, true},
		{"other domain relay", func(s db.DB) { common.FailIfErr(t, (&definition.PauseState{RelayPaused: true}).SaveDomain(s, 2)) }, false},
		{"global submit", func(s db.DB) { common.FailIfErr(t, (&definition.PauseState{SubmitPaused: true}).SaveGlobal(s)) }, false},
		{"domain submit", func(s db.DB) {
			common.FailIfErr(t, (&definition.PauseState{SubmitPaused: true}).SaveDomain(s, relayDomain))
		}, false},
	} {
		f := releaseFixture(t, 1)
		c.pause(f.storage)
		_, err := releaseWithdrawals(f.storage, 10, relayDomain)
		if c.blocked {
			expectErr(t, c.name, err, constants.ErrSettlementRelayPaused)
		} else {
			common.FailIfErr(t, err)
		}
	}
}

func TestSettlementRelayMessageValidateSendBlock(t *testing.T) {
	method := &SettlementRelayMessageMethod{MethodName: definition.SettlementRelayMessageMethodName}
	good := contractMsg(0, 0, relayRecipientA, 5).encode()
	block := func(amount int64, message, proof []byte) *nom.AccountBlock {
		data, err := definition.ABISettlement.PackMethod(method.MethodName, relayDomain, relayBatch, message, proof)
		common.FailIfErr(t, err)
		return &nom.AccountBlock{Address: relayRecipientA, Amount: big.NewInt(amount), Data: data}
	}
	common.FailIfErr(t, method.ValidateSendBlock(block(0, good, nil)))
	expectErr(t, "attached amount", method.ValidateSendBlock(block(1, good, nil)), constants.ErrInvalidTokenOrAmount)
	expectErr(t, "malformed message", method.ValidateSendBlock(block(0, good[:132], nil)), constants.ErrForbiddenParam)
	expectErr(t, "garbage data", method.ValidateSendBlock(&nom.AccountBlock{Amount: big.NewInt(0), Data: []byte{1, 2, 3}}), constants.ErrUnpackError)
}
