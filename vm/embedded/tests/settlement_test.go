package tests

import (
	"math/big"
	"testing"

	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"

	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/rpc/api"
	"github.com/zenon-network/go-zenon/rpc/api/embedded"
	"github.com/zenon-network/go-zenon/zenon/mock"
)

func activateOffchainExecution(t *testing.T, z mock.MockZenon) {
	saveSporkState(t)
	sporkAPI := embedded.NewSporkApi(z)
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.Spork.Address,
		ToAddress: types.SporkContract,
		Data: definition.ABISpork.PackMethodPanic(definition.SporkCreateMethodName,
			"spork-offchain-execution",              // name
			"activate spork for offchain execution", // description
		),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()

	sporkList, _ := sporkAPI.GetAll(0, 10)
	id := sporkList.List[0].Id

	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.Spork.Address,
		ToAddress: types.SporkContract,
		Data: definition.ABISpork.PackMethodPanic(definition.SporkActivateMethodName,
			id, // id
		),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()
	types.OffchainExecutionSpork.SporkId = id
	types.ImplementedSporksMap[id] = true
	z.InsertMomentumsTo(20)
}

func TestSettlement_RejectedBeforeSpork(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)

	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.SettlementContract,
		Data:          definition.ABISettlement.PackMethodPanic(definition.SettlementUpdateMethodName, uint32(0)),
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
	}, constants.ErrContractDoesntExist, mock.SkipVmChanges)
	z.InsertNewMomentum()
}

func setPeripheryConfigStep(admin types.Address, withdrawalDelay uint64, maxBatchWithdrawal *big.Int, runtimeUpgradeDelay uint64) *nom.AccountBlock {
	return &nom.AccountBlock{
		Address:       admin,
		ToAddress:     types.SettlementContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
		Data: definition.ABISettlement.PackMethodPanic(definition.SettlementSetPeripheryConfigMethodName,
			withdrawalDelay,
			maxBatchWithdrawal,
			runtimeUpgradeDelay,
		),
	}
}

func pauseStep(admin types.Address, domainId uint32, isGlobal bool, scope uint8, active bool) *nom.AccountBlock {
	return &nom.AccountBlock{
		Address:       admin,
		ToAddress:     types.SettlementContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
		Data: definition.ABISettlement.PackMethodPanic(definition.SettlementPauseMethodName,
			domainId,
			isGlobal,
			scope,
			active,
		),
	}
}

func initiateEscapeHatchStep(admin types.Address, domainId uint32) *nom.AccountBlock {
	return &nom.AccountBlock{
		Address:       admin,
		ToAddress:     types.SettlementContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
		Data: definition.ABISettlement.PackMethodPanic(definition.SettlementInitiateEscapeHatchMethodName,
			domainId,
		),
	}
}

func TestSettlement_AdminGate_RejectsNonNetworkAdmin(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateOffchainExecution(t, z)
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)

	call := z.CallContract(setPeripheryConfigStep(g.User1.Address, constants.WithdrawalDelayMin, big.NewInt(1000), constants.WithdrawalDelayMin))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, constants.ErrPermissionDenied)

	call = z.CallContract(pauseStep(g.User1.Address, 0, true, definition.PauseScopeSubmit, true))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, constants.ErrPermissionDenied)

	call = z.CallContract(initiateEscapeHatchStep(g.User1.Address, 1))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, constants.ErrPermissionDenied)
}

