package momentum

import (
	"bytes"
	"sort"
	"testing"

	"github.com/zenon-network/go-zenon/chain/account"
	"github.com/zenon-network/go-zenon/chain/account/mailbox"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
)

func wasmAddr(n byte) types.Address {
	var addr types.Address
	addr[0] = types.WasmContractAddrByte
	addr[1] = n
	return addr
}

func TestMarkWasmPending_AddsToSet(t *testing.T) {
	ms := NewGenesisStore().(*momentumStore)
	addr := wasmAddr(1)

	ms.markWasmPending(addr)

	addrs, err := ms.GetWasmPendingAddresses()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 1 {
		t.Fatalf("expected 1 address, got %d", len(addrs))
	}
	if addrs[0] != addr {
		t.Fatalf("expected %v, got %v", addr, addrs[0])
	}
}

func TestGetWasmPendingAddresses_SortedOrder(t *testing.T) {
	ms := NewGenesisStore().(*momentumStore)

	// Insert in reverse order
	addrs := []types.Address{wasmAddr(3), wasmAddr(1), wasmAddr(2)}
	for _, addr := range addrs {
		ms.markWasmPending(addr)
	}

	got, err := ms.GetWasmPendingAddresses()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 addresses, got %d", len(got))
	}

	// Verify sorted by bytes
	sorted := make([]types.Address, len(addrs))
	copy(sorted, addrs)
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i][:], sorted[j][:]) < 0
	})
	for i := range got {
		if got[i] != sorted[i] {
			t.Fatalf("index %d: expected %v, got %v", i, sorted[i], got[i])
		}
	}
}

func TestClearWasmPendingIfDrained_EmptyMailbox(t *testing.T) {
	ms := NewGenesisStore().(*momentumStore)
	addr := wasmAddr(1)

	ms.markWasmPending(addr)

	// Mailbox has no items — should clear
	if err := ms.clearWasmPendingIfDrained(addr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	addrs, err := ms.GetWasmPendingAddresses()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 0 {
		t.Fatalf("expected 0 addresses after clear, got %d", len(addrs))
	}
}

func TestClearWasmPendingIfDrained_NonEmptyMailbox(t *testing.T) {
	ms := NewGenesisStore().(*momentumStore)
	addr := wasmAddr(1)

	ms.markWasmPending(addr)

	// Push a sequencer item directly to the mailbox DB
	mailboxPrefix := common.JoinBytes(accountMailboxPrefix, addr.Bytes())
	mailboxDB := ms.DB.Subset(mailboxPrefix)
	mb := mailbox.NewAccountMailbox(addr, mailboxDB)
	mb.SequencerPushBack(types.AccountHeader{
		Address: addr,
		HashHeight: types.HashHeight{
			Hash:   types.Hash{1},
			Height: 1,
		},
	})

	// Mailbox has items — should NOT clear
	if err := ms.clearWasmPendingIfDrained(addr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	addrs, err := ms.GetWasmPendingAddresses()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 1 {
		t.Fatalf("expected 1 address (not cleared), got %d", len(addrs))
	}
	if addrs[0] != addr {
		t.Fatalf("expected %v, got %v", addr, addrs[0])
	}
}

func TestGetWasmPendingAddresses_Empty(t *testing.T) {
	ms := NewGenesisStore().(*momentumStore)

	addrs, err := ms.GetWasmPendingAddresses()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 0 {
		t.Fatalf("expected 0 addresses, got %d", len(addrs))
	}
}

func userAddr(n byte) types.Address {
	var addr types.Address
	addr[0] = types.UserAddrByte
	addr[19] = n
	return addr
}

// buildBlockPatch serializes block into a fresh account-store DB exactly as the
// account-pool commit does, and returns the resulting patch ready to feed to
// AddAccountBlockTransaction. When popSequencer is set it also advances the
// account's sequencer cursor, mirroring the VM's SequencerPopFront on a
// contract-receive (vm/vm.go) — that cursor advance is what makes the committed
// apply path observe a drained sequencer.
func buildBlockPatch(t *testing.T, addr types.Address, block *nom.AccountBlock, popSequencer bool) db.Patch {
	t.Helper()
	accDB := db.NewMemDB()
	data, err := block.Serialize()
	if err != nil {
		t.Fatalf("serialize block: %v", err)
	}
	if err := db.SetFrontier(accDB, block.Identifier(), data); err != nil {
		t.Fatalf("set frontier: %v", err)
	}
	if popSequencer {
		account.NewAccountStore(addr, accDB).SequencerPopFront()
	}
	patch, err := accDB.Changes()
	if err != nil {
		t.Fatalf("changes: %v", err)
	}
	return patch
}

// TestAddAccountBlockTransaction_WasmReceiveDrainsPending verifies that the
// wasm-pending index is pruned by the committed momentum-apply path
// (AddAccountBlockTransaction) when a WASM contract's sequencer drains — not by
// the pillar worker against an ephemeral frontier snapshot, where the Delete
// would be silently discarded.
func TestAddAccountBlockTransaction_WasmReceiveDrainsPending(t *testing.T) {
	ms := NewGenesisStore().(*momentumStore)

	sender := userAddr(7)
	wasm := wasmAddr(1)

	// Step A: a user sends to the WASM contract. The committed apply path indexes
	// the send, pushes the WASM sequencer and marks the contract wasm-pending.
	sendBlock := &nom.AccountBlock{
		BlockType: nom.BlockTypeUserSend,
		Address:   sender,
		ToAddress: wasm,
		Height:    1,
		Hash:      types.Hash{0x5e, 0x4d, 0x01},
	}
	if err := ms.AddAccountBlockTransaction(sendBlock.Header(), buildBlockPatch(t, sender, sendBlock, false)); err != nil {
		t.Fatalf("apply send: %v", err)
	}

	addrs, err := ms.GetWasmPendingAddresses()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 1 || addrs[0] != wasm {
		t.Fatalf("after send: expected pending [%v], got %v", wasm, addrs)
	}

	// Step B: the WASM contract receives that send. The receive patch advances the
	// sequencer cursor (as the VM does), so the apply path sees a drained sequencer
	// and prunes the index.
	recvBlock := &nom.AccountBlock{
		BlockType:     nom.BlockTypeContractReceive,
		Address:       wasm,
		Height:        1,
		Hash:          types.Hash{0xec, 0xed, 0x02},
		FromBlockHash: sendBlock.Hash,
	}
	if err := ms.AddAccountBlockTransaction(recvBlock.Header(), buildBlockPatch(t, wasm, recvBlock, true)); err != nil {
		t.Fatalf("apply receive: %v", err)
	}

	addrs, err = ms.GetWasmPendingAddresses()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 0 {
		t.Fatalf("after receive: expected wasm-pending index pruned, got %v", addrs)
	}
}
