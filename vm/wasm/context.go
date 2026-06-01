package wasm

import (
	"context"
	"errors"
	"math/big"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/vm/vm_context"
)

var (
	ErrInsufficientDeposit = errors.New("insufficient QSR for state write burn")
	ErrMaxDescendants      = errors.New("max descendant blocks exceeded")
	ErrInsufficientBalance = errors.New("insufficient spendable balance")
	ErrReadOnly            = errors.New("cannot mutate in read-only context")
)

// stateKeyPrefix is prepended to all user-facing state keys.
var stateKeyPrefix = []byte{0xFE}

type wasmContextKey struct{}

func contextWithWasmCtx(ctx context.Context, wc *WasmContext) context.Context {
	return context.WithValue(ctx, wasmContextKey{}, wc)
}

func wasmCtxFromContext(ctx context.Context) *WasmContext {
	wc, _ := ctx.Value(wasmContextKey{}).(*WasmContext)
	return wc
}

type WasmContext struct {
	vmCtx        vm_context.AccountVmContext
	sendBlock    *nom.AccountBlock
	storageDB    db.DB
	events       *EventCollector
	transfers    []*nom.AccountBlock
	gasRemaining uint64
	selfAddr     types.Address // the contract's own 0x02 address
	callerAddr   types.Address // the immediate sender (sendBlock.Address)
	burnAmount   *big.Int      // total QSR to burn this execution
	readOnly     bool
	vars         *definition.WasmVariables
}

func NewWasmContext(
	vmCtx vm_context.AccountVmContext,
	sendBlock *nom.AccountBlock,
	contractAddr types.Address,
	vars *definition.WasmVariables,
) *WasmContext {
	origin := types.Address{}
	if sendBlock != nil {
		origin = sendBlock.Address
	}
	return &WasmContext{
		vmCtx:      vmCtx,
		sendBlock:  sendBlock,
		storageDB:  vmCtx.Storage(),
		events:     NewEventCollector(contractAddr, vars),
		transfers:  make([]*nom.AccountBlock, 0),
		selfAddr:   contractAddr,
		callerAddr: origin,
		burnAmount: big.NewInt(0),
		vars:       vars,
	}
}

// NewViewContext creates a read-only context for view calls. Mutating host
// functions trap; read accessors work normally.
func NewViewContext(
	vmCtx vm_context.AccountVmContext,
	contractAddr types.Address,
	vars *definition.WasmVariables,
) *WasmContext {
	return &WasmContext{
		vmCtx:      vmCtx,
		storageDB:  vmCtx.Storage(),
		events:     NewEventCollector(contractAddr, vars),
		transfers:  make([]*nom.AccountBlock, 0),
		selfAddr:   contractAddr,
		burnAmount: big.NewInt(0),
		readOnly:   true,
		vars:       vars,
	}
}

// effectiveSpendable returns how much of a token the contract may commit right
// now. For QSR, it subtracts the pending burn and all in-flight transfers that
// have not yet been settled by applySend. For other tokens, it subtracts only
// in-flight transfers.
func (wc *WasmContext) effectiveSpendable(zts types.ZenonTokenStandard) *big.Int {
	spendable, err := wc.vmCtx.GetBalance(zts)
	if err != nil || spendable == nil {
		return big.NewInt(0)
	}
	s := new(big.Int).Set(spendable)

	// Reserve pending transfers (not yet debited by applySend)
	for _, tx := range wc.transfers {
		if tx.TokenStandard == zts {
			s.Sub(s, tx.Amount)
		}
	}

	// Reserve pending burn (QSR only)
	if zts == types.QsrTokenStandard && wc.burnAmount.Sign() > 0 {
		s.Sub(s, wc.burnAmount)
	}

	if s.Sign() < 0 {
		return big.NewInt(0)
	}
	return s
}

// StateRead reads a key from the 0xFE user state namespace.
// Returns (value, true) if found, (nil, false) if absent.
//
// The account storage wraps Get with DisableNotFound, so an absent key reads
// back as an empty slice — indistinguishable from a key written with an empty
// value. Existence is therefore probed with Has (which is NOT wrapped) so that
// (a) state_read can report a genuine miss as 0xFFFFFFFF (§7.1), and (b)
// state_write charges a fresh key's key bytes (oldCost = 0) rather than treating
// the absent key as an empty-but-existing value (spec §4.2).
func (wc *WasmContext) StateRead(key []byte) ([]byte, bool) {
	fullKey := append(stateKeyPrefix, key...)
	has, err := wc.storageDB.Has(fullKey)
	if err != nil || !has {
		return nil, false
	}
	value, err := wc.storageDB.Get(fullKey)
	if err != nil {
		return nil, false
	}
	return value, true
}

// StateHas checks if a key exists in the 0xFE user state namespace.
func (wc *WasmContext) StateHas(key []byte) bool {
	fullKey := append(stateKeyPrefix, key...)
	has, err := wc.storageDB.Has(fullKey)
	if err != nil {
		return false
	}
	return has
}

