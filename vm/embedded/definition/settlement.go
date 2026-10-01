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
		{"type":"function","name":"Update","inputs":[
			{"name":"domainId","type":"uint32"}]},

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
			{"name":"executorIndex","type":"uint8"},
			{"name":"submittedAtHeight","type":"uint64"},
			{"name":"assetFlowSummary","type":"bytes"},
			{"name":"proofData","type":"bytes"},
			{"name":"state","type":"uint8"}
		]},

		{"type":"variable","name":"inputSequenceCursor","inputs":[
			{"name":"lastConsumedInputSeq","type":"uint64"}
		]},

		{"type":"variable","name":"receivedInputCounter","inputs":[
			{"name":"count","type":"uint64"}
		]},

		{"type":"variable","name":"assignedIndexRecord","inputs":[
			{"name":"assetId","type":"hash"},
			{"name":"amount","type":"uint256"}
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

		{"type":"variable","name":"deployRecord","inputs":[
			{"name":"metadataHash","type":"hash"},
			{"name":"totalSize","type":"uint64"},
			{"name":"chunkCount","type":"uint32"},
			{"name":"deployer","type":"address"},
			{"name":"startHeight","type":"uint64"},
			{"name":"receivedChunks","type":"bytes"}
		]},

		{"type":"variable","name":"executorBond","inputs":[
			{"name":"znnAmount","type":"uint256"},
			{"name":"qsrAmount","type":"uint256"},
			{"name":"status","type":"uint8"}
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

	SettlementUpdateMethodName = "Update"

	// declared, not yet wired
	SettlementSetPeripheryConfigMethodName  = "SetPeripheryConfig"
	SettlementInitiateEscapeHatchMethodName = "InitiateEscapeHatch"

	SettlementRegisterDomainMethodName     = "RegisterDomain"
	SettlementSetDomainConfigMethodName    = "SetDomainConfig"
	SettlementSetAssetConfigMethodName     = "SetAssetConfig"
	SettlementSetApprovedDAModesMethodName = "SetApprovedDAModes"
	// declared for Tickets 11+, not yet wired
	SettlementRegisterExecutorMethodName = "RegisterExecutor"
	// declared for Tickets 11+, not yet wired
	SettlementPostBondMethodName = "PostBond"
	// declared for Tickets 11+, not yet wired
	SettlementRevokeBondMethodName = "RevokeBond"
	// declared for Tickets 11+, not yet wired
	SettlementDepositMethodName = "Deposit"
	// declared for Tickets 11+, not yet wired
	SettlementSubmitBatchMethodName = "SubmitBatch"
	// declared for Tickets 11+, not yet wired
	SettlementRelayMessageMethodName = "RelayMessage"

	SettlementPauseMethodName = "Pause"
	// declared for Tickets 11+, not yet wired
	SettlementCallL2MethodName = "CallL2"
	// declared for Tickets 11+, not yet wired
	SettlementDeployContractStartMethodName = "DeployContractStart"
	// declared for Tickets 11+, not yet wired
	SettlementDeployContractChunkMethodName = "DeployContractChunk"
	// declared for Tickets 11+, not yet wired
	SettlementDeployContractFinalizeMethodName = "DeployContractFinalize"
	// declared for Tickets 11+, not yet wired
	SettlementUpgradeContractMethodName = "UpgradeContract"

	domainRecordVariableName              = "domainRecord"
	batchEntryVariableName                = "batchEntry"
	inputSequenceCursorVariableName       = "inputSequenceCursor"
	receivedInputCounterVariableName      = "receivedInputCounter"
	assignedIndexRecordVariableName       = "assignedIndexRecord"
	cumulativeDepositTotalVariableName    = "cumulativeDepositTotal"
	conservationCountersVariableName      = "conservationCounters"
	releaseQueueEntryVariableName         = "releaseQueueEntry"
	releaseQueueMetaVariableName          = "releaseQueueMeta"
	deployRecordVariableName              = "deployRecord"
	executorBondVariableName              = "executorBond"
	domainConfigVariableName              = "domainConfig"
	assetConfigVariableName               = "assetConfig"
	approvedDAModeSetVariableName         = "approvedDAModeSet"
	domainCounterVariableName             = "domainCounter"
	pauseStateVariableName                = "pauseState"
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

// BatchEntry.state
const (
	BatchStateSubmitted uint8 = iota
	BatchStateFinalized
)

// ExecutorBond.status
const (
	ExecutorStatusRegistered uint8 = iota
	ExecutorStatusActive
)

// Pause scopes
const (
	PauseScopeSubmit uint8 = iota // halts SubmitBatch (Ticket 15)
	PauseScopeRelay               // halts RelayMessage registration + Update release (Ticket 16)
)

var (
	ABISettlement = abi.JSONToABIContract(strings.NewReader(jsonSettlement))

	// Settlement contract storage key-prefix table. Storage is namespaced to
	// SettlementContract, so prefixes only need uniqueness within this contract.
	// The reused common accessors (GetLastUpdate, GetQsrDeposit) occupy 129/130.
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
	deployRecordPrefix           = []byte{0x0A} // key: domainId ‖ codeHash (32B)
	processedOutboxPrefix        = []byte{0x0B} // key: outboxId (32B)
	executorBondPrefix           = []byte{0x0C} // key: domainId ‖ executorAddress
	_                            = []byte{0x0D} // reserved: network-wide Periphery record
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
		domainRecordVariableName,
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
	if err := ABISettlement.UnpackVariable(record, domainRecordVariableName, data); err != nil {
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
	ExecutorIndex     uint8
	SubmittedAtHeight uint64
	AssetFlowSummary  []byte
	ProofData         []byte
	State             uint8
}

func (b *BatchEntry) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		batchEntryVariableName,
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
		b.ExecutorIndex,
		b.SubmittedAtHeight,
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
	if err := ABISettlement.UnpackVariable(entry, batchEntryVariableName, data); err != nil {
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

type InputSequenceCursor struct {
	DomainId             uint32
	LastConsumedInputSeq uint64
}

func getInputSequenceCursorKey(domainId uint32) []byte {
	return common.JoinBytes(inputSequenceCursorPrefix, uint32ToBytesBE(domainId))
}
func (c *InputSequenceCursor) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		inputSequenceCursorVariableName,
		c.LastConsumedInputSeq,
	)
	if err != nil {
		return err
	}
	return context.Put(getInputSequenceCursorKey(c.DomainId), data)
}
func GetInputSequenceCursor(context db.DB, domainId uint32) (*InputSequenceCursor, error) {
	key := getInputSequenceCursorKey(domainId)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return &InputSequenceCursor{DomainId: domainId, LastConsumedInputSeq: 0}, nil
	} else {
		cursor := new(InputSequenceCursor)
		if err := ABISettlement.UnpackVariable(cursor, inputSequenceCursorVariableName, data); err != nil {
			return nil, err
		}
		cursor.DomainId = domainId
		return cursor, nil
	}
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
}

func (r *AssignedIndexRecord) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		assignedIndexRecordVariableName,
		r.AssetId,
		r.Amount,
	)
	if err != nil {
		return err
	}
	return context.Put(r.Key(), data)
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

func getReleaseQueueMetaKey(domainId uint32) []byte {
	return common.JoinBytes(releaseQueueMetaPrefix, uint32ToBytesBE(domainId))
}

type ReleaseQueueMeta struct {
	DomainId uint32
	Head     uint64
	Tail     uint64
}

func (m *ReleaseQueueMeta) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		releaseQueueMetaVariableName,
		m.Head,
		m.Tail,
	)
	if err != nil {
		return err
	}
	return context.Put(getReleaseQueueMetaKey(m.DomainId), data)
}
func GetReleaseQueueMeta(context db.DB, domainId uint32) (*ReleaseQueueMeta, error) {
	key := getReleaseQueueMetaKey(domainId)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return &ReleaseQueueMeta{DomainId: domainId, Head: 0, Tail: 0}, nil
	} else {
		meta := new(ReleaseQueueMeta)
		if err := ABISettlement.UnpackVariable(meta, releaseQueueMetaVariableName, data); err != nil {
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
// Deploy record (pending chunked deployment)
// ============================================================================

type DeployRecordKey struct {
	DomainId uint32
	CodeHash types.Hash
}

func (k *DeployRecordKey) Key() []byte {
	return common.JoinBytes(deployRecordPrefix, uint32ToBytesBE(k.DomainId), k.CodeHash.Bytes())
}

type DeployRecord struct {
	DeployRecordKey
	MetadataHash   types.Hash
	TotalSize      uint64
	ChunkCount     uint32
	Deployer       types.Address
	StartHeight    uint64
	ReceivedChunks []byte
}

func (r *DeployRecord) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		deployRecordVariableName,
		r.MetadataHash,
		r.TotalSize,
		r.ChunkCount,
		r.Deployer,
		r.StartHeight,
		r.ReceivedChunks,
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
func unmarshalDeployRecordKey(key []byte) (uint32, types.Hash, error) {
	if !isDeployRecordKey(key) {
		return 0, types.ZeroHash, errors.Errorf("invalid key! Not deploy record key")
	}
	domainId := bytesBEToUint32(key[1:5])
	codeHash, err := types.BytesToHash(key[5 : 5+types.HashSize])
	if err != nil {
		return 0, types.ZeroHash, err
	}
	return domainId, codeHash, nil
}
func parseDeployRecord(key, data []byte) (*DeployRecord, error) {
	if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	}
	record := new(DeployRecord)
	if err := ABISettlement.UnpackVariable(record, deployRecordVariableName, data); err != nil {
		return nil, err
	}
	domainId, codeHash, err := unmarshalDeployRecordKey(key)
	if err != nil {
		return nil, err
	}
	record.DomainId = domainId
	record.CodeHash = codeHash
	return record, nil
}
func GetDeployRecord(context db.DB, domainId uint32, codeHash types.Hash) (*DeployRecord, error) {
	key := (&DeployRecordKey{DomainId: domainId, CodeHash: codeHash}).Key()
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else {
		return parseDeployRecord(key, data)
	}
}

// ============================================================================
// processedOutbox membership set
// ============================================================================

func getProcessedOutboxKey(outboxId types.Hash) []byte {
	return common.JoinBytes(processedOutboxPrefix, outboxId.Bytes())
}

// SetProcessedOutbox records outboxId as processed.
func SetProcessedOutbox(context db.DB, outboxId types.Hash) error {
	return context.Put(getProcessedOutboxKey(outboxId), []byte{1})
}

// IsOutboxProcessed reports whether outboxId has already been recorded as
// processed.
func IsOutboxProcessed(context db.DB, outboxId types.Hash) (bool, error) {
	return context.Has(getProcessedOutboxKey(outboxId))
}

// ============================================================================
// Executor registration + bond (per domain)
// ============================================================================

// The bond is keyed by (domainId, executorAddress). Registering a replacement
// executor for a domain leaves the outgoing executor's record in place under its
// own address, which is what lets that executor reclaim both legs once it is no
// longer the domain's registered executor and every batch it submitted has
// finalized. Both key fields are recovered from the key rather than stored in the
// value, as DomainRecord and BatchEntry do.
func getExecutorBondKey(domainId uint32, executor types.Address) []byte {
	return common.JoinBytes(executorBondPrefix, uint32ToBytesBE(domainId), executor.Bytes())
}

type ExecutorBond struct {
	DomainId  uint32
	Executor  types.Address
	ZnnAmount *big.Int
	QsrAmount *big.Int
	Status    uint8
}

func (b *ExecutorBond) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		executorBondVariableName,
		b.ZnnAmount,
		b.QsrAmount,
		b.Status,
	)
	if err != nil {
		return err
	}
	return context.Put(getExecutorBondKey(b.DomainId, b.Executor), data)
}
func (b *ExecutorBond) Delete(context db.DB) error {
	return context.Delete(getExecutorBondKey(b.DomainId, b.Executor))
}

