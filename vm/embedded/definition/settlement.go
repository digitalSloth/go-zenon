package definition

import (
	"encoding/binary"
	"math/big"
	"strings"

	"github.com/pkg/errors"

	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/abi"
	"github.com/zenon-network/go-zenon/vm/constants"
)

const (
	jsonSettlement = `
	[
		{"type":"function","name":"Update","inputs":[]},
		{"type":"function","name":"ReleaseWithdrawals","inputs":[
			{"name":"domainId","type":"uint32"}]},
		{"type":"function","name":"CollectReward","inputs":[]},
		{"type":"function","name":"RegisterExecutor","inputs":[
			{"name":"pillarName","type":"string"},
			{"name":"executorAddress","type":"address"}]},
		{"type":"function","name":"SetPeripheryConfig","inputs":[
			{"name":"znnPerByteOfBytecode","type":"uint256"},
			{"name":"znnPerByteOfState","type":"uint256"}]},

		{"type":"function","name":"RegisterDomain","inputs":[
			{"name":"domainId","type":"uint32"},
			{"name":"domainClass","type":"uint8"},
			{"name":"runtimeKind","type":"uint8"},
			{"name":"stfSpecHash","type":"hash"},
			{"name":"inputSource","type":"uint8"},
			{"name":"execProfile","type":"uint8"},
			{"name":"foreignProfile","type":"uint8"},
			{"name":"profileConfig","type":"bytes"},
			{"name":"chainBinding","type":"bytes"},
			{"name":"minExecutors","type":"uint8"},
			{"name":"proposerPolicy","type":"uint8"},
			{"name":"valueCaps","type":"bytes"},
			{"name":"finalityModel","type":"uint8"},
			{"name":"status","type":"uint8"},
			{"name":"withdrawalDelay","type":"uint64"},
			{"name":"runtimeUpgradeDelay","type":"uint64"}]},
		{"type":"function","name":"SetDomainConfig","inputs":[
			{"name":"domainId","type":"uint32"},
			{"name":"withdrawalDelay","type":"uint64"},
			{"name":"runtimeUpgradeDelay","type":"uint64"}]},
		{"type":"function","name":"SetAssetConfig","inputs":[
			{"name":"domainId","type":"uint32"},
			{"name":"assetId","type":"hash"},
			{"name":"maxBatchWithdrawal","type":"uint256"}]},
		{"type":"function","name":"SetApprovedDAModes","inputs":[
			{"name":"domainId","type":"uint32"},
			{"name":"modes","type":"bytes"}]},
		{"type":"function","name":"Pause","inputs":[
			{"name":"domainId","type":"uint32"},
			{"name":"isGlobal","type":"bool"},
			{"name":"scope","type":"uint8"},
			{"name":"active","type":"bool"}]},
		{"type":"function","name":"InitiateEscapeHatch","inputs":[
			{"name":"domainId","type":"uint32"}]},
		{"type":"function","name":"Deposit","inputs":[
			{"name":"domainId","type":"uint32"},
			{"name":"targetContract","type":"hash"},
			{"name":"plasmaLimit","type":"uint64"}]},
		{"type":"function","name":"SubmitBatch","inputs":[
			{"name":"commitment","type":"bytes"}]},
		{"type":"function","name":"RelayMessage","inputs":[
			{"name":"domainId","type":"uint32"},
			{"name":"batchId","type":"uint64"},
			{"name":"message","type":"bytes"},
			{"name":"inclusionProof","type":"bytes"}]},
		{"type":"function","name":"DeployContractStart","inputs":[
			{"name":"domainId","type":"uint32"},
			{"name":"codeHash","type":"hash"},
			{"name":"metadataHash","type":"hash"},
			{"name":"totalSize","type":"uint64"},
			{"name":"chunkCount","type":"uint32"},
			{"name":"metadata","type":"bytes"}]},
		{"type":"function","name":"DeployContractChunk","inputs":[
			{"name":"domainId","type":"uint32"},
			{"name":"codeHash","type":"hash"},
			{"name":"chunkIndex","type":"uint32"},
			{"name":"chunkHash","type":"hash"},
			{"name":"chunkData","type":"bytes"}]},
		{"type":"function","name":"DeployContractFinalize","inputs":[
			{"name":"domainId","type":"uint32"},
			{"name":"codeHash","type":"hash"}]},
		{"type":"function","name":"CallL2","inputs":[
			{"name":"domainId","type":"uint32"},
			{"name":"targetContract","type":"hash"},
			{"name":"plasmaLimit","type":"uint64"},
			{"name":"payload","type":"bytes"}]},
		{"type":"function","name":"UpgradeContract","inputs":[
			{"name":"domainId","type":"uint32"},
			{"name":"contractId","type":"hash"},
			{"name":"newCodeHash","type":"hash"}]},
		{"type":"function","name":"TransferContractOwnership","inputs":[
			{"name":"domainId","type":"uint32"},
			{"name":"contractId","type":"hash"},
			{"name":"newOwner","type":"address"}]},
		{"type":"function","name":"BuyStorage","inputs":[
			{"name":"domainId","type":"uint32"},
			{"name":"contractId","type":"hash"},
			{"name":"bytes","type":"uint64"}]},

		{"type":"variable","name":"domainRecord","inputs":[
			{"name":"domainClass","type":"uint8"},
			{"name":"runtimeKind","type":"uint8"},
			{"name":"stfSpecHash","type":"hash"},
			{"name":"inputSource","type":"uint8"},
			{"name":"execProfile","type":"uint8"},
			{"name":"foreignProfile","type":"uint8"},
			{"name":"profileConfig","type":"bytes"},
			{"name":"chainBinding","type":"bytes"},
			{"name":"minExecutors","type":"uint8"},
			{"name":"proposerPolicy","type":"uint8"},
			{"name":"valueCaps","type":"bytes"},
			{"name":"finalityModel","type":"uint8"},
			{"name":"status","type":"uint8"}
		]},

		{"type":"variable","name":"batchEntry","inputs":[
			{"name":"protocolVersion","type":"uint16"},
			{"name":"firstInputSeq","type":"uint64"},
			{"name":"lastInputSeq","type":"uint64"},
			{"name":"preStateRoot","type":"hash"},
			{"name":"postStateRoot","type":"hash"},
			{"name":"inputRoot","type":"hash"},
			{"name":"receiptRoot","type":"hash"},
			{"name":"eventRoot","type":"hash"},
			{"name":"outboxRoot","type":"hash"},
			{"name":"daHash","type":"hash"},
			{"name":"daMode","type":"uint8"},
			{"name":"executorPillar","type":"string"},
			{"name":"submittedAtHeight","type":"uint64"},
			{"name":"finalizeAtHeight","type":"uint64"},
			{"name":"assetFlowSummary","type":"bytes"},
			{"name":"proofData","type":"bytes"},
			{"name":"state","type":"uint8"}
		]},

		{"type":"variable","name":"inputSequenceCursor","inputs":[
			{"name":"consumedInputCount","type":"uint64"},
			{"name":"nextBatchId","type":"uint64"},
			{"name":"recordsSweptThrough","type":"uint64"},
			{"name":"sweptInputCount","type":"uint64"}
		]},

		{"type":"variable","name":"domainInputCounter","inputs":[
			{"name":"count","type":"uint64"}
		]},

		{"type":"variable","name":"domainInputOrdinal","inputs":[
			{"name":"globalInputIndex","type":"uint64"},
			{"name":"sourceAccountBlockHash","type":"hash"}
		]},

		{"type":"variable","name":"receivedInputCounter","inputs":[
			{"name":"count","type":"uint64"}
		]},

		{"type":"variable","name":"assignedIndexRecord","inputs":[
			{"name":"assetId","type":"hash"},
			{"name":"amount","type":"uint256"},
			{"name":"fee","type":"uint256"}
		]},

		{"type":"variable","name":"cumulativeDepositTotal","inputs":[
			{"name":"cumulativeDeposited","type":"uint256"}
		]},

		{"type":"variable","name":"conservationCounters","inputs":[
			{"name":"totalDeposited","type":"uint256"},
			{"name":"pendingWithdrawalReserve","type":"uint256"},
			{"name":"totalReleased","type":"uint256"}
		]},

		{"type":"variable","name":"releaseQueueEntry","inputs":[
			{"name":"recipient","type":"address"},
			{"name":"assetId","type":"hash"},
			{"name":"amount","type":"uint256"},
			{"name":"outboxId","type":"hash"}
		]},

		{"type":"variable","name":"releaseQueueMeta","inputs":[
			{"name":"head","type":"uint64"},
			{"name":"tail","type":"uint64"}
		]},

		{"type":"variable","name":"releaseRateLimit","inputs":[
			{"name":"height","type":"uint64"}
		]},

		{"type":"variable","name":"batchWithdrawalTotal","inputs":[
			{"name":"contractAmount","type":"uint256"},
			{"name":"refundAmount","type":"uint256"}
		]},

		{"type":"variable","name":"deployRecord","inputs":[
			{"name":"metadataHash","type":"hash"},
			{"name":"totalSize","type":"uint64"},
			{"name":"chunkCount","type":"uint32"},
			{"name":"startHeight","type":"uint64"},
			{"name":"receivedCount","type":"uint32"}
		]},

		{"type":"variable","name":"deployChunk","inputs":[
			{"name":"chunkHash","type":"hash"}
		]},

		{"type":"variable","name":"deployStartQueueEntry","inputs":[
			{"name":"domainId","type":"uint32"},
			{"name":"deployer","type":"address"},
			{"name":"codeHash","type":"hash"},
			{"name":"startHeight","type":"uint64"}
		]},

		{"type":"variable","name":"deployStartQueueMeta","inputs":[
			{"name":"head","type":"uint64"},
			{"name":"tail","type":"uint64"}
		]},

		{"type":"variable","name":"executorSetMember","inputs":[
			{"name":"pillarName","type":"string"},
			{"name":"executorAddress","type":"address"},
			{"name":"registrationHeight","type":"uint64"},
			{"name":"status","type":"uint8"}
		]},

		{"type":"variable","name":"peripheryConfig","inputs":[
			{"name":"znnPerByteOfBytecode","type":"uint256"},
			{"name":"znnPerByteOfState","type":"uint256"}
		]},

		{"type":"variable","name":"pendingFee","inputs":[
			{"name":"pillarName","type":"string"},
			{"name":"amount","type":"uint256"}
		]},

		{"type":"variable","name":"feeEpoch","inputs":[
			{"name":"finalityHeight","type":"uint64"}
		]},

		{"type":"variable","name":"feeCounters","inputs":[
			{"name":"totalFeesCollected","type":"uint256"},
			{"name":"totalFeesBurned","type":"uint256"},
			{"name":"totalFeesCredited","type":"uint256"},
			{"name":"totalRewardsCollected","type":"uint256"}
		]},

		{"type":"variable","name":"domainConfig","inputs":[
			{"name":"withdrawalDelay","type":"uint64"},
			{"name":"runtimeUpgradeDelay","type":"uint64"}
		]},

		{"type":"variable","name":"assetConfig","inputs":[
			{"name":"maxBatchWithdrawal","type":"uint256"}
		]},

		{"type":"variable","name":"approvedDAModeSet","inputs":[
			{"name":"modes","type":"bytes"}
		]},

		{"type":"variable","name":"domainCounter","inputs":[
			{"name":"lastDomainId","type":"uint32"}
		]},

		{"type":"variable","name":"pauseState","inputs":[
			{"name":"submitPaused","type":"bool"},
			{"name":"relayPaused","type":"bool"}
		]},

		{"type":"variable","name":"escapeHatchRecord","inputs":[
			{"name":"triggeredAtHeight","type":"uint64"}
		]},

		{"type":"variable","name":"assetRegistryEntry","inputs":[
			{"name":"custodyMode","type":"uint8"},
			{"name":"foreignChainId","type":"bytes"},
			{"name":"foreignAssetRef","type":"bytes"},
			{"name":"decimalsPolicy","type":"bytes"},
			{"name":"provenForeignLocks","type":"uint256"},
			{"name":"mintedSupply","type":"uint256"},
			{"name":"burnedSupply","type":"uint256"},
			{"name":"authorizedForeignRelease","type":"uint256"}
		]},

		{"type":"variable","name":"messageState","inputs":[
			{"name":"timeout","type":"uint64"},
			{"name":"ackPolicy","type":"uint8"},
			{"name":"state","type":"uint8"}
		]},

		{"type":"variable","name":"fraudRefereeRegistryEntry","inputs":[
			{"name":"reserved","type":"bytes"}
		]},

		{"type":"variable","name":"validityKeyRegistryEntry","inputs":[
			{"name":"reserved","type":"bytes"}
		]}
	]`

	SettlementUpdateMethodName             = "Update"
	SettlementReleaseWithdrawalsMethodName = "ReleaseWithdrawals"
	SettlementCollectRewardMethodName      = "CollectReward"

	SettlementRegisterExecutorMethodName   = "RegisterExecutor"
	SettlementSetPeripheryConfigMethodName = "SetPeripheryConfig"
	// declared, not yet wired
	SettlementInitiateEscapeHatchMethodName = "InitiateEscapeHatch"

	SettlementRegisterDomainMethodName     = "RegisterDomain"
	SettlementSetDomainConfigMethodName    = "SetDomainConfig"
	SettlementSetAssetConfigMethodName     = "SetAssetConfig"
	SettlementSetApprovedDAModesMethodName = "SetApprovedDAModes"
	SettlementDepositMethodName            = "Deposit"
	SettlementSubmitBatchMethodName        = "SubmitBatch"
	SettlementRelayMessageMethodName       = "RelayMessage"

	SettlementPauseMethodName  = "Pause"
	SettlementCallL2MethodName = "CallL2"

	SettlementDeployContractStartMethodName    = "DeployContractStart"
	SettlementDeployContractChunkMethodName    = "DeployContractChunk"
	SettlementDeployContractFinalizeMethodName = "DeployContractFinalize"
	SettlementUpgradeContractMethodName        = "UpgradeContract"

	SettlementTransferContractOwnershipMethodName = "TransferContractOwnership"
	SettlementBuyStorageMethodName                = "BuyStorage"

	DomainRecordVariableName              = "domainRecord"
	BatchEntryVariableName                = "batchEntry"
	InputSequenceCursorVariableName       = "inputSequenceCursor"
	receivedInputCounterVariableName      = "receivedInputCounter"
	DomainInputCounterVariableName        = "domainInputCounter"
	DomainInputOrdinalVariableName        = "domainInputOrdinal"
	assignedIndexRecordVariableName       = "assignedIndexRecord"
	cumulativeDepositTotalVariableName    = "cumulativeDepositTotal"
	conservationCountersVariableName      = "conservationCounters"
	releaseQueueEntryVariableName         = "releaseQueueEntry"
	ReleaseQueueMetaVariableName          = "releaseQueueMeta"
	releaseRateLimitVariableName          = "releaseRateLimit"
	batchWithdrawalTotalVariableName      = "batchWithdrawalTotal"
	deployRecordVariableName              = "deployRecord"
	deployChunkVariableName               = "deployChunk"
	deployStartQueueEntryVariableName     = "deployStartQueueEntry"
	deployStartQueueMetaVariableName      = "deployStartQueueMeta"
	ExecutorSetMemberVariableName         = "executorSetMember"
	peripheryConfigVariableName           = "peripheryConfig"
	pendingFeeVariableName                = "pendingFee"
	feeEpochVariableName                  = "feeEpoch"
	feeCountersVariableName               = "feeCounters"
	domainConfigVariableName              = "domainConfig"
	AssetConfigVariableName               = "assetConfig"
	approvedDAModeSetVariableName         = "approvedDAModeSet"
	domainCounterVariableName             = "domainCounter"
	PauseStateVariableName                = "pauseState"
	escapeHatchRecordVariableName         = "escapeHatchRecord"
	assetRegistryEntryVariableName        = "assetRegistryEntry"
	messageStateVariableName              = "messageState"
	fraudRefereeRegistryEntryVariableName = "fraudRefereeRegistryEntry"
	validityKeyRegistryEntryVariableName  = "validityKeyRegistryEntry"
)

