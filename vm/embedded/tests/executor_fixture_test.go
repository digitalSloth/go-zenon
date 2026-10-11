package tests

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"sort"
	"testing"

	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/crypto"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc/api"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/zenon/mock"
)

type executorFixture struct {
	ReadHeight           uint64                  `json:"readHeight"`
	ExecutorAddress      types.Address           `json:"executorAddress"`
	PillarName           string                  `json:"pillarName"`
	DomainID             uint32                  `json:"domainId"`
	EmptyDomainID        uint32                  `json:"emptyDomainId"`
	AssetID              types.Hash              `json:"assetId"`
	UnconfiguredAssetID  types.Hash              `json:"unconfiguredAssetId"`
	ProcessedOutboxID    types.Hash              `json:"processedOutboxId"`
	UnprocessedOutboxID  types.Hash              `json:"unprocessedOutboxId"`
	StateRoot            types.Hash              `json:"stateRoot"`
	Proofs               []executorFixtureProof  `json:"proofs"`
	Momenta              []*api.DetailedMomentum `json:"momenta"`
	FrontierAccountBlock *api.AccountBlock       `json:"frontierAccountBlock"`
	AccountBlocks        []*api.AccountBlock     `json:"accountBlocks"`
	Assignment           executorFixtureAssign   `json:"assignment"`
	Expected             executorFixtureExpected `json:"expected"`
}

type executorFixtureAssign struct {
	PreStateRootEmptyHeight      uint64                       `json:"preStateRootEmptyHeight"`
	PreStateRootSettlementHeight uint64                       `json:"preStateRootSettlementHeight"`
	SendHeight                   uint64                       `json:"sendHeight"`
	ReceiveHeight                uint64                       `json:"receiveHeight"`
	EmptyHeight                  uint64                       `json:"emptyHeight"`
	RejectedSendHash             types.Hash                   `json:"rejectedSendHash"`
	Entries                      []executorFixtureAssignEntry `json:"entries"`
}

type executorFixtureAssignEntry struct {
	Ordinal                uint64     `json:"ordinal"`
	GlobalInputIndex       uint64     `json:"globalInputIndex"`
	SourceAccountBlockHash types.Hash `json:"sourceAccountBlockHash"`
	SendMomentumHeight     uint64     `json:"sendMomentumHeight"`
}

type executorFixtureProof struct {
	Height uint64          `json:"height"`
	Key    []byte          `json:"key"`
	Result *api.StateProof `json:"result"`
}

type executorFixtureExpected struct {
	DomainRecord       *definition.DomainRecord        `json:"domainRecord"`
	Cursor             *definition.InputSequenceCursor `json:"cursor"`
	BatchEntries       []*definition.BatchEntry        `json:"batchEntries"`
	DomainInputCounter uint64                          `json:"domainInputCounter"`
	Ordinal            *definition.DomainInputOrdinal  `json:"ordinal"`
	AssetConfig        *definition.AssetConfig         `json:"assetConfig"`
	GlobalPause        *definition.PauseState          `json:"globalPause"`
	DomainPause        *definition.PauseState          `json:"domainPause"`
	ExecutorSetMember  *definition.ExecutorSetMember   `json:"executorSetMember"`
}

// executorOutboxId recomputes the outbox id the Settlement contract
// derives for a message: SHA3-256(domainId, batchId, inputIndex,
// outboxIndex), big-endian.
func executorOutboxId(domainId uint32, batchId, inputIndex uint64, outboxIndex uint32) types.Hash {
	b := binary.BigEndian.AppendUint32(nil, domainId)
	b = binary.BigEndian.AppendUint64(b, batchId)
	b = binary.BigEndian.AppendUint64(b, inputIndex)
	b = binary.BigEndian.AppendUint32(b, outboxIndex)
	return types.BytesToHashPanic(crypto.Hash(b))
}

