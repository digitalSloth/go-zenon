package implementation

import (
	"math/big"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/crypto"
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

// SettlementUpdateMethod is the permissionless, rate-limited release crank. It
// carries the domainId whose release queue it drains, so its rate limit applies
// per domain: one domain's traffic cannot starve another domain's exits. The
// method validates and dispatches; releasing queued withdrawals is not
// implemented.
type SettlementUpdateMethod struct{ MethodName string }

func (p *SettlementUpdateMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementUpdateMethod) ValidateSendBlock(block *nom.AccountBlock) error {
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
func (p *SettlementUpdateMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	if err := p.ValidateSendBlock(sendBlock); err != nil {
		return nil, err
	}

	settlementLog.Debug("settlement method dispatched (not implemented)", "method", p.MethodName, "address", sendBlock.Address)
	return nil, constants.ErrNotImplemented
}

// ============================================================================
// SetPeripheryConfig
// ============================================================================

type SettlementPeripheryConfigParam struct {
	WithdrawalDelay     uint64
	MaxBatchWithdrawal  *big.Int
	RuntimeUpgradeDelay uint64
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

	if !constants.IsWithdrawalDelayInBounds(param.WithdrawalDelay) {
		return constants.ErrForbiddenParam
	}
	if !constants.IsMaxBatchWithdrawalInBounds(param.MaxBatchWithdrawal) {
		return constants.ErrForbiddenParam
	}
	if !constants.IsRuntimeUpgradeDelayValid(param.RuntimeUpgradeDelay, param.WithdrawalDelay) {
		return constants.ErrForbiddenParam
	}

	block.Data, err = definition.ABISettlement.PackMethod(p.MethodName, param.WithdrawalDelay, param.MaxBatchWithdrawal, param.RuntimeUpgradeDelay)
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
		common.Uint64ToBytes(param.WithdrawalDelay),
		common.BigIntToBytes(param.MaxBatchWithdrawal),
		common.Uint64ToBytes(param.RuntimeUpgradeDelay),
	)
	timeChallengeInfo, err := TimeChallenge(context, p.MethodName, paramsHash, constants.MinSoftDelay)
	if err != nil {
		return nil, err
	}
	// if paramsHash is not zero it means we had a new challenge and we can't go further to save the change into local db
	if !timeChallengeInfo.ParamsHash.IsZero() {
		return nil, nil
	}

	config, err := definition.GetPeripheryConfig(context.Storage())
	if err != nil {
		if err == constants.ErrDataNonExistent {
			config = &definition.PeripheryConfig{}
		} else {
			return nil, err
		}
	}
	config.WithdrawalDelay = param.WithdrawalDelay
	config.MaxBatchWithdrawal = param.MaxBatchWithdrawal
	config.RuntimeUpgradeDelay = param.RuntimeUpgradeDelay
	common.DealWithErr(config.Save(context.Storage()))
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