// DomainRecord.domainClass
const (
	DomainClassExecution uint8 = iota
	DomainClassBridge
	DomainClassMessaging
)

// DomainRecord.runtimeKind
const (
	RuntimeKindWASM uint8 = iota
)

// DomainRecord.inputSource
const (
	InputSourceL1Native uint8 = iota
	InputSourceL1Relayed
	// value 2 is retired and must not be reassigned
)

// DomainRecord.execProfile
const (
	ExecProfileAttestation uint8 = iota
	ExecProfileOptimistic
	ExecProfileZkValidity
)

// DomainRecord.foreignProfile
const (
	ForeignProfileNone uint8 = iota
	ForeignProfileCommittee
	ForeignProfileLightClient
	ForeignProfileZkConsensus
)

// DomainRecord.proposerPolicy
const (
	ProposerPolicySingle uint8 = iota
	ProposerPolicyRandomBackup
	// reserved (permissionless / Phase 3)
	ProposerPolicyStakeWeighted
)

// DomainRecord.finalityModel
const (
	FinalityModelDelay uint8 = iota
	FinalityModelChallengeWindow
	FinalityModelProofVerified
)

// DomainRecord.status
const (
	DomainStatusActive uint8 = iota
	DomainStatusPaused
)

// BatchEntry.daMode
const (
	DAModeBestEffort uint8 = 0
)

// BatchEntry.state. The stored value is the acceptance state: SubmitBatch
// writes BatchStateSubmitted and no code writes BatchStateFinalized.
// BatchEntry.IsFinalized(height) is the authority on finality.
const (
	BatchStateSubmitted uint8 = iota
	BatchStateFinalized
)

// ExecutorSetMember.status
const (
	ExecutorStatusActive uint8 = iota
	// reserved
	ExecutorStatusLapsed
	// reserved
	ExecutorStatusCooldown
)

// Pause scopes
const (
	PauseScopeSubmit uint8 = iota // halts SubmitBatch (Ticket 15)
	PauseScopeRelay               // halts RelayMessage registration, ReleaseWithdrawals release, fee settlement and CollectReward
)

