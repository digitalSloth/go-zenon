package wasm

import (
	"math/big"
	"testing"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/vm/vm_context"
)

// fakeBalCtx is a minimal AccountVmContext stand-in for white-box testing.
type fakeBalCtx struct {
	vm_context.AccountVmContext
	store    db.DB
	balances map[types.ZenonTokenStandard]*big.Int
}

func newFakeBalCtx() *fakeBalCtx {
	return &fakeBalCtx{
		store:    db.NewMemDB(),
		balances: map[types.ZenonTokenStandard]*big.Int{},
	}
}

func (f *fakeBalCtx) setBalance(zts types.ZenonTokenStandard, v int64) {
	f.balances[zts] = big.NewInt(v)
}

func (f *fakeBalCtx) bal(zts types.ZenonTokenStandard) *big.Int {
	if v, ok := f.balances[zts]; ok {
		return v
	}
	return big.NewInt(0)
}

func (f *fakeBalCtx) Storage() db.DB { return f.store }

func (f *fakeBalCtx) GetBalance(zts types.ZenonTokenStandard) (*big.Int, error) {
	return new(big.Int).Set(f.bal(zts)), nil
}

func (f *fakeBalCtx) SubBalance(zts *types.ZenonTokenStandard, amount *big.Int) {
	f.balances[*zts] = new(big.Int).Sub(f.bal(*zts), amount)
}

func newBurnTestContext(t *testing.T, fake *fakeBalCtx) *WasmContext {
	t.Helper()
	send := &nom.AccountBlock{Address: types.Address{}}
	return NewWasmContext(fake, send, types.Address{}, definition.DefaultWasmVariables())
}

// --- BalanceGet ---

func TestWasmContext_BalanceGetReturnsFullBalance(t *testing.T) {
	fake := newFakeBalCtx()
	fake.setBalance(types.QsrTokenStandard, 200)
	wc := newBurnTestContext(t, fake)

	got, err := wc.BalanceGet(types.QsrTokenStandard)
	if err != nil {
		t.Fatal(err)
	}
	if got.Int64() != 200 {
		t.Errorf("BalanceGet(QSR) = %d, want 200", got.Int64())
	}
}

// --- Transfer ---

func TestWasmContext_TransferSingleDebit(t *testing.T) {
	fake := newFakeBalCtx()
	fake.setBalance(types.QsrTokenStandard, 100)
	wc := newBurnTestContext(t, fake)

	// Transfer 60 — should succeed (no in-execution SubBalance).
	rc, err := wc.Transfer(types.QsrTokenStandard, types.Address{1}, big.NewInt(60))
	if err != nil || rc != 0 {
		t.Fatalf("Transfer(60) failed: rc=%d err=%v", rc, err)
	}
	// Balance must NOT change during execution (deferred to applySend).
	if fake.bal(types.QsrTokenStandard).Int64() != 100 {
		t.Errorf("balance changed during execution: %d, want 100", fake.bal(types.QsrTokenStandard).Int64())
	}

	// effectiveSpendable should now be 100 - 60 = 40 (pending transfer reserved).
	// Transfer 40 — should succeed.
	rc, err = wc.Transfer(types.QsrTokenStandard, types.Address{2}, big.NewInt(40))
	if err != nil || rc != 0 {
		t.Fatalf("Transfer(40) failed: rc=%d err=%v", rc, err)
	}

	// Transfer 1 — should fail (0 remaining).
	rc, err = wc.Transfer(types.QsrTokenStandard, types.Address{3}, big.NewInt(1))
	if err != nil {
		t.Fatal(err)
	}
	if rc != 1 {
		t.Errorf("Transfer(1) rc = %d, want 1 (nothing left)", rc)
	}

	if len(wc.Transfers()) != 2 {
		t.Errorf("expected 2 queued transfers, got %d", len(wc.Transfers()))
	}
}

// --- StateWrite ---

