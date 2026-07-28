package tests

import (
	"math/big"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"

	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
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
		Data:          definition.ABISettlement.PackMethodPanic(definition.SettlementUpdateMethodName),
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
	}, constants.ErrContractDoesntExist, mock.SkipVmChanges)
	z.InsertNewMomentum()
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
		Data:          definition.ABISettlement.PackMethodPanic(definition.SettlementUpdateMethodName),
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
	}).Error(t, constants.ErrNotImplemented)
	z.InsertNewMomentum()
	z.InsertNewMomentum()
}