var (
	ABISettlement = abi.JSONToABIContract(strings.NewReader(jsonSettlement))

	// Settlement contract storage key-prefix table. Storage is namespaced to
	// SettlementContract, so prefixes only need uniqueness within this contract.
	// The reused common accessors (GetLastUpdate, RewardDeposit) occupy 129/128.
	_                            = byte(0)      // reserve 0x00
	domainRecordPrefix           = []byte{0x01} // DomainRecord, key: domainId (u32 BE)
	batchEntryPrefix             = []byte{0x02} // batch entry, key: domainId ‖ batchId (u64 BE)
	inputSequenceCursorPrefix    = []byte{0x03} // per-domain input cursor, key: domainId
	receivedInputCounterKey      = []byte{0x04} // global received-input counter, singleton
	assignedIndexRecordPrefix    = []byte{0x05} // key: domainId ‖ globalInputIndex (u64 BE)
	cumulativeDepositTotalPrefix = []byte{0x06} // key: domainId ‖ assetId (32B)
	conservationCountersPrefix   = []byte{0x07} // key: domainId ‖ assetId (32B)
	releaseQueueEntryPrefix      = []byte{0x08} // key: domainId ‖ registrationIndex (u64 BE)
	releaseQueueMetaPrefix       = []byte{0x09} // key: domainId
	deployRecordPrefix           = []byte{0x0A} // key: domainId ‖ deployer (20B) ‖ codeHash (32B)
	processedOutboxPrefix        = []byte{0x0B} // key: outboxId (32B)
	executorSetMemberPrefix      = []byte{0x0C} // ExecutorSetMember, key: hash(pillarName)
	peripheryConfigKey           = []byte{0x0D} // PeripheryConfig, singleton
	assetRegistryPrefix          = []byte{0x0E} // reserved, key: domainId ‖ assetId (32B)
	messageStatePrefix           = []byte{0x0F} // reserved, key: outboxId (32B)
	fraudRefereeRegistryPrefix   = []byte{0x10} // reserved, key: domainId
	validityKeyRegistryPrefix    = []byte{0x11} // reserved, key: verifyingKeyId (32B)
	globalPauseStateKey          = []byte{0x12} // PauseState, singleton
	domainPauseStatePrefix       = []byte{0x13} // PauseState, key: domainId (u32 BE)
	escapeHatchPrefix            = []byte{0x14} // EscapeHatchRecord, key: domainId (u32 BE)
	domainConfigPrefix           = []byte{0x15} // DomainConfig, key: domainId (u32 BE)
	assetConfigPrefix            = []byte{0x16} // AssetConfig, key: domainId ‖ assetId (32B)
	approvedDAModeSetPrefix      = []byte{0x17} // ApprovedDAModeSet, key: domainId (u32 BE)
	domainCounterKey             = []byte{0x18} // last assigned domainId, singleton
	pendingFeePrefix             = []byte{0x19} // key: epoch (u64 BE) ‖ hash(pillarName)
	feeEpochPrefix               = []byte{0x1A} // pending-epoch queue, key: epoch (u64 BE)
	feeCountersKey               = []byte{0x1B} // FeeCounters, singleton
	deployChunkPrefix            = []byte{0x1C} // key: domainId ‖ deployer (20B) ‖ codeHash (32B) ‖ startHeight (u64 BE) ‖ chunkIndex (u32 BE)
	deployStartQueueEntryPrefix  = []byte{0x1D} // key: queueIndex (u64 BE)
	deployStartQueueMetaKey      = []byte{0x1E} // DeployStartQueueMeta, singleton
	domainInputCounterPrefix     = []byte{0x1F} // key: domainId (u32 BE)
	domainInputOrdinalPrefix     = []byte{0x20} // key: domainId ‖ ordinal (u64 BE)
	releaseRateLimitPrefix       = []byte{0x21} // key: domainId (u32 BE)
	batchWithdrawalTotalPrefix   = []byte{0x22} // key: domainId ‖ batchId (u64 BE) ‖ assetId (32B)
)

func uint32ToBytesBE(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}
func bytesBEToUint32(b []byte) uint32 {
	return binary.BigEndian.Uint32(b)
}
func uint64ToBytesBE(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}
func bytesBEToUint64(b []byte) uint64 {
	return binary.BigEndian.Uint64(b)
}

// ============================================================================
// DomainRecord
// ============================================================================

type DomainRecordKey struct {
	DomainId uint32
}

func (k *DomainRecordKey) Key() []byte {
	return common.JoinBytes(domainRecordPrefix, uint32ToBytesBE(k.DomainId))
}

type DomainRecord struct {
	DomainRecordKey
	DomainClass    uint8
	RuntimeKind    uint8
	StfSpecHash    types.Hash
	InputSource    uint8
	ExecProfile    uint8
	ForeignProfile uint8
	ProfileConfig  []byte
	ChainBinding   []byte
	MinExecutors   uint8
	ProposerPolicy uint8
	ValueCaps      []byte
	FinalityModel  uint8
	Status         uint8
}

func (d *DomainRecord) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		DomainRecordVariableName,
		d.DomainClass,
		d.RuntimeKind,
		d.StfSpecHash,
		d.InputSource,
		d.ExecProfile,
		d.ForeignProfile,
		d.ProfileConfig,
		d.ChainBinding,
		d.MinExecutors,
		d.ProposerPolicy,
		d.ValueCaps,
		d.FinalityModel,
		d.Status,
	)
	if err != nil {
		return err
	}
	return context.Put(d.Key(), data)
}
func (d *DomainRecord) Delete(context db.DB) error {
	return context.Delete(d.Key())
}

func isDomainRecordKey(key []byte) bool {
	return len(key) > 0 && key[0] == domainRecordPrefix[0]
}
func unmarshalDomainRecordKey(key []byte) (uint32, error) {
	if !isDomainRecordKey(key) {
		return 0, errors.Errorf("invalid key! Not domain record key")
	}
	return bytesBEToUint32(key[1:5]), nil
}
func parseDomainRecord(key, data []byte) (*DomainRecord, error) {
	if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	}
	record := new(DomainRecord)
	if err := ABISettlement.UnpackVariable(record, DomainRecordVariableName, data); err != nil {
		return nil, err
	}
	domainId, err := unmarshalDomainRecordKey(key)
	if err != nil {
		return nil, err
	}
	record.DomainId = domainId
	return record, nil
}
func GetDomainRecord(context db.DB, domainId uint32) (*DomainRecord, error) {
	key := (&DomainRecordKey{DomainId: domainId}).Key()
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else {
		return parseDomainRecord(key, data)
	}
}

// ============================================================================
// Batch entry
// ============================================================================

type BatchEntryKey struct {
	DomainId uint32
	BatchId  uint64
}

func (k *BatchEntryKey) Key() []byte {
	return common.JoinBytes(batchEntryPrefix, uint32ToBytesBE(k.DomainId), uint64ToBytesBE(k.BatchId))
}

type BatchEntry struct {
	BatchEntryKey
	ProtocolVersion   uint16
	FirstInputSeq     uint64
	LastInputSeq      uint64
	PreStateRoot      types.Hash
	PostStateRoot     types.Hash
	InputRoot         types.Hash
	ReceiptRoot       types.Hash
	EventRoot         types.Hash
	OutboxRoot        types.Hash
	DaHash            types.Hash
	DaMode            uint8
	ExecutorPillar    string
	SubmittedAtHeight uint64
	FinalizeAtHeight  uint64
	AssetFlowSummary  []byte
	ProofData         []byte
	// State is the acceptance state. No code writes BatchStateFinalized;
	// IsFinalized(height) is authoritative.
	State uint8
}

// IsFinalized reports whether the batch is FINALIZED at height.
func (b *BatchEntry) IsFinalized(height uint64) bool {
	return height >= b.FinalizeAtHeight
}

func (b *BatchEntry) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		BatchEntryVariableName,
		b.ProtocolVersion,
		b.FirstInputSeq,
		b.LastInputSeq,
		b.PreStateRoot,
		b.PostStateRoot,
		b.InputRoot,
		b.ReceiptRoot,
		b.EventRoot,
		b.OutboxRoot,
		b.DaHash,
		b.DaMode,
		b.ExecutorPillar,
		b.SubmittedAtHeight,
		b.FinalizeAtHeight,
		b.AssetFlowSummary,
		b.ProofData,
		b.State,
	)
	if err != nil {
		return err
	}
	return context.Put(b.Key(), data)
}
func (b *BatchEntry) Delete(context db.DB) error {
	return context.Delete(b.Key())
}

func isBatchEntryKey(key []byte) bool {
	return len(key) > 0 && key[0] == batchEntryPrefix[0]
}
func unmarshalBatchEntryKey(key []byte) (uint32, uint64, error) {
	if !isBatchEntryKey(key) {
		return 0, 0, errors.Errorf("invalid key! Not batch entry key")
	}
	domainId := bytesBEToUint32(key[1:5])
	batchId := bytesBEToUint64(key[5:13])
	return domainId, batchId, nil
}
func parseBatchEntry(key, data []byte) (*BatchEntry, error) {
	if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	}
	entry := new(BatchEntry)
	if err := ABISettlement.UnpackVariable(entry, BatchEntryVariableName, data); err != nil {
		return nil, err
	}
	domainId, batchId, err := unmarshalBatchEntryKey(key)
	if err != nil {
		return nil, err
	}
	entry.DomainId = domainId
	entry.BatchId = batchId
	return entry, nil
}
func GetBatchEntry(context db.DB, domainId uint32, batchId uint64) (*BatchEntry, error) {
	key := (&BatchEntryKey{DomainId: domainId, BatchId: batchId}).Key()
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else {
		return parseBatchEntry(key, data)
	}
}

// ============================================================================
// Input-sequence cursor (per domain)
// ============================================================================

// InputSequenceCursor reads as all zero when absent; NextBatchId == 0
// means the domain has no accepted batch.
type InputSequenceCursor struct {
	DomainId            uint32
	ConsumedInputCount  uint64 // domain ordinal of the earliest unconsumed input
	NextBatchId         uint64 // batchId the next accepted batch must carry
	RecordsSweptThrough uint64 // batches whose input records have been deleted
	SweptInputCount     uint64 // ordinals whose input records have been deleted
}

