package embedded

import (
	"strconv"
	"strings"

	"github.com/pkg/errors"

	"github.com/zenon-network/go-zenon/chain"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc/api"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/zenon"
)

type SettlementApi struct {
	chain chain.Chain
}

func NewSettlementApi(z zenon.Zenon) *SettlementApi {
	return &SettlementApi{
		chain: z.Chain(),
	}
}

type SettlementPauseState struct {
	SubmitPaused bool `json:"submitPaused"`
	RelayPaused  bool `json:"relayPaused"`
}

type SettlementDomainPauseState struct {
	DomainId     uint32 `json:"domainId"`
	SubmitPaused bool   `json:"submitPaused"`
	RelayPaused  bool   `json:"relayPaused"`
}

type SettlementInputCursor struct {
	ConsumedInputCount  uint64 `json:"consumedInputCount"`
	NextBatchId         uint64 `json:"nextBatchId"`
	RecordsSweptThrough uint64 `json:"recordsSweptThrough"`
	SweptInputCount     uint64 `json:"sweptInputCount"`
}

type SettlementEscapeHatch struct {
	TriggeredAtHeight uint64 `json:"triggeredAtHeight"`
}

type SettlementDomainInfo struct {
	DomainId            uint32                 `json:"domainId"`
	DomainClass         uint8                  `json:"domainClass"`
	RuntimeKind         uint8                  `json:"runtimeKind"`
	StfSpecHash         types.Hash             `json:"stfSpecHash"`
	InputSource         uint8                  `json:"inputSource"`
	ExecProfile         uint8                  `json:"execProfile"`
	ForeignProfile      uint8                  `json:"foreignProfile"`
	ProfileConfig       []byte                 `json:"profileConfig"`
	ChainBinding        []byte                 `json:"chainBinding"`
	MinExecutors        uint8                  `json:"minExecutors"`
	ProposerPolicy      uint8                  `json:"proposerPolicy"`
	ValueCaps           []byte                 `json:"valueCaps"`
	FinalityModel       uint8                  `json:"finalityModel"`
	Status              uint8                  `json:"status"`
	WithdrawalDelay     uint64                 `json:"withdrawalDelay"`
	RuntimeUpgradeDelay uint64                 `json:"runtimeUpgradeDelay"`
	ApprovedDAModes     []int                  `json:"approvedDAModes"`
	PauseState          *SettlementPauseState  `json:"pauseState"`
	InputCursor         *SettlementInputCursor `json:"inputCursor"`
	ReceivedInputCount  uint64                 `json:"receivedInputCount"`
	EscapeHatch         *SettlementEscapeHatch `json:"escapeHatch"`
}

type SettlementBatch struct {
	DomainId          uint32     `json:"domainId"`
	BatchId           uint64     `json:"batchId"`
	ProtocolVersion   uint16     `json:"protocolVersion"`
	FirstInputSeq     uint64     `json:"firstInputSeq"`
	LastInputSeq      uint64     `json:"lastInputSeq"`
	PreStateRoot      types.Hash `json:"preStateRoot"`
	PostStateRoot     types.Hash `json:"postStateRoot"`
	InputRoot         types.Hash `json:"inputRoot"`
	ReceiptRoot       types.Hash `json:"receiptRoot"`
	EventRoot         types.Hash `json:"eventRoot"`
	OutboxRoot        types.Hash `json:"outboxRoot"`
	DaHash            types.Hash `json:"daHash"`
	DaMode            uint8      `json:"daMode"`
	ExecutorPillar    string     `json:"executorPillar"`
	SubmittedAtHeight uint64     `json:"submittedAtHeight"`
	FinalizeAtHeight  uint64     `json:"finalizeAtHeight"`
	AssetFlowSummary  []byte     `json:"assetFlowSummary"`
	ProofData         []byte     `json:"proofData"`
	State             uint8      `json:"state"`
	IsFinalized       bool       `json:"isFinalized"`
}

type SettlementDeployRecord struct {
	DomainId        uint32        `json:"domainId"`
	Deployer        types.Address `json:"deployer"`
	CodeHash        types.Hash    `json:"codeHash"`
	MetadataHash    types.Hash    `json:"metadataHash"`
	TotalSize       uint64        `json:"totalSize"`
	ChunkCount      uint32        `json:"chunkCount"`
	StartHeight     uint64        `json:"startHeight"`
	ReceivedCount   uint32        `json:"receivedCount"`
	ExpiresAtHeight uint64        `json:"expiresAtHeight"`
	IsExpired       bool          `json:"isExpired"`
}