func TestSettlement_SetPeripheryConfig_MaxBatchWithdrawalCeiling(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateOffchainExecution(t, z)
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)
	saveBridgeConstants(t)
	constants.MinSoftDelay = 10

	savedAdmin := types.NetworkAdmin
	types.NetworkAdmin = g.User5.Address
	defer func() { types.NetworkAdmin = savedAdmin }()

	overCeiling := new(big.Int).Add(constants.MaxBatchWithdrawalCeiling, big.NewInt(1))
	z.InsertSendBlock(setPeripheryConfigStep(g.User5.Address, constants.WithdrawalDelayMin, overCeiling, constants.WithdrawalDelayMin),
		constants.ErrForbiddenParam, mock.SkipVmChanges)
	z.InsertNewMomentum()

	withinCeiling := big.NewInt(12345)
	call := z.CallContract(setPeripheryConfigStep(g.User5.Address, constants.WithdrawalDelayMin, withinCeiling, constants.WithdrawalDelayMin))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	// config unchanged until the challenge elapses
	_, context, err := api.GetFrontierContext(z.Chain(), types.SettlementContract)
	common.FailIfErr(t, err)
	if _, err := definition.GetPeripheryConfig(context.Storage()); err != constants.ErrDataNonExistent {
		t.Fatalf("expected periphery config unset before challenge completes, got err %v", err)
	}

	frMom, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	z.InsertMomentumsTo(frMom.Height + constants.MinSoftDelay)

	call = z.CallContract(setPeripheryConfigStep(g.User5.Address, constants.WithdrawalDelayMin, withinCeiling, constants.WithdrawalDelayMin))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	_, context, err = api.GetFrontierContext(z.Chain(), types.SettlementContract)
	common.FailIfErr(t, err)
	config, err := definition.GetPeripheryConfig(context.Storage())
	common.FailIfErr(t, err)
	if config.MaxBatchWithdrawal.Cmp(withinCeiling) != 0 {
		t.Fatalf("expected maxBatchWithdrawal %v, got %v", withinCeiling, config.MaxBatchWithdrawal)
	}
}

func TestSettlement_SetPeripheryConfig_RuntimeUpgradeDelayFloor(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateOffchainExecution(t, z)
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)

	savedAdmin := types.NetworkAdmin
	types.NetworkAdmin = g.User5.Address
	defer func() { types.NetworkAdmin = savedAdmin }()

	withdrawalDelay := constants.WithdrawalDelayMin + 100
	z.InsertSendBlock(setPeripheryConfigStep(g.User5.Address, withdrawalDelay, big.NewInt(1000), withdrawalDelay-1),
		constants.ErrForbiddenParam, mock.SkipVmChanges)
	z.InsertNewMomentum()
}

func TestSettlement_SetPeripheryConfig_WithdrawalDelayBounds(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateOffchainExecution(t, z)
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)

	savedAdmin := types.NetworkAdmin
	types.NetworkAdmin = g.User5.Address
	defer func() { types.NetworkAdmin = savedAdmin }()

	belowMin := constants.WithdrawalDelayMin - 1
	z.InsertSendBlock(setPeripheryConfigStep(g.User5.Address, belowMin, big.NewInt(1000), constants.WithdrawalDelayMax),
		constants.ErrForbiddenParam, mock.SkipVmChanges)
	z.InsertNewMomentum()

	aboveMax := constants.WithdrawalDelayMax + 1
	z.InsertSendBlock(setPeripheryConfigStep(g.User5.Address, aboveMax, big.NewInt(1000), aboveMax),
		constants.ErrForbiddenParam, mock.SkipVmChanges)
	z.InsertNewMomentum()
}

