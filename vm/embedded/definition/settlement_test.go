package definition

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/constants"
)

func hashFromByte(b byte) types.Hash {
	var raw [types.HashSize]byte
	for i := range raw {
		raw[i] = b
	}
	h, err := types.BytesToHash(raw[:])
	if err != nil {
		panic(err)
	}
	return h
}

func addressFromByte(b byte) types.Address {
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

func TestDomainRecord_RoundTrip_FullFields(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	record := &DomainRecord{
		DomainRecordKey: DomainRecordKey{DomainId: 7},
		DomainClass:     DomainClassExecution,
		RuntimeKind:     RuntimeKindWASM,
		StfSpecHash:     hashFromByte(1),
		InputSource:     InputSourceL1Native,
		ExecProfile:     ExecProfileAttestation,
		ForeignProfile:  ForeignProfileNone,
		ProfileConfig:   []byte("attester-policy-config"),
		ChainBinding:    []byte("chain-binding-blob"),
		MinExecutors:    2,
		ProposerPolicy:  ProposerPolicySingle,
		ValueCaps:       []byte("value-caps-blob"),
		FinalityModel:   FinalityModelDelay,
		Status:          DomainStatusActive,
	}
	if err := record.Save(memDb); err != nil {
		t.Fatal(err)
	}

	got, err := GetDomainRecord(memDb, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.DomainId != 7 {
		t.Fatalf("domainId mismatch: %v", got.DomainId)
	}
	if got.DomainClass != record.DomainClass || got.RuntimeKind != record.RuntimeKind ||
		got.StfSpecHash != record.StfSpecHash || got.InputSource != record.InputSource ||
		got.ExecProfile != record.ExecProfile || got.ForeignProfile != record.ForeignProfile ||
		got.MinExecutors != record.MinExecutors || got.ProposerPolicy != record.ProposerPolicy ||
		got.FinalityModel != record.FinalityModel || got.Status != record.Status {
		t.Fatalf("scalar field mismatch: got %+v want %+v", got, record)
	}
	if string(got.ProfileConfig) != string(record.ProfileConfig) {
		t.Fatalf("profileConfig mismatch: got %q want %q", got.ProfileConfig, record.ProfileConfig)
	}
	if string(got.ChainBinding) != string(record.ChainBinding) {
		t.Fatalf("chainBinding mismatch: got %q want %q", got.ChainBinding, record.ChainBinding)
	}
	if string(got.ValueCaps) != string(record.ValueCaps) {
		t.Fatalf("valueCaps mismatch: got %q want %q", got.ValueCaps, record.ValueCaps)
	}
}

func TestDomainRecord_RoundTrip_Defaults(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	record := &DomainRecord{
		DomainRecordKey: DomainRecordKey{DomainId: 1},
		DomainClass:     DomainClassExecution,
		RuntimeKind:     RuntimeKindWASM,
		StfSpecHash:     types.ZeroHash,
		InputSource:     InputSourceL1Native,
		ExecProfile:     ExecProfileAttestation,
		ForeignProfile:  ForeignProfileNone,
		ProfileConfig:   []byte{},
		ChainBinding:    []byte{},
		MinExecutors:    1,
		ProposerPolicy:  ProposerPolicySingle,
		ValueCaps:       []byte{},
		FinalityModel:   FinalityModelDelay,
		Status:          DomainStatusActive,
	}
	if err := record.Save(memDb); err != nil {
		t.Fatal(err)
	}

	got, err := GetDomainRecord(memDb, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ProfileConfig) != 0 || len(got.ChainBinding) != 0 || len(got.ValueCaps) != 0 {
		t.Fatalf("expected empty reserved blobs, got %+v", got)
	}
}

func TestDomainRecord_NotFound(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	_, err := GetDomainRecord(memDb, 42)
	if err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent, got %v", err)
	}
}

func TestBatchEntry_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	entry := &BatchEntry{
		BatchEntryKey:     BatchEntryKey{DomainId: 3, BatchId: 5},
		ProtocolVersion:   1,
		FirstInputSeq:     100,
		LastInputSeq:      163,
		PreStateRoot:      hashFromByte(0x10),
		PostStateRoot:     hashFromByte(0x11),
		InputRoot:         hashFromByte(0x12),
		ReceiptRoot:       hashFromByte(0x13),
		EventRoot:         hashFromByte(0x14),
		OutboxRoot:        hashFromByte(0x15),
		DaHash:            hashFromByte(0x16),
		DaMode:            1,
		ExecutorPillar:    "pillar-a",
		SubmittedAtHeight: 12345,
		FinalizeAtHeight:  12400,
		AssetFlowSummary:  []byte("asset-flow-summary-blob"),
		ProofData:         []byte{},
		State:             BatchStateSubmitted,
	}
	if err := entry.Save(memDb); err != nil {
		t.Fatal(err)
	}

	got, err := GetBatchEntry(memDb, 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	if got.DomainId != 3 || got.BatchId != 5 {
		t.Fatalf("key mismatch: %+v", got.BatchEntryKey)
	}
	if got.ProtocolVersion != entry.ProtocolVersion || got.FirstInputSeq != entry.FirstInputSeq ||
		got.LastInputSeq != entry.LastInputSeq || got.PreStateRoot != entry.PreStateRoot ||
		got.PostStateRoot != entry.PostStateRoot || got.InputRoot != entry.InputRoot ||
		got.ReceiptRoot != entry.ReceiptRoot || got.EventRoot != entry.EventRoot ||
		got.OutboxRoot != entry.OutboxRoot || got.DaHash != entry.DaHash ||
		got.DaMode != entry.DaMode || got.ExecutorPillar != entry.ExecutorPillar ||
		got.SubmittedAtHeight != entry.SubmittedAtHeight || got.FinalizeAtHeight != entry.FinalizeAtHeight || got.State != entry.State {
		t.Fatalf("field mismatch: got %+v want %+v", got, entry)
	}
	if string(got.AssetFlowSummary) != string(entry.AssetFlowSummary) {
		t.Fatalf("assetFlowSummary mismatch: got %q want %q", got.AssetFlowSummary, entry.AssetFlowSummary)
	}

	if _, err := GetBatchEntry(memDb, 3, 6); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent, got %v", err)
	}
}