func InputSequenceCursorKey(domainId uint32) []byte {
	return common.JoinBytes(inputSequenceCursorPrefix, uint32ToBytesBE(domainId))
}
func (c *InputSequenceCursor) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		InputSequenceCursorVariableName,
		c.ConsumedInputCount,
		c.NextBatchId,
		c.RecordsSweptThrough,
		c.SweptInputCount,
	)
	if err != nil {
		return err
	}
	return context.Put(InputSequenceCursorKey(c.DomainId), data)
}
func GetInputSequenceCursor(context db.DB, domainId uint32) (*InputSequenceCursor, error) {
	key := InputSequenceCursorKey(domainId)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return &InputSequenceCursor{DomainId: domainId}, nil
	} else {
		cursor := new(InputSequenceCursor)
		if err := ABISettlement.UnpackVariable(cursor, InputSequenceCursorVariableName, data); err != nil {
			return nil, err
		}
		cursor.DomainId = domainId
		return cursor, nil
	}
}

// ============================================================================
// Per-domain input counter and ordinal index
// ============================================================================

type DomainInputCounter struct {
	DomainId uint32
	Count    uint64
}

func DomainInputCounterKey(domainId uint32) []byte {
	return common.JoinBytes(domainInputCounterPrefix, uint32ToBytesBE(domainId))
}
func (c *DomainInputCounter) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		DomainInputCounterVariableName,
		c.Count,
	)
	if err != nil {
		return err
	}
	return context.Put(DomainInputCounterKey(c.DomainId), data)
}
func GetDomainInputCounter(context db.DB, domainId uint32) (*DomainInputCounter, error) {
	if data, err := context.Get(DomainInputCounterKey(domainId)); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return &DomainInputCounter{DomainId: domainId}, nil
	} else {
		counter := new(DomainInputCounter)
		if err := ABISettlement.UnpackVariable(counter, DomainInputCounterVariableName, data); err != nil {
			return nil, err
		}
		counter.DomainId = domainId
		return counter, nil
	}
}

// DomainInputOrdinal maps the ordinal of a domain's input to its global
// input index and the hash of the send Settlement accepted.
type DomainInputOrdinal struct {
	DomainId               uint32
	Ordinal                uint64
	GlobalInputIndex       uint64
	SourceAccountBlockHash types.Hash
}

func DomainInputOrdinalKey(domainId uint32, ordinal uint64) []byte {
	return common.JoinBytes(domainInputOrdinalPrefix, uint32ToBytesBE(domainId), uint64ToBytesBE(ordinal))
}
func (o *DomainInputOrdinal) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		DomainInputOrdinalVariableName,
		o.GlobalInputIndex,
		o.SourceAccountBlockHash,
	)
	if err != nil {
		return err
	}
	return context.Put(DomainInputOrdinalKey(o.DomainId, o.Ordinal), data)
}
func (o *DomainInputOrdinal) Delete(context db.DB) error {
	return context.Delete(DomainInputOrdinalKey(o.DomainId, o.Ordinal))
}
func GetDomainInputOrdinal(context db.DB, domainId uint32, ordinal uint64) (*DomainInputOrdinal, error) {
	o := &DomainInputOrdinal{DomainId: domainId, Ordinal: ordinal}
	if data, err := context.Get(DomainInputOrdinalKey(o.DomainId, o.Ordinal)); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	} else if err := ABISettlement.UnpackVariable(o, DomainInputOrdinalVariableName, data); err != nil {
		return nil, err
	}
	return o, nil
}

// ============================================================================
// Global received-input counter (singleton)
// ============================================================================

type ReceivedInputCounter struct {
	Count uint64
}

func (c *ReceivedInputCounter) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		receivedInputCounterVariableName,
		c.Count,
	)
	if err != nil {
		return err
	}
	return context.Put(receivedInputCounterKey, data)
}
func GetReceivedInputCounter(context db.DB) (*ReceivedInputCounter, error) {
	if data, err := context.Get(receivedInputCounterKey); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return &ReceivedInputCounter{Count: 0}, nil
	} else {
		counter := new(ReceivedInputCounter)
		if err := ABISettlement.UnpackVariable(counter, receivedInputCounterVariableName, data); err != nil {
			return nil, err
		}
		return counter, nil
	}
}

// ============================================================================
// Assigned-index record (per domain)
// ============================================================================

type AssignedIndexRecordKey struct {
	DomainId         uint32
	GlobalInputIndex uint64
}

func (k *AssignedIndexRecordKey) Key() []byte {
	return common.JoinBytes(assignedIndexRecordPrefix, uint32ToBytesBE(k.DomainId), uint64ToBytesBE(k.GlobalInputIndex))
}

type AssignedIndexRecord struct {
	AssignedIndexRecordKey
	AssetId types.Hash
	Amount  *big.Int
	Fee     *big.Int
}

func (r *AssignedIndexRecord) Save(context db.DB) error {
	fee := r.Fee
	if fee == nil {
		fee = new(big.Int)
	}
	data, err := ABISettlement.PackVariable(
		assignedIndexRecordVariableName,
		r.AssetId,
		r.Amount,
		fee,
	)
	if err != nil {
		return err
	}
	return context.Put(r.Key(), data)
}
func (r *AssignedIndexRecord) Delete(context db.DB) error {
	return context.Delete(r.Key())
}

func isAssignedIndexRecordKey(key []byte) bool {
	return len(key) > 0 && key[0] == assignedIndexRecordPrefix[0]
}
func unmarshalAssignedIndexRecordKey(key []byte) (uint32, uint64, error) {
	if !isAssignedIndexRecordKey(key) {
		return 0, 0, errors.Errorf("invalid key! Not assigned index record key")
	}
	return bytesBEToUint32(key[1:5]), bytesBEToUint64(key[5:13]), nil
}
func parseAssignedIndexRecord(key, data []byte) (*AssignedIndexRecord, error) {
	if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	}
	record := new(AssignedIndexRecord)
	if err := ABISettlement.UnpackVariable(record, assignedIndexRecordVariableName, data); err != nil {
		return nil, err
	}
	domainId, globalInputIndex, err := unmarshalAssignedIndexRecordKey(key)
	if err != nil {
		return nil, err
	}
	record.DomainId = domainId
	record.GlobalInputIndex = globalInputIndex
	return record, nil
}
func GetAssignedIndexRecord(context db.DB, domainId uint32, globalInputIndex uint64) (*AssignedIndexRecord, error) {
	key := (&AssignedIndexRecordKey{DomainId: domainId, GlobalInputIndex: globalInputIndex}).Key()
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else {
		return parseAssignedIndexRecord(key, data)
	}
}

// ============================================================================
// Cumulative deposit total (per domain, asset)
// ============================================================================

func getCumulativeDepositTotalKey(domainId uint32, assetId types.Hash) []byte {
	return common.JoinBytes(cumulativeDepositTotalPrefix, uint32ToBytesBE(domainId), assetId.Bytes())
}

type CumulativeDepositTotal struct {
	DomainId            uint32
	AssetId             types.Hash
	CumulativeDeposited *big.Int
}

func (c *CumulativeDepositTotal) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		cumulativeDepositTotalVariableName,
		c.CumulativeDeposited,
	)
	if err != nil {
		return err
	}
	return context.Put(getCumulativeDepositTotalKey(c.DomainId, c.AssetId), data)
}
func GetCumulativeDepositTotal(context db.DB, domainId uint32, assetId types.Hash) (*CumulativeDepositTotal, error) {
	key := getCumulativeDepositTotalKey(domainId, assetId)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return &CumulativeDepositTotal{DomainId: domainId, AssetId: assetId, CumulativeDeposited: big.NewInt(0)}, nil
	} else {
		total := new(CumulativeDepositTotal)
		if err := ABISettlement.UnpackVariable(total, cumulativeDepositTotalVariableName, data); err != nil {
			return nil, err
		}
		total.DomainId = domainId
		total.AssetId = assetId
		return total, nil
	}
}

// ============================================================================
// Conservation counters (per domain, asset)
// ============================================================================

// AssetIdFromZTS returns the canonical AssetID of zts: 22 zero bytes, then
// the 10 ZTS bytes in native order.
func AssetIdFromZTS(zts types.ZenonTokenStandard) types.Hash {
	var id types.Hash
	copy(id[22:], zts[:])
	return id
}

// ZTSFromAssetId returns the ZTS held in id[22:32]. It returns
// constants.ErrForbiddenParam when any byte of id[0:22] is non-zero, which
// rejects an asset id that carries a ZTS at the wrong offset.
func ZTSFromAssetId(id types.Hash) (types.ZenonTokenStandard, error) {
	var zts types.ZenonTokenStandard
	for _, b := range id[0:22] {
		if b != 0 {
			return zts, constants.ErrForbiddenParam
		}
	}
	copy(zts[:], id[22:])
	return zts, nil
}

func getConservationCountersKey(domainId uint32, assetId types.Hash) []byte {
	return common.JoinBytes(conservationCountersPrefix, uint32ToBytesBE(domainId), assetId.Bytes())
}

type ConservationCounters struct {
	DomainId                 uint32
	AssetId                  types.Hash
	TotalDeposited           *big.Int
	PendingWithdrawalReserve *big.Int
	TotalReleased            *big.Int
}