// TestSettlement_ExecutorReadPathFixture writes the golden fixture the
// executor's L1 read path is tested against. It runs only when
// EXECUTOR_FIXTURE_OUT names the output file.
func TestSettlement_ExecutorReadPathFixture(t *testing.T) {
	out := os.Getenv("EXECUTOR_FIXTURE_OUT")
	if out == "" {
		t.Skip("EXECUTOR_FIXTURE_OUT not set")
	}

	z := mock.NewMockZenon(t)
	t.Cleanup(z.StopPanic)
	saveSporkState(t)
	activateSporkByName(t, z, "spork-offchain-execution", types.OffchainExecutionSpork)
	advanceMomentums(t, z, 10)
	saveBridgeConstants(t)

	savedAdmin := types.NetworkAdmin
	types.NetworkAdmin = g.User5.Address
	t.Cleanup(func() { types.NetworkAdmin = savedAdmin })

	constants.MinSoftDelay = 10
	constants.MinAdministratorDelay = 10
	savedMin := constants.WithdrawalDelayMin
	constants.WithdrawalDelayMin = 10
	t.Cleanup(func() { constants.WithdrawalDelayMin = savedMin })

	// The domain registers between the two spork activations, so a momentum below
	// version 3 holds a Settlement account block.
	registerDomains(t, z, 1)
	activateStateRoot(t, z)
	advanceMomentums(t, z, 10)
	logs := z.SaveLogs(common.EmbeddedLogger)
	t.Cleanup(func() { logs.Equals(t, ``) })

	settlementCall(t, z, setApprovedDAModesStep(g.User5.Address, 1, []byte{0}), nil)
	advanceMomentums(t, z, constants.MinSoftDelay)
	settlementCall(t, z, setApprovedDAModesStep(g.User5.Address, 1, []byte{0}), nil)
	configureAsset(t, z, 1, assetIdFor(types.ZnnTokenStandard))
	registerExecutorFully(t, z, g.Pillar1Name, g.User1.Address)

	target := types.Hash{0x07}
	settlementCall(t, z, depositStep(g.User2.Address, types.ZnnTokenStandard, 100, 1, target, 5000), nil)
	settlementCall(t, z, depositStep(g.User3.Address, types.ZnnTokenStandard, 50, 1, target, 5000), nil)

	leaves := [][]byte{
		outboxMessage(types.ZeroHash, 0, 0, g.User2.Address, 100),
		outboxMessage(hashFromByteForTest(0x77), 1, 0, g.User4.Address, 10),
		outboxMessage(types.ZeroHash, 1, 1, g.User3.Address, 30),
	}
	root, proofs := outboxTree(leaves)
	commitment := batchCommitment(1, 0, 0, 1, types.ZeroHash, hashFromByteForTest(0x51), g.User1.Address, withdrawalFlowSummary(150, 140))
	copy(commitment[206:238], root.Bytes())
	settlementCall(t, z, submitBatchStep(g.User1.Address, commitment), nil)

	// Finalize batch 0, then relay its first message.
	entry0, err := definition.GetBatchEntry(settlementStorage(t, z), 1, 0)
	common.FailIfErr(t, err)
	frMom, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	if frMom.Height < entry0.FinalizeAtHeight {
		advanceMomentums(t, z, entry0.FinalizeAtHeight-frMom.Height)
	}
	settlementCall(t, z, relayMessageStep(g.User1.Address, 1, 0, leaves[0], proofs[0]), nil)

	// Input 2 and batch 1; accepting batch 1 sweeps batch 0's input records.
	settlementCall(t, z, depositStep(g.User2.Address, types.ZnnTokenStandard, 1, 1, target, 5000), nil)
	commitment1 := batchCommitment(1, 1, 2, 2, hashFromByteForTest(0x51), hashFromByteForTest(0x52), g.User1.Address, znnDepositSummary(1))
	settlementCall(t, z, submitBatchStep(g.User1.Address, commitment1), nil)

	settlementCall(t, z, pauseStep(g.User5.Address, 1, false, definition.PauseScopeRelay, true), nil)
	settlementCall(t, z, pauseStep(g.User5.Address, 0, true, definition.PauseScopeSubmit, true), nil)

	processedOutboxId := executorOutboxId(1, 0, 0, 0)
	unprocessedOutboxId := executorOutboxId(1, 0, 1, 0)

	ledgerApi := api.NewLedgerApi(z)
	frontier, err := ledgerApi.GetFrontierMomentum()
	common.FailIfErr(t, err)
	h := frontier.Height
	store := settlementStorage(t, z)

	cursor, err := definition.GetInputSequenceCursor(store, 1)
	common.FailIfErr(t, err)
	batch0, err := definition.GetBatchEntry(store, 1, 0)
	common.FailIfErr(t, err)
	batch1, err := definition.GetBatchEntry(store, 1, 1)
	common.FailIfErr(t, err)
	if _, err := definition.GetDomainInputOrdinal(store, 1, 0); err != constants.ErrDataNonExistent {
		t.Fatalf("ordinal (1, 0): want ErrDataNonExistent, got %v", err)
	}
	ordinal, err := definition.GetDomainInputOrdinal(store, 1, 2)
	common.FailIfErr(t, err)
	processed, err := definition.IsOutboxProcessed(store, processedOutboxId)
	common.FailIfErr(t, err)
	unprocessed, err := definition.IsOutboxProcessed(store, unprocessedOutboxId)
	common.FailIfErr(t, err)
	globalPause, err := definition.GetGlobalPauseState(store)
	common.FailIfErr(t, err)
	domainPause, err := definition.GetDomainPauseState(store, 1)
	common.FailIfErr(t, err)

	switch {
	case frontier.Version != 3:
		t.Fatalf("frontier momentum version %d want 3", frontier.Version)
	case cursor.NextBatchId != 2 || cursor.RecordsSweptThrough != 1:
		t.Fatalf("cursor %+v want NextBatchId 2 and RecordsSweptThrough 1", cursor)
	case !batch0.IsFinalized(h):
		t.Fatalf("batch 0 not finalized at %d (finalizeAtHeight %d)", h, batch0.FinalizeAtHeight)
	case batch1.IsFinalized(h):
		t.Fatalf("batch 1 finalized at %d (finalizeAtHeight %d)", h, batch1.FinalizeAtHeight)
	case !processed:
		t.Fatalf("outbox %s not processed", processedOutboxId)
	case unprocessed:
		t.Fatalf("outbox %s processed", unprocessedOutboxId)
	case !globalPause.SubmitPaused:
		t.Fatalf("global pause %+v want SubmitPaused", globalPause)
	case !domainPause.RelayPaused:
		t.Fatalf("domain 1 pause %+v want RelayPaused", domainPause)
	}

	assetId := assetIdFor(types.ZnnTokenStandard)
	unconfiguredAssetId := assetIdFor(types.QsrTokenStandard)
	if _, err := definition.GetAssetConfig(store, 1, unconfiguredAssetId); err != constants.ErrDataNonExistent {
		t.Fatalf("asset config for the unconfigured asset: err %v want %v", err, constants.ErrDataNonExistent)
	}
	record, err := definition.GetDomainRecord(store, 1)
	common.FailIfErr(t, err)
	counter, err := definition.GetDomainInputCounter(store, 1)
	common.FailIfErr(t, err)
	assetConfig, err := definition.GetAssetConfig(store, 1, assetId)
	common.FailIfErr(t, err)
	executors, err := definition.GetExecutorSet(store)
	common.FailIfErr(t, err)
	var member *definition.ExecutorSetMember
	matches := 0
	for _, m := range executors {
		if m.PillarName == g.Pillar1Name {
			member = m
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("executor set has %d members named %s, want 1", matches, g.Pillar1Name)
	}

	stateRoot, err := ledgerApi.GetStateRoot(h)
	common.FailIfErr(t, err)

	rawKey := func(k []byte) []byte {
		out := []byte{0x03}
		out = append(out, types.SettlementContract.Bytes()...)
		out = append(out, 0x04)
		return append(out, k...)
	}
	keys := [][]byte{
		(&definition.DomainRecordKey{DomainId: 1}).Key(),
		(&definition.DomainRecordKey{DomainId: 2}).Key(),
		definition.InputSequenceCursorKey(1),
		definition.InputSequenceCursorKey(2),
		(&definition.BatchEntryKey{DomainId: 1, BatchId: 0}).Key(),
		(&definition.BatchEntryKey{DomainId: 1, BatchId: 1}).Key(),
		(&definition.BatchEntryKey{DomainId: 1, BatchId: 2}).Key(),
		definition.DomainInputCounterKey(1),
		definition.DomainInputOrdinalKey(1, 0),
		definition.DomainInputOrdinalKey(1, 2),
		definition.AssetConfigKey(1, assetId),
		definition.GlobalPauseStateKey(),
		definition.DomainPauseStateKey(1),
		definition.ExecutorSetMemberKey(g.Pillar1Name),
		definition.ProcessedOutboxKey(processedOutboxId),
		definition.ProcessedOutboxKey(unprocessedOutboxId),
		definition.AssetConfigKey(1, unconfiguredAssetId),
	}
	fixtureProofs := make([]executorFixtureProof, 0, len(keys))
	for _, k := range keys {
		raw := rawKey(k)
		p, err := ledgerApi.GetProof(h, raw)
		common.FailIfErr(t, err)
		fixtureProofs = append(fixtureProofs, executorFixtureProof{Height: h, Key: raw, Result: p})
	}

	// Assignment scenario: two accepted sends and one rejected send, received in one momentum.
	c0 := counter.Count
	call2 := z.CallContract(depositStep(g.User2.Address, types.ZnnTokenStandard, 3, 1, target, 5000))
	call3 := z.CallContract(depositStep(g.User3.Address, types.QsrTokenStandard, 7, 1, target, 5000))
	call5 := z.CallContract(depositStep(g.User5.Address, types.ZnnTokenStandard, 4, 1, target, 5000))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call2.Error(t, nil)
	call3.Error(t, constants.ErrInvalidToken)
	call5.Error(t, nil)

	var sendHashes []types.Hash
	var sendHeight, receiveHeight uint64
	for i, user := range []types.Address{g.User2.Address, g.User3.Address, g.User5.Address} {
		blk, err := ledgerApi.GetFrontierAccountBlock(user)
		common.FailIfErr(t, err)
		b, err := ledgerApi.GetAccountBlockByHash(blk.Hash)
		common.FailIfErr(t, err)
		if b == nil || b.ConfirmationDetail == nil || b.PairedAccountBlock == nil || b.PairedAccountBlock.ConfirmationDetail == nil {
			t.Fatalf("send %s is not confirmed with a confirmed receive", blk.Hash)
		}
		sh, rh := b.ConfirmationDetail.MomentumHeight, b.PairedAccountBlock.ConfirmationDetail.MomentumHeight
		if i == 0 {
			sendHeight, receiveHeight = sh, rh
		}
		if sh != sendHeight || rh != receiveHeight {
			t.Fatalf("send %s confirmed at %d received at %d, want %d and %d", blk.Hash, sh, rh, sendHeight, receiveHeight)
		}
		sendHashes = append(sendHashes, blk.Hash)
	}

	scenarioFrontier, err := ledgerApi.GetFrontierMomentum()
	common.FailIfErr(t, err)
	if scenarioFrontier.Height != receiveHeight {
		t.Fatalf("frontier momentum height %d, want the receive height %d", scenarioFrontier.Height, receiveHeight)
	}

	scenarioStore := settlementStorage(t, z)
	c1, err := definition.GetDomainInputCounter(scenarioStore, 1)
	common.FailIfErr(t, err)
	if c1.Count != c0+2 {
		t.Fatalf("domain 1 counter %d want %d", c1.Count, c0+2)
	}
	var entries []executorFixtureAssignEntry
	for o := c0; o < c1.Count; o++ {
		ord, err := definition.GetDomainInputOrdinal(scenarioStore, 1, o)
		common.FailIfErr(t, err)
		entries = append(entries, executorFixtureAssignEntry{
			Ordinal:                o,
			GlobalInputIndex:       ord.GlobalInputIndex,
			SourceAccountBlockHash: ord.SourceAccountBlockHash,
			SendMomentumHeight:     sendHeight,
		})
	}
	// Settlement receives the sends in either order within the momentum.
	gotSends := map[types.Hash]bool{}
	for _, e := range entries {
		gotSends[e.SourceAccountBlockHash] = true
	}
	if len(entries) != 2 || !gotSends[sendHashes[0]] || !gotSends[sendHashes[2]] {
		t.Fatalf("assignment entries %+v want the sends %s and %s", entries, sendHashes[0], sendHashes[2])
	}
	if c0 < 1 {
		t.Fatalf("domain 1 counter %d before the scenario, want at least 1", c0)
	}
	if _, err := definition.GetDomainInputOrdinal(scenarioStore, 1, c0-1); err != nil {
		t.Fatalf("ordinal (1, %d): %v", c0-1, err)
	}

	assignKeys := [][]byte{definition.DomainInputCounterKey(1), definition.DomainInputCounterKey(2)}
	for o := c0 - 1; o < c1.Count; o++ {
		assignKeys = append(assignKeys, definition.DomainInputOrdinalKey(1, o))
	}
	for _, k := range assignKeys {
		raw := rawKey(k)
		p, err := ledgerApi.GetProof(receiveHeight, raw)
		common.FailIfErr(t, err)
		fixtureProofs = append(fixtureProofs, executorFixtureProof{Height: receiveHeight, Key: raw, Result: p})
	}

	advanceMomentums(t, z, 10)
	frontierAfter, err := ledgerApi.GetFrontierMomentum()
	common.FailIfErr(t, err)
	f := frontierAfter.Height

	accountBlock, err := ledgerApi.GetFrontierAccountBlock(g.User1.Address)
	common.FailIfErr(t, err)

	var momenta []*api.DetailedMomentum
	for start := uint64(1); start <= f; start += api.RpcMaxCountSize {
		list, err := ledgerApi.GetDetailedMomentumsByHeight(start, api.RpcMaxCountSize)
		common.FailIfErr(t, err)
		momenta = append(momenta, list.List...)
	}
	sort.Slice(momenta, func(i, j int) bool { return momenta[i].Momentum.Height < momenta[j].Momentum.Height })
	if uint64(len(momenta)) != f {
		t.Fatalf("fetched %d momenta, want %d", len(momenta), f)
	}

	var accountBlocks []*api.AccountBlock
	for _, hash := range sendHashes {
		b, err := ledgerApi.GetAccountBlockByHash(hash)
		common.FailIfErr(t, err)
		accountBlocks = append(accountBlocks, b)
	}

	var preEmpty, preSettlement, emptyHeight uint64
	for _, dm := range momenta {
		m := dm.Momentum
		settles := false
		for _, hd := range m.Content {
			if hd.Address == types.SettlementContract {
				settles = true
			}
		}
		switch {
		case m.Version < 3 && !settles && m.Height >= 2 && preEmpty == 0:
			preEmpty = m.Height
		case m.Version < 3 && settles && preSettlement == 0:
			preSettlement = m.Height
		case m.Version == 3 && !settles && m.Height > receiveHeight && emptyHeight == 0:
			emptyHeight = m.Height
		}
	}
	if preEmpty == 0 || preSettlement == 0 || emptyHeight == 0 {
		t.Fatalf("heights not found: preStateRootEmpty %d preStateRootSettlement %d empty %d", preEmpty, preSettlement, emptyHeight)
	}

	fx := executorFixture{
		ReadHeight:           h,
		ExecutorAddress:      g.User1.Address,
		PillarName:           g.Pillar1Name,
		DomainID:             1,
		EmptyDomainID:        2,
		AssetID:              assetId,
		UnconfiguredAssetID:  unconfiguredAssetId,
		ProcessedOutboxID:    processedOutboxId,
		UnprocessedOutboxID:  unprocessedOutboxId,
		StateRoot:            stateRoot,
		Proofs:               fixtureProofs,
		Momenta:              momenta,
		FrontierAccountBlock: accountBlock,
		AccountBlocks:        accountBlocks,
		Assignment: executorFixtureAssign{
			PreStateRootEmptyHeight:      preEmpty,
			PreStateRootSettlementHeight: preSettlement,
			SendHeight:                   sendHeight,
			ReceiveHeight:                receiveHeight,
			EmptyHeight:                  emptyHeight,
			RejectedSendHash:             sendHashes[1],
			Entries:                      entries,
		},
		Expected: executorFixtureExpected{
			DomainRecord:       record,
			Cursor:             cursor,
			BatchEntries:       []*definition.BatchEntry{batch0, batch1},
			DomainInputCounter: counter.Count,
			Ordinal:            ordinal,
			AssetConfig:        assetConfig,
			GlobalPause:        globalPause,
			DomainPause:        domainPause,
			ExecutorSetMember:  member,
		},
	}
	data, err := json.MarshalIndent(&fx, "", "  ")
	common.FailIfErr(t, err)
	common.FailIfErr(t, os.WriteFile(out, data, 0o644))
}
