package implementation

import (
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/vm_context"
)

var (
	settlementLog = common.EmbeddedLogger.New("contract", "settlement")
)

type SettlementUpdateMethod struct{ MethodName string }

func (p *SettlementUpdateMethod) GetPlasma(plasmaTable *constants.PlasmaTable) (uint64, error) {
	return plasmaTable.EmbeddedSimple, nil
}
func (p *SettlementUpdateMethod) ValidateSendBlock(block *nom.AccountBlock) error {
	// scaffold: admit the send so the receive can dispatch; no param validation yet
	return nil
}
func (p *SettlementUpdateMethod) ReceiveBlock(context vm_context.AccountVmContext, sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	settlementLog.Debug("settlement method dispatched (not implemented)", "method", p.MethodName, "address", sendBlock.Address)
	return nil, constants.ErrNotImplemented
}