func (c *ConservationCounters) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		conservationCountersVariableName,
		c.TotalDeposited,
		c.PendingWithdrawalReserve,
		c.TotalReleased,
	)
	if err != nil {
		return err
	}
	return context.Put(getConservationCountersKey(c.DomainId, c.AssetId), data)
}
func GetConservationCounters(context db.DB, domainId uint32, assetId types.Hash) (*ConservationCounters, error) {
	key := getConservationCountersKey(domainId, assetId)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return &ConservationCounters{
			DomainId:                 domainId,
			AssetId:                  assetId,
			TotalDeposited:           big.NewInt(0),
			PendingWithdrawalReserve: big.NewInt(0),
			TotalReleased:            big.NewInt(0),
		}, nil
	} else {
		counters := new(ConservationCounters)
		if err := ABISettlement.UnpackVariable(counters, conservationCountersVariableName, data); err != nil {
			return nil, err
		}
		counters.DomainId = domainId
		counters.AssetId = assetId
		return counters, nil
	}
}

// ============================================================================
// Withdrawal release queue (per domain FIFO): entries + head/tail meta
// ============================================================================

type ReleaseQueueEntryKey struct {
	DomainId          uint32
	RegistrationIndex uint64
}

func (k *ReleaseQueueEntryKey) Key() []byte {
	return common.JoinBytes(releaseQueueEntryPrefix, uint32ToBytesBE(k.DomainId), uint64ToBytesBE(k.RegistrationIndex))
}

type ReleaseQueueEntry struct {
	ReleaseQueueEntryKey
	Recipient types.Address
	AssetId   types.Hash
	Amount    *big.Int
	OutboxId  types.Hash
}

func (e *ReleaseQueueEntry) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		releaseQueueEntryVariableName,
		e.Recipient,
		e.AssetId,
		e.Amount,
		e.OutboxId,
	)
	if err != nil {
		return err
	}
	return context.Put(e.Key(), data)
}
func (e *ReleaseQueueEntry) Delete(context db.DB) error {
	return context.Delete(e.Key())
}
func parseReleaseQueueEntry(data []byte) (*ReleaseQueueEntry, error) {
	if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	}
	entry := new(ReleaseQueueEntry)
	if err := ABISettlement.UnpackVariable(entry, releaseQueueEntryVariableName, data); err != nil {
		return nil, err
	}
	return entry, nil
}
func GetReleaseQueueEntry(context db.DB, domainId uint32, registrationIndex uint64) (*ReleaseQueueEntry, error) {
	key := (&ReleaseQueueEntryKey{DomainId: domainId, RegistrationIndex: registrationIndex}).Key()
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else {
		entry, err := parseReleaseQueueEntry(data)
		if err != nil {
			return nil, err
		}
		entry.DomainId = domainId
		entry.RegistrationIndex = registrationIndex
		return entry, nil
	}
}

func ReleaseQueueMetaKey(domainId uint32) []byte {
	return common.JoinBytes(releaseQueueMetaPrefix, uint32ToBytesBE(domainId))
}

type ReleaseQueueMeta struct {
	DomainId uint32
	Head     uint64
	Tail     uint64
}

func (m *ReleaseQueueMeta) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		ReleaseQueueMetaVariableName,
		m.Head,
		m.Tail,
	)
	if err != nil {
		return err
	}
	return context.Put(ReleaseQueueMetaKey(m.DomainId), data)
}
func GetReleaseQueueMeta(context db.DB, domainId uint32) (*ReleaseQueueMeta, error) {
	key := ReleaseQueueMetaKey(domainId)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return &ReleaseQueueMeta{DomainId: domainId, Head: 0, Tail: 0}, nil
	} else {
		meta := new(ReleaseQueueMeta)
		if err := ABISettlement.UnpackVariable(meta, ReleaseQueueMetaVariableName, data); err != nil {
			return nil, err
		}
		meta.DomainId = domainId
		return meta, nil
	}
}

// EnqueueRelease appends entry to the tail of the domain's release queue and
// advances the tail cursor. It is a thin storage helper; it performs no
// conservation or release logic.
func EnqueueRelease(context db.DB, domainId uint32, entry *ReleaseQueueEntry) error {
	meta, err := GetReleaseQueueMeta(context, domainId)
	if err != nil {
		return err
	}
	entry.DomainId = domainId
	entry.RegistrationIndex = meta.Tail
	if err := entry.Save(context); err != nil {
		return err
	}
	meta.Tail++
	return meta.Save(context)
}

// PeekRelease returns the entry at the head of the domain's release queue
// without removing it. Returns constants.ErrDataNonExistent when the queue is
// empty.
func PeekRelease(context db.DB, domainId uint32) (*ReleaseQueueEntry, error) {
	meta, err := GetReleaseQueueMeta(context, domainId)
	if err != nil {
		return nil, err
	}
	if meta.Head >= meta.Tail {
		return nil, constants.ErrDataNonExistent
	}
	return GetReleaseQueueEntry(context, domainId, meta.Head)
}

// DequeueRelease removes and returns the entry at the head of the domain's
// release queue, advancing the head cursor. Returns constants.ErrDataNonExistent
// when the queue is empty.
func DequeueRelease(context db.DB, domainId uint32) (*ReleaseQueueEntry, error) {
	entry, err := PeekRelease(context, domainId)
	if err != nil {
		return nil, err
	}
	if err := entry.Delete(context); err != nil {
		return nil, err
	}
	meta, err := GetReleaseQueueMeta(context, domainId)
	if err != nil {
		return nil, err
	}
	meta.Head++
	if err := meta.Save(context); err != nil {
		return nil, err
	}
	return entry, nil
}

// ============================================================================
// Release rate limit and per-batch withdrawal totals
// ============================================================================

// ReleaseRateLimit holds the height of the domain's last accepted
// ReleaseWithdrawals. Height 0 means the domain has none.
type ReleaseRateLimit struct {
	DomainId uint32
	Height   uint64
}

func getReleaseRateLimitKey(domainId uint32) []byte {
	return common.JoinBytes(releaseRateLimitPrefix, uint32ToBytesBE(domainId))
}

func (r *ReleaseRateLimit) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(releaseRateLimitVariableName, r.Height)
	if err != nil {
		return err
	}
	return context.Put(getReleaseRateLimitKey(r.DomainId), data)
}
func GetReleaseRateLimit(context db.DB, domainId uint32) (*ReleaseRateLimit, error) {
	data, err := context.Get(getReleaseRateLimitKey(domainId))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return &ReleaseRateLimit{DomainId: domainId}, nil
	}
	limit := new(ReleaseRateLimit)
	if err := ABISettlement.UnpackVariable(limit, releaseRateLimitVariableName, data); err != nil {
		return nil, err
	}
	limit.DomainId = domainId
	return limit, nil
}

// BatchWithdrawalTotal holds a batch's registered withdrawals of one
// asset: contract-emitted ones and runtime-emitted refunds, apart.
type BatchWithdrawalTotal struct {
	DomainId       uint32
	BatchId        uint64
	AssetId        types.Hash
	ContractAmount *big.Int
	RefundAmount   *big.Int
}

func getBatchWithdrawalTotalKey(domainId uint32, batchId uint64, assetId types.Hash) []byte {
	return common.JoinBytes(batchWithdrawalTotalPrefix, uint32ToBytesBE(domainId), uint64ToBytesBE(batchId), assetId.Bytes())
}

func (t *BatchWithdrawalTotal) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		batchWithdrawalTotalVariableName,
		t.ContractAmount,
		t.RefundAmount,
	)
	if err != nil {
		return err
	}
	return context.Put(getBatchWithdrawalTotalKey(t.DomainId, t.BatchId, t.AssetId), data)
}
func GetBatchWithdrawalTotal(context db.DB, domainId uint32, batchId uint64, assetId types.Hash) (*BatchWithdrawalTotal, error) {
	data, err := context.Get(getBatchWithdrawalTotalKey(domainId, batchId, assetId))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return &BatchWithdrawalTotal{
			DomainId:       domainId,
			BatchId:        batchId,
			AssetId:        assetId,
			ContractAmount: big.NewInt(0),
			RefundAmount:   big.NewInt(0),
		}, nil
	}
	total := new(BatchWithdrawalTotal)
	if err := ABISettlement.UnpackVariable(total, batchWithdrawalTotalVariableName, data); err != nil {
		return nil, err
	}
	total.DomainId = domainId
	total.BatchId = batchId
	total.AssetId = assetId
	return total, nil
}

// ============================================================================
// Deploy record (pending chunked deployment)
// ============================================================================

type DeployRecordKey struct {
	DomainId uint32
	Deployer types.Address
	CodeHash types.Hash
}

func (k *DeployRecordKey) Key() []byte {
	return common.JoinBytes(deployRecordPrefix, uint32ToBytesBE(k.DomainId), k.Deployer.Bytes(), k.CodeHash.Bytes())
}

type DeployRecord struct {
	DeployRecordKey
	MetadataHash types.Hash
	TotalSize    uint64
	ChunkCount   uint32
	StartHeight  uint64
	// ReceivedCount is the number of chunk entries stored for this attempt.
	ReceivedCount uint32
}