// StateWrite writes a key into the 0xFE user state namespace and accumulates
// the QSR growth delta into burnAmount. The actual burn is emitted as a single
// aggregated descendant Burn block at the end of Execute.
func (wc *WasmContext) StateWrite(key []byte, value []byte) error {
	if wc.readOnly {
		return ErrReadOnly
	}

	// Check if value already exists to compute growth delta
	fullKey := append(stateKeyPrefix, key...)
	oldValue, exists := wc.StateRead(key)

	newCost := int64(len(key)+len(value)) * int64(wc.vars.QSRPerByteOfState)
	oldCost := int64(0)
	if exists {
		oldCost = int64(len(key)+len(oldValue)) * int64(wc.vars.QSRPerByteOfState)
	}
	delta := newCost - oldCost

	if delta > 0 {
		deltaBig := big.NewInt(delta)
		spendable := wc.effectiveSpendable(types.QsrTokenStandard)
		if spendable.Cmp(deltaBig) < 0 {
			return ErrInsufficientDeposit
		}
		wc.burnAmount.Add(wc.burnAmount, deltaBig)
	}

	return wc.storageDB.Put(fullKey, value)
}

// StateDelete deletes a key from the 0xFE user state namespace. Under the
// burn model the QSR was already burned at write time, so no balance changes.
func (wc *WasmContext) StateDelete(key []byte) (bool, error) {
	if wc.readOnly {
		return false, ErrReadOnly
	}

	fullKey := append(stateKeyPrefix, key...)
	has, err := wc.storageDB.Has(fullKey)
	if err != nil {
		return false, err
	}
	if !has {
		return false, nil
	}

	return true, wc.storageDB.Delete(fullKey)
}

// BalanceGet reads the spendable balance for a token standard.
func (wc *WasmContext) BalanceGet(zts types.ZenonTokenStandard) (*big.Int, error) {
	balance, err := wc.vmCtx.GetBalance(zts)
	if err != nil {
		return nil, err
	}
	return balance, nil
}

// Transfer queues a token transfer as a descendant block. The actual debit
// happens when applySend processes the descendant — no in-execution SubBalance.
func (wc *WasmContext) Transfer(zts types.ZenonTokenStandard, to types.Address, amount *big.Int) (uint32, error) {
	if wc.readOnly {
		return 1, ErrReadOnly
	}
	// Reserve one descendant slot for the aggregated burn block that Execute
	// emits after the transfers, so the total descendant count can never exceed
	// MaxDescendantBlocksPerExecute.
	if len(wc.transfers) >= int(wc.vars.MaxDescendantBlocks)-1 {
		return 1, ErrMaxDescendants
	}

	spendable := wc.effectiveSpendable(zts)
	if spendable.Cmp(amount) < 0 {
		return 1, nil
	}

	wc.transfers = append(wc.transfers, &nom.AccountBlock{
		ToAddress:     to,
		TokenStandard: zts,
		Amount:        new(big.Int).Set(amount),
	})
	return 0, nil
}

func (wc *WasmContext) GetHeight() uint64 {
	m, err := wc.vmCtx.GetFrontierMomentum()
	if err != nil {
		return 0
	}
	return m.Height
}

func (wc *WasmContext) GetTimestamp() uint64 {
	m, err := wc.vmCtx.GetFrontierMomentum()
	if err != nil {
		return 0
	}
	return m.TimestampUnix
}

func (wc *WasmContext) GetPrevHash() types.Hash {
	m, err := wc.vmCtx.GetFrontierMomentum()
	if err != nil {
		return types.ZeroHash
	}
	return m.PreviousHash
}

// GetCaller returns the immediate sender of the triggering block.
func (wc *WasmContext) GetCaller() types.Address {
	return wc.callerAddr
}

// GetAddress returns the executing contract's own address.
func (wc *WasmContext) GetAddress() types.Address {
	return wc.selfAddr
}

// GetCallToken returns the token standard attached to the triggering send.
func (wc *WasmContext) GetCallToken() types.ZenonTokenStandard {
	if wc.sendBlock == nil {
		return types.ZenonTokenStandard{}
	}
	return wc.sendBlock.TokenStandard
}

// GetCallAmount returns the amount attached to the triggering send.
func (wc *WasmContext) GetCallAmount() *big.Int {
	if wc.sendBlock == nil || wc.sendBlock.Amount == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(wc.sendBlock.Amount)
}

func (wc *WasmContext) GetBlockHash() types.Hash {
	if wc.sendBlock != nil {
		return wc.sendBlock.Hash
	}
	return types.ZeroHash
}

func (wc *WasmContext) GetRemainingGas() uint64 {
	return wc.gasRemaining
}

func (wc *WasmContext) SetRemainingGas(gas uint64) {
	wc.gasRemaining = gas
}

func (wc *WasmContext) Events() []WasmEvent {
	return wc.events.Events()
}

func (wc *WasmContext) Transfers() []*nom.AccountBlock {
	return wc.transfers
}

// BurnAmount returns the total QSR to burn from this execution.
func (wc *WasmContext) BurnAmount() *big.Int {
	return wc.burnAmount
}
