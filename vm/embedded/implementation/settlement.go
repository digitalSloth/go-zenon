package implementation

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/big"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/crypto"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/vm/vm_context"
)

var (
	settlementLog = common.EmbeddedLogger.New("contract", "settlement")
)

// checkNetworkAdmin enforces the single global authority for admin-only
// Settlement methods.
func checkNetworkAdmin(sendBlock *nom.AccountBlock) error {
	if sendBlock.Address != types.NetworkAdmin {
		return constants.ErrPermissionDenied
	}
	return nil
}

// SettlementUpdateMethod is the permissionless, rate-limited crank that settles
// the oldest queued fee epoch once it has ended and every batch in it has
// finalized. Pillars send it automatically.
type SettlementUpdateMethod struct{ MethodName string }

func (p *SettlementUpdateMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedWWithdraw, nil
}
func (p *SettlementUpdateMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error

	if err := definition.ABISettlement.UnpackEmptyMethod(p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName)
	return err
}
func (p *SettlementUpdateMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	if err := checkAndPerformUpdate(context); err != nil {
		return nil, err
	}

	frontier, err := context.GetFrontierMomentum()
	if err != nil {
		return nil, err
	}
	activePillars, err := context.MomentumStore().GetActivePillars()
	if err != nil {
		return nil, err
	}
	burn, err := settleNextFeeEpoch(context.Storage(), frontier.Height, activePillars)
	if err != nil {
		return nil, err
	}
	return burnBlocks(burn), nil
}

// burnBlocks returns the descendant that burns the settled burn share, or
// none when there is nothing to burn.
func burnBlocks(burn *big.Int) []*nom.AccountBlock {
	if burn == nil || burn.Sign() == 0 {
		return nil
	}
	return []*nom.AccountBlock{
		{
			Address:       types.SettlementContract,
			ToAddress:     types.TokenContract,
			BlockType:     nom.BlockTypeContractSend,
			Amount:        burn,
			TokenStandard: types.ZnnTokenStandard,
			Data:          definition.ABIToken.PackMethodPanic(definition.BurnMethodName),
		},
	}
}

// SettlementReleaseWithdrawalsMethod is the per-domain release crank. It carries
// the domainId whose release queue it drains, so its rate limit applies per
// domain: one domain's traffic cannot starve another domain's exits. The method
// validates and dispatches; releasing queued withdrawals is not implemented.
type SettlementReleaseWithdrawalsMethod struct{ MethodName string }

func (p *SettlementReleaseWithdrawalsMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementReleaseWithdrawalsMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	domainId := new(uint32)

	if err := definition.ABISettlement.UnpackMethod(domainId, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName, *domainId)
	return err
}
func (p *SettlementReleaseWithdrawalsMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	settlementLog.Debug("settlement method dispatched (not implemented)", "method", p.MethodName, "address", sendBlock.Address)
	return nil, constants.ErrNotImplemented
}

// valueCapWindow returns the rolling-window length carried in the first 8
// bytes (big-endian) of a non-empty valueCaps.
func valueCapWindow(valueCaps []byte) uint64 {
	return binary.BigEndian.Uint64(valueCaps[:8])
}

// getRegisteredDomain returns the domain's record, or ErrForbiddenParam
// when the domain is not registered.
func getRegisteredDomain(context vm_context.AccountVmContext, domainId uint32) (*definition.DomainRecord, error) {
	record, err := definition.GetDomainRecord(context.Storage(), domainId)
	if err == constants.ErrDataNonExistent {
		return nil, constants.ErrForbiddenParam
	}
	return record, err
}

// ============================================================================
// RegisterDomain
// ============================================================================

type SettlementRegisterDomainParam struct {
	DomainId            uint32
	DomainClass         uint8
	RuntimeKind         uint8
	StfSpecHash         types.Hash
	InputSource         uint8
	ExecProfile         uint8
	ForeignProfile      uint8
	ProfileConfig       []byte
	ChainBinding        []byte
	MinExecutors        uint8
	ProposerPolicy      uint8
	ValueCaps           []byte
	FinalityModel       uint8
	Status              uint8
	WithdrawalDelay     uint64
	RuntimeUpgradeDelay uint64
}

type SettlementRegisterDomainMethod struct{ MethodName string }

func (p *SettlementRegisterDomainMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementRegisterDomainMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	param := new(SettlementRegisterDomainParam)

	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}

	if param.DomainId == 0 {
		return constants.ErrForbiddenParam
	}
	if param.DomainClass != definition.DomainClassExecution ||
		param.RuntimeKind != definition.RuntimeKindWASM ||
		param.InputSource != definition.InputSourceL1Native ||
		param.ExecProfile != definition.ExecProfileAttestation ||
		param.ForeignProfile != definition.ForeignProfileNone ||
		param.ProposerPolicy != definition.ProposerPolicySingle ||
		param.FinalityModel != definition.FinalityModelDelay ||
		param.MinExecutors != 1 {
		return constants.ErrForbiddenParam
	}
	if len(param.ChainBinding) != 0 || len(param.ProfileConfig) != 0 {
		return constants.ErrForbiddenParam
	}
	if param.Status != definition.DomainStatusActive && param.Status != definition.DomainStatusPaused {
		return constants.ErrForbiddenParam
	}
	if !constants.IsWithdrawalDelayInBounds(param.WithdrawalDelay) {
		return constants.ErrForbiddenParam
	}
	if !constants.IsRuntimeUpgradeDelayValid(param.RuntimeUpgradeDelay, param.WithdrawalDelay) {
		return constants.ErrForbiddenParam
	}
	if len(param.ValueCaps) != 0 {
		if len(param.ValueCaps) < 8 || !constants.IsValueCapWindowValid(valueCapWindow(param.ValueCaps), param.WithdrawalDelay) {
			return constants.ErrForbiddenParam
		}
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName,
		param.DomainId,
		param.DomainClass,
		param.RuntimeKind,
		param.StfSpecHash,
		param.InputSource,
		param.ExecProfile,
		param.ForeignProfile,
		param.ProfileConfig,
		param.ChainBinding,
		param.MinExecutors,
		param.ProposerPolicy,
		param.ValueCaps,
		param.FinalityModel,
		param.Status,
		param.WithdrawalDelay,
		param.RuntimeUpgradeDelay,
	)
	return err
}
func (p *SettlementRegisterDomainMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	if err := checkNetworkAdmin(sendBlock); err != nil {
		return nil, err
	}

	param := new(SettlementRegisterDomainParam)
	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, sendBlock.Data); err != nil {
		return nil, err
	}

	last, err := definition.GetLastDomainId(context.Storage())
	if err != nil {
		return nil, err
	}
	next := last + 1
	if param.DomainId > next {
		return nil, constants.ErrForbiddenParam
	}

	if param.DomainId == next {
		record := &definition.DomainRecord{
			DomainRecordKey: definition.DomainRecordKey{DomainId: param.DomainId},
			DomainClass:     param.DomainClass,
			RuntimeKind:     param.RuntimeKind,
			StfSpecHash:     param.StfSpecHash,
			InputSource:     param.InputSource,
			ExecProfile:     param.ExecProfile,
			ForeignProfile:  param.ForeignProfile,
			ProfileConfig:   param.ProfileConfig,
			ChainBinding:    param.ChainBinding,
			MinExecutors:    param.MinExecutors,
			ProposerPolicy:  param.ProposerPolicy,
			ValueCaps:       param.ValueCaps,
			FinalityModel:   param.FinalityModel,
			Status:          param.Status,
		}
		config := &definition.DomainConfig{
			DomainId:            param.DomainId,
			WithdrawalDelay:     param.WithdrawalDelay,
			RuntimeUpgradeDelay: param.RuntimeUpgradeDelay,
		}
		common.DealWithErr(record.Save(context.Storage()))
		common.DealWithErr(config.Save(context.Storage()))
		common.DealWithErr(definition.SetLastDomainId(context.Storage(), param.DomainId))
		return nil, nil
	}

	record, err := definition.GetDomainRecord(context.Storage(), param.DomainId)
	if err != nil {
		return nil, err
	}
	config, err := definition.GetDomainConfig(context.Storage(), param.DomainId)
	if err != nil {
		return nil, err
	}

	if param.DomainClass != record.DomainClass ||
		param.RuntimeKind != record.RuntimeKind ||
		param.InputSource != record.InputSource ||
		param.ExecProfile != record.ExecProfile ||
		param.ForeignProfile != record.ForeignProfile ||
		!bytes.Equal(param.ProfileConfig, record.ProfileConfig) ||
		!bytes.Equal(param.ChainBinding, record.ChainBinding) ||
		param.ProposerPolicy != record.ProposerPolicy ||
		param.FinalityModel != record.FinalityModel {
		return nil, constants.ErrForbiddenParam
	}
	if param.WithdrawalDelay != config.WithdrawalDelay || param.RuntimeUpgradeDelay != config.RuntimeUpgradeDelay {
		return nil, constants.ErrForbiddenParam
	}

	custodyChanged := !bytes.Equal(param.ValueCaps, record.ValueCaps) || param.MinExecutors != record.MinExecutors
	specChanged := param.StfSpecHash != record.StfSpecHash
	statusChanged := param.Status != record.Status
	if !custodyChanged && !specChanged && !statusChanged {
		return nil, constants.ErrForbiddenParam
	}

	// Status never enters a time challenge and applies on every call that
	// returns without error.
	record.Status = param.Status

	if custodyChanged || specChanged {
		var delay uint64
		if custodyChanged {
			delay = constants.MinAdministratorDelay
		}
		if specChanged && config.RuntimeUpgradeDelay > delay {
			delay = config.RuntimeUpgradeDelay
		}
		paramsHash := crypto.Hash(
			common.Uint32ToBytes(param.DomainId),
			param.StfSpecHash.Bytes(),
			[]byte{param.MinExecutors},
			common.Uint64ToBytes(param.WithdrawalDelay),
			common.Uint64ToBytes(param.RuntimeUpgradeDelay),
			param.ValueCaps,
		)
		timeChallengeInfo, err := TimeChallenge(context, fmt.Sprintf("%s/%d", p.MethodName, param.DomainId), paramsHash, delay)
		if err != nil {
			return nil, err
		}
		// if paramsHash is not zero it means we had a new challenge; only the status is applied
		if !timeChallengeInfo.ParamsHash.IsZero() {
			common.DealWithErr(record.Save(context.Storage()))
			return nil, nil
		}
		record.ValueCaps = param.ValueCaps
		record.MinExecutors = param.MinExecutors
		record.StfSpecHash = param.StfSpecHash
	}

	common.DealWithErr(record.Save(context.Storage()))
	return nil, nil
}