func (r *DeployRecord) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		deployRecordVariableName,
		r.MetadataHash,
		r.TotalSize,
		r.ChunkCount,
		r.StartHeight,
		r.ReceivedCount,
	)
	if err != nil {
		return err
	}
	return context.Put(r.Key(), data)
}
func (r *DeployRecord) Delete(context db.DB) error {
	return context.Delete(r.Key())
}
func isDeployRecordKey(key []byte) bool {
	return len(key) > 0 && key[0] == deployRecordPrefix[0]
}
func unmarshalDeployRecordKey(key []byte) (uint32, types.Address, types.Hash, error) {
	if !isDeployRecordKey(key) {
		return 0, types.Address{}, types.ZeroHash, errors.Errorf("invalid key! Not deploy record key")
	}
	domainId := bytesBEToUint32(key[1:5])
	deployer, err := types.BytesToAddress(key[5 : 5+types.AddressSize])
	if err != nil {
		return 0, types.Address{}, types.ZeroHash, err
	}
	codeHash, err := types.BytesToHash(key[5+types.AddressSize : 5+types.AddressSize+types.HashSize])
	if err != nil {
		return 0, types.Address{}, types.ZeroHash, err
	}
	return domainId, deployer, codeHash, nil
}
func parseDeployRecord(key, data []byte) (*DeployRecord, error) {
	if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	}
	record := new(DeployRecord)
	if err := ABISettlement.UnpackVariable(record, deployRecordVariableName, data); err != nil {
		return nil, err
	}
	domainId, deployer, codeHash, err := unmarshalDeployRecordKey(key)
	if err != nil {
		return nil, err
	}
	record.DomainId = domainId
	record.Deployer = deployer
	record.CodeHash = codeHash
	return record, nil
}
func GetDeployRecord(context db.DB, domainId uint32, deployer types.Address, codeHash types.Hash) (*DeployRecord, error) {
	key := (&DeployRecordKey{DomainId: domainId, Deployer: deployer, CodeHash: codeHash}).Key()
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else {
		return parseDeployRecord(key, data)
	}
}

// ============================================================================
// Deploy chunk entries (one per received chunk of an attempt)
// ============================================================================

type DeployChunkKey struct {
	DeployRecordKey
	StartHeight uint64
	ChunkIndex  uint32
}

func (k *DeployChunkKey) Key() []byte {
	return common.JoinBytes(deployChunkPrefix, uint32ToBytesBE(k.DomainId), k.Deployer.Bytes(), k.CodeHash.Bytes(), uint64ToBytesBE(k.StartHeight), uint32ToBytesBE(k.ChunkIndex))
}

type DeployChunk struct {
	DeployChunkKey
	ChunkHash types.Hash
}

func (c *DeployChunk) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(deployChunkVariableName, c.ChunkHash)
	if err != nil {
		return err
	}
	return context.Put(c.Key(), data)
}

// GetDeployChunk returns constants.ErrDataNonExistent when the entry is
// absent.
func GetDeployChunk(context db.DB, key DeployChunkKey) (*DeployChunk, error) {
	data, err := context.Get(key.Key())
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	}
	chunk := new(DeployChunk)
	if err := ABISettlement.UnpackVariable(chunk, deployChunkVariableName, data); err != nil {
		return nil, err
	}
	chunk.DeployChunkKey = key
	return chunk, nil
}

// DeleteDeployChunks deletes every chunk entry of the attempt (record,
// startHeight). It collects the keys first and deletes after the iterator
// is released.
func DeleteDeployChunks(context db.DB, record DeployRecordKey, startHeight uint64) error {
	prefix := common.JoinBytes(deployChunkPrefix, uint32ToBytesBE(record.DomainId), record.Deployer.Bytes(), record.CodeHash.Bytes(), uint64ToBytesBE(startHeight))
	iterator := context.NewIterator(prefix)
	keys := make([][]byte, 0)
	for iterator.Next() {
		if len(iterator.Value()) == 0 {
			continue
		}
		keys = append(keys, append([]byte(nil), iterator.Key()...))
	}
	err := iterator.Error()
	iterator.Release()
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := context.Delete(key); err != nil {
			return err
		}
	}
	return nil
}

// ============================================================================
// Deploy start queue (global FIFO of started attempts): entries + head/tail meta
// ============================================================================

type DeployStartQueueEntry struct {
	Index       uint64 // queue position, from the key, not in the ABI value
	DomainId    uint32
	Deployer    types.Address
	CodeHash    types.Hash
	StartHeight uint64
}

func (e *DeployStartQueueEntry) key() []byte {
	return common.JoinBytes(deployStartQueueEntryPrefix, uint64ToBytesBE(e.Index))
}
func (e *DeployStartQueueEntry) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		deployStartQueueEntryVariableName,
		e.DomainId,
		e.Deployer,
		e.CodeHash,
		e.StartHeight,
	)
	if err != nil {
		return err
	}
	return context.Put(e.key(), data)
}
func (e *DeployStartQueueEntry) Delete(context db.DB) error {
	return context.Delete(e.key())
}
func getDeployStartQueueEntry(context db.DB, index uint64) (*DeployStartQueueEntry, error) {
	key := common.JoinBytes(deployStartQueueEntryPrefix, uint64ToBytesBE(index))
	data, err := context.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	}
	entry := new(DeployStartQueueEntry)
	if err := ABISettlement.UnpackVariable(entry, deployStartQueueEntryVariableName, data); err != nil {
		return nil, err
	}
	entry.Index = index
	return entry, nil
}

type DeployStartQueueMeta struct {
	Head uint64
	Tail uint64
}

func (m *DeployStartQueueMeta) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		deployStartQueueMetaVariableName,
		m.Head,
		m.Tail,
	)
	if err != nil {
		return err
	}
	return context.Put(deployStartQueueMetaKey, data)
}

// GetDeployStartQueueMeta returns {0, 0} when no meta is stored.
func GetDeployStartQueueMeta(context db.DB) (*DeployStartQueueMeta, error) {
	data, err := context.Get(deployStartQueueMetaKey)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return &DeployStartQueueMeta{}, nil
	}
	meta := new(DeployStartQueueMeta)
	if err := ABISettlement.UnpackVariable(meta, deployStartQueueMetaVariableName, data); err != nil {
		return nil, err
	}
	return meta, nil
}

// EnqueueDeployStart appends entry to the tail of the start queue and
// advances the tail cursor.
func EnqueueDeployStart(context db.DB, entry *DeployStartQueueEntry) error {
	meta, err := GetDeployStartQueueMeta(context)
	if err != nil {
		return err
	}
	entry.Index = meta.Tail
	if err := entry.Save(context); err != nil {
		return err
	}
	meta.Tail++
	return meta.Save(context)
}

// PeekDeployStart returns the entry at the head of the start queue without
// removing it. It reads the meta and then the head entry only, and never
// iterates the entry prefix. Returns constants.ErrDataNonExistent when the
// queue is empty.
func PeekDeployStart(context db.DB) (*DeployStartQueueEntry, error) {
	meta, err := GetDeployStartQueueMeta(context)
	if err != nil {
		return nil, err
	}
	if meta.Head >= meta.Tail {
		return nil, constants.ErrDataNonExistent
	}
	return getDeployStartQueueEntry(context, meta.Head)
}

// DequeueDeployStart removes and returns the entry at the head of the start
// queue, advancing the head cursor. Returns constants.ErrDataNonExistent
// when the queue is empty.
func DequeueDeployStart(context db.DB) (*DeployStartQueueEntry, error) {
	entry, err := PeekDeployStart(context)
	if err != nil {
		return nil, err
	}
	if err := entry.Delete(context); err != nil {
		return nil, err
	}
	meta, err := GetDeployStartQueueMeta(context)
	if err != nil {
		return nil, err
	}
	meta.Head++
	if err := meta.Save(context); err != nil {
		return nil, err
	}
	return entry, nil
}

// ============================================================================
// processedOutbox membership set
// ============================================================================

func ProcessedOutboxKey(outboxId types.Hash) []byte {
	return common.JoinBytes(processedOutboxPrefix, outboxId.Bytes())
}

// SetProcessedOutbox records outboxId as processed.
func SetProcessedOutbox(context db.DB, outboxId types.Hash) error {
	return context.Put(ProcessedOutboxKey(outboxId), []byte{1})
}

// IsOutboxProcessed reports whether outboxId has already been recorded as
// processed.
func IsOutboxProcessed(context db.DB, outboxId types.Hash) (bool, error) {
	return context.Has(ProcessedOutboxKey(outboxId))
}

// ============================================================================
// Executor set, periphery config, fee accounting
// ============================================================================

func ExecutorSetMemberKey(pillarName string) []byte {
	return common.JoinBytes(executorSetMemberPrefix, types.NewHash([]byte(pillarName)).Bytes())
}

type ExecutorSetMember struct {
	PillarName         string
	ExecutorAddress    types.Address
	RegistrationHeight uint64
	Status             uint8
}

func (m *ExecutorSetMember) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		ExecutorSetMemberVariableName,
		m.PillarName,
		m.ExecutorAddress,
		m.RegistrationHeight,
		m.Status,
	)
	if err != nil {
		return err
	}
	return context.Put(ExecutorSetMemberKey(m.PillarName), data)
}
func (m *ExecutorSetMember) Delete(context db.DB) error {
	return context.Delete(ExecutorSetMemberKey(m.PillarName))
}

// GetExecutorSet returns every stored executor-set member.
func GetExecutorSet(context db.DB) ([]*ExecutorSetMember, error) {
	iterator := context.NewIterator(executorSetMemberPrefix)
	defer iterator.Release()
	list := make([]*ExecutorSetMember, 0)
	for iterator.Next() {
		if len(iterator.Value()) == 0 {
			continue
		}
		member := new(ExecutorSetMember)
		if err := ABISettlement.UnpackVariable(member, ExecutorSetMemberVariableName, iterator.Value()); err != nil {
			return nil, err
		}
		list = append(list, member)
	}
	if iterator.Error() != nil {
		return nil, iterator.Error()
	}
	return list, nil
}

type PeripheryConfig struct {
	ZnnPerByteOfBytecode *big.Int
	ZnnPerByteOfState    *big.Int
}

func (c *PeripheryConfig) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		peripheryConfigVariableName,
		c.ZnnPerByteOfBytecode,
		c.ZnnPerByteOfState,
	)
	if err != nil {
		return err
	}
	return context.Put(peripheryConfigKey, data)
}