type SettlementPauseInfo struct {
	Global  *SettlementPauseState         `json:"global"`
	Domains []*SettlementDomainPauseState `json:"domains"`
}

type SettlementPeripheryConfig struct {
	ZnnPerByteOfBytecode string `json:"znnPerByteOfBytecode"`
	ZnnPerByteOfState    string `json:"znnPerByteOfState"`
}

type SettlementExecutor struct {
	PillarName         string        `json:"pillarName"`
	ExecutorAddress    types.Address `json:"executorAddress"`
	RegistrationHeight uint64        `json:"registrationHeight"`
	Status             uint8         `json:"status"`
}

type SettlementFeeEpoch struct {
	Epoch          uint64 `json:"epoch"`
	FinalityHeight uint64 `json:"finalityHeight"`
}

type SettlementFeeInfo struct {
	TotalFeesCollected    string              `json:"totalFeesCollected"`
	TotalFeesBurned       string              `json:"totalFeesBurned"`
	TotalFeesCredited     string              `json:"totalFeesCredited"`
	TotalRewardsCollected string              `json:"totalRewardsCollected"`
	PendingEpochHead      *SettlementFeeEpoch `json:"pendingEpochHead"`
}

type SettlementAssetInfo struct {
	DomainId                 uint32     `json:"domainId"`
	AssetId                  types.Hash `json:"assetId"`
	MaxBatchWithdrawal       string     `json:"maxBatchWithdrawal"`
	CumulativeDeposited      string     `json:"cumulativeDeposited"`
	TotalDeposited           string     `json:"totalDeposited"`
	PendingWithdrawalReserve string     `json:"pendingWithdrawalReserve"`
	TotalReleased            string     `json:"totalReleased"`
}

type SettlementReleaseQueueEntry struct {
	RegistrationIndex uint64        `json:"registrationIndex"`
	Recipient         types.Address `json:"recipient"`
	AssetId           types.Hash    `json:"assetId"`
	Amount            string        `json:"amount"`
	OutboxId          types.Hash    `json:"outboxId"`
}

type SettlementReleaseQueue struct {
	Head  uint64                         `json:"head"`
	Tail  uint64                         `json:"tail"`
	Count int                            `json:"count"`
	List  []*SettlementReleaseQueueEntry `json:"list"`
}

type SettlementTimeChallenge struct {
	MethodName           string     `json:"methodName"`
	ParamsHash           types.Hash `json:"paramsHash"`
	ChallengeStartHeight uint64     `json:"challengeStartHeight"`
	// DueHeight is the first frontier height at which the apply passes.
	// For RegisterDomain/<domainId> it is an upper bound computed from the
	// DomainConfig in force at read time; a later SetDomainConfig that
	// changes WithdrawalDelay or RuntimeUpgradeDelay leaves this challenge
	// unappliable until a resend restarts it.
	DueHeight uint64 `json:"dueHeight"`
}

type SettlementDomainInfoList struct {
	Count int                     `json:"count"`
	List  []*SettlementDomainInfo `json:"list"`
}

type SettlementBatchList struct {
	Count int                `json:"count"`
	List  []*SettlementBatch `json:"list"`
}

type SettlementDeployRecordList struct {
	Count int                       `json:"count"`
	List  []*SettlementDeployRecord `json:"list"`
}

type SettlementExecutorList struct {
	Count int                   `json:"count"`
	List  []*SettlementExecutor `json:"list"`
}

type SettlementTimeChallengeList struct {
	Count int                        `json:"count"`
	List  []*SettlementTimeChallenge `json:"list"`
}