// ============================================================================
// SetDomainConfig
// ============================================================================

type SettlementDomainConfigParam struct {
	DomainId            uint32
	WithdrawalDelay     uint64
	RuntimeUpgradeDelay uint64
}

type SettlementSetDomainConfigMethod struct{ MethodName string }

func (p *SettlementSetDomainConfigMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementSetDomainConfigMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	param := new(SettlementDomainConfigParam)

	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}

	if !constants.IsWithdrawalDelayInBounds(param.WithdrawalDelay) {
		return constants.ErrForbiddenParam
	}
	if !constants.IsRuntimeUpgradeDelayValid(param.RuntimeUpgradeDelay, param.WithdrawalDelay) {
		return constants.ErrForbiddenParam
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName, param.DomainId, param.WithdrawalDelay, param.RuntimeUpgradeDelay)
	return err
}
func (p *SettlementSetDomainConfigMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	if err := checkNetworkAdmin(sendBlock); err != nil {
		return nil, err
	}

	param := new(SettlementDomainConfigParam)
	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, sendBlock.Data); err != nil {
		return nil, err
	}

	record, err := getRegisteredDomain(context, param.DomainId)
	if err != nil {
		return nil, err
	}
	if len(record.ValueCaps) != 0 && !constants.IsValueCapWindowValid(valueCapWindow(record.ValueCaps), param.WithdrawalDelay) {
		return nil, constants.ErrForbiddenParam
	}

	paramsHash := crypto.Hash(
		common.Uint32ToBytes(param.DomainId),
		common.Uint64ToBytes(param.WithdrawalDelay),
		common.Uint64ToBytes(param.RuntimeUpgradeDelay),
	)
	timeChallengeInfo, err := TimeChallenge(context, fmt.Sprintf("%s/%d", p.MethodName, param.DomainId), paramsHash, constants.MinSoftDelay)
	if err != nil {
		return nil, err
	}
	// if paramsHash is not zero it means we had a new challenge and we can't go further to save the change into local db
	if !timeChallengeInfo.ParamsHash.IsZero() {
		return nil, nil
	}

	config := &definition.DomainConfig{
		DomainId:            param.DomainId,
		WithdrawalDelay:     param.WithdrawalDelay,
		RuntimeUpgradeDelay: param.RuntimeUpgradeDelay,
	}
	common.DealWithErr(config.Save(context.Storage()))
	return nil, nil
}

// ============================================================================
// SetAssetConfig
// ============================================================================

type SettlementAssetConfigParam struct {
	DomainId           uint32
	AssetId            types.Hash
	MaxBatchWithdrawal *big.Int
}

type SettlementSetAssetConfigMethod struct{ MethodName string }

func (p *SettlementSetAssetConfigMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementSetAssetConfigMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	param := new(SettlementAssetConfigParam)

	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}

	if _, err := definition.ZTSFromAssetId(param.AssetId); err != nil {
		return constants.ErrForbiddenParam
	}
	if !constants.IsMaxBatchWithdrawalInBounds(param.MaxBatchWithdrawal) {
		return constants.ErrForbiddenParam
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName, param.DomainId, param.AssetId, param.MaxBatchWithdrawal)
	return err
}
func (p *SettlementSetAssetConfigMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	if err := checkNetworkAdmin(sendBlock); err != nil {
		return nil, err
	}

	param := new(SettlementAssetConfigParam)
	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, sendBlock.Data); err != nil {
		return nil, err
	}

	if _, err := getRegisteredDomain(context, param.DomainId); err != nil {
		return nil, err
	}
	stored, err := definition.GetAssetConfig(context.Storage(), param.DomainId, param.AssetId)
	if err == nil {
		if param.MaxBatchWithdrawal.Cmp(stored.MaxBatchWithdrawal) < 0 {
			return nil, constants.ErrForbiddenParam
		}
	} else if err != constants.ErrDataNonExistent {
		return nil, err
	}

	paramsHash := crypto.Hash(
		common.Uint32ToBytes(param.DomainId),
		param.AssetId.Bytes(),
		common.BigIntToBytes(param.MaxBatchWithdrawal),
	)
	scope := fmt.Sprintf("%s/%d/%s", p.MethodName, param.DomainId, param.AssetId.String())
	timeChallengeInfo, err := TimeChallenge(context, scope, paramsHash, constants.MinSoftDelay)
	if err != nil {
		return nil, err
	}
	// if paramsHash is not zero it means we had a new challenge and we can't go further to save the change into local db
	if !timeChallengeInfo.ParamsHash.IsZero() {
		return nil, nil
	}

	config := &definition.AssetConfig{
		DomainId:           param.DomainId,
		AssetId:            param.AssetId,
		MaxBatchWithdrawal: param.MaxBatchWithdrawal,
	}
	common.DealWithErr(config.Save(context.Storage()))
	return nil, nil
}

// ============================================================================
// SetApprovedDAModes
// ============================================================================

type SettlementApprovedDAModesParam struct {
	DomainId uint32
	Modes    []byte
}

type SettlementSetApprovedDAModesMethod struct{ MethodName string }

func (p *SettlementSetApprovedDAModesMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementSetApprovedDAModesMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	param := new(SettlementApprovedDAModesParam)

	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}

	if len(param.Modes) == 0 {
		return constants.ErrForbiddenParam
	}
	seen := make(map[byte]bool)
	for _, mode := range param.Modes {
		if mode != definition.DAModeBestEffort || seen[mode] {
			return constants.ErrForbiddenParam
		}
		seen[mode] = true
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName, param.DomainId, param.Modes)
	return err
}
func (p *SettlementSetApprovedDAModesMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	if err := checkNetworkAdmin(sendBlock); err != nil {
		return nil, err
	}

	param := new(SettlementApprovedDAModesParam)
	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, sendBlock.Data); err != nil {
		return nil, err
	}

	if _, err := getRegisteredDomain(context, param.DomainId); err != nil {
		return nil, err
	}

	paramsHash := crypto.Hash(common.Uint32ToBytes(param.DomainId), param.Modes)
	timeChallengeInfo, err := TimeChallenge(context, fmt.Sprintf("%s/%d", p.MethodName, param.DomainId), paramsHash, constants.MinSoftDelay)
	if err != nil {
		return nil, err
	}
	// if paramsHash is not zero it means we had a new challenge and we can't go further to save the change into local db
	if !timeChallengeInfo.ParamsHash.IsZero() {
		return nil, nil
	}

	set := &definition.ApprovedDAModeSet{DomainId: param.DomainId, Modes: param.Modes}
	common.DealWithErr(set.Save(context.Storage()))
	return nil, nil
}

// ============================================================================
// Pause
// ============================================================================

type SettlementPauseParam struct {
	DomainId uint32
	IsGlobal bool
	Scope    uint8
	Active   bool
}

type SettlementPauseMethod struct{ MethodName string }

func (p *SettlementPauseMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementPauseMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	param := new(SettlementPauseParam)

	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}

	if param.Scope != definition.PauseScopeSubmit && param.Scope != definition.PauseScopeRelay {
		return constants.ErrForbiddenParam
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName, param.DomainId, param.IsGlobal, param.Scope, param.Active)
	return err
}
func (p *SettlementPauseMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	if err := checkNetworkAdmin(sendBlock); err != nil {
		return nil, err
	}

	param := new(SettlementPauseParam)
	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, sendBlock.Data); err != nil {
		return nil, err
	}

	var state *definition.PauseState
	var err error
	if param.IsGlobal {
		state, err = definition.GetGlobalPauseState(context.Storage())
	} else {
		state, err = definition.GetDomainPauseState(context.Storage(), param.DomainId)
	}
	if err != nil {
		return nil, err
	}

	switch param.Scope {
	case definition.PauseScopeSubmit:
		state.SubmitPaused = param.Active
	case definition.PauseScopeRelay:
		state.RelayPaused = param.Active
	}

	if param.IsGlobal {
		common.DealWithErr(state.SaveGlobal(context.Storage()))
	} else {
		common.DealWithErr(state.SaveDomain(context.Storage(), param.DomainId))
	}
	return nil, nil
}

// ============================================================================
// InitiateEscapeHatch (stub: signature + durable record + time-lock only;
// forced-withdrawal mechanics are a tracked follow-up)
// ============================================================================

type SettlementInitiateEscapeHatchMethod struct{ MethodName string }

func (p *SettlementInitiateEscapeHatchMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementInitiateEscapeHatchMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	domainId := new(uint32)

	if err := definition.ABISettlement.UnpackMethod(domainId, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName, *domainId)
	return err
}
func (p *SettlementInitiateEscapeHatchMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	if err := checkNetworkAdmin(sendBlock); err != nil {
		return nil, err
	}

	domainId := new(uint32)
	if err := definition.ABISettlement.UnpackMethod(domainId, p.MethodName, sendBlock.Data); err != nil {
		return nil, err
	}

	paramsHash := crypto.Hash(common.Uint32ToBytes(*domainId))
	timeChallengeInfo, err := TimeChallenge(context, p.MethodName, paramsHash, constants.MinAdministratorDelay)
	if err != nil {
		return nil, err
	}
	// if paramsHash is not zero it means we had a new challenge and we can't go further to save the change into local db
	if !timeChallengeInfo.ParamsHash.IsZero() {
		return nil, nil
	}

	momentum, err := context.GetFrontierMomentum()
	common.DealWithErr(err)

	record := &definition.EscapeHatchRecord{
		DomainId:          *domainId,
		TriggeredAtHeight: momentum.Height,
	}
	common.DealWithErr(record.Save(context.Storage()))
	return nil, nil
}

// ============================================================================
// Executor registration, periphery config, fee settlement
// ============================================================================

// activeExecutorPillar returns the pillar name of the ACTIVE executor-set
// member whose executor address is caller, when that pillar is in
// activePillars; otherwise constants.ErrPermissionDenied.
func activeExecutorPillar(storage db.DB, activePillars []*definition.PillarInfo, caller types.Address) (string, error) {
	set, err := definition.GetExecutorSet(storage)
	if err != nil {
		return "", err
	}
	for _, member := range set {
		if member.ExecutorAddress != caller || member.Status != definition.ExecutorStatusActive {
			continue
		}
		for _, pillar := range activePillars {
			if pillar.Name == member.PillarName {
				return member.PillarName, nil
			}
		}
	}
	return "", constants.ErrPermissionDenied
}

func feeEpochOf(height uint64) uint64 {
	return height / uint64(constants.MomentumsPerEpoch)
}

// accrueBatchFees adds the fees of the batch's assigned-index records to
// the pending amount of (feeEpochOf(entry.SubmittedAtHeight),
// entry.ExecutorPillar), and queues that epoch. inputs are the domain's
// records in [entry.FirstInputSeq, entry.LastInputSeq], supplied by
// SubmitBatch, so the cost is bounded by MaxBatchInputs.
func accrueBatchFees(storage db.DB, entry *definition.BatchEntry, inputs []*definition.AssignedIndexRecord) error {
	epoch := feeEpochOf(entry.SubmittedAtHeight)

	sum := new(big.Int)
	for _, input := range inputs {
		if input.Fee != nil {
			sum.Add(sum, input.Fee)
		}
	}
	if sum.Sign() > 0 {
		pending, err := definition.GetPendingFee(storage, epoch, entry.ExecutorPillar)
		if err != nil {
			return err
		}
		pending.Amount.Add(pending.Amount, sum)
		if err := pending.Save(storage); err != nil {
			return err
		}
	}

	queued, err := definition.GetFeeEpoch(storage, epoch)
	if err == constants.ErrDataNonExistent {
		queued = &definition.FeeEpoch{Epoch: epoch, FinalityHeight: entry.FinalizeAtHeight}
	} else if err != nil {
		return err
	} else if entry.FinalizeAtHeight > queued.FinalityHeight {
		queued.FinalityHeight = entry.FinalizeAtHeight
	}
	return queued.Save(storage)
}

// feeGuardHolds reports whether Burned+Credited <= Collected and
// RewardsCollected <= Credited.
func feeGuardHolds(c *definition.FeeCounters) bool {
	settled := new(big.Int).Add(c.TotalFeesBurned, c.TotalFeesCredited)
	return settled.Cmp(c.TotalFeesCollected) <= 0 && c.TotalRewardsCollected.Cmp(c.TotalFeesCredited) <= 0
}