// GetPeripheryConfig returns ErrDataNonExistent when the config is unset.
func GetPeripheryConfig(context db.DB) (*PeripheryConfig, error) {
	if data, err := context.Get(peripheryConfigKey); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	} else {
		config := new(PeripheryConfig)
		if err := ABISettlement.UnpackVariable(config, peripheryConfigVariableName, data); err != nil {
			return nil, err
		}
		return config, nil
	}
}

func getPendingFeeKey(epoch uint64, pillarName string) []byte {
	return common.JoinBytes(pendingFeePrefix, uint64ToBytesBE(epoch), types.NewHash([]byte(pillarName)).Bytes())
}

type PendingFee struct {
	Epoch      uint64
	PillarName string
	Amount     *big.Int
}

func (p *PendingFee) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		pendingFeeVariableName,
		p.PillarName,
		p.Amount,
	)
	if err != nil {
		return err
	}
	return context.Put(getPendingFeeKey(p.Epoch, p.PillarName), data)
}
func (p *PendingFee) Delete(context db.DB) error {
	return context.Delete(getPendingFeeKey(p.Epoch, p.PillarName))
}

func parsePendingFee(key, data []byte) (*PendingFee, error) {
	fee := new(PendingFee)
	if err := ABISettlement.UnpackVariable(fee, pendingFeeVariableName, data); err != nil {
		return nil, err
	}
	fee.Epoch = bytesBEToUint64(key[1:9])
	return fee, nil
}

// GetPendingFee returns a zero Amount when no record exists.
func GetPendingFee(context db.DB, epoch uint64, pillarName string) (*PendingFee, error) {
	key := getPendingFeeKey(epoch, pillarName)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return &PendingFee{Epoch: epoch, PillarName: pillarName, Amount: new(big.Int)}, nil
	} else {
		return parsePendingFee(key, data)
	}
}

// GetPendingFeesForEpoch returns every pending fee record of one epoch.
func GetPendingFeesForEpoch(context db.DB, epoch uint64) ([]*PendingFee, error) {
	iterator := context.NewIterator(common.JoinBytes(pendingFeePrefix, uint64ToBytesBE(epoch)))
	defer iterator.Release()
	list := make([]*PendingFee, 0)
	for iterator.Next() {
		if len(iterator.Value()) == 0 {
			continue
		}
		fee, err := parsePendingFee(iterator.Key(), iterator.Value())
		if err != nil {
			return nil, err
		}
		list = append(list, fee)
	}
	if iterator.Error() != nil {
		return nil, iterator.Error()
	}
	return list, nil
}

func getFeeEpochKey(epoch uint64) []byte {
	return common.JoinBytes(feeEpochPrefix, uint64ToBytesBE(epoch))
}

type FeeEpoch struct {
	Epoch          uint64
	FinalityHeight uint64
}

func (e *FeeEpoch) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		feeEpochVariableName,
		e.FinalityHeight,
	)
	if err != nil {
		return err
	}
	return context.Put(getFeeEpochKey(e.Epoch), data)
}
func (e *FeeEpoch) Delete(context db.DB) error {
	return context.Delete(getFeeEpochKey(e.Epoch))
}

func parseFeeEpoch(key, data []byte) (*FeeEpoch, error) {
	epoch := new(FeeEpoch)
	if err := ABISettlement.UnpackVariable(epoch, feeEpochVariableName, data); err != nil {
		return nil, err
	}
	epoch.Epoch = bytesBEToUint64(key[1:9])
	return epoch, nil
}

// GetFeeEpoch returns ErrDataNonExistent when the epoch is not queued.
func GetFeeEpoch(context db.DB, epoch uint64) (*FeeEpoch, error) {
	key := getFeeEpochKey(epoch)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	} else {
		return parseFeeEpoch(key, data)
	}
}

// GetFeeEpochQueueHead returns the lowest queued epoch, or
// ErrDataNonExistent when the queue is empty. Big-endian keys iterate in
// ascending epoch order. Entries deleted inside a VM snapshot remain as
// empty values and are skipped.
func GetFeeEpochQueueHead(context db.DB) (*FeeEpoch, error) {
	iterator := context.NewIterator(feeEpochPrefix)
	defer iterator.Release()
	for iterator.Next() {
		if len(iterator.Value()) == 0 {
			continue
		}
		return parseFeeEpoch(iterator.Key(), iterator.Value())
	}
	if iterator.Error() != nil {
		return nil, iterator.Error()
	}
	return nil, constants.ErrDataNonExistent
}

type FeeCounters struct {
	TotalFeesCollected    *big.Int
	TotalFeesBurned       *big.Int
	TotalFeesCredited     *big.Int
	TotalRewardsCollected *big.Int
}

func (c *FeeCounters) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		feeCountersVariableName,
		c.TotalFeesCollected,
		c.TotalFeesBurned,
		c.TotalFeesCredited,
		c.TotalRewardsCollected,
	)
	if err != nil {
		return err
	}
	return context.Put(feeCountersKey, data)
}

// GetFeeCounters returns all-zero counters when none are stored.
func GetFeeCounters(context db.DB) (*FeeCounters, error) {
	if data, err := context.Get(feeCountersKey); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return &FeeCounters{
			TotalFeesCollected:    new(big.Int),
			TotalFeesBurned:       new(big.Int),
			TotalFeesCredited:     new(big.Int),
			TotalRewardsCollected: new(big.Int),
		}, nil
	} else {
		counters := new(FeeCounters)
		if err := ABISettlement.UnpackVariable(counters, feeCountersVariableName, data); err != nil {
			return nil, err
		}
		return counters, nil
	}
}

// ============================================================================
// Domain config, asset config, approved DA modes, domain counter
// ============================================================================

type DomainConfig struct {
	DomainId            uint32
	WithdrawalDelay     uint64
	RuntimeUpgradeDelay uint64
}

func getDomainConfigKey(domainId uint32) []byte {
	return common.JoinBytes(domainConfigPrefix, uint32ToBytesBE(domainId))
}
func (c *DomainConfig) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		domainConfigVariableName,
		c.WithdrawalDelay,
		c.RuntimeUpgradeDelay,
	)
	if err != nil {
		return err
	}
	return context.Put(getDomainConfigKey(c.DomainId), data)
}
func GetDomainConfig(context db.DB, domainId uint32) (*DomainConfig, error) {
	if data, err := context.Get(getDomainConfigKey(domainId)); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	} else {
		config := new(DomainConfig)
		if err := ABISettlement.UnpackVariable(config, domainConfigVariableName, data); err != nil {
			return nil, err
		}
		config.DomainId = domainId
		return config, nil
	}
}

type AssetConfig struct {
	DomainId           uint32
	AssetId            types.Hash
	MaxBatchWithdrawal *big.Int
}

func AssetConfigKey(domainId uint32, assetId types.Hash) []byte {
	return common.JoinBytes(assetConfigPrefix, uint32ToBytesBE(domainId), assetId.Bytes())
}
func (c *AssetConfig) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		AssetConfigVariableName,
		c.MaxBatchWithdrawal,
	)
	if err != nil {
		return err
	}
	return context.Put(AssetConfigKey(c.DomainId, c.AssetId), data)
}
func GetAssetConfig(context db.DB, domainId uint32, assetId types.Hash) (*AssetConfig, error) {
	if data, err := context.Get(AssetConfigKey(domainId, assetId)); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	} else {
		config := new(AssetConfig)
		if err := ABISettlement.UnpackVariable(config, AssetConfigVariableName, data); err != nil {
			return nil, err
		}
		config.DomainId = domainId
		config.AssetId = assetId
		return config, nil
	}
}

// ApprovedDAModeSet holds one byte per approved DAMode.
type ApprovedDAModeSet struct {
	DomainId uint32
	Modes    []byte
}

func getApprovedDAModeSetKey(domainId uint32) []byte {
	return common.JoinBytes(approvedDAModeSetPrefix, uint32ToBytesBE(domainId))
}
func (s *ApprovedDAModeSet) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		approvedDAModeSetVariableName,
		s.Modes,
	)
	if err != nil {
		return err
	}
	return context.Put(getApprovedDAModeSetKey(s.DomainId), data)
}
func GetApprovedDAModeSet(context db.DB, domainId uint32) (*ApprovedDAModeSet, error) {
	if data, err := context.Get(getApprovedDAModeSetKey(domainId)); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return &ApprovedDAModeSet{DomainId: domainId}, nil
	} else {
		set := new(ApprovedDAModeSet)
		if err := ABISettlement.UnpackVariable(set, approvedDAModeSetVariableName, data); err != nil {
			return nil, err
		}
		set.DomainId = domainId
		return set, nil
	}
}

type domainCounter struct {
	LastDomainId uint32
}

// GetLastDomainId returns the last assigned domainId, or 0 when no domain
// is registered.
func GetLastDomainId(context db.DB) (uint32, error) {
	if data, err := context.Get(domainCounterKey); err != nil {
		return 0, err
	} else if len(data) == 0 {
		return 0, nil
	} else {
		counter := new(domainCounter)
		if err := ABISettlement.UnpackVariable(counter, domainCounterVariableName, data); err != nil {
			return 0, err
		}
		return counter.LastDomainId, nil
	}
}
func SetLastDomainId(context db.DB, domainId uint32) error {
	data, err := ABISettlement.PackVariable(domainCounterVariableName, domainId)
	if err != nil {
		return err
	}
	return context.Put(domainCounterKey, data)
}

// ============================================================================
// Pause state (global singleton + per-domain)
// ============================================================================