func TestConservationCounters_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	domainId := uint32(1)
	assetId := hashFromByte(0x20)

	// zero-default on absence
	zero, err := GetConservationCounters(memDb, domainId, assetId)
	if err != nil {
		t.Fatal(err)
	}
	if zero.TotalDeposited.Sign() != 0 || zero.PendingWithdrawalReserve.Sign() != 0 || zero.TotalReleased.Sign() != 0 {
		t.Fatalf("expected zero-default counters, got %+v", zero)
	}

	counters := &ConservationCounters{
		DomainId:                 domainId,
		AssetId:                  assetId,
		TotalDeposited:           big.NewInt(1000),
		PendingWithdrawalReserve: big.NewInt(200),
		TotalReleased:            big.NewInt(300),
	}
	if err := counters.Save(memDb); err != nil {
		t.Fatal(err)
	}

	got, err := GetConservationCounters(memDb, domainId, assetId)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalDeposited.Cmp(counters.TotalDeposited) != 0 ||
		got.PendingWithdrawalReserve.Cmp(counters.PendingWithdrawalReserve) != 0 ||
		got.TotalReleased.Cmp(counters.TotalReleased) != 0 {
		t.Fatalf("counters mismatch: got %+v want %+v", got, counters)
	}
}

func TestReceivedInputCounter_AssignedIndex_CumulativeDeposit_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	// received input counter: zero-default, then saved
	zeroCounter, err := GetReceivedInputCounter(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if zeroCounter.Count != 0 {
		t.Fatalf("expected zero-default counter, got %v", zeroCounter.Count)
	}
	counter := &ReceivedInputCounter{Count: 42}
	if err := counter.Save(memDb); err != nil {
		t.Fatal(err)
	}
	gotCounter, err := GetReceivedInputCounter(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if gotCounter.Count != 42 {
		t.Fatalf("counter mismatch: got %v want 42", gotCounter.Count)
	}

	// assigned index record: absent -> ErrDataNonExistent, then round-trip
	domainId := uint32(2)
	assetId := hashFromByte(0x30)
	if _, err := GetAssignedIndexRecord(memDb, domainId, 41); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent, got %v", err)
	}
	record := &AssignedIndexRecord{
		AssignedIndexRecordKey: AssignedIndexRecordKey{DomainId: domainId, GlobalInputIndex: 41},
		AssetId:                assetId,
		Amount:                 big.NewInt(555),
	}
	if err := record.Save(memDb); err != nil {
		t.Fatal(err)
	}
	gotRecord, err := GetAssignedIndexRecord(memDb, domainId, 41)
	if err != nil {
		t.Fatal(err)
	}
	if gotRecord.AssetId != assetId || gotRecord.Amount.Cmp(big.NewInt(555)) != 0 {
		t.Fatalf("assigned index record mismatch: %+v", gotRecord)
	}

	// cumulative deposit total: zero-default, then round-trip
	zeroTotal, err := GetCumulativeDepositTotal(memDb, domainId, assetId)
	if err != nil {
		t.Fatal(err)
	}
	if zeroTotal.CumulativeDeposited.Sign() != 0 {
		t.Fatalf("expected zero-default total, got %v", zeroTotal.CumulativeDeposited)
	}
	total := &CumulativeDepositTotal{DomainId: domainId, AssetId: assetId, CumulativeDeposited: big.NewInt(999)}
	if err := total.Save(memDb); err != nil {
		t.Fatal(err)
	}
	gotTotal, err := GetCumulativeDepositTotal(memDb, domainId, assetId)
	if err != nil {
		t.Fatal(err)
	}
	if gotTotal.CumulativeDeposited.Cmp(big.NewInt(999)) != 0 {
		t.Fatalf("total mismatch: got %v want 999", gotTotal.CumulativeDeposited)
	}
}

func TestReleaseQueue_FIFOOrder(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	domainId := uint32(4)

	const n = 5
	for i := 0; i < n; i++ {
		entry := &ReleaseQueueEntry{
			Recipient: addressFromByte(byte(0x40 + i)),
			AssetId:   hashFromByte(byte(0x50 + i)),
			Amount:    big.NewInt(int64(1000 + i)),
			OutboxId:  hashFromByte(byte(0x60 + i)),
		}
		if err := EnqueueRelease(memDb, domainId, entry); err != nil {
			t.Fatal(err)
		}
	}

	meta, err := GetReleaseQueueMeta(memDb, domainId)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Head != 0 || meta.Tail != n {
		t.Fatalf("unexpected meta: %+v", meta)
	}

	for i := 0; i < n; i++ {
		entry, err := DequeueRelease(memDb, domainId)
		if err != nil {
			t.Fatal(err)
		}
		if entry.RegistrationIndex != uint64(i) {
			t.Fatalf("FIFO order broken: got index %d want %d", entry.RegistrationIndex, i)
		}
		if entry.Amount.Cmp(big.NewInt(int64(1000+i))) != 0 {
			t.Fatalf("FIFO amount mismatch at index %d: got %v", i, entry.Amount)
		}
	}

	if _, err := DequeueRelease(memDb, domainId); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent on empty queue, got %v", err)
	}
}

func TestDeployRecord_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	domainId := uint32(9)
	codeHash := hashFromByte(0x70)

	if _, err := GetDeployRecord(memDb, domainId, addressFromByte(0x72), codeHash); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent, got %v", err)
	}

	record := &DeployRecord{
		DeployRecordKey: DeployRecordKey{DomainId: domainId, Deployer: addressFromByte(0x72), CodeHash: codeHash},
		MetadataHash:    hashFromByte(0x71),
		TotalSize:       123456,
		ChunkCount:      9,
		StartHeight:     555,
		ReceivedCount:   4,
	}
	if err := record.Save(memDb); err != nil {
		t.Fatal(err)
	}

	got, err := GetDeployRecord(memDb, domainId, addressFromByte(0x72), codeHash)
	if err != nil {
		t.Fatal(err)
	}
	if got.CodeHash != codeHash || got.MetadataHash != record.MetadataHash ||
		got.TotalSize != record.TotalSize || got.ChunkCount != record.ChunkCount ||
		got.Deployer != record.Deployer || got.StartHeight != record.StartHeight ||
		got.ReceivedCount != record.ReceivedCount {
		t.Fatalf("deploy record mismatch: got %+v want %+v", got, record)
	}
}