// settleNextFeeEpoch settles the head of the pending-epoch queue and
// returns the burn total. It returns nil, having written nothing, when the
// queue is empty, the head is not settleable, PAUSE_RELAY is in force
// anywhere, or the guard would break.
func settleNextFeeEpoch(storage db.DB, height uint64, activePillars []*definition.PillarInfo) (*big.Int, error) {
	head, err := definition.GetFeeEpochQueueHead(storage)
	if err == constants.ErrDataNonExistent {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if height < (head.Epoch+1)*uint64(constants.MomentumsPerEpoch) || height < head.FinalityHeight {
		return nil, nil
	}
	paused, err := definition.IsRelayPausedAnywhere(storage)
	if err != nil {
		return nil, err
	}
	if paused {
		return nil, nil
	}

	rewardAddress := make(map[string]types.Address, len(activePillars))
	for _, pillar := range activePillars {
		rewardAddress[pillar.Name] = pillar.RewardWithdrawAddress
	}
	pending, err := definition.GetPendingFeesForEpoch(storage, head.Epoch)
	if err != nil {
		return nil, err
	}

	burnTotal := new(big.Int)
	creditTotal := new(big.Int)
	credits := make(map[types.Address]*big.Int)
	creditOrder := make([]types.Address, 0, len(pending))
	for _, fee := range pending {
		address, active := rewardAddress[fee.PillarName]
		if !active {
			burnTotal.Add(burnTotal, fee.Amount)
			continue
		}
		burn := new(big.Int).Mul(fee.Amount, big.NewInt(constants.FeeBurnShare))
		burn.Div(burn, big.NewInt(100))
		credit := new(big.Int).Sub(fee.Amount, burn)
		burnTotal.Add(burnTotal, burn)
		creditTotal.Add(creditTotal, credit)
		if _, seen := credits[address]; !seen {
			credits[address] = new(big.Int)
			creditOrder = append(creditOrder, address)
		}
		credits[address].Add(credits[address], credit)
	}

	counters, err := definition.GetFeeCounters(storage)
	if err != nil {
		return nil, err
	}
	counters.TotalFeesBurned = new(big.Int).Add(counters.TotalFeesBurned, burnTotal)
	counters.TotalFeesCredited = new(big.Int).Add(counters.TotalFeesCredited, creditTotal)
	if !feeGuardHolds(counters) {
		return nil, nil
	}

	for _, address := range creditOrder {
		address := address
		deposit, err := definition.GetRewardDeposit(storage, &address)
		if err != nil {
			return nil, err
		}
		deposit.Znn.Add(deposit.Znn, credits[address])
		if err := deposit.Save(storage); err != nil {
			return nil, err
		}
	}
	if err := counters.Save(storage); err != nil {
		return nil, err
	}
	for _, fee := range pending {
		if err := fee.Delete(storage); err != nil {
			return nil, err
		}
	}
	if err := head.Delete(storage); err != nil {
		return nil, err
	}
	return burnTotal, nil
}

// collectFeeReward pays caller's ZNN RewardDeposit and returns the one
// descendant send.
func collectFeeReward(storage db.DB, caller types.Address) ([]*nom.AccountBlock, error) {
	deposit, err := definition.GetRewardDeposit(storage, &caller)
	if err != nil {
		return nil, err
	}
	if deposit.Znn.Sign() == 0 {
		return nil, constants.ErrNothingToWithdraw
	}
	counters, err := definition.GetFeeCounters(storage)
	if err != nil {
		return nil, err
	}
	counters.TotalRewardsCollected = new(big.Int).Add(counters.TotalRewardsCollected, deposit.Znn)
	if !feeGuardHolds(counters) {
		return nil, constants.ErrSettlementFeeGuard
	}
	if err := counters.Save(storage); err != nil {
		return nil, err
	}
	if err := deposit.Delete(storage); err != nil {
		return nil, err
	}
	return []*nom.AccountBlock{
		{
			Address:       types.SettlementContract,
			ToAddress:     caller,
			BlockType:     nom.BlockTypeContractSend,
			Amount:        deposit.Znn,
			TokenStandard: types.ZnnTokenStandard,
			Data:          []byte{},
		},
	}, nil
}

// ============================================================================
// CollectReward
// ============================================================================

type SettlementCollectRewardMethod struct{ MethodName string }

func (p *SettlementCollectRewardMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedWWithdraw, nil
}
func (p *SettlementCollectRewardMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error

	if err := definition.ABISettlement.UnpackEmptyMethod(p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName)
	return err
}
func (p *SettlementCollectRewardMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	paused, err := definition.IsRelayPausedAnywhere(context.Storage())
	if err != nil {
		return nil, err
	}
	if paused {
		return nil, constants.ErrSettlementRelayPaused
	}
	return collectFeeReward(context.Storage(), sendBlock.Address)
}

// ============================================================================
// RegisterExecutor
// ============================================================================

type SettlementRegisterExecutorParam struct {
	PillarName      string
	ExecutorAddress types.Address
}

type SettlementRegisterExecutorMethod struct{ MethodName string }

func (p *SettlementRegisterExecutorMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementRegisterExecutorMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	param := new(SettlementRegisterExecutorParam)

	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName, param.PillarName, param.ExecutorAddress)
	return err
}
func (p *SettlementRegisterExecutorMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	if err := checkNetworkAdmin(sendBlock); err != nil {
		return nil, err
	}

	param := new(SettlementRegisterExecutorParam)
	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, sendBlock.Data); err != nil {
		return nil, err
	}

	activePillars, err := context.MomentumStore().GetActivePillars()
	if err != nil {
		return nil, err
	}
	isActive := false
	for _, pillar := range activePillars {
		if pillar.Name == param.PillarName {
			isActive = true
			break
		}
	}
	if !isActive {
		return nil, constants.ErrForbiddenParam
	}

	paramsHash := crypto.Hash([]byte(param.PillarName), param.ExecutorAddress.Bytes())
	timeChallengeInfo, err := TimeChallenge(context, p.MethodName, paramsHash, constants.MinAdministratorDelay)
	if err != nil {
		return nil, err
	}
	// if paramsHash is not zero it means we had a new challenge and we can't go further to save the change into local db
	if !timeChallengeInfo.ParamsHash.IsZero() {
		return nil, nil
	}

	frontier, err := context.GetFrontierMomentum()
	if err != nil {
		return nil, err
	}
	set, err := definition.GetExecutorSet(context.Storage())
	if err != nil {
		return nil, err
	}
	for _, member := range set {
		common.DealWithErr(member.Delete(context.Storage()))
	}
	member := &definition.ExecutorSetMember{
		PillarName:         param.PillarName,
		ExecutorAddress:    param.ExecutorAddress,
		RegistrationHeight: frontier.Height,
		Status:             definition.ExecutorStatusActive,
	}
	common.DealWithErr(member.Save(context.Storage()))
	return nil, nil
}

// ============================================================================
// SetPeripheryConfig
// ============================================================================

type SettlementPeripheryConfigParam struct {
	ZnnPerByteOfBytecode *big.Int
	ZnnPerByteOfState    *big.Int
}

type SettlementSetPeripheryConfigMethod struct{ MethodName string }

func (p *SettlementSetPeripheryConfigMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementSetPeripheryConfigMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	param := new(SettlementPeripheryConfigParam)

	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}

	if !constants.IsZnnPerByteOfBytecodeInBounds(param.ZnnPerByteOfBytecode) ||
		!constants.IsZnnPerByteOfStateInBounds(param.ZnnPerByteOfState) {
		return constants.ErrForbiddenParam
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName, param.ZnnPerByteOfBytecode, param.ZnnPerByteOfState)
	return err
}
func (p *SettlementSetPeripheryConfigMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	if err := checkNetworkAdmin(sendBlock); err != nil {
		return nil, err
	}

	param := new(SettlementPeripheryConfigParam)
	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, sendBlock.Data); err != nil {
		return nil, err
	}

	paramsHash := crypto.Hash(
		common.BigIntToBytes(param.ZnnPerByteOfBytecode),
		common.BigIntToBytes(param.ZnnPerByteOfState),
	)
	timeChallengeInfo, err := TimeChallenge(context, p.MethodName, paramsHash, constants.MinSoftDelay)
	if err != nil {
		return nil, err
	}
	// if paramsHash is not zero it means we had a new challenge and we can't go further to save the change into local db
	if !timeChallengeInfo.ParamsHash.IsZero() {
		return nil, nil
	}

	config := &definition.PeripheryConfig{
		ZnnPerByteOfBytecode: param.ZnnPerByteOfBytecode,
		ZnnPerByteOfState:    param.ZnnPerByteOfState,
	}
	common.DealWithErr(config.Save(context.Storage()))
	return nil, nil
}