func (a *SettlementApi) GetDomainInfo(domainId uint32) (*SettlementDomainInfo, error) {
	_, context, err := api.GetFrontierContext(a.chain, types.SettlementContract)
	if err != nil {
		return nil, err
	}
	storage := context.Storage()
	record, err := definition.GetDomainRecord(storage, domainId)
	if err == constants.ErrDataNonExistent {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return toSettlementDomainInfo(storage, record)
}

func (a *SettlementApi) GetAllDomains(pageIndex, pageSize uint32) (*SettlementDomainInfoList, error) {
	if pageSize > api.RpcMaxPageSize {
		return nil, api.ErrPageSizeParamTooBig
	}
	_, context, err := api.GetFrontierContext(a.chain, types.SettlementContract)
	if err != nil {
		return nil, err
	}
	storage := context.Storage()
	count, err := definition.GetLastDomainId(storage)
	if err != nil {
		return nil, err
	}
	start, end := api.GetRange(pageIndex, pageSize, count)
	list := make([]*SettlementDomainInfo, 0)
	for i := start; i < end; i++ {
		record, err := definition.GetDomainRecord(storage, i+1)
		if err != nil {
			return nil, err
		}
		info, err := toSettlementDomainInfo(storage, record)
		if err != nil {
			return nil, err
		}
		list = append(list, info)
	}
	return &SettlementDomainInfoList{Count: int(count), List: list}, nil
}

func (a *SettlementApi) GetBatch(domainId uint32, batchId uint64) (*SettlementBatch, error) {
	frontier, context, err := api.GetFrontierContext(a.chain, types.SettlementContract)
	if err != nil {
		return nil, err
	}
	entry, err := definition.GetBatchEntry(context.Storage(), domainId, batchId)
	if err == constants.ErrDataNonExistent {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return toSettlementBatch(entry, frontier.Height), nil
}

func (a *SettlementApi) GetBatches(domainId, pageIndex, pageSize uint32) (*SettlementBatchList, error) {
	if pageSize > api.RpcMaxPageSize {
		return nil, api.ErrPageSizeParamTooBig
	}
	frontier, context, err := api.GetFrontierContext(a.chain, types.SettlementContract)
	if err != nil {
		return nil, err
	}
	storage := context.Storage()
	cursor, err := definition.GetInputSequenceCursor(storage, domainId)
	if err != nil {
		return nil, err
	}
	count := cursor.NextBatchId
	start, end := api.GetRange(pageIndex, pageSize, uint32(count))
	list := make([]*SettlementBatch, 0)
	for i := start; i < end; i++ {
		entry, err := definition.GetBatchEntry(storage, domainId, count-1-uint64(i))
		if err != nil {
			return nil, err
		}
		list = append(list, toSettlementBatch(entry, frontier.Height))
	}
	return &SettlementBatchList{Count: int(count), List: list}, nil
}

func (a *SettlementApi) GetDeployRecord(domainId uint32, deployer types.Address, codeHash types.Hash) (*SettlementDeployRecord, error) {
	frontier, context, err := api.GetFrontierContext(a.chain, types.SettlementContract)
	if err != nil {
		return nil, err
	}
	record, err := definition.GetDeployRecord(context.Storage(), domainId, deployer, codeHash)
	if err == constants.ErrDataNonExistent {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return toSettlementDeployRecord(record, frontier.Height), nil
}

func (a *SettlementApi) GetDeployRecordsByDeployer(domainId uint32, deployer types.Address, pageIndex, pageSize uint32) (*SettlementDeployRecordList, error) {
	if pageSize > api.RpcMaxPageSize {
		return nil, api.ErrPageSizeParamTooBig
	}
	frontier, context, err := api.GetFrontierContext(a.chain, types.SettlementContract)
	if err != nil {
		return nil, err
	}
	records, err := definition.GetDeployRecordsByDeployer(context.Storage(), domainId, deployer)
	if err != nil {
		return nil, err
	}
	start, end := api.GetRange(pageIndex, pageSize, uint32(len(records)))
	list := make([]*SettlementDeployRecord, 0)
	for _, record := range records[start:end] {
		list = append(list, toSettlementDeployRecord(record, frontier.Height))
	}
	return &SettlementDeployRecordList{Count: len(records), List: list}, nil
}

func (a *SettlementApi) GetPauseState() (*SettlementPauseInfo, error) {
	_, context, err := api.GetFrontierContext(a.chain, types.SettlementContract)
	if err != nil {
		return nil, err
	}
	storage := context.Storage()
	global, err := definition.GetGlobalPauseState(storage)
	if err != nil {
		return nil, err
	}
	states, err := definition.GetDomainPauseStates(storage)
	if err != nil {
		return nil, err
	}
	domains := make([]*SettlementDomainPauseState, 0)
	for _, state := range states {
		domains = append(domains, &SettlementDomainPauseState{
			DomainId:     state.DomainId,
			SubmitPaused: state.SubmitPaused,
			RelayPaused:  state.RelayPaused,
		})
	}
	return &SettlementPauseInfo{
		Global:  &SettlementPauseState{SubmitPaused: global.SubmitPaused, RelayPaused: global.RelayPaused},
		Domains: domains,
	}, nil
}

func (a *SettlementApi) GetPeripheryConfig() (*SettlementPeripheryConfig, error) {
	_, context, err := api.GetFrontierContext(a.chain, types.SettlementContract)
	if err != nil {
		return nil, err
	}
	config, err := definition.GetPeripheryConfig(context.Storage())
	if err == constants.ErrDataNonExistent {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &SettlementPeripheryConfig{
		ZnnPerByteOfBytecode: config.ZnnPerByteOfBytecode.String(),
		ZnnPerByteOfState:    config.ZnnPerByteOfState.String(),
	}, nil
}

func (a *SettlementApi) GetExecutorSet() (*SettlementExecutorList, error) {
	_, context, err := api.GetFrontierContext(a.chain, types.SettlementContract)
	if err != nil {
		return nil, err
	}
	members, err := definition.GetExecutorSet(context.Storage())
	if err != nil {
		return nil, err
	}
	list := make([]*SettlementExecutor, 0)
	for _, member := range members {
		list = append(list, &SettlementExecutor{
			PillarName:         member.PillarName,
			ExecutorAddress:    member.ExecutorAddress,
			RegistrationHeight: member.RegistrationHeight,
			Status:             member.Status,
		})
	}
	return &SettlementExecutorList{Count: len(list), List: list}, nil
}

func (a *SettlementApi) GetFeeInfo() (*SettlementFeeInfo, error) {
	_, context, err := api.GetFrontierContext(a.chain, types.SettlementContract)
	if err != nil {
		return nil, err
	}
	storage := context.Storage()
	counters, err := definition.GetFeeCounters(storage)
	if err != nil {
		return nil, err
	}
	info := &SettlementFeeInfo{
		TotalFeesCollected:    counters.TotalFeesCollected.String(),
		TotalFeesBurned:       counters.TotalFeesBurned.String(),
		TotalFeesCredited:     counters.TotalFeesCredited.String(),
		TotalRewardsCollected: counters.TotalRewardsCollected.String(),
	}
	head, err := definition.GetFeeEpochQueueHead(storage)
	if err == nil {
		info.PendingEpochHead = &SettlementFeeEpoch{Epoch: head.Epoch, FinalityHeight: head.FinalityHeight}
	} else if err != constants.ErrDataNonExistent {
		return nil, err
	}
	return info, nil
}

func (a *SettlementApi) GetAssetInfo(domainId uint32, assetId types.Hash) (*SettlementAssetInfo, error) {
	_, context, err := api.GetFrontierContext(a.chain, types.SettlementContract)
	if err != nil {
		return nil, err
	}
	storage := context.Storage()
	config, err := definition.GetAssetConfig(storage, domainId, assetId)
	if err == constants.ErrDataNonExistent {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	counters, err := definition.GetConservationCounters(storage, domainId, assetId)
	if err != nil {
		return nil, err
	}
	cumulative, err := definition.GetCumulativeDepositTotal(storage, domainId, assetId)
	if err != nil {
		return nil, err
	}
	return &SettlementAssetInfo{
		DomainId:                 domainId,
		AssetId:                  assetId,
		MaxBatchWithdrawal:       config.MaxBatchWithdrawal.String(),
		CumulativeDeposited:      cumulative.CumulativeDeposited.String(),
		TotalDeposited:           counters.TotalDeposited.String(),
		PendingWithdrawalReserve: counters.PendingWithdrawalReserve.String(),
		TotalReleased:            counters.TotalReleased.String(),
	}, nil
}

func (a *SettlementApi) GetReleaseQueue(domainId, pageIndex, pageSize uint32) (*SettlementReleaseQueue, error) {
	if pageSize > api.RpcMaxPageSize {
		return nil, api.ErrPageSizeParamTooBig
	}
	_, context, err := api.GetFrontierContext(a.chain, types.SettlementContract)
	if err != nil {
		return nil, err
	}
	storage := context.Storage()
	meta, err := definition.GetReleaseQueueMeta(storage, domainId)
	if err != nil {
		return nil, err
	}
	count := meta.Tail - meta.Head
	start, end := api.GetRange(pageIndex, pageSize, uint32(count))
	list := make([]*SettlementReleaseQueueEntry, 0)
	for i := start; i < end; i++ {
		entry, err := definition.GetReleaseQueueEntry(storage, domainId, meta.Head+uint64(i))
		if err != nil {
			return nil, err
		}
		list = append(list, &SettlementReleaseQueueEntry{
			RegistrationIndex: entry.RegistrationIndex,
			Recipient:         entry.Recipient,
			AssetId:           entry.AssetId,
			Amount:            entry.Amount.String(),
			OutboxId:          entry.OutboxId,
		})
	}
	return &SettlementReleaseQueue{Head: meta.Head, Tail: meta.Tail, Count: int(count), List: list}, nil
}

func (a *SettlementApi) IsOutboxProcessed(outboxId types.Hash) (bool, error) {
	_, context, err := api.GetFrontierContext(a.chain, types.SettlementContract)
	if err != nil {
		return false, err
	}
	return definition.IsOutboxProcessed(context.Storage(), outboxId)
}

func (a *SettlementApi) GetTimeChallengesInfo() (*SettlementTimeChallengeList, error) {
	_, context, err := api.GetFrontierContext(a.chain, types.SettlementContract)
	if err != nil {
		return nil, err
	}
	storage := context.Storage()
	infos, err := definition.GetAllTimeChallengeInfos(storage)
	if err != nil {
		return nil, err
	}
	list := make([]*SettlementTimeChallenge, 0)
	for _, info := range infos {
		if info.ParamsHash.IsZero() {
			continue
		}
		delay, err := settlementChallengeDelay(storage, info.MethodName)
		if err != nil {
			return nil, err
		}
		list = append(list, &SettlementTimeChallenge{
			MethodName:           info.MethodName,
			ParamsHash:           info.ParamsHash,
			ChallengeStartHeight: info.ChallengeStartHeight,
			DueHeight:            info.ChallengeStartHeight + delay + 1,
		})
	}
	return &SettlementTimeChallengeList{Count: len(list), List: list}, nil
}

func toSettlementDomainInfo(storage db.DB, record *definition.DomainRecord) (*SettlementDomainInfo, error) {
	config, err := definition.GetDomainConfig(storage, record.DomainId)
	if err != nil {
		return nil, err
	}
	modes, err := definition.GetApprovedDAModeSet(storage, record.DomainId)
	if err != nil {
		return nil, err
	}
	pause, err := definition.GetDomainPauseState(storage, record.DomainId)
	if err != nil {
		return nil, err
	}
	cursor, err := definition.GetInputSequenceCursor(storage, record.DomainId)
	if err != nil {
		return nil, err
	}
	counter, err := definition.GetDomainInputCounter(storage, record.DomainId)
	if err != nil {
		return nil, err
	}
	info := &SettlementDomainInfo{
		DomainId:            record.DomainId,
		DomainClass:         record.DomainClass,
		RuntimeKind:         record.RuntimeKind,
		StfSpecHash:         record.StfSpecHash,
		InputSource:         record.InputSource,
		ExecProfile:         record.ExecProfile,
		ForeignProfile:      record.ForeignProfile,
		ProfileConfig:       record.ProfileConfig,
		ChainBinding:        record.ChainBinding,
		MinExecutors:        record.MinExecutors,
		ProposerPolicy:      record.ProposerPolicy,
		ValueCaps:           record.ValueCaps,
		FinalityModel:       record.FinalityModel,
		Status:              record.Status,
		WithdrawalDelay:     config.WithdrawalDelay,
		RuntimeUpgradeDelay: config.RuntimeUpgradeDelay,
		ApprovedDAModes:     make([]int, 0),
		PauseState:          &SettlementPauseState{SubmitPaused: pause.SubmitPaused, RelayPaused: pause.RelayPaused},
		InputCursor: &SettlementInputCursor{
			ConsumedInputCount:  cursor.ConsumedInputCount,
			NextBatchId:         cursor.NextBatchId,
			RecordsSweptThrough: cursor.RecordsSweptThrough,
			SweptInputCount:     cursor.SweptInputCount,
		},
		ReceivedInputCount: counter.Count,
	}
	for _, mode := range modes.Modes {
		info.ApprovedDAModes = append(info.ApprovedDAModes, int(mode))
	}
	escapeHatch, err := definition.GetEscapeHatchRecord(storage, record.DomainId)
	if err == nil {
		info.EscapeHatch = &SettlementEscapeHatch{TriggeredAtHeight: escapeHatch.TriggeredAtHeight}
	} else if err != constants.ErrDataNonExistent {
		return nil, err
	}
	return info, nil
}

func toSettlementBatch(entry *definition.BatchEntry, height uint64) *SettlementBatch {
	return &SettlementBatch{
		DomainId:          entry.DomainId,
		BatchId:           entry.BatchId,
		ProtocolVersion:   entry.ProtocolVersion,
		FirstInputSeq:     entry.FirstInputSeq,
		LastInputSeq:      entry.LastInputSeq,
		PreStateRoot:      entry.PreStateRoot,
		PostStateRoot:     entry.PostStateRoot,
		InputRoot:         entry.InputRoot,
		ReceiptRoot:       entry.ReceiptRoot,
		EventRoot:         entry.EventRoot,
		OutboxRoot:        entry.OutboxRoot,
		DaHash:            entry.DaHash,
		DaMode:            entry.DaMode,
		ExecutorPillar:    entry.ExecutorPillar,
		SubmittedAtHeight: entry.SubmittedAtHeight,
		FinalizeAtHeight:  entry.FinalizeAtHeight,
		AssetFlowSummary:  entry.AssetFlowSummary,
		ProofData:         entry.ProofData,
		State:             entry.State,
		IsFinalized:       entry.IsFinalized(height),
	}
}

// toSettlementDeployRecord applies the same expiry rule as the deploy
// handlers: a record expires once the height reaches StartHeight plus
// constants.DeploymentExpiryWindow.
func toSettlementDeployRecord(record *definition.DeployRecord, height uint64) *SettlementDeployRecord {
	expiresAt := record.StartHeight + constants.DeploymentExpiryWindow
	return &SettlementDeployRecord{
		DomainId:        record.DomainId,
		Deployer:        record.Deployer,
		CodeHash:        record.CodeHash,
		MetadataHash:    record.MetadataHash,
		TotalSize:       record.TotalSize,
		ChunkCount:      record.ChunkCount,
		StartHeight:     record.StartHeight,
		ReceivedCount:   record.ReceivedCount,
		ExpiresAtHeight: expiresAt,
		IsExpired:       height >= expiresAt,
	}
}

// settlementChallengeDelay returns the delay the apply path passes to
// TimeChallenge for methodName. The apply is refused while
// challengeStartHeight + delay >= frontier height.
func settlementChallengeDelay(storage db.DB, methodName string) (uint64, error) {
	prefix, scope, _ := strings.Cut(methodName, "/")
	switch prefix {
	case definition.SettlementSetPeripheryConfigMethodName,
		definition.SettlementSetDomainConfigMethodName,
		definition.SettlementSetAssetConfigMethodName,
		definition.SettlementSetApprovedDAModesMethodName:
		return constants.MinSoftDelay, nil
	case definition.SettlementRegisterExecutorMethodName,
		definition.SettlementInitiateEscapeHatchMethodName:
		return constants.MinAdministratorDelay, nil
	case definition.SettlementRegisterDomainMethodName:
		domainId, err := strconv.ParseUint(scope, 10, 32)
		if err != nil {
			return 0, errors.Wrapf(err, "invalid settlement time challenge %q", methodName)
		}
		config, err := definition.GetDomainConfig(storage, uint32(domainId))
		if err != nil {
			return 0, err
		}
		// The apply picks MinAdministratorDelay, RuntimeUpgradeDelay or
		// their maximum depending on which fields changed, so the maximum
		// is an upper bound.
		return max(constants.MinAdministratorDelay, config.RuntimeUpgradeDelay), nil
	default:
		return 0, errors.Errorf("unknown settlement time challenge %q", methodName)
	}
}