func TestKeyPrefixes_NoCollisions(t *testing.T) {
	prefixes := map[string][]byte{
		"domainRecord":           domainRecordPrefix,
		"batchEntry":             batchEntryPrefix,
		"inputSequenceCursor":    inputSequenceCursorPrefix,
		"receivedInputCounter":   receivedInputCounterKey,
		"assignedIndexRecord":    assignedIndexRecordPrefix,
		"cumulativeDepositTotal": cumulativeDepositTotalPrefix,
		"conservationCounters":   conservationCountersPrefix,
		"releaseQueueEntry":      releaseQueueEntryPrefix,
		"releaseQueueMeta":       releaseQueueMetaPrefix,
		"deployRecord":           deployRecordPrefix,
		"processedOutbox":        processedOutboxPrefix,
		"executorSetMember":      executorSetMemberPrefix,
		"peripheryConfig":        peripheryConfigKey,
		"pendingFee":             pendingFeePrefix,
		"feeEpoch":               feeEpochPrefix,
		"feeCounters":            feeCountersKey,
		"assetRegistry":          assetRegistryPrefix,
		"messageState":           messageStatePrefix,
		"fraudRefereeRegistry":   fraudRefereeRegistryPrefix,
		"validityKeyRegistry":    validityKeyRegistryPrefix,
		"globalPauseState":       globalPauseStateKey,
		"domainPauseState":       domainPauseStatePrefix,
		"escapeHatch":            escapeHatchPrefix,
		"domainConfig":           domainConfigPrefix,
		"assetConfig":            assetConfigPrefix,
		"approvedDAModeSet":      approvedDAModeSetPrefix,
		"domainCounter":          domainCounterKey,
		"deployChunk":            deployChunkPrefix,
		"deployStartQueueEntry":  deployStartQueueEntryPrefix,
		"deployStartQueueMeta":   deployStartQueueMetaKey,
		"domainInputCounter":     domainInputCounterPrefix,
		"domainInputOrdinal":     domainInputOrdinalPrefix,
		"releaseRateLimit":       releaseRateLimitPrefix,
		"batchWithdrawalTotal":   batchWithdrawalTotalPrefix,
	}

	seen := make(map[byte]string, len(prefixes))
	for name, prefix := range prefixes {
		if len(prefix) != 1 {
			t.Fatalf("%s: expected single-byte prefix, got %v", name, prefix)
		}
		b := prefix[0]
		if other, ok := seen[b]; ok {
			t.Fatalf("prefix collision: %s and %s both use 0x%02X", name, other, b)
		}
		seen[b] = name
	}
}

func TestInputSequenceCursor_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	domainId := uint32(6)

	zero, err := GetInputSequenceCursor(memDb, domainId)
	if err != nil {
		t.Fatal(err)
	}
	if *zero != (InputSequenceCursor{DomainId: domainId}) {
		t.Fatalf("expected zero-default cursor, got %+v", zero)
	}

	cursor := &InputSequenceCursor{DomainId: domainId, ConsumedInputCount: 77, NextBatchId: 5, RecordsSweptThrough: 3, SweptInputCount: 40}
	if err := cursor.Save(memDb); err != nil {
		t.Fatal(err)
	}
	got, err := GetInputSequenceCursor(memDb, domainId)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *cursor {
		t.Fatalf("cursor mismatch: got %+v want %+v", got, cursor)
	}
}

func TestDomainInputCounter_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	zero, err := GetDomainInputCounter(memDb, 3)
	if err != nil {
		t.Fatal(err)
	}
	if zero.Count != 0 {
		t.Fatalf("expected zero-default counter, got %d", zero.Count)
	}

	if err := (&DomainInputCounter{DomainId: 3, Count: 9}).Save(memDb); err != nil {
		t.Fatal(err)
	}
	got, err := GetDomainInputCounter(memDb, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 9 {
		t.Fatalf("counter mismatch: got %d want 9", got.Count)
	}
	other, err := GetDomainInputCounter(memDb, 4)
	if err != nil {
		t.Fatal(err)
	}
	if other.Count != 0 {
		t.Fatalf("expected domain 4 untouched, got %d", other.Count)
	}
}

func TestDomainInputOrdinal_RoundTripAndDelete(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	if _, err := GetDomainInputOrdinal(memDb, 3, 0); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent, got %v", err)
	}

	ordinal := &DomainInputOrdinal{DomainId: 3, Ordinal: 2, GlobalInputIndex: 41, SourceAccountBlockHash: hashFromByte(0x5A)}
	if err := ordinal.Save(memDb); err != nil {
		t.Fatal(err)
	}
	got, err := GetDomainInputOrdinal(memDb, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *ordinal {
		t.Fatalf("ordinal mismatch: got %+v want %+v", got, ordinal)
	}
	if _, err := GetDomainInputOrdinal(memDb, 4, 2); err != constants.ErrDataNonExistent {
		t.Fatalf("expected other domain absent, got %v", err)
	}

	if err := ordinal.Delete(memDb); err != nil {
		t.Fatal(err)
	}
	if _, err := GetDomainInputOrdinal(memDb, 3, 2); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent after delete, got %v", err)
	}
}

func TestAssignedIndexRecord_Delete(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	record := &AssignedIndexRecord{
		AssignedIndexRecordKey: AssignedIndexRecordKey{DomainId: 2, GlobalInputIndex: 8},
		AssetId:                hashFromByte(0x11),
		Amount:                 big.NewInt(5),
	}
	if err := record.Save(memDb); err != nil {
		t.Fatal(err)
	}
	if err := record.Delete(memDb); err != nil {
		t.Fatal(err)
	}
	if _, err := GetAssignedIndexRecord(memDb, 2, 8); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent after delete, got %v", err)
	}
}

func TestBatchEntry_IsFinalized(t *testing.T) {
	batch := &BatchEntry{FinalizeAtHeight: 100}
	if batch.IsFinalized(99) {
		t.Fatal("expected not finalized at FinalizeAtHeight-1")
	}
	if !batch.IsFinalized(100) || !batch.IsFinalized(101) {
		t.Fatal("expected finalized from FinalizeAtHeight")
	}
}

func TestDomainConfig_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	if _, err := GetDomainConfig(memDb, 1); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent, got %v", err)
	}

	first := &DomainConfig{DomainId: 1, WithdrawalDelay: 1234, RuntimeUpgradeDelay: 2345}
	second := &DomainConfig{DomainId: 2, WithdrawalDelay: 11, RuntimeUpgradeDelay: 22}
	if err := first.Save(memDb); err != nil {
		t.Fatal(err)
	}
	if err := second.Save(memDb); err != nil {
		t.Fatal(err)
	}

	for _, want := range []*DomainConfig{first, second} {
		got, err := GetDomainConfig(memDb, want.DomainId)
		if err != nil {
			t.Fatal(err)
		}
		if *got != *want {
			t.Fatalf("domain config mismatch: got %+v want %+v", got, want)
		}
	}
}

