package definition

import (
	"math/big"
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
		ExecutorIndex:     0,
		SubmittedAtHeight: 12345,
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
		got.DaMode != entry.DaMode || got.ExecutorIndex != entry.ExecutorIndex ||
		got.SubmittedAtHeight != entry.SubmittedAtHeight || got.State != entry.State {
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

	if _, err := GetDeployRecord(memDb, domainId, codeHash); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent, got %v", err)
	}

	record := &DeployRecord{
		DeployRecordKey: DeployRecordKey{DomainId: domainId, CodeHash: codeHash},
		MetadataHash:    hashFromByte(0x71),
		TotalSize:       123456,
		ChunkCount:      9,
		Deployer:        addressFromByte(0x72),
		StartHeight:     555,
		ReceivedChunks:  []byte{},
	}
	if err := record.Save(memDb); err != nil {
		t.Fatal(err)
	}

	got, err := GetDeployRecord(memDb, domainId, codeHash)
	if err != nil {
		t.Fatal(err)
	}
	if got.CodeHash != codeHash || got.MetadataHash != record.MetadataHash ||
		got.TotalSize != record.TotalSize || got.ChunkCount != record.ChunkCount ||
		got.Deployer != record.Deployer || got.StartHeight != record.StartHeight {
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
		"executorBond":           executorBondPrefix,
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
	if zero.LastConsumedInputSeq != 0 {
		t.Fatalf("expected zero-default cursor, got %v", zero.LastConsumedInputSeq)
	}

	cursor := &InputSequenceCursor{DomainId: domainId, LastConsumedInputSeq: 77}
	if err := cursor.Save(memDb); err != nil {
		t.Fatal(err)
	}
	got, err := GetInputSequenceCursor(memDb, domainId)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastConsumedInputSeq != 77 {
		t.Fatalf("cursor mismatch: got %v want 77", got.LastConsumedInputSeq)
	}
}

func TestExecutorBond_RoundTrip(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	domainId := uint32(11)
	executor := addressFromByte(0x90)

	if _, err := GetExecutorBond(memDb, domainId, executor); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent, got %v", err)
	}

	bond := &ExecutorBond{
		DomainId:  domainId,
		Executor:  executor,
		ZnnAmount: big.NewInt(1000),
		QsrAmount: big.NewInt(2000),
		Status:    ExecutorStatusActive,
	}
	if err := bond.Save(memDb); err != nil {
		t.Fatal(err)
	}
	got, err := GetExecutorBond(memDb, domainId, executor)
	if err != nil {
		t.Fatal(err)
	}
	if got.DomainId != domainId || got.Executor != bond.Executor ||
		got.ZnnAmount.Cmp(bond.ZnnAmount) != 0 ||
		got.QsrAmount.Cmp(bond.QsrAmount) != 0 || got.Status != bond.Status {
		t.Fatalf("executor bond mismatch: got %+v want %+v", got, bond)
	}

	if err := bond.Delete(memDb); err != nil {
		t.Fatal(err)
	}
	if _, err := GetExecutorBond(memDb, domainId, executor); err != constants.ErrDataNonExistent {
		t.Fatalf("expected ErrDataNonExistent after delete, got %v", err)
	}
}

// Registering a replacement executor leaves the outgoing executor's bond record
// readable under its own address, which is what RevokeBond needs to return that
// executor's legs.
func TestExecutorBond_ReplacementDoesNotStrandOutgoingBond(t *testing.T) {
	memDb := db.DisableNotFound(db.NewMemDB())
	domainId := uint32(11)
	outgoing := addressFromByte(0x90)
	incoming := addressFromByte(0x91)

	outgoingBond := &ExecutorBond{
		DomainId:  domainId,
		Executor:  outgoing,
		ZnnAmount: big.NewInt(1000),
		QsrAmount: big.NewInt(2000),
		Status:    ExecutorStatusActive,
	}
	if err := outgoingBond.Save(memDb); err != nil {
		t.Fatal(err)
	}

	incomingBond := &ExecutorBond{
		DomainId:  domainId,
		Executor:  incoming,
		ZnnAmount: big.NewInt(3000),
		QsrAmount: big.NewInt(4000),
		Status:    ExecutorStatusRegistered,
	}
	if err := incomingBond.Save(memDb); err != nil {
		t.Fatal(err)
	}

	got, err := GetExecutorBond(memDb, domainId, outgoing)
	if err != nil {
		t.Fatalf("outgoing bond stranded by the replacement: %v", err)
	}
	if got.Executor != outgoing || got.ZnnAmount.Cmp(big.NewInt(1000)) != 0 ||
		got.QsrAmount.Cmp(big.NewInt(2000)) != 0 || got.Status != ExecutorStatusActive {
		t.Fatalf("outgoing bond overwritten: got %+v", got)
	}

	got, err = GetExecutorBond(memDb, domainId, incoming)
	if err != nil {
		t.Fatal(err)
	}
	if got.Executor != incoming || got.ZnnAmount.Cmp(big.NewInt(3000)) != 0 {
		t.Fatalf("incoming bond mismatch: got %+v", got)
	}

	// The same executor address under a different domain is a different record.
	if _, err := GetExecutorBond(memDb, domainId+1, outgoing); err != constants.ErrDataNonExistent {
		t.Fatalf("expected per-domain isolation, got %v", err)
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