// ============================================================================
// Deposit
// ============================================================================

// getInputDomain returns the record of the domain an input method
// addresses. It returns ErrForbiddenParam when the domain is not
// registered or its status is DomainStatusPaused: such a call is not an
// input. Admin methods keep using getRegisteredDomain, which accepts a
// paused domain.
func getInputDomain(context vm_context.AccountVmContext, domainId uint32) (*definition.DomainRecord, error) {
	record, err := getRegisteredDomain(context, domainId)
	if err != nil {
		return nil, err
	}
	if record.Status == definition.DomainStatusPaused {
		return nil, constants.ErrForbiddenParam
	}
	return record, nil
}

// admitInput assigns the next global input index to an accepted input of
// domainId. It saves the assigned-index record {assetId, amount, fee} under
// (domainId, index), adds a positive amount to the (domainId, assetId)
// cumulative deposit total, and advances the global counter. It also
// records the global index under the next ordinal of domainId's input
// sequence and advances the domain counter. Callers run it only after
// every receive check has passed. A nil amount or fee is
// zero; an input without a deposit passes types.ZeroHash and nil.
func admitInput(storage db.DB, domainId uint32, assetId types.Hash, amount, fee *big.Int) error {
	counter, err := definition.GetReceivedInputCounter(storage)
	if err != nil {
		return err
	}
	if counter.Count == math.MaxUint64 {
		return constants.ErrSettlementInputIndexExhausted
	}
	if amount == nil {
		amount = new(big.Int)
	}

	record := &definition.AssignedIndexRecord{
		AssignedIndexRecordKey: definition.AssignedIndexRecordKey{DomainId: domainId, GlobalInputIndex: counter.Count},
		AssetId:                assetId,
		Amount:                 amount,
		Fee:                    fee,
	}
	if err := record.Save(storage); err != nil {
		return err
	}

	domainCounter, err := definition.GetDomainInputCounter(storage, domainId)
	if err != nil {
		return err
	}
	ordinal := &definition.DomainInputOrdinal{DomainId: domainId, Ordinal: domainCounter.Count, GlobalInputIndex: counter.Count}
	if err := ordinal.Save(storage); err != nil {
		return err
	}
	domainCounter.Count++
	if err := domainCounter.Save(storage); err != nil {
		return err
	}

	if amount.Sign() > 0 {
		total, err := definition.GetCumulativeDepositTotal(storage, domainId, assetId)
		if err != nil {
			return err
		}
		total.CumulativeDeposited = new(big.Int).Add(total.CumulativeDeposited, amount)
		if err := total.Save(storage); err != nil {
			return err
		}
	}

	counter.Count++
	return counter.Save(storage)
}

// saveCustodyCounters saves c when
// totalReleased + pendingWithdrawalReserve <= totalDeposited, the ESCROW
// conservation invariant. Otherwise it returns
// ErrSettlementConservationViolated and writes nothing. Every path that
// changes the counters saves them through this function.
func saveCustodyCounters(storage db.DB, c *definition.ConservationCounters) error {
	committed := new(big.Int).Add(c.TotalReleased, c.PendingWithdrawalReserve)
	if committed.Cmp(c.TotalDeposited) > 0 {
		return constants.ErrSettlementConservationViolated
	}
	return c.Save(storage)
}

const (
	assetFlowEntrySize = 96
	assetFlowAmountLen = 32
)

type assetFlowEntry struct {
	AssetId         types.Hash
	DepositCredit   *big.Int
	WithdrawalDebit *big.Int
}

