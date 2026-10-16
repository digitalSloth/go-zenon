package embedded

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
)

// TestDumpSettlementSdkRpcFixture writes the JSON the Settlement RPC types
// marshal to, for the TypeScript SDK models to decode. It runs only when
// SETTLEMENT_SDK_RPC_FIXTURE_OUT names an output file.
func TestDumpSettlementSdkRpcFixture(t *testing.T) {
	out := os.Getenv("SETTLEMENT_SDK_RPC_FIXTURE_OUT")
	if out == "" {
		t.Skip("SETTLEMENT_SDK_RPC_FIXTURE_OUT not set")
	}

	h := func(n byte) types.Hash { return types.NewHash([]byte{n}) }
	addr := types.ParseAddressPanic("z1qxemdeddedxplasmaxxxxxxxxxxxxxxxxsctrp")

	domain := &SettlementDomainInfo{
		DomainId: 3, DomainClass: 0, RuntimeKind: 0, StfSpecHash: h(1), InputSource: 0,
		ExecProfile: 2, ForeignProfile: 0, ProfileConfig: []byte{1, 2, 3}, ChainBinding: []byte{},
		MinExecutors: 2, ProposerPolicy: 0, FinalityModel: 2, Status: 0,
		WithdrawalDelay: 100800, RuntimeUpgradeDelay: 201600, ApprovedDAModes: []int{0},
		PauseState:         &SettlementPauseState{SubmitPaused: false, RelayPaused: true},
		InputCursor:        &SettlementInputCursor{ConsumedInputCount: 7, NextBatchId: 2, RecordsSweptThrough: 1, SweptInputCount: 3},
		ReceivedInputCount: 11,
		EscapeHatch:        &SettlementEscapeHatch{TriggeredAtHeight: 555},
	}
	domainBare := &SettlementDomainInfo{
		DomainId: 4, StfSpecHash: h(2), ProfileConfig: []byte{}, ChainBinding: []byte{},
		ApprovedDAModes: []int{0},
	}
	batch := &SettlementBatch{
		DomainId: 3, BatchId: 1, ProtocolVersion: 1, FirstInputSeq: 4, LastInputSeq: 9,
		PreStateRoot: h(3), PostStateRoot: h(4), InputRoot: h(5), ReceiptRoot: h(6), EventRoot: h(7),
		OutboxRoot: h(8), DaHash: h(9), DaMode: 0, ExecutorPillar: "pillar-one",
		SubmittedAtHeight: 1000, FinalizeAtHeight: 2000, AssetFlowSummary: []byte{0, 0, 0, 0},
		ProofData: []byte{}, State: 0, IsFinalized: true,
	}
	record := &SettlementDeployRecord{
		DomainId: 3, Deployer: addr, CodeHash: h(10), MetadataHash: h(11), TotalSize: 40000,
		ChunkCount: 3, StartHeight: 100, ReceivedCount: 2, ExpiresAtHeight: 2980, IsExpired: false,
	}
	asset := &SettlementAssetInfo{
		DomainId: 3, AssetId: h(12), MaxBatchWithdrawal: "100000000000000", CumulativeDeposited: "500",
		TotalDeposited: "400", PendingWithdrawalReserve: "30", TotalReleased: "20",
	}

	fixture := map[string]interface{}{
		"domainInfo":      domain,
		"domainInfoBare":  domainBare,
		"allDomains":      &SettlementDomainInfoList{Count: 2, List: []*SettlementDomainInfo{domain, domainBare}},
		"emptyDomains":    &SettlementDomainInfoList{Count: 0, List: []*SettlementDomainInfo{}},
		"batch":           batch,
		"batches":         &SettlementBatchList{Count: 1, List: []*SettlementBatch{batch}},
		"deployRecord":    record,
		"deployRecords":   &SettlementDeployRecordList{Count: 1, List: []*SettlementDeployRecord{record}},
		"pauseState":      &SettlementPauseInfo{Global: &SettlementPauseState{SubmitPaused: true}, Domains: []*SettlementDomainPauseState{{DomainId: 3, RelayPaused: true}}},
		"peripheryConfig": &SettlementPeripheryConfig{ZnnPerByteOfBytecode: "100", ZnnPerByteOfState: "200"},
		"executorSet":     &SettlementExecutorList{Count: 1, List: []*SettlementExecutor{{PillarName: "pillar-one", ExecutorAddress: addr, RegistrationHeight: 50, Status: 0}}},
		"feeInfo":         &SettlementFeeInfo{TotalFeesCollected: "1000", TotalFeesBurned: "300", TotalFeesCredited: "700", TotalRewardsCollected: "100", PendingEpochHead: &SettlementFeeEpoch{Epoch: 4, FinalityHeight: 9000}},
		"feeInfoNoHead":   &SettlementFeeInfo{TotalFeesCollected: "0", TotalFeesBurned: "0", TotalFeesCredited: "0", TotalRewardsCollected: "0"},
		"assetInfo":       asset,
		"releaseQueue":    &SettlementReleaseQueue{Head: 2, Tail: 3, Count: 1, List: []*SettlementReleaseQueueEntry{{RegistrationIndex: 2, Recipient: addr, AssetId: h(12), Amount: "77", OutboxId: h(13)}}},
		"timeChallenges":  &SettlementTimeChallengeList{Count: 1, List: []*SettlementTimeChallenge{{MethodName: "SetAssetConfig/3/" + h(12).String(), ParamsHash: h(14), ChallengeStartHeight: 800, DueHeight: 1801}}},
		"emptyChallenges": &SettlementTimeChallengeList{Count: 0, List: []*SettlementTimeChallenge{}},
	}
	raw, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