func TestAssetConfig_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	assetA, assetB := hashFromByte(0xA1), hashFromByte(0xB2)

	if _, err := GetAssetConfig(memDb, 1, assetA); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent, got %v", err)
	}

	config := &AssetConfig{DomainId: 1, AssetId: assetA, MaxBatchWithdrawal: big.NewInt(56789)}
	if err := config.Save(memDb); err != nil {
		t.Fatal(err)
	}
	got, err := GetAssetConfig(memDb, 1, assetA)
	if err != nil {
		t.Fatal(err)
	}
	if got.DomainId != 1 || got.AssetId != assetA || got.MaxBatchWithdrawal.Cmp(big.NewInt(56789)) != 0 {
		t.Fatalf("asset config mismatch: got %+v", got)
	}

	if _, err := GetAssetConfig(memDb, 1, assetB); err != constants.ErrDataNonExistent {
		t.Fatalf("expected per-asset isolation, got %v", err)
	}
	if _, err := GetAssetConfig(memDb, 2, assetA); err != constants.ErrDataNonExistent {
		t.Fatalf("expected per-domain isolation, got %v", err)
	}
}

func TestApprovedDAModeSet_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	empty, err := GetApprovedDAModeSet(memDb, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Modes) != 0 {
		t.Fatalf("expected empty set, got %+v", empty)
	}

	set := &ApprovedDAModeSet{DomainId: 1, Modes: []byte{DAModeBestEffort}}
	if err := set.Save(memDb); err != nil {
		t.Fatal(err)
	}
	got, err := GetApprovedDAModeSet(memDb, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.DomainId != 1 || string(got.Modes) != string(set.Modes) {
		t.Fatalf("mode set mismatch: got %+v", got)
	}

	other, err := GetApprovedDAModeSet(memDb, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Modes) != 0 {
		t.Fatalf("expected per-domain isolation, got %+v", other)
	}
}

func TestLastDomainId(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	last, err := GetLastDomainId(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if last != 0 {
		t.Fatalf("expected 0 when absent, got %d", last)
	}

	if err := SetLastDomainId(memDb, 5); err != nil {
		t.Fatal(err)
	}
	last, err = GetLastDomainId(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if last != 5 {
		t.Fatalf("expected 5, got %d", last)
	}
}

func TestAssetRegistryEntry_RoundTrip_FullFields(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	assetId := hashFromByte(0xC3)

	entry := &AssetRegistryEntry{
		DomainId:                 3,
		AssetId:                  assetId,
		CustodyMode:              1,
		ForeignChainId:           []byte("chain"),
		ForeignAssetRef:          []byte("asset-ref"),
		DecimalsPolicy:           []byte("decimals"),
		ProvenForeignLocks:       big.NewInt(11),
		MintedSupply:             big.NewInt(22),
		BurnedSupply:             big.NewInt(33),
		AuthorizedForeignRelease: big.NewInt(44),
	}
	if err := entry.Save(memDb); err != nil {
		t.Fatal(err)
	}
	got, err := GetAssetRegistryEntry(memDb, 3, assetId)
	if err != nil {
		t.Fatal(err)
	}
	if got.CustodyMode != 1 ||
		string(got.ForeignChainId) != "chain" || string(got.ForeignAssetRef) != "asset-ref" ||
		string(got.DecimalsPolicy) != "decimals" ||
		got.ProvenForeignLocks.Cmp(big.NewInt(11)) != 0 || got.MintedSupply.Cmp(big.NewInt(22)) != 0 ||
		got.BurnedSupply.Cmp(big.NewInt(33)) != 0 || got.AuthorizedForeignRelease.Cmp(big.NewInt(44)) != 0 {
		t.Fatalf("asset registry entry mismatch: got %+v want %+v", got, entry)
	}
}

func TestPauseState_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	// absent decodes to {false,false}, no error
	globalZero, err := GetGlobalPauseState(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if globalZero.SubmitPaused || globalZero.RelayPaused {
		t.Fatalf("expected zero-default global pause state, got %+v", globalZero)
	}
	domainZero, err := GetDomainPauseState(memDb, 7)
	if err != nil {
		t.Fatal(err)
	}
	if domainZero.SubmitPaused || domainZero.RelayPaused {
		t.Fatalf("expected zero-default domain pause state, got %+v", domainZero)
	}

	global := &PauseState{SubmitPaused: true, RelayPaused: false}
	if err := global.SaveGlobal(memDb); err != nil {
		t.Fatal(err)
	}
	gotGlobal, err := GetGlobalPauseState(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if !gotGlobal.SubmitPaused || gotGlobal.RelayPaused {
		t.Fatalf("global pause state mismatch: got %+v", gotGlobal)
	}

	domain := &PauseState{SubmitPaused: false, RelayPaused: true}
	if err := domain.SaveDomain(memDb, 7); err != nil {
		t.Fatal(err)
	}
	gotDomain, err := GetDomainPauseState(memDb, 7)
	if err != nil {
		t.Fatal(err)
	}
	if gotDomain.SubmitPaused || !gotDomain.RelayPaused {
		t.Fatalf("domain pause state mismatch: got %+v", gotDomain)
	}

	// another domain remains unaffected
	otherDomain, err := GetDomainPauseState(memDb, 8)
	if err != nil {
		t.Fatal(err)
	}
	if otherDomain.SubmitPaused || otherDomain.RelayPaused {
		t.Fatalf("expected domain 8 unaffected, got %+v", otherDomain)
	}
}

func TestIsSubmitPaused_IsRelayPaused(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	submitPaused, err := IsSubmitPaused(memDb, 1)
	if err != nil {
		t.Fatal(err)
	}
	if submitPaused {
		t.Fatal("expected SUBMIT not paused before any pause")
	}
	relayPaused, err := IsRelayPaused(memDb, 1)
	if err != nil {
		t.Fatal(err)
	}
	if relayPaused {
		t.Fatal("expected RELAY not paused before any pause")
	}

	// global SUBMIT pause affects any domain, RELAY stays independent
	global := &PauseState{SubmitPaused: true}
	if err := global.SaveGlobal(memDb); err != nil {
		t.Fatal(err)
	}
	if paused, err := IsSubmitPaused(memDb, 1); err != nil || !paused {
		t.Fatalf("expected SUBMIT paused globally, got %v err %v", paused, err)
	}
	if paused, err := IsRelayPaused(memDb, 1); err != nil || paused {
		t.Fatalf("expected RELAY unaffected by global SUBMIT pause, got %v err %v", paused, err)
	}

	// per-domain RELAY pause only affects that domain
	domain := &PauseState{RelayPaused: true}
	if err := domain.SaveDomain(memDb, 2); err != nil {
		t.Fatal(err)
	}
	if paused, err := IsRelayPaused(memDb, 2); err != nil || !paused {
		t.Fatalf("expected RELAY paused for domain 2, got %v err %v", paused, err)
	}
	if paused, err := IsRelayPaused(memDb, 3); err != nil || paused {
		t.Fatalf("expected RELAY unaffected for domain 3, got %v err %v", paused, err)
	}
	if paused, err := IsSubmitPaused(memDb, 3); err != nil || !paused {
		t.Fatalf("expected SUBMIT still paused globally for domain 3, got %v err %v", paused, err)
	}
}

func TestEscapeHatchRecord_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	domainId := uint32(3)

	if _, err := GetEscapeHatchRecord(memDb, domainId); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent, got %v", err)
	}

	record := &EscapeHatchRecord{DomainId: domainId, TriggeredAtHeight: 4242}
	if err := record.Save(memDb); err != nil {
		t.Fatal(err)
	}
	got, err := GetEscapeHatchRecord(memDb, domainId)
	if err != nil {
		t.Fatal(err)
	}
	if got.DomainId != domainId || got.TriggeredAtHeight != record.TriggeredAtHeight {
		t.Fatalf("escape hatch record mismatch: got %+v want %+v", got, record)
	}
}

func TestProcessedOutbox_Membership(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	outboxId := hashFromByte(0x80)

	processed, err := IsOutboxProcessed(memDb, outboxId)
	if err != nil {
		t.Fatal(err)
	}
	if processed {
		t.Fatal("expected outbox to be unprocessed before it is set")
	}

	if err := SetProcessedOutbox(memDb, outboxId); err != nil {
		t.Fatal(err)
	}

	processed, err = IsOutboxProcessed(memDb, outboxId)
	if err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("expected outbox to be processed after it is set")
	}
}