// decodeAssetFlowSummary decodes the SPEC §27.6 encoding: u32 BE count N,
// then N entries of asset_id(32) ‖ deposit_credit(u256 BE) ‖
// withdrawal_debit(u256 BE). The caller passes exactly the summary bytes:
// trailing bytes are an error, so a caller reading the summary inline
// from a larger encoding reads N, bounds it, and slices 4 + N*96 bytes
// first.
func decodeAssetFlowSummary(b []byte) ([]assetFlowEntry, error) {
	if len(b) < 4 {
		return nil, constants.ErrSettlementInvalidAssetFlow
	}
	n := uint64(binary.BigEndian.Uint32(b[:4]))
	if n > constants.MaxAssetsPerBatch || uint64(len(b)) != 4+n*assetFlowEntrySize {
		return nil, constants.ErrSettlementInvalidAssetFlow
	}

	entries := make([]assetFlowEntry, 0, n)
	for i := uint64(0); i < n; i++ {
		raw := b[4+i*assetFlowEntrySize : 4+(i+1)*assetFlowEntrySize]
		var entry assetFlowEntry
		copy(entry.AssetId[:], raw[:32])
		if _, err := definition.ZTSFromAssetId(entry.AssetId); err != nil {
			return nil, constants.ErrSettlementInvalidAssetFlow
		}
		if i > 0 && bytes.Compare(entry.AssetId[:], entries[i-1].AssetId[:]) <= 0 {
			return nil, constants.ErrSettlementInvalidAssetFlow
		}
		entry.DepositCredit = new(big.Int).SetBytes(raw[32 : 32+assetFlowAmountLen])
		entry.WithdrawalDebit = new(big.Int).SetBytes(raw[32+assetFlowAmountLen:])
		if entry.DepositCredit.Sign() == 0 && entry.WithdrawalDebit.Sign() == 0 {
			return nil, constants.ErrSettlementInvalidAssetFlow
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// verifyAssetFlowDeposits checks a batch's AssetFlowSummary against the
// deposits recorded at the batch's assigned indices. inputs are the
// domain's assigned-index records in [firstInputSeq, lastInputSeq],
// supplied by SubmitBatch, so the cost is bounded by MaxBatchInputs.
// WithdrawalDebit is not checked here.
func verifyAssetFlowDeposits(summary []byte, inputs []*definition.AssignedIndexRecord) error {
	entries, err := decodeAssetFlowSummary(summary)
	if err != nil {
		return err
	}

	deposited := make(map[types.Hash]*big.Int)
	for _, input := range inputs {
		if input.Amount == nil || input.Amount.Sign() <= 0 {
			continue
		}
		sum, ok := deposited[input.AssetId]
		if !ok {
			sum = new(big.Int)
			deposited[input.AssetId] = sum
		}
		sum.Add(sum, input.Amount)
	}

	for _, entry := range entries {
		sum, ok := deposited[entry.AssetId]
		if !ok {
			sum = new(big.Int)
		}
		if entry.DepositCredit.Cmp(sum) != 0 {
			return constants.ErrSettlementInvalidAssetFlow
		}
		delete(deposited, entry.AssetId)
	}
	if len(deposited) != 0 {
		return constants.ErrSettlementInvalidAssetFlow
	}
	return nil
}

type SettlementDepositParam struct {
	DomainId       uint32
	TargetContract types.Hash
	PlasmaLimit    uint64
}

type SettlementDepositMethod struct{ MethodName string }

func (p *SettlementDepositMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementDepositMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	param := new(SettlementDepositParam)

	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.Amount.Sign() <= 0 {
		return constants.ErrInvalidTokenOrAmount
	}
	if !constants.IsInputGasInBounds(param.PlasmaLimit) {
		return constants.ErrForbiddenParam
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName, param.DomainId, param.TargetContract, param.PlasmaLimit)
	return err
}

// ReceiveBlock custodies the deposit. The first three steps write
// nothing, so a rejected deposit consumes no input index and the VM
// returns the attached value. When admission fails after the counters are
// saved, the VM rollback discards every write.
func (p *SettlementDepositMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	param := new(SettlementDepositParam)
	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, sendBlock.Data); err != nil {
		return nil, err
	}

	if _, err := getInputDomain(context, param.DomainId); err != nil {
		return nil, err
	}

	storage := context.Storage()
	assetId := definition.AssetIdFromZTS(sendBlock.TokenStandard)
	if _, err := definition.GetAssetConfig(storage, param.DomainId, assetId); err == constants.ErrDataNonExistent {
		return nil, constants.ErrInvalidToken
	} else if err != nil {
		return nil, err
	}

	counters, err := definition.GetConservationCounters(storage, param.DomainId, assetId)
	if err != nil {
		return nil, err
	}
	counters.TotalDeposited = new(big.Int).Add(counters.TotalDeposited, sendBlock.Amount)
	if err := saveCustodyCounters(storage, counters); err != nil {
		return nil, err
	}

	if err := admitInput(storage, param.DomainId, assetId, sendBlock.Amount, nil); err != nil {
		return nil, err
	}
	return nil, nil
}

// ============================================================================
// SubmitBatch
// ============================================================================

// commitmentVersions holds the nine version fields of a batch commitment
// in wire order.
var commitmentVersions = [9]uint16{
	constants.SettlementProtocolVersion, constants.SettlementRuntimeProfileVersion,
	constants.SettlementMeteringVersion, constants.SettlementSerializationVersion,
	constants.SettlementStateDiffVersion, constants.SettlementExecutionResultVersion,
	constants.SettlementReceiptVersion, constants.SettlementOutboxVersion,
	constants.SettlementDAModeVersion,
}

const (
	commitmentVersionBlockLen = 18
	commitmentMinLen          = 303 // fixed fields plus the summary's u32 count
	commitmentSummaryOffset   = 299
)

// decodeBatchCommitment decodes a big-endian batch commitment: nine u16
// version fields, domainId (u32), batchId, firstInputSeq, lastInputSeq
// (u64), seven 32-byte hashes (preStateRoot, postStateRoot, inputRoot,
// receiptRoot, eventRoot, outboxRoot, DAHash), DAMode (u8), executorId
// (20 bytes), submittedAtHeight (u64), the asset flow summary and a u32
// proofData length that must be 0. It returns the entry, the executorId
// and the wire submittedAtHeight, and checks neither of the last two.
func decodeBatchCommitment(b []byte) (*definition.BatchEntry, types.Address, uint64, error) {
	var executor types.Address
	if len(b) < commitmentVersionBlockLen {
		return nil, executor, 0, constants.ErrForbiddenParam
	}
	for i, want := range commitmentVersions {
		if binary.BigEndian.Uint16(b[2*i:2*i+2]) != want {
			return nil, executor, 0, constants.ErrForbiddenParam
		}
	}
	if len(b) < commitmentMinLen {
		return nil, executor, 0, constants.ErrForbiddenParam
	}

	entry := &definition.BatchEntry{
		BatchEntryKey: definition.BatchEntryKey{
			DomainId: binary.BigEndian.Uint32(b[18:22]),
			BatchId:  binary.BigEndian.Uint64(b[22:30]),
		},
		ProtocolVersion: binary.BigEndian.Uint16(b[0:2]),
		FirstInputSeq:   binary.BigEndian.Uint64(b[30:38]),
		LastInputSeq:    binary.BigEndian.Uint64(b[38:46]),
		DaMode:          b[270],
		ProofData:       []byte{},
	}
	hashes := []*types.Hash{
		&entry.PreStateRoot, &entry.PostStateRoot, &entry.InputRoot, &entry.ReceiptRoot,
		&entry.EventRoot, &entry.OutboxRoot, &entry.DaHash,
	}
	for i, h := range hashes {
		copy(h[:], b[46+32*i:78+32*i])
	}
	copy(executor[:], b[271:291])
	submittedAtHeight := binary.BigEndian.Uint64(b[291:299])

	n := uint64(binary.BigEndian.Uint32(b[commitmentSummaryOffset:commitmentMinLen]))
	if n > constants.MaxAssetsPerBatch || uint64(len(b)-commitmentMinLen) < n*assetFlowEntrySize {
		return nil, executor, 0, constants.ErrSettlementInvalidAssetFlow
	}
	summaryEnd := commitmentMinLen + int(n)*assetFlowEntrySize
	entry.AssetFlowSummary = b[commitmentSummaryOffset:summaryEnd]
	if _, err := decodeAssetFlowSummary(entry.AssetFlowSummary); err != nil {
		return nil, executor, 0, err
	}

	rest := b[summaryEnd:]
	if len(rest) != 4 || binary.BigEndian.Uint32(rest) != 0 {
		return nil, executor, 0, constants.ErrForbiddenParam
	}
	return entry, executor, submittedAtHeight, nil
}

// collectBatchInputs returns the domain's assigned-index records from
// ordinal startOrdinal through the one whose global index is
// lastInputSeq. The first must have global index firstInputSeq. It
// returns ErrForbiddenParam when the range is not a contiguous run of
// the domain's received inputs or holds more than MaxBatchInputs.
func collectBatchInputs(storage db.DB, domainId uint32, startOrdinal, firstInputSeq, lastInputSeq uint64) ([]*definition.AssignedIndexRecord, error) {
	counter, err := definition.GetDomainInputCounter(storage, domainId)
	if err != nil {
		return nil, err
	}

	inputs := make([]*definition.AssignedIndexRecord, 0, constants.MaxBatchInputs)
	for i := uint64(0); i < constants.MaxBatchInputs; i++ {
		ordinal := startOrdinal + i
		if ordinal >= counter.Count {
			return nil, constants.ErrForbiddenParam
		}
		o, err := definition.GetDomainInputOrdinal(storage, domainId, ordinal)
		if err != nil {
			return nil, err
		}
		g := o.GlobalInputIndex
		if (i == 0 && g != firstInputSeq) || g > lastInputSeq {
			return nil, constants.ErrForbiddenParam
		}
		record, err := definition.GetAssignedIndexRecord(storage, domainId, g)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, record)
		if g == lastInputSeq {
			return inputs, nil
		}
	}
	return nil, constants.ErrForbiddenParam
}

// sweepFinalizedBatchRecords deletes the assigned-index records and
// ordinal entries of at most constants.MaxRecordSweepBatchesPerSubmit
// finalized batches, starting at cursor.RecordsSweptThrough. It stops at
// the first batch that is not finalized at height. It advances the sweep
// fields of cursor and does not save it.
func sweepFinalizedBatchRecords(storage db.DB, height uint64, cursor *definition.InputSequenceCursor) error {
	for i := 0; i < constants.MaxRecordSweepBatchesPerSubmit && cursor.RecordsSweptThrough < cursor.NextBatchId; i++ {
		batch, err := definition.GetBatchEntry(storage, cursor.DomainId, cursor.RecordsSweptThrough)
		if err != nil {
			return err
		}
		if !batch.IsFinalized(height) {
			return nil
		}
		for {
			o, err := definition.GetDomainInputOrdinal(storage, cursor.DomainId, cursor.SweptInputCount)
			if err != nil {
				return err
			}
			record := &definition.AssignedIndexRecord{AssignedIndexRecordKey: definition.AssignedIndexRecordKey{DomainId: cursor.DomainId, GlobalInputIndex: o.GlobalInputIndex}}
			if err := record.Delete(storage); err != nil {
				return err
			}
			if err := o.Delete(storage); err != nil {
				return err
			}
			cursor.SweptInputCount++
			if o.GlobalInputIndex == batch.LastInputSeq {
				break
			}
		}
		cursor.RecordsSweptThrough++
	}
	return nil
}

// acceptBatch validates entry against the domain state and, when every
// check passes, stores it, advances the domain cursor, sweeps the input
// records of finalized batches and accrues the batch fees. The checks
// write nothing, so a rejected batch changes no state.
func acceptBatch(storage db.DB, height uint64, activePillars []*definition.PillarInfo, caller types.Address, entry *definition.BatchEntry) error {
	paused, err := definition.IsSubmitPaused(storage, entry.DomainId)
	if err != nil {
		return err
	}
	if paused {
		return constants.ErrSettlementSubmitPaused
	}

	if _, err := definition.GetDomainRecord(storage, entry.DomainId); err == constants.ErrDataNonExistent {
		return constants.ErrForbiddenParam
	} else if err != nil {
		return err
	}
	config, err := definition.GetDomainConfig(storage, entry.DomainId)
	if err == constants.ErrDataNonExistent {
		return constants.ErrForbiddenParam
	} else if err != nil {
		return err
	}

	pillar, err := activeExecutorPillar(storage, activePillars, caller)
	if err != nil {
		return err
	}

	modes, err := definition.GetApprovedDAModeSet(storage, entry.DomainId)
	if err != nil {
		return err
	}
	if !bytes.Contains(modes.Modes, []byte{entry.DaMode}) {
		return constants.ErrForbiddenParam
	}

	cursor, err := definition.GetInputSequenceCursor(storage, entry.DomainId)
	if err != nil {
		return err
	}
	if entry.BatchId != cursor.NextBatchId {
		return constants.ErrForbiddenParam
	}
	expectedPreState := types.ZeroHash
	if cursor.NextBatchId != 0 {
		previous, err := definition.GetBatchEntry(storage, entry.DomainId, cursor.NextBatchId-1)
		if err != nil {
			return err
		}
		expectedPreState = previous.PostStateRoot
	}
	if entry.PreStateRoot != expectedPreState {
		return constants.ErrForbiddenParam
	}

	inputs, err := collectBatchInputs(storage, entry.DomainId, cursor.ConsumedInputCount, entry.FirstInputSeq, entry.LastInputSeq)
	if err != nil {
		return err
	}
	if err := verifyAssetFlowDeposits(entry.AssetFlowSummary, inputs); err != nil {
		return err
	}

	entry.ExecutorPillar = pillar
	entry.SubmittedAtHeight = height
	entry.FinalizeAtHeight = height + config.WithdrawalDelay
	entry.State = definition.BatchStateSubmitted
	if err := entry.Save(storage); err != nil {
		return err
	}

	cursor.ConsumedInputCount += uint64(len(inputs))
	cursor.NextBatchId++
	if err := sweepFinalizedBatchRecords(storage, height, cursor); err != nil {
		return err
	}
	if err := cursor.Save(storage); err != nil {
		return err
	}
	return accrueBatchFees(storage, entry, inputs)
}

type SettlementSubmitBatchParam struct {
	Commitment []byte
}

// SettlementSubmitBatchMethod accepts a batch commitment from the active
// executor of an entitled pillar.
type SettlementSubmitBatchMethod struct{ MethodName string }

func (p *SettlementSubmitBatchMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementSubmitBatchMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	param := new(SettlementSubmitBatchParam)

	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}
	if len(param.Commitment) > constants.MaxOnChainCommitmentSize {
		return constants.ErrForbiddenParam
	}
	_, executor, submittedAtHeight, err := decodeBatchCommitment(param.Commitment)
	if err != nil {
		return err
	}
	if executor != block.Address {
		return constants.ErrPermissionDenied
	}
	if submittedAtHeight != 0 {
		return constants.ErrForbiddenParam
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName, param.Commitment)
	return err
}
func (p *SettlementSubmitBatchMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	param := new(SettlementSubmitBatchParam)
	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, sendBlock.Data); err != nil {
		return nil, err
	}
	entry, _, _, err := decodeBatchCommitment(param.Commitment)
	if err != nil {
		return nil, err
	}

	frontier, err := context.GetFrontierMomentum()
	if err != nil {
		return nil, err
	}
	activePillars, err := context.MomentumStore().GetActivePillars()
	if err != nil {
		return nil, err
	}
	return nil, acceptBatch(context.Storage(), frontier.Height, activePillars, sendBlock.Address, entry)
}