func TestSettlement_SetPeripheryConfig_TimeChallengeNotDue(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateOffchainExecution(t, z)
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)
	saveBridgeConstants(t)
	constants.MinSoftDelay = 10

	savedAdmin := types.NetworkAdmin
	types.NetworkAdmin = g.User5.Address
	defer func() { types.NetworkAdmin = savedAdmin }()

	newValue := big.NewInt(54321)
	call := z.CallContract(setPeripheryConfigStep(g.User5.Address, constants.WithdrawalDelayMin, newValue, constants.WithdrawalDelayMin))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	// repeating the same call before the delay elapses must not reset or
	// prematurely apply the pending change
	call = z.CallContract(setPeripheryConfigStep(g.User5.Address, constants.WithdrawalDelayMin, newValue, constants.WithdrawalDelayMin))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, constants.ErrTimeChallengeNotDue)

	_, context, err := api.GetFrontierContext(z.Chain(), types.SettlementContract)
	common.FailIfErr(t, err)
	if _, err := definition.GetPeripheryConfig(context.Storage()); err != constants.ErrDataNonExistent {
		t.Fatalf("expected periphery config still unset after premature retry, got err %v", err)
	}

	frMom, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	z.InsertMomentumsTo(frMom.Height + constants.MinSoftDelay)

	call = z.CallContract(setPeripheryConfigStep(g.User5.Address, constants.WithdrawalDelayMin, newValue, constants.WithdrawalDelayMin))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	_, context, err = api.GetFrontierContext(z.Chain(), types.SettlementContract)
	common.FailIfErr(t, err)
	config, err := definition.GetPeripheryConfig(context.Storage())
	common.FailIfErr(t, err)
	if config.MaxBatchWithdrawal.Cmp(newValue) != 0 {
		t.Fatalf("expected maxBatchWithdrawal %v, got %v", newValue, config.MaxBatchWithdrawal)
	}
}

func TestSettlement_Pause_StateMachine(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateOffchainExecution(t, z)
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)

	savedAdmin := types.NetworkAdmin
	types.NetworkAdmin = g.User5.Address
	defer func() { types.NetworkAdmin = savedAdmin }()

	getStorage := func() db.DB {
		_, context, err := api.GetFrontierContext(z.Chain(), types.SettlementContract)
		common.FailIfErr(t, err)
		return context.Storage()
	}

	// global SUBMIT pause
	call := z.CallContract(pauseStep(g.User5.Address, 0, true, definition.PauseScopeSubmit, true))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	storage := getStorage()
	if paused, err := definition.IsSubmitPaused(storage, 0); err != nil || !paused {
		t.Fatalf("expected SUBMIT paused globally, got %v err %v", paused, err)
	}
	if paused, err := definition.IsRelayPaused(storage, 0); err != nil || paused {
		t.Fatalf("expected RELAY unaffected, got %v err %v", paused, err)
	}

	// global RELAY pause too (independence)
	call = z.CallContract(pauseStep(g.User5.Address, 0, true, definition.PauseScopeRelay, true))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	storage = getStorage()
	if paused, err := definition.IsSubmitPaused(storage, 0); err != nil || !paused {
		t.Fatalf("expected SUBMIT still paused, got %v err %v", paused, err)
	}
	if paused, err := definition.IsRelayPaused(storage, 0); err != nil || !paused {
		t.Fatalf("expected RELAY paused, got %v err %v", paused, err)
	}

	// deactivate SUBMIT, RELAY unaffected
	call = z.CallContract(pauseStep(g.User5.Address, 0, true, definition.PauseScopeSubmit, false))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	storage = getStorage()
	if paused, err := definition.IsSubmitPaused(storage, 0); err != nil || paused {
		t.Fatalf("expected SUBMIT deactivated, got %v err %v", paused, err)
	}
	if paused, err := definition.IsRelayPaused(storage, 0); err != nil || !paused {
		t.Fatalf("expected RELAY still paused, got %v err %v", paused, err)
	}

	// deactivate RELAY, SUBMIT unaffected (already off)
	call = z.CallContract(pauseStep(g.User5.Address, 0, true, definition.PauseScopeRelay, false))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	storage = getStorage()
	if paused, err := definition.IsSubmitPaused(storage, 0); err != nil || paused {
		t.Fatalf("expected SUBMIT still deactivated, got %v err %v", paused, err)
	}
	if paused, err := definition.IsRelayPaused(storage, 0); err != nil || paused {
		t.Fatalf("expected RELAY deactivated, got %v err %v", paused, err)
	}

	// per-domain SUBMIT pause only affects that domain
	call = z.CallContract(pauseStep(g.User5.Address, 7, false, definition.PauseScopeSubmit, true))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	storage = getStorage()
	if paused, err := definition.IsSubmitPaused(storage, 7); err != nil || !paused {
		t.Fatalf("expected SUBMIT paused for domain 7, got %v err %v", paused, err)
	}
	if paused, err := definition.IsSubmitPaused(storage, 8); err != nil || paused {
		t.Fatalf("expected SUBMIT unaffected for domain 8, got %v err %v", paused, err)
	}
}