func TestAssignedIndexRecord_Fee_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	withFee := &AssignedIndexRecord{
		AssignedIndexRecordKey: AssignedIndexRecordKey{DomainId: 1, GlobalInputIndex: 7},
		AssetId:                hashFromByte(0x31),
		Amount:                 big.NewInt(10),
		Fee:                    big.NewInt(1234),
	}
	if err := withFee.Save(memDb); err != nil {
		t.Fatal(err)
	}
	got, err := GetAssignedIndexRecord(memDb, 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.Fee.Cmp(big.NewInt(1234)) != 0 {
		t.Fatalf("fee mismatch: got %v want 1234", got.Fee)
	}

	nilFee := &AssignedIndexRecord{
		AssignedIndexRecordKey: AssignedIndexRecordKey{DomainId: 1, GlobalInputIndex: 8},
		AssetId:                hashFromByte(0x31),
		Amount:                 big.NewInt(10),
	}
	if err := nilFee.Save(memDb); err != nil {
		t.Fatal(err)
	}
	got, err = GetAssignedIndexRecord(memDb, 1, 8)
	if err != nil {
		t.Fatal(err)
	}
	if got.Fee == nil || got.Fee.Sign() != 0 {
		t.Fatalf("expected zero fee for a nil Fee, got %v", got.Fee)
	}
}

func TestExecutorSet_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	set, err := GetExecutorSet(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if len(set) != 0 {
		t.Fatalf("expected empty set, got %d", len(set))
	}

	a := &ExecutorSetMember{PillarName: "pillar-a", ExecutorAddress: addressFromByte(0x01), RegistrationHeight: 10, Status: ExecutorStatusActive}
	b := &ExecutorSetMember{PillarName: "pillar-b", ExecutorAddress: addressFromByte(0x02), RegistrationHeight: 11, Status: ExecutorStatusLapsed}
	if err := a.Save(memDb); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(memDb); err != nil {
		t.Fatal(err)
	}
	set, err = GetExecutorSet(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if len(set) != 2 {
		t.Fatalf("expected 2 members, got %d", len(set))
	}
	byName := map[string]*ExecutorSetMember{}
	for _, m := range set {
		byName[m.PillarName] = m
	}
	for _, want := range []*ExecutorSetMember{a, b} {
		got := byName[want.PillarName]
		if got == nil || *got != *want {
			t.Fatalf("member mismatch: got %+v want %+v", got, want)
		}
	}

	if err := a.Delete(memDb); err != nil {
		t.Fatal(err)
	}
	set, err = GetExecutorSet(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if len(set) != 1 || set[0].PillarName != "pillar-b" {
		t.Fatalf("expected only pillar-b after delete, got %+v", set)
	}
}

func TestExecutorSet_SkipsEmptyValues(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	a := &ExecutorSetMember{PillarName: "pillar-a", ExecutorAddress: addressFromByte(0x01), Status: ExecutorStatusActive}
	if err := a.Save(memDb); err != nil {
		t.Fatal(err)
	}
	if err := memDb.Put(ExecutorSetMemberKey("pillar-tombstone"), []byte{}); err != nil {
		t.Fatal(err)
	}
	set, err := GetExecutorSet(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if len(set) != 1 || set[0].PillarName != "pillar-a" {
		t.Fatalf("expected the tombstone to be skipped, got %+v", set)
	}
}

func TestPeripheryConfig_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	if _, err := GetPeripheryConfig(memDb); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent, got %v", err)
	}
	config := &PeripheryConfig{ZnnPerByteOfBytecode: big.NewInt(5), ZnnPerByteOfState: big.NewInt(9)}
	if err := config.Save(memDb); err != nil {
		t.Fatal(err)
	}
	got, err := GetPeripheryConfig(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if got.ZnnPerByteOfBytecode.Cmp(big.NewInt(5)) != 0 || got.ZnnPerByteOfState.Cmp(big.NewInt(9)) != 0 {
		t.Fatalf("config mismatch: %+v", got)
	}
}

func TestPendingFee_RoundTrip_EpochIsolation(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	absent, err := GetPendingFee(memDb, 4, "pillar-a")
	if err != nil {
		t.Fatal(err)
	}
	if absent.Amount.Sign() != 0 || absent.Epoch != 4 || absent.PillarName != "pillar-a" {
		t.Fatalf("expected zero default, got %+v", absent)
	}

	for _, p := range []*PendingFee{
		{Epoch: 4, PillarName: "pillar-a", Amount: big.NewInt(100)},
		{Epoch: 4, PillarName: "pillar-b", Amount: big.NewInt(200)},
		{Epoch: 5, PillarName: "pillar-a", Amount: big.NewInt(300)},
	} {
		if err := p.Save(memDb); err != nil {
			t.Fatal(err)
		}
	}
	got, err := GetPendingFee(memDb, 4, "pillar-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Amount.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("amount mismatch: got %v want 100", got.Amount)
	}

	list, err := GetPendingFeesForEpoch(memDb, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 records for epoch 4, got %d", len(list))
	}
	total := new(big.Int)
	for _, p := range list {
		if p.Epoch != 4 {
			t.Fatalf("epoch 4 listing returned epoch %d", p.Epoch)
		}
		total.Add(total, p.Amount)
	}
	if total.Cmp(big.NewInt(300)) != 0 {
		t.Fatalf("expected epoch 4 total 300, got %v", total)
	}

	if err := list[0].Delete(memDb); err != nil {
		t.Fatal(err)
	}
	list, err = GetPendingFeesForEpoch(memDb, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 record after delete, got %d", len(list))
	}
}

func TestFeeEpochQueue_HeadAndRoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	if _, err := GetFeeEpochQueueHead(memDb); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent on empty queue, got %v", err)
	}
	if _, err := GetFeeEpoch(memDb, 9); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent, got %v", err)
	}

	for _, e := range []*FeeEpoch{
		{Epoch: 300, FinalityHeight: 1},
		{Epoch: 2, FinalityHeight: 22},
		{Epoch: 70000, FinalityHeight: 3},
	} {
		if err := e.Save(memDb); err != nil {
			t.Fatal(err)
		}
	}
	head, err := GetFeeEpochQueueHead(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if head.Epoch != 2 || head.FinalityHeight != 22 {
		t.Fatalf("expected head epoch 2 / finality 22, got %+v", head)
	}
	if err := head.Delete(memDb); err != nil {
		t.Fatal(err)
	}
	head, err = GetFeeEpochQueueHead(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if head.Epoch != 300 {
		t.Fatalf("expected head epoch 300 after delete, got %d", head.Epoch)
	}
}

func TestFeeEpochQueue_HeadSkipsEmptyValues(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	if err := memDb.Put(getFeeEpochKey(1), []byte{}); err != nil {
		t.Fatal(err)
	}
	if _, err := GetFeeEpochQueueHead(memDb); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent with only a tombstone, got %v", err)
	}
	if _, err := GetFeeEpoch(memDb, 1); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent for a tombstoned epoch, got %v", err)
	}
	if err := (&FeeEpoch{Epoch: 2, FinalityHeight: 5}).Save(memDb); err != nil {
		t.Fatal(err)
	}
	head, err := GetFeeEpochQueueHead(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if head.Epoch != 2 {
		t.Fatalf("expected head epoch 2 past the tombstone, got %d", head.Epoch)
	}
}