// ============================================================================
// Deploy
// ============================================================================

// getLiveDeployRecord returns the pending deploy record of
// (domainId, deployer, codeHash). A record is live for exactly
// constants.DeploymentExpiryWindow heights from its start height. An
// absent or expired record returns ErrDataNonExistent.
func getLiveDeployRecord(storage db.DB, domainId uint32, deployer types.Address, codeHash types.Hash, height uint64) (*definition.DeployRecord, error) {
	record, err := definition.GetDeployRecord(storage, domainId, deployer, codeHash)
	if err != nil {
		return nil, err
	}
	if deployExpired(record.StartHeight, height) {
		return nil, constants.ErrDataNonExistent
	}
	return record, nil
}

// deployExpired reports whether an attempt started at startHeight is
// expired at height.
func deployExpired(startHeight, height uint64) bool {
	return height >= startHeight+constants.DeploymentExpiryWindow
}

// consumeDeployRecord returns and deletes the live deploy record of
// (domainId, caller, codeHash). It deletes the header and every chunk
// entry of the attempt. It returns ErrForbiddenParam, and keeps both,
// while a chunk is missing.
func consumeDeployRecord(storage db.DB, domainId uint32, caller types.Address, codeHash types.Hash, height uint64) (*definition.DeployRecord, error) {
	record, err := getLiveDeployRecord(storage, domainId, caller, codeHash, height)
	if err != nil {
		return nil, err
	}
	if record.ReceivedCount != record.ChunkCount {
		return nil, constants.ErrForbiddenParam
	}
	if err := record.Delete(storage); err != nil {
		return nil, err
	}
	if err := definition.DeleteDeployChunks(storage, record.DeployRecordKey, record.StartHeight); err != nil {
		return nil, err
	}
	return record, nil
}

// sweepExpiredDeployStarts pops at most constants.MaxDeploySweepPerStart
// expired entries from the head of the start queue. For each it deletes
// the attempt's chunk entries and, when the stored header belongs to the
// same attempt, the header. It stops at the first unexpired entry.
func sweepExpiredDeployStarts(storage db.DB, height uint64) error {
	for i := 0; i < constants.MaxDeploySweepPerStart; i++ {
		entry, err := definition.PeekDeployStart(storage)
		if err == constants.ErrDataNonExistent {
			return nil
		} else if err != nil {
			return err
		}
		if !deployExpired(entry.StartHeight, height) {
			return nil
		}
		key := definition.DeployRecordKey{DomainId: entry.DomainId, Deployer: entry.Deployer, CodeHash: entry.CodeHash}
		if err := definition.DeleteDeployChunks(storage, key, entry.StartHeight); err != nil {
			return err
		}
		record, err := definition.GetDeployRecord(storage, entry.DomainId, entry.Deployer, entry.CodeHash)
		if err == nil {
			if record.StartHeight == entry.StartHeight {
				if err := record.Delete(storage); err != nil {
					return err
				}
			}
		} else if err != constants.ErrDataNonExistent {
			return err
		}
		if _, err := definition.DequeueDeployStart(storage); err != nil {
			return err
		}
	}
	return nil
}

// chargeBytecodeFee checks that sendBlock pays exactly
// totalSize * ZnnPerByteOfBytecode in ZNN, adds the fee to
// TotalFeesCollected and returns it. It never touches the conservation
// counters. It returns ErrDataNonExistent when no Periphery config is
// stored.
func chargeBytecodeFee(storage db.DB, totalSize uint64, sendBlock *nom.AccountBlock) (*big.Int, error) {
	config, err := definition.GetPeripheryConfig(storage)
	if err != nil {
		return nil, err
	}
	fee := new(big.Int).Mul(new(big.Int).SetUint64(totalSize), config.ZnnPerByteOfBytecode)
	if sendBlock.TokenStandard != types.ZnnTokenStandard || sendBlock.Amount.Cmp(fee) != 0 {
		return nil, constants.ErrInvalidTokenOrAmount
	}
	counters, err := definition.GetFeeCounters(storage)
	if err != nil {
		return nil, err
	}
	counters.TotalFeesCollected = new(big.Int).Add(counters.TotalFeesCollected, fee)
	if err := counters.Save(storage); err != nil {
		return nil, err
	}
	return fee, nil
}