func TestSettlement_InitiateEscapeHatch_Stub(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateOffchainExecution(t, z)
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)
	saveBridgeConstants(t)
	constants.MinAdministratorDelay = 20

	savedAdmin := types.NetworkAdmin
	types.NetworkAdmin = g.User5.Address
	defer func() { types.NetworkAdmin = savedAdmin }()

	domainId := uint32(3)

	call := z.CallContract(initiateEscapeHatchStep(g.User5.Address, domainId))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	_, context, err := api.GetFrontierContext(z.Chain(), types.SettlementContract)
	common.FailIfErr(t, err)
	if _, err := definition.GetEscapeHatchRecord(context.Storage(), domainId); err != constants.ErrDataNonExistent {
		t.Fatalf("expected no record before challenge completes, got err %v", err)
	}

	frMom, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	z.InsertMomentumsTo(frMom.Height + constants.MinAdministratorDelay)

	call = z.CallContract(initiateEscapeHatchStep(g.User5.Address, domainId))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	_, context, err = api.GetFrontierContext(z.Chain(), types.SettlementContract)
	common.FailIfErr(t, err)
	record, err := definition.GetEscapeHatchRecord(context.Storage(), domainId)
	common.FailIfErr(t, err)
	if record.DomainId != domainId {
		t.Fatalf("expected domainId %d, got %d", domainId, record.DomainId)
	}
	completionMom, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	if record.TriggeredAtHeight > completionMom.Height {
		t.Fatalf("expected triggeredAtHeight <= frontier height, got %d > %d", record.TriggeredAtHeight, completionMom.Height)
	}
}

func TestSettlement_DispatchAfterSpork(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, `
t=2001-09-09T01:46:50+0000 lvl=dbug msg=created module=embedded contract=spork spork="&{Id:bed2a35fd610e53f6a6c0ffaacc6638fef0588003588cff784b226d12aad4027 Name:spork-offchain-execution Description:activate spork for offchain execution Activated:false EnforcementHeight:0}"
t=2001-09-09T01:47:00+0000 lvl=dbug msg=activated module=embedded contract=spork spork="&{Id:bed2a35fd610e53f6a6c0ffaacc6638fef0588003588cff784b226d12aad4027 Name:spork-offchain-execution Description:activate spork for offchain execution Activated:true EnforcementHeight:9}"
t=2001-09-09T01:50:00+0000 lvl=dbug msg="settlement method dispatched (not implemented)" module=embedded contract=settlement method=Update address=z1qzal6c5s9rjnnxd2z7dvdhjxpmmj4fmw56a0mz
`)
	activateOffchainExecution(t, z)

	defer z.CallContract(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.SettlementContract,
		Data:          definition.ABISettlement.PackMethodPanic(definition.SettlementUpdateMethodName, uint32(0)),
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
	}).Error(t, constants.ErrNotImplemented)
	z.InsertNewMomentum()
	z.InsertNewMomentum()
}

// Update moves value out of custody and carries no payment of its own, so an
// attached amount is always a mistake and is rejected at send validation.
func TestSettlement_Update_RejectsAttachedAmount(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateOffchainExecution(t, z)
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)

	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.SettlementContract,
		Data:          definition.ABISettlement.PackMethodPanic(definition.SettlementUpdateMethodName, uint32(0)),
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(1),
	}, constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertNewMomentum()
}