func TestPendingFees_SkipsEmptyValues(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	if err := memDb.Put(getPendingFeeKey(3, "pillar-gone"), []byte{}); err != nil {
		t.Fatal(err)
	}
	if err := (&PendingFee{Epoch: 3, PillarName: "pillar-a", Amount: big.NewInt(1)}).Save(memDb); err != nil {
		t.Fatal(err)
	}
	list, err := GetPendingFeesForEpoch(memDb, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].PillarName != "pillar-a" {
		t.Fatalf("expected the tombstone to be skipped, got %+v", list)
	}
}

func TestFeeCounters_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	zero, err := GetFeeCounters(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if zero.TotalFeesCollected.Sign() != 0 || zero.TotalFeesBurned.Sign() != 0 ||
		zero.TotalFeesCredited.Sign() != 0 || zero.TotalRewardsCollected.Sign() != 0 {
		t.Fatalf("expected zero counters, got %+v", zero)
	}
	counters := &FeeCounters{
		TotalFeesCollected:    big.NewInt(1),
		TotalFeesBurned:       big.NewInt(2),
		TotalFeesCredited:     big.NewInt(3),
		TotalRewardsCollected: big.NewInt(4),
	}
	if err := counters.Save(memDb); err != nil {
		t.Fatal(err)
	}
	got, err := GetFeeCounters(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalFeesCollected.Int64() != 1 || got.TotalFeesBurned.Int64() != 2 ||
		got.TotalFeesCredited.Int64() != 3 || got.TotalRewardsCollected.Int64() != 4 {
		t.Fatalf("counters mismatch: %+v", got)
	}
}

func TestIsRelayPausedAnywhere(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	expect := func(want bool, label string) {
		t.Helper()
		got, err := IsRelayPausedAnywhere(memDb)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s: got %v want %v", label, got, want)
		}
	}

	expect(false, "no records")

	if err := (&PauseState{SubmitPaused: true}).SaveDomain(memDb, 3); err != nil {
		t.Fatal(err)
	}
	expect(false, "domain with only SubmitPaused")

	if err := (&PauseState{RelayPaused: true}).SaveDomain(memDb, 4); err != nil {
		t.Fatal(err)
	}
	expect(true, "domain RELAY paused")

	if err := (&PauseState{}).SaveDomain(memDb, 4); err != nil {
		t.Fatal(err)
	}
	expect(false, "domain unpaused after a pause")

	if err := (&PauseState{RelayPaused: true}).SaveGlobal(memDb); err != nil {
		t.Fatal(err)
	}
	expect(true, "global RELAY paused")
}

func TestIsRelayPausedAnywhere_SkipsEmptyDomainValue(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	if err := memDb.Put(DomainPauseStateKey(5), []byte{}); err != nil {
		t.Fatal(err)
	}
	got, err := IsRelayPausedAnywhere(memDb)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("expected an empty domain pause value to be ignored")
	}
}

func TestAssetId_CanonicalMapping(t *testing.T) {
	id := AssetIdFromZTS(types.ZnnTokenStandard)
	if id.String() != "0000000000000000000000000000000000000000000014e66318c6318c6318c6" {
		t.Fatalf("unexpected asset id %s", id.String())
	}
	zts, err := ZTSFromAssetId(id)
	if err != nil || zts != types.ZnnTokenStandard {
		t.Fatalf("expected ZNN, got %v, %v", zts, err)
	}
}

func TestAssetId_RejectsNonCanonical(t *testing.T) {
	for _, hexId := range []string{
		"0100000000000000000000000000000000000000000014e66318c6318c6318c6",
		"14e66318c6318c6318c600000000000000000000000000000000000000000000",
	} {
		id := types.HexToHashPanic(hexId)
		if _, err := ZTSFromAssetId(id); err != constants.ErrForbiddenParam {
			t.Fatalf("expected ErrForbiddenParam for %s, got %v", hexId, err)
		}
	}
}