type SettlementDeployStartParam struct {
	DomainId     uint32
	CodeHash     types.Hash
	MetadataHash types.Hash
	TotalSize    uint64
	ChunkCount   uint32
	Metadata     []byte
}

type SettlementDeployChunkParam struct {
	DomainId   uint32
	CodeHash   types.Hash
	ChunkIndex uint32
	ChunkHash  types.Hash
	ChunkData  []byte
}

type SettlementDeployFinalizeParam struct {
	DomainId uint32
	CodeHash types.Hash
}

// SettlementDeployContractStartMethod first sweeps expired starts, then
// opens a pending deploy record for the sender.
type SettlementDeployContractStartMethod struct{ MethodName string }

func (p *SettlementDeployContractStartMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementDeployContractStartMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	param := new(SettlementDeployStartParam)

	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}
	if len(param.Metadata) > constants.MaxContractMetadataSize || types.NewHash(param.Metadata) != param.MetadataHash {
		return constants.ErrForbiddenParam
	}
	if param.TotalSize == 0 || param.TotalSize > constants.MaxCodeSize {
		return constants.ErrForbiddenParam
	}
	if uint64(param.ChunkCount) != (param.TotalSize+constants.MaxDeployChunkData-1)/constants.MaxDeployChunkData ||
		param.ChunkCount > constants.MaxChunkCount {
		return constants.ErrForbiddenParam
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName, param.DomainId, param.CodeHash, param.MetadataHash, param.TotalSize, param.ChunkCount, param.Metadata)
	return err
}
func (p *SettlementDeployContractStartMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	param := new(SettlementDeployStartParam)
	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, sendBlock.Data); err != nil {
		return nil, err
	}

	if _, err := getInputDomain(context, param.DomainId); err != nil {
		return nil, err
	}

	frontier, err := context.GetFrontierMomentum()
	if err != nil {
		return nil, err
	}
	storage := context.Storage()
	if _, err := getLiveDeployRecord(storage, param.DomainId, sendBlock.Address, param.CodeHash, frontier.Height); err == nil {
		return nil, constants.ErrForbiddenParam
	} else if err != constants.ErrDataNonExistent {
		return nil, err
	}

	if err := sweepExpiredDeployStarts(storage, frontier.Height); err != nil {
		return nil, err
	}

	record := &definition.DeployRecord{
		DeployRecordKey: definition.DeployRecordKey{DomainId: param.DomainId, Deployer: sendBlock.Address, CodeHash: param.CodeHash},
		MetadataHash:    param.MetadataHash,
		TotalSize:       param.TotalSize,
		ChunkCount:      param.ChunkCount,
		StartHeight:     frontier.Height,
		ReceivedCount:   0,
	}
	if err := record.Save(storage); err != nil {
		return nil, err
	}
	if err := definition.EnqueueDeployStart(storage, &definition.DeployStartQueueEntry{
		DomainId:    param.DomainId,
		Deployer:    sendBlock.Address,
		CodeHash:    param.CodeHash,
		StartHeight: frontier.Height,
	}); err != nil {
		return nil, err
	}
	if err := admitInput(storage, param.DomainId, types.ZeroHash, nil, nil); err != nil {
		return nil, err
	}
	return nil, nil
}

// SettlementDeployContractChunkMethod stores one chunk-hash entry for the
// sender's pending deploy record.
type SettlementDeployContractChunkMethod struct{ MethodName string }

func (p *SettlementDeployContractChunkMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementDeployContractChunkMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	param := new(SettlementDeployChunkParam)

	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.Amount.Sign() != 0 {
		return constants.ErrInvalidTokenOrAmount
	}
	if len(param.ChunkData) == 0 || len(param.ChunkData) > constants.MaxDeployChunkData || types.NewHash(param.ChunkData) != param.ChunkHash {
		return constants.ErrForbiddenParam
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName, param.DomainId, param.CodeHash, param.ChunkIndex, param.ChunkHash, param.ChunkData)
	return err
}
func (p *SettlementDeployContractChunkMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	param := new(SettlementDeployChunkParam)
	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, sendBlock.Data); err != nil {
		return nil, err
	}

	if _, err := getInputDomain(context, param.DomainId); err != nil {
		return nil, err
	}

	frontier, err := context.GetFrontierMomentum()
	if err != nil {
		return nil, err
	}
	storage := context.Storage()
	record, err := getLiveDeployRecord(storage, param.DomainId, sendBlock.Address, param.CodeHash, frontier.Height)
	if err != nil {
		return nil, err
	}

	if param.ChunkIndex >= record.ChunkCount {
		return nil, constants.ErrForbiddenParam
	}
	expected := uint64(constants.MaxDeployChunkData)
	if param.ChunkIndex == record.ChunkCount-1 {
		expected = record.TotalSize - uint64(record.ChunkCount-1)*constants.MaxDeployChunkData
	}
	if uint64(len(param.ChunkData)) != expected {
		return nil, constants.ErrForbiddenParam
	}

	chunkKey := definition.DeployChunkKey{DeployRecordKey: record.DeployRecordKey, StartHeight: record.StartHeight, ChunkIndex: param.ChunkIndex}
	if _, err := definition.GetDeployChunk(storage, chunkKey); err == nil {
		return nil, constants.ErrForbiddenParam
	} else if err != constants.ErrDataNonExistent {
		return nil, err
	}
	if err := (&definition.DeployChunk{DeployChunkKey: chunkKey, ChunkHash: param.ChunkHash}).Save(storage); err != nil {
		return nil, err
	}
	record.ReceivedCount++
	if err := record.Save(storage); err != nil {
		return nil, err
	}
	if err := admitInput(storage, param.DomainId, types.ZeroHash, nil, nil); err != nil {
		return nil, err
	}
	return nil, nil
}

// SettlementDeployContractFinalizeMethod consumes the sender's complete
// deploy record and charges the bytecode fee.
type SettlementDeployContractFinalizeMethod struct{ MethodName string }

func (p *SettlementDeployContractFinalizeMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementDeployContractFinalizeMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	var err error
	param := new(SettlementDeployFinalizeParam)

	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, block.Data); err != nil {
		return constants.ErrUnpackError
	}

	if block.TokenStandard != types.ZnnTokenStandard || block.Amount.Sign() <= 0 {
		return constants.ErrInvalidTokenOrAmount
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName, param.DomainId, param.CodeHash)
	return err
}

// ReceiveBlock deletes the deploy record before it charges the fee. When
// the fee check or admission fails, the VM rollback restores the record
// and discards every write.
func (p *SettlementDeployContractFinalizeMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	param := new(SettlementDeployFinalizeParam)
	if err := definition.ABISettlement.UnpackMethod(param, p.MethodName, sendBlock.Data); err != nil {
		return nil, err
	}

	if _, err := getInputDomain(context, param.DomainId); err != nil {
		return nil, err
	}

	frontier, err := context.GetFrontierMomentum()
	if err != nil {
		return nil, err
	}
	storage := context.Storage()
	record, err := consumeDeployRecord(storage, param.DomainId, sendBlock.Address, param.CodeHash, frontier.Height)
	if err != nil {
		return nil, err
	}
	fee, err := chargeBytecodeFee(storage, record.TotalSize, sendBlock)
	if err != nil {
		return nil, err
	}
	if err := admitInput(storage, param.DomainId, types.ZeroHash, nil, fee); err != nil {
		return nil, err
	}
	return nil, nil
}
