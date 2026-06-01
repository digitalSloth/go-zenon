package vm_context

import (
	"github.com/zenon-network/go-zenon/chain/account"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/chain/store"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/consensus/api"
	"github.com/zenon-network/go-zenon/vm/constants"
)

type accountVmContext struct {
	accountStoreSnapshot store.Account
	api.PillarReader
	store.Account
	momentumStore store.Momentum
	events        []nom.AccountBlockEvent
}

func (ctx *accountVmContext) MomentumStore() store.Momentum {
	return ctx.momentumStore
}

func NewAccountContext(momentumStore store.Momentum, accountBlock store.Account, pillarReader api.PillarReader) AccountVmContext {
	return &accountVmContext{
		momentumStore: momentumStore,
		Account:       accountBlock,
		PillarReader:  pillarReader,
	}
}

func NewGenesisAccountContext(address types.Address) AccountVmContext {
	return NewAccountContext(nil, account.NewAccountStore(address, db.NewMemDB()), nil)
}

// WasmContractHasBytecode returns whether the given address has deployed WASM bytecode.
// Reads from the WasmContract management contract's storage at the bytecode prefix key.
func (ctx *accountVmContext) WasmContractHasBytecode(addr types.Address) bool {
	wasmContractStore := ctx.momentumStore.GetAccountStore(types.WasmContract)
	has, err := wasmContractStore.Storage().Has(common.JoinBytes(constants.WasmBytecodeKeyPrefix, addr.Bytes()))
	common.DealWithErr(err)
	return has
}

func (ctx *accountVmContext) Events() []nom.AccountBlockEvent {
	return ctx.events
}

func (ctx *accountVmContext) SetEvents(events []nom.AccountBlockEvent) {
	ctx.events = events
}