func TestReleaseRateLimit_RoundTripAndDefault(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())

	absent, err := GetReleaseRateLimit(memDb, 3)
	if err != nil {
		t.Fatal(err)
	}
	if absent.DomainId != 3 || absent.Height != 0 {
		t.Fatalf("expected zero-default rate limit, got %+v", absent)
	}

	if err := (&ReleaseRateLimit{DomainId: 3, Height: 912}).Save(memDb); err != nil {
		t.Fatal(err)
	}
	if err := (&ReleaseRateLimit{DomainId: 4, Height: 77}).Save(memDb); err != nil {
		t.Fatal(err)
	}
	got, err := GetReleaseRateLimit(memDb, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got.DomainId != 3 || got.Height != 912 {
		t.Fatalf("rate limit mismatch: %+v", got)
	}
	other, err := GetReleaseRateLimit(memDb, 4)
	if err != nil {
		t.Fatal(err)
	}
	if other.Height != 77 {
		t.Fatalf("domains must not share a rate limit record: %+v", other)
	}
}

func TestBatchWithdrawalTotal_RoundTripAndDefault(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	asset := hashFromByte(0x31)

	absent, err := GetBatchWithdrawalTotal(memDb, 1, 2, asset)
	if err != nil {
		t.Fatal(err)
	}
	if absent.ContractAmount.Sign() != 0 || absent.RefundAmount.Sign() != 0 ||
		absent.DomainId != 1 || absent.BatchId != 2 || absent.AssetId != asset {
		t.Fatalf("expected zero-default total, got %+v", absent)
	}

	big256 := new(big.Int).Lsh(big.NewInt(1), 200)
	total := &BatchWithdrawalTotal{
		DomainId: 1, BatchId: 2, AssetId: asset,
		ContractAmount: big256,
		RefundAmount:   big.NewInt(5),
	}
	if err := total.Save(memDb); err != nil {
		t.Fatal(err)
	}
	got, err := GetBatchWithdrawalTotal(memDb, 1, 2, asset)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContractAmount.Cmp(big256) != 0 || got.RefundAmount.Cmp(big.NewInt(5)) != 0 ||
		got.DomainId != 1 || got.BatchId != 2 || got.AssetId != asset {
		t.Fatalf("total mismatch: %+v", got)
	}
}

func TestBatchWithdrawalTotal_KeyedPerDomainBatchAndAsset(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	assetA, assetB := hashFromByte(0x41), hashFromByte(0x42)

	save := func(domain uint32, batch uint64, asset types.Hash, contract, refund int64) {
		t.Helper()
		total := &BatchWithdrawalTotal{
			DomainId: domain, BatchId: batch, AssetId: asset,
			ContractAmount: big.NewInt(contract), RefundAmount: big.NewInt(refund),
		}
		if err := total.Save(memDb); err != nil {
			t.Fatal(err)
		}
	}
	save(1, 0, assetA, 10, 1)
	save(1, 1, assetA, 20, 2)
	save(1, 0, assetB, 30, 3)
	save(2, 0, assetA, 40, 4)

	cases := []struct {
		domain   uint32
		batch    uint64
		asset    types.Hash
		contract int64
		refund   int64
	}{
		{1, 0, assetA, 10, 1},
		{1, 1, assetA, 20, 2},
		{1, 0, assetB, 30, 3},
		{2, 0, assetA, 40, 4},
		{2, 1, assetA, 0, 0},
	}
	for _, c := range cases {
		got, err := GetBatchWithdrawalTotal(memDb, c.domain, c.batch, c.asset)
		if err != nil {
			t.Fatal(err)
		}
		if got.ContractAmount.Cmp(big.NewInt(c.contract)) != 0 || got.RefundAmount.Cmp(big.NewInt(c.refund)) != 0 {
			t.Fatalf("(%d,%d,%x): got %v/%v want %d/%d", c.domain, c.batch, c.asset[:2], got.ContractAmount, got.RefundAmount, c.contract, c.refund)
		}
	}
}

func TestExportedKeyBuilders_MatchPrefixTable(t *testing.T) {
	h := hashFromByte(0xAA)
	hHex := strings.Repeat("aa", 32)
	a := addressFromByte(0x72)
	aHex := hex.EncodeToString(a.Bytes())
	pillarHex := hex.EncodeToString(types.NewHash([]byte("pillar-a")).Bytes())
	record := DeployRecordKey{DomainId: 7, Deployer: a, CodeHash: h}

	for _, row := range []struct {
		name    string
		got     []byte
		wantHex string
	}{
		{"InputSequenceCursorKey", InputSequenceCursorKey(7), "03" + "00000007"},
		{"DomainInputCounterKey", DomainInputCounterKey(7), "1f" + "00000007"},
		{"DomainInputOrdinalKey", DomainInputOrdinalKey(7, 9), "20" + "00000007" + "0000000000000009"},
		{"BatchEntryKey", (&BatchEntryKey{DomainId: 7, BatchId: 9}).Key(), "02" + "00000007" + "0000000000000009"},
		{"ReleaseQueueMetaKey", ReleaseQueueMetaKey(7), "09" + "00000007"},
		{"AssetConfigKey", AssetConfigKey(7, h), "16" + "00000007" + hHex},
		{"GlobalPauseStateKey", GlobalPauseStateKey(), "12"},
		{"DomainPauseStateKey", DomainPauseStateKey(7), "13" + "00000007"},
		{"ExecutorSetMemberKey", ExecutorSetMemberKey("pillar-a"), "0c" + pillarHex},
		{"ProcessedOutboxKey", ProcessedOutboxKey(h), "0b" + hHex},
		{"DomainRecordKey", (&DomainRecordKey{DomainId: 7}).Key(), "01" + "00000007"},
		{"AssignedIndexRecordKey", (&AssignedIndexRecordKey{DomainId: 7, GlobalInputIndex: 9}).Key(), "05" + "00000007" + "0000000000000009"},
		{"ReleaseQueueEntryKey", (&ReleaseQueueEntryKey{DomainId: 7, RegistrationIndex: 9}).Key(), "08" + "00000007" + "0000000000000009"},
		{"DeployRecordKey", record.Key(), "0a" + "00000007" + aHex + hHex},
		{"DeployChunkKey", (&DeployChunkKey{DeployRecordKey: record, StartHeight: 11, ChunkIndex: 3}).Key(), "1c" + "00000007" + aHex + hHex + "000000000000000b" + "00000003"},
	} {
		if got := hex.EncodeToString(row.got); got != row.wantHex {
			t.Errorf("%s: got %s want %s", row.name, got, row.wantHex)
		}
	}
}