type PauseState struct {
	SubmitPaused bool
	RelayPaused  bool
}

func (p *PauseState) SaveGlobal(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		PauseStateVariableName,
		p.SubmitPaused,
		p.RelayPaused,
	)
	if err != nil {
		return err
	}
	return context.Put(globalPauseStateKey, data)
}
func GetGlobalPauseState(context db.DB) (*PauseState, error) {
	if data, err := context.Get(globalPauseStateKey); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return &PauseState{}, nil
	} else {
		state := new(PauseState)
		if err := ABISettlement.UnpackVariable(state, PauseStateVariableName, data); err != nil {
			return nil, err
		}
		return state, nil
	}
}

func DomainPauseStateKey(domainId uint32) []byte {
	return common.JoinBytes(domainPauseStatePrefix, uint32ToBytesBE(domainId))
}
func (p *PauseState) SaveDomain(context db.DB, domainId uint32) error {
	data, err := ABISettlement.PackVariable(
		PauseStateVariableName,
		p.SubmitPaused,
		p.RelayPaused,
	)
	if err != nil {
		return err
	}
	return context.Put(DomainPauseStateKey(domainId), data)
}
func GetDomainPauseState(context db.DB, domainId uint32) (*PauseState, error) {
	key := DomainPauseStateKey(domainId)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return &PauseState{}, nil
	} else {
		state := new(PauseState)
		if err := ABISettlement.UnpackVariable(state, PauseStateVariableName, data); err != nil {
			return nil, err
		}
		return state, nil
	}
}

// IsSubmitPaused reports whether SUBMIT is paused globally or for domainId.
func IsSubmitPaused(context db.DB, domainId uint32) (bool, error) {
	global, err := GetGlobalPauseState(context)
	if err != nil {
		return false, err
	}
	if global.SubmitPaused {
		return true, nil
	}
	domain, err := GetDomainPauseState(context, domainId)
	if err != nil {
		return false, err
	}
	return domain.SubmitPaused, nil
}

// IsRelayPaused reports whether RELAY is paused globally or for domainId.
func IsRelayPaused(context db.DB, domainId uint32) (bool, error) {
	global, err := GetGlobalPauseState(context)
	if err != nil {
		return false, err
	}
	if global.RelayPaused {
		return true, nil
	}
	domain, err := GetDomainPauseState(context, domainId)
	if err != nil {
		return false, err
	}
	return domain.RelayPaused, nil
}

// IsRelayPausedAnywhere reports whether RELAY is paused globally or for any
// domainId whose pause record has RelayPaused set.
func IsRelayPausedAnywhere(context db.DB) (bool, error) {
	global, err := GetGlobalPauseState(context)
	if err != nil {
		return false, err
	}
	if global.RelayPaused {
		return true, nil
	}
	iterator := context.NewIterator(domainPauseStatePrefix)
	defer iterator.Release()
	for iterator.Next() {
		if len(iterator.Value()) == 0 {
			continue
		}
		state := new(PauseState)
		if err := ABISettlement.UnpackVariable(state, PauseStateVariableName, iterator.Value()); err != nil {
			return false, err
		}
		if state.RelayPaused {
			return true, nil
		}
	}
	return false, iterator.Error()
}

// ============================================================================
// Escape-hatch record (per domain, stub)
// ============================================================================

type EscapeHatchRecord struct {
	DomainId          uint32
	TriggeredAtHeight uint64
}

func getEscapeHatchKey(domainId uint32) []byte {
	return common.JoinBytes(escapeHatchPrefix, uint32ToBytesBE(domainId))
}
func (r *EscapeHatchRecord) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		escapeHatchRecordVariableName,
		r.TriggeredAtHeight,
	)
	if err != nil {
		return err
	}
	return context.Put(getEscapeHatchKey(r.DomainId), data)
}
func GetEscapeHatchRecord(context db.DB, domainId uint32) (*EscapeHatchRecord, error) {
	key := getEscapeHatchKey(domainId)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	} else {
		record := new(EscapeHatchRecord)
		if err := ABISettlement.UnpackVariable(record, escapeHatchRecordVariableName, data); err != nil {
			return nil, err
		}
		record.DomainId = domainId
		return record, nil
	}
}

// ============================================================================
// Reserved storage entities (schema present, unused in Phase 1)
// ============================================================================

// AssetRegistryEntry reserves the per-(domain, asset) custody, foreign-binding
// and supply-counter schema. No callers exist in Phase 1.
type AssetRegistryEntry struct {
	DomainId                 uint32
	AssetId                  types.Hash
	CustodyMode              uint8
	ForeignChainId           []byte
	ForeignAssetRef          []byte
	DecimalsPolicy           []byte
	ProvenForeignLocks       *big.Int
	MintedSupply             *big.Int
	BurnedSupply             *big.Int
	AuthorizedForeignRelease *big.Int
}

func getAssetRegistryKey(domainId uint32, assetId types.Hash) []byte {
	return common.JoinBytes(assetRegistryPrefix, uint32ToBytesBE(domainId), assetId.Bytes())
}
func (e *AssetRegistryEntry) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		assetRegistryEntryVariableName,
		e.CustodyMode,
		e.ForeignChainId,
		e.ForeignAssetRef,
		e.DecimalsPolicy,
		e.ProvenForeignLocks,
		e.MintedSupply,
		e.BurnedSupply,
		e.AuthorizedForeignRelease,
	)
	if err != nil {
		return err
	}
	return context.Put(getAssetRegistryKey(e.DomainId, e.AssetId), data)
}
func GetAssetRegistryEntry(context db.DB, domainId uint32, assetId types.Hash) (*AssetRegistryEntry, error) {
	key := getAssetRegistryKey(domainId, assetId)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	} else {
		entry := new(AssetRegistryEntry)
		if err := ABISettlement.UnpackVariable(entry, assetRegistryEntryVariableName, data); err != nil {
			return nil, err
		}
		entry.DomainId = domainId
		entry.AssetId = assetId
		return entry, nil
	}
}

// MessageStateEntry reserves the per-outboxId timeout/ack-policy schema. No
// callers exist in Phase 1.
type MessageStateEntry struct {
	OutboxId  types.Hash
	Timeout   uint64
	AckPolicy uint8
	State     uint8
}

func getMessageStateKey(outboxId types.Hash) []byte {
	return common.JoinBytes(messageStatePrefix, outboxId.Bytes())
}
func (m *MessageStateEntry) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		messageStateVariableName,
		m.Timeout,
		m.AckPolicy,
		m.State,
	)
	if err != nil {
		return err
	}
	return context.Put(getMessageStateKey(m.OutboxId), data)
}
func GetMessageStateEntry(context db.DB, outboxId types.Hash) (*MessageStateEntry, error) {
	key := getMessageStateKey(outboxId)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	} else {
		entry := new(MessageStateEntry)
		if err := ABISettlement.UnpackVariable(entry, messageStateVariableName, data); err != nil {
			return nil, err
		}
		entry.OutboxId = outboxId
		return entry, nil
	}
}

// FraudRefereeRegistryEntry reserves the per-domain fraud-proof referee
// registry schema (Phase 2, OPTIMISTIC). No callers exist in Phase 1.
type FraudRefereeRegistryEntry struct {
	DomainId uint32
	Reserved []byte
}

func getFraudRefereeRegistryKey(domainId uint32) []byte {
	return common.JoinBytes(fraudRefereeRegistryPrefix, uint32ToBytesBE(domainId))
}
func (e *FraudRefereeRegistryEntry) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		fraudRefereeRegistryEntryVariableName,
		e.Reserved,
	)
	if err != nil {
		return err
	}
	return context.Put(getFraudRefereeRegistryKey(e.DomainId), data)
}
func GetFraudRefereeRegistryEntry(context db.DB, domainId uint32) (*FraudRefereeRegistryEntry, error) {
	key := getFraudRefereeRegistryKey(domainId)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	} else {
		entry := new(FraudRefereeRegistryEntry)
		if err := ABISettlement.UnpackVariable(entry, fraudRefereeRegistryEntryVariableName, data); err != nil {
			return nil, err
		}
		entry.DomainId = domainId
		return entry, nil
	}
}

// ValidityKeyRegistryEntry reserves the per-verifyingKeyId validity-proof
// verifier registry schema (Phase 3, ZK_VALIDITY). No callers exist in Phase 1.
type ValidityKeyRegistryEntry struct {
	VerifyingKeyId types.Hash
	Reserved       []byte
}

func getValidityKeyRegistryKey(verifyingKeyId types.Hash) []byte {
	return common.JoinBytes(validityKeyRegistryPrefix, verifyingKeyId.Bytes())
}
func (e *ValidityKeyRegistryEntry) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		validityKeyRegistryEntryVariableName,
		e.Reserved,
	)
	if err != nil {
		return err
	}
	return context.Put(getValidityKeyRegistryKey(e.VerifyingKeyId), data)
}
func GetValidityKeyRegistryEntry(context db.DB, verifyingKeyId types.Hash) (*ValidityKeyRegistryEntry, error) {
	key := getValidityKeyRegistryKey(verifyingKeyId)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	} else {
		entry := new(ValidityKeyRegistryEntry)
		if err := ABISettlement.UnpackVariable(entry, validityKeyRegistryEntryVariableName, data); err != nil {
			return nil, err
		}
		entry.VerifyingKeyId = verifyingKeyId
		return entry, nil
	}
}

// GlobalPauseStateKey returns a copy of the storage key of the global
// pause state.
func GlobalPauseStateKey() []byte {
	return common.JoinBytes(globalPauseStateKey)
}