func TestWasmContext_StateWriteAccumulatesBurn(t *testing.T) {
	fake := newFakeBalCtx()
	fake.setBalance(types.QsrTokenStandard, 1_000_000_000) // plenty of QSR
	wc := newBurnTestContext(t, fake)

	// Write "k" -> "v": (1 + 1) * 1000 = 2000 burn
	if err := wc.StateWrite([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("StateWrite failed: %v", err)
	}
	if wc.BurnAmount().Int64() != 2000 {
		t.Errorf("burnAmount = %d, want 2000", wc.BurnAmount().Int64())
	}

	// Overwrite "k" -> "vvv": (1 + 3) * 1000 = 4000; delta = 4000 - 2000 = 2000
	if err := wc.StateWrite([]byte("k"), []byte("vvv")); err != nil {
		t.Fatalf("StateWrite overwrite failed: %v", err)
	}
	if wc.BurnAmount().Int64() != 4000 {
		t.Errorf("burnAmount = %d, want 4000 (2000 + 2000 growth)", wc.BurnAmount().Int64())
	}

	// Shrink "k" -> "": (1 + 0) * 1000 = 1000; delta = 1000 - 4000 = -3000; no burn growth
	if err := wc.StateWrite([]byte("k"), []byte("")); err != nil {
		t.Fatalf("StateWrite shrink failed: %v", err)
	}
	if wc.BurnAmount().Int64() != 4000 {
		t.Errorf("burnAmount = %d, want 4000 (no refund on shrink)", wc.BurnAmount().Int64())
	}
}

func TestWasmContext_StateWriteInsufficientBalance(t *testing.T) {
	fake := newFakeBalCtx()
	fake.setBalance(types.QsrTokenStandard, 100) // only 100 QSR
	wc := newBurnTestContext(t, fake)

	// Write costs 6000 — way more than 100 available.
	err := wc.StateWrite([]byte("k"), []byte("v"))
	if err != ErrInsufficientDeposit {
		t.Errorf("StateWrite err = %v, want ErrInsufficientDeposit", err)
	}
	// burnAmount must not have changed.
	if wc.BurnAmount().Sign() != 0 {
		t.Errorf("burnAmount = %d, want 0 (failed write)", wc.BurnAmount().Int64())
	}
}

func TestWasmContext_StateWriteReservesBurnForTransfer(t *testing.T) {
	fake := newFakeBalCtx()
	fake.setBalance(types.QsrTokenStandard, 10000)
	wc := newBurnTestContext(t, fake)

	// Write costs 2000. After that, effectiveSpendable = 10000 - 2000 = 8000.
	if err := wc.StateWrite([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("StateWrite failed: %v", err)
	}

	// Transfer 8000 — should succeed (exactly the remaining).
	rc, err := wc.Transfer(types.QsrTokenStandard, types.Address{1}, big.NewInt(8000))
	if err != nil || rc != 0 {
		t.Fatalf("Transfer(8000) failed: rc=%d err=%v", rc, err)
	}

	// Transfer 1 — should fail (0 remaining).
	rc, err = wc.Transfer(types.QsrTokenStandard, types.Address{2}, big.NewInt(1))
	if err != nil {
		t.Fatal(err)
	}
	if rc != 1 {
		t.Errorf("Transfer(1) rc = %d, want 1 (nothing left after burn + transfer)", rc)
	}
}

// --- StateDelete ---

func TestWasmContext_StateDeleteNoBurn(t *testing.T) {
	fake := newFakeBalCtx()
	fake.setBalance(types.QsrTokenStandard, 1_000_000_000)
	wc := newBurnTestContext(t, fake)

	// Write then delete.
	if err := wc.StateWrite([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	burnAfterWrite := new(big.Int).Set(wc.BurnAmount())

	deleted, err := wc.StateDelete([]byte("k"))
	if err != nil || !deleted {
		t.Fatalf("StateDelete failed: deleted=%v err=%v", deleted, err)
	}

	// Burn amount must not change on delete (QSR was already burned at write time).
	if wc.BurnAmount().Cmp(burnAfterWrite) != 0 {
		t.Errorf("burnAmount changed on delete: %d -> %d", burnAfterWrite, wc.BurnAmount())
	}
}

// --- GetCaller / GetAddress ---

func TestWasmContext_GetCallerReturnsSender(t *testing.T) {
	sender := types.Address{0xAA, 0xBB}
	fake := newFakeBalCtx()
	send := &nom.AccountBlock{Address: sender}
	wc := NewWasmContext(fake, send, types.Address{0x02}, definition.DefaultWasmVariables())

	if wc.GetCaller() != sender {
		t.Errorf("GetCaller() = %s, want %s", wc.GetCaller(), sender)
	}
	if wc.GetAddress() != (types.Address{0x02}) {
		t.Errorf("GetAddress() = %s, want 0x02", wc.GetAddress())
	}
}

// --- GetCallToken / GetCallAmount ---

func TestWasmContext_GetCallTokenAndAmount(t *testing.T) {
	fake := newFakeBalCtx()
	zts := types.ZenonTokenStandard{1, 2, 3}
	amount := big.NewInt(12345)
	send := &nom.AccountBlock{
		Address:       types.Address{},
		TokenStandard: zts,
		Amount:        amount,
	}
	wc := NewWasmContext(fake, send, types.Address{0x02}, definition.DefaultWasmVariables())

	gotZTS := wc.GetCallToken()
	if gotZTS != zts {
		t.Errorf("GetCallToken() = %v, want %v", gotZTS, zts)
	}

	gotAmount := wc.GetCallAmount()
	if gotAmount.Cmp(amount) != 0 {
		t.Errorf("GetCallAmount() = %s, want %s", gotAmount, amount)
	}
	// Must return a copy, not an alias.
	gotAmount.Add(gotAmount, big.NewInt(1))
	if wc.GetCallAmount().Cmp(amount) != 0 {
		t.Error("GetCallAmount() returned an alias, not a copy")
	}
}

func TestWasmContext_GetCallTokenAndAmountNilSend(t *testing.T) {
	fake := newFakeBalCtx()
	// ViewContext has no sendBlock.
	wc := NewViewContext(fake, types.Address{0x02}, definition.DefaultWasmVariables())

	if wc.GetCallToken() != (types.ZenonTokenStandard{}) {
		t.Errorf("GetCallToken() should be zero for view context")
	}
	if wc.GetCallAmount().Sign() != 0 {
		t.Errorf("GetCallAmount() should be 0 for view context")
	}
}

// M2/M3 regression: through the production DisableNotFound wrapper, state_read
// must distinguish a genuinely absent key from one written with an empty value,
// and a fresh write must charge its key bytes (oldCost = 0), not treat the
// absent key as an empty-but-existing value.
func newDisableNotFoundCtx() *fakeBalCtx {
	f := newFakeBalCtx()
	f.store = db.DisableNotFound(f.store) // reproduce vmCtx.Storage()
	f.setBalance(types.QsrTokenStandard, 1<<62)
	return f
}

func TestWasmContext_StateReadDistinguishesAbsentFromEmpty(t *testing.T) {
	fake := newDisableNotFoundCtx()
	wc := NewWasmContext(fake, &nom.AccountBlock{Address: types.Address{}}, types.Address{}, definition.DefaultWasmVariables())

	if v, ok := wc.StateRead([]byte("missing")); ok {
		t.Fatalf("absent key reported present (value=%q)", v)
	}
	if err := wc.StateWrite([]byte("e"), []byte{}); err != nil {
		t.Fatal(err)
	}
	if v, ok := wc.StateRead([]byte("e")); !ok || len(v) != 0 {
		t.Fatalf("empty-valued key: got (%q,%v), want (empty,true)", v, ok)
	}
	if _, ok := wc.StateRead([]byte("other")); ok {
		t.Fatal("second absent key reported present")
	}
}

func TestWasmContext_StateWriteFreshKeyChargesKeyBytes(t *testing.T) {
	fake := newDisableNotFoundCtx()
	wc := NewWasmContext(fake, &nom.AccountBlock{Address: types.Address{}}, types.Address{}, definition.DefaultWasmVariables())

	// Fresh "k" -> "vvvv" must burn (1+4)*rate, not 4*rate.
	if err := wc.StateWrite([]byte("k"), []byte("vvvv")); err != nil {
		t.Fatal(err)
	}
	want := int64(5) * int64(definition.DefaultWasmVariables().QSRPerByteOfState)
	if got := wc.BurnAmount().Int64(); got != want {
		t.Fatalf("fresh-write burn = %d, want %d", got, want)
	}
}