func TestExportedVariableNames_DecodeEntries(t *testing.T) {
	h := hashFromByte(0xAA)
	a := addressFromByte(0x72)
	newDb := func() db.DB { return db.DisableNotFound(db.NewMemDB()) }
	raw := func(t *testing.T, store db.DB, key []byte) []byte {
		t.Helper()
		data, err := store.Get(key)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) == 0 {
			t.Fatalf("no value stored under %x", key)
		}
		return data
	}

	t.Run("DomainRecord", func(t *testing.T) {
		store := newDb()
		in := &DomainRecord{DomainRecordKey: DomainRecordKey{DomainId: 7}, StfSpecHash: h, MinExecutors: 1, Status: DomainStatusPaused, ProfileConfig: []byte{}, ChainBinding: []byte{}, ValueCaps: []byte{}}
		if err := in.Save(store); err != nil {
			t.Fatal(err)
		}
		out := new(DomainRecord)
		if err := ABISettlement.UnpackVariable(out, DomainRecordVariableName, raw(t, store, in.Key())); err != nil {
			t.Fatal(err)
		}
		if out.StfSpecHash != h || out.MinExecutors != 1 || out.Status != DomainStatusPaused {
			t.Fatalf("got %+v", out)
		}
	})
	t.Run("BatchEntry", func(t *testing.T) {
		store := newDb()
		in := &BatchEntry{BatchEntryKey: BatchEntryKey{DomainId: 7, BatchId: 9}, LastInputSeq: 12, PostStateRoot: h, ExecutorPillar: "pillar-a", FinalizeAtHeight: 30, AssetFlowSummary: []byte{}, ProofData: []byte{}}
		if err := in.Save(store); err != nil {
			t.Fatal(err)
		}
		out := new(BatchEntry)
		if err := ABISettlement.UnpackVariable(out, BatchEntryVariableName, raw(t, store, in.Key())); err != nil {
			t.Fatal(err)
		}
		if out.LastInputSeq != 12 || out.PostStateRoot != h || out.ExecutorPillar != "pillar-a" || out.FinalizeAtHeight != 30 {
			t.Fatalf("got %+v", out)
		}
	})
	t.Run("InputSequenceCursor", func(t *testing.T) {
		store := newDb()
		in := &InputSequenceCursor{DomainId: 7, ConsumedInputCount: 1, NextBatchId: 2, RecordsSweptThrough: 3, SweptInputCount: 4}
		if err := in.Save(store); err != nil {
			t.Fatal(err)
		}
		out := new(InputSequenceCursor)
		if err := ABISettlement.UnpackVariable(out, InputSequenceCursorVariableName, raw(t, store, InputSequenceCursorKey(7))); err != nil {
			t.Fatal(err)
		}
		out.DomainId = 7
		if *out != *in {
			t.Fatalf("got %+v want %+v", out, in)
		}
	})
	t.Run("DomainInputCounter", func(t *testing.T) {
		store := newDb()
		in := &DomainInputCounter{DomainId: 7, Count: 5}
		if err := in.Save(store); err != nil {
			t.Fatal(err)
		}
		out := new(DomainInputCounter)
		if err := ABISettlement.UnpackVariable(out, DomainInputCounterVariableName, raw(t, store, DomainInputCounterKey(7))); err != nil {
			t.Fatal(err)
		}
		if out.Count != 5 {
			t.Fatalf("got %+v", out)
		}
	})
	t.Run("DomainInputOrdinal", func(t *testing.T) {
		store := newDb()
		in := &DomainInputOrdinal{DomainId: 7, Ordinal: 9, GlobalInputIndex: 41, SourceAccountBlockHash: h}
		if err := in.Save(store); err != nil {
			t.Fatal(err)
		}
		out := new(DomainInputOrdinal)
		if err := ABISettlement.UnpackVariable(out, DomainInputOrdinalVariableName, raw(t, store, DomainInputOrdinalKey(7, 9))); err != nil {
			t.Fatal(err)
		}
		if out.GlobalInputIndex != 41 || out.SourceAccountBlockHash != h {
			t.Fatalf("got %+v", out)
		}
	})
	t.Run("ReleaseQueueMeta", func(t *testing.T) {
		store := newDb()
		in := &ReleaseQueueMeta{DomainId: 7, Head: 2, Tail: 5}
		if err := in.Save(store); err != nil {
			t.Fatal(err)
		}
		out := new(ReleaseQueueMeta)
		if err := ABISettlement.UnpackVariable(out, ReleaseQueueMetaVariableName, raw(t, store, ReleaseQueueMetaKey(7))); err != nil {
			t.Fatal(err)
		}
		if out.Head != 2 || out.Tail != 5 {
			t.Fatalf("got %+v", out)
		}
	})
	t.Run("ExecutorSetMember", func(t *testing.T) {
		store := newDb()
		in := &ExecutorSetMember{PillarName: "pillar-a", ExecutorAddress: a, RegistrationHeight: 12, Status: ExecutorStatusActive}
		if err := in.Save(store); err != nil {
			t.Fatal(err)
		}
		out := new(ExecutorSetMember)
		if err := ABISettlement.UnpackVariable(out, ExecutorSetMemberVariableName, raw(t, store, ExecutorSetMemberKey("pillar-a"))); err != nil {
			t.Fatal(err)
		}
		if *out != *in {
			t.Fatalf("got %+v want %+v", out, in)
		}
	})
	t.Run("AssetConfig", func(t *testing.T) {
		store := newDb()
		in := &AssetConfig{DomainId: 7, AssetId: h, MaxBatchWithdrawal: big.NewInt(1000)}
		if err := in.Save(store); err != nil {
			t.Fatal(err)
		}
		out := new(AssetConfig)
		if err := ABISettlement.UnpackVariable(out, AssetConfigVariableName, raw(t, store, AssetConfigKey(7, h))); err != nil {
			t.Fatal(err)
		}
		if out.MaxBatchWithdrawal.Cmp(big.NewInt(1000)) != 0 {
			t.Fatalf("got %+v", out)
		}
	})
	t.Run("PauseState", func(t *testing.T) {
		store := newDb()
		if err := (&PauseState{SubmitPaused: true}).SaveGlobal(store); err != nil {
			t.Fatal(err)
		}
		if err := (&PauseState{RelayPaused: true}).SaveDomain(store, 7); err != nil {
			t.Fatal(err)
		}
		global := new(PauseState)
		if err := ABISettlement.UnpackVariable(global, PauseStateVariableName, raw(t, store, GlobalPauseStateKey())); err != nil {
			t.Fatal(err)
		}
		domain := new(PauseState)
		if err := ABISettlement.UnpackVariable(domain, PauseStateVariableName, raw(t, store, DomainPauseStateKey(7))); err != nil {
			t.Fatal(err)
		}
		if !global.SubmitPaused || global.RelayPaused || domain.SubmitPaused || !domain.RelayPaused {
			t.Fatalf("got global %+v domain %+v", global, domain)
		}
	})
}