func isExecutorBondKey(key []byte) bool {
	return len(key) == 1+4+types.AddressSize && key[0] == executorBondPrefix[0]
}
func unmarshalExecutorBondKey(key []byte) (uint32, types.Address, error) {
	if !isExecutorBondKey(key) {
		return 0, types.Address{}, errors.Errorf("invalid key! Not executor bond key")
	}
	executor, err := types.BytesToAddress(key[5:])
	if err != nil {
		return 0, types.Address{}, err
	}
	return bytesBEToUint32(key[1:5]), executor, nil
}
func parseExecutorBond(key, data []byte) (*ExecutorBond, error) {
	if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	}
	bond := new(ExecutorBond)
	if err := ABISettlement.UnpackVariable(bond, executorBondVariableName, data); err != nil {
		return nil, err
	}
	domainId, executor, err := unmarshalExecutorBondKey(key)
	if err != nil {
		return nil, err
	}
	bond.DomainId = domainId
	bond.Executor = executor
	return bond, nil
}
func GetExecutorBond(context db.DB, domainId uint32, executor types.Address) (*ExecutorBond, error) {
	key := getExecutorBondKey(domainId, executor)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else {
		return parseExecutorBond(key, data)
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

func getAssetConfigKey(domainId uint32, assetId types.Hash) []byte {
	return common.JoinBytes(assetConfigPrefix, uint32ToBytesBE(domainId), assetId.Bytes())
}
func (c *AssetConfig) Save(context db.DB) error {
	data, err := ABISettlement.PackVariable(
		assetConfigVariableName,
		c.MaxBatchWithdrawal,
	)
	if err != nil {
		return err
	}
	return context.Put(getAssetConfigKey(c.DomainId, c.AssetId), data)
}
func GetAssetConfig(context db.DB, domainId uint32, assetId types.Hash) (*AssetConfig, error) {
	if data, err := context.Get(getAssetConfigKey(domainId, assetId)); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return nil, constants.ErrDataNonExistent
	} else {
		config := new(AssetConfig)
		if err := ABISettlement.UnpackVariable(config, assetConfigVariableName, data); err != nil {
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
		pauseStateVariableName,
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
		if err := ABISettlement.UnpackVariable(state, pauseStateVariableName, data); err != nil {
			return nil, err
		}
		return state, nil
	}
}

func getDomainPauseStateKey(domainId uint32) []byte {
	return common.JoinBytes(domainPauseStatePrefix, uint32ToBytesBE(domainId))
}
func (p *PauseState) SaveDomain(context db.DB, domainId uint32) error {
	data, err := ABISettlement.PackVariable(
		pauseStateVariableName,
		p.SubmitPaused,
		p.RelayPaused,
	)
	if err != nil {
		return err
	}
	return context.Put(getDomainPauseStateKey(domainId), data)
}
func GetDomainPauseState(context db.DB, domainId uint32) (*PauseState, error) {
	key := getDomainPauseStateKey(domainId)
	if data, err := context.Get(key); err != nil {
		return nil, err
	} else if len(data) == 0 {
		return &PauseState{}, nil
	} else {
		state := new(PauseState)
		if err := ABISettlement.UnpackVariable(state, pauseStateVariableName, data); err != nil {
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
