package wasm

import (
	"context"
	"math/big"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/zenon-network/go-zenon/common/types"
)

// registerHostFunctions registers all 18 host functions on the given builder.
func registerHostFunctions(builder wazero.HostModuleBuilder, metering *HostGasListenerFactory) {
	// Note: wazero's HostModuleBuilder uses NewFunctionBuilder().WithFunc(...).Export(...)
	// for each host function. The function signature must match WASM types.

	b := builder.(wazero.HostModuleBuilder)

	b.NewFunctionBuilder().
		WithFunc(hostStateRead).
		Export("state_read")

	b.NewFunctionBuilder().
		WithFunc(hostStateWrite).
		Export("state_write")

	b.NewFunctionBuilder().
		WithFunc(hostStateDelete).
		Export("state_delete")

	b.NewFunctionBuilder().
		WithFunc(hostStateHas).
		Export("state_has")

	b.NewFunctionBuilder().
		WithFunc(hostBalanceGet).
		Export("balance_get")

	b.NewFunctionBuilder().
		WithFunc(hostTransfer).
		Export("transfer")

	b.NewFunctionBuilder().
		WithFunc(hostGetHeight).
		Export("get_height")

	b.NewFunctionBuilder().
		WithFunc(hostGetTimestamp).
		Export("get_timestamp")

	b.NewFunctionBuilder().
		WithFunc(hostGetPrevHash).
		Export("get_prev_hash")

	b.NewFunctionBuilder().
		WithFunc(hostGetCaller).
		Export("get_caller")

	b.NewFunctionBuilder().
		WithFunc(hostGetCallToken).
		Export("get_call_token")

	b.NewFunctionBuilder().
		WithFunc(hostGetCallAmount).
		Export("get_call_amount")

	b.NewFunctionBuilder().
		WithFunc(hostGetAddress).
		Export("get_address")

	b.NewFunctionBuilder().
		WithFunc(hostGetBlockHash).
		Export("get_block_hash")

	b.NewFunctionBuilder().
		WithFunc(hostGetRemainingGas).
		Export("get_remaining_gas")

	b.NewFunctionBuilder().
		WithFunc(hostEmitEvent).
		Export("emit_event")

	b.NewFunctionBuilder().
		WithFunc(hostAbort).
		Export("abort")

	b.NewFunctionBuilder().
		WithFunc(hostLogMsg).
		Export("log_msg")
}

// readOnlyTrap aborts a view call (§11.3) when a mutating host function is
// invoked. It panics; wazero converts the panic into a trap that unwinds the
// Call with an error, exactly like hostAbort, so no partial effects are
// possible — a view never commits.
func readOnlyTrap() {
	panic("wasm: state mutation attempted in read-only view call")
}

// --- State Operations ---

// hostStateRead reads a value from the contract's state.
// Signature: (key_ptr, key_len, result_ptr, result_max_len) -> value_len
// Returns -1 (0xFFFFFFFF) if key not found.
func hostStateRead(ctx context.Context, mod api.Module, keyPtr, keyLen, resultPtr, resultMaxLen uint32) uint32 {
	wc := wasmCtxFromContext(ctx)
	if wc == nil {
		return 0xFFFFFFFF
	}

	key, ok := mod.Memory().Read(keyPtr, keyLen)
	if !ok {
		return 0xFFFFFFFF
	}

	value, exists := wc.StateRead(key)
	if !exists {
		return 0xFFFFFFFF
	}

	valueLen := uint32(len(value))
	if valueLen <= resultMaxLen {
		mod.Memory().Write(resultPtr, value)
	} else {
		// Truncate to fit.
		mod.Memory().Write(resultPtr, value[:resultMaxLen])
	}
	return valueLen
}

// hostStateWrite writes a value to the contract's state.
// Signature: (key_ptr, key_len, value_ptr, value_len) -> i32
// Returns 0=ok, 1=insufficient QSR for burn.
func hostStateWrite(ctx context.Context, mod api.Module, keyPtr, keyLen, valuePtr, valueLen uint32) uint32 {
	wc := wasmCtxFromContext(ctx)
	if wc == nil {
		return 1
	}
	if wc.readOnly {
		readOnlyTrap()
	}

	key, ok := mod.Memory().Read(keyPtr, keyLen)
	if !ok {
		return 1
	}

	value, ok := mod.Memory().Read(valuePtr, valueLen)
	if !ok {
		return 1
	}

	if err := wc.StateWrite(key, value); err != nil {
		// Any failure (insufficient QSR for burn, read-only, etc.) maps to a
		// non-zero status code the guest can branch on.
		return 1
	}
	return 0
}

// hostStateDelete deletes a key from the contract's state.
// Signature: (key_ptr, key_len) -> i32
// Returns 1=deleted, 0=not found.
func hostStateDelete(ctx context.Context, mod api.Module, keyPtr, keyLen uint32) uint32 {
	wc := wasmCtxFromContext(ctx)
	if wc == nil {
		return 0
	}
	if wc.readOnly {
		readOnlyTrap()
	}

	key, ok := mod.Memory().Read(keyPtr, keyLen)
	if !ok {
		return 0
	}

	deleted, err := wc.StateDelete(key)
	if err != nil || !deleted {
		return 0
	}
	return 1
}

// hostStateHas checks if a key exists in the contract's state.
// Signature: (key_ptr, key_len) -> i32
// Returns 1=exists, 0=not.
func hostStateHas(ctx context.Context, mod api.Module, keyPtr, keyLen uint32) uint32 {
	wc := wasmCtxFromContext(ctx)
	if wc == nil {
		return 0
	}

	key, ok := mod.Memory().Read(keyPtr, keyLen)
	if !ok {
		return 0
	}

	if wc.StateHas(key) {
		return 1
	}
	return 0
}

// --- Token Operations ---

// hostBalanceGet reads the spendable balance of a token standard.
// Signature: (zts_ptr, result_ptr) -> void
// Writes 32-byte big-endian amount to result_ptr.
func hostBalanceGet(ctx context.Context, mod api.Module, ztsPtr, resultPtr uint32) {
	wc := wasmCtxFromContext(ctx)
	if wc == nil {
		return
	}

	ztsBytes, ok := mod.Memory().Read(ztsPtr, types.ZenonTokenStandardSize)
	if !ok {
		return
	}

	var zts types.ZenonTokenStandard
	copy(zts[:], ztsBytes)

	balance, err := wc.BalanceGet(zts)
	if err != nil || balance == nil {
		// Write zero.
		mod.Memory().Write(resultPtr, make([]byte, 32))
		return
	}

	// Write balance as 32-byte big-endian.
	balanceBytes := balance.Bytes()
	result := make([]byte, 32)
	copy(result[32-len(balanceBytes):], balanceBytes)
	mod.Memory().Write(resultPtr, result)
}

// hostTransfer transfers tokens from the contract to another address.
// Signature: (zts_ptr, to_ptr, amount_ptr) -> i32
// Returns 0=queued, 1=insufficient balance.
func hostTransfer(ctx context.Context, mod api.Module, ztsPtr, toPtr, amountPtr uint32) uint32 {
	wc := wasmCtxFromContext(ctx)
	if wc == nil {
		return 1
	}
	if wc.readOnly {
		readOnlyTrap()
	}

	ztsBytes, ok := mod.Memory().Read(ztsPtr, types.ZenonTokenStandardSize)
	if !ok {
		return 1
	}
	var zts types.ZenonTokenStandard
	copy(zts[:], ztsBytes)

	toBytes, ok := mod.Memory().Read(toPtr, types.AddressSize)
	if !ok {
		return 1
	}
	var to types.Address
	copy(to[:], toBytes)

	amountBytes, ok := mod.Memory().Read(amountPtr, 32)
	if !ok {
		return 1
	}
	amount := new(big.Int).SetBytes(amountBytes)

	result, err := wc.Transfer(zts, to, amount)
	if err != nil {
		return 1
	}
	return uint32(result)
}

// --- Block & Context Info ---

// hostGetHeight returns the current momentum height.
// Signature: () -> u64
func hostGetHeight(ctx context.Context, mod api.Module) uint64 {
	wc := wasmCtxFromContext(ctx)
	if wc == nil {
		return 0
	}
	return wc.GetHeight()
}

// hostGetTimestamp returns the current momentum timestamp.
// Signature: () -> u64
func hostGetTimestamp(ctx context.Context, mod api.Module) uint64 {
	wc := wasmCtxFromContext(ctx)
	if wc == nil {
		return 0
	}
	return wc.GetTimestamp()
}

// hostGetPrevHash writes the previous momentum hash (32 bytes).
// Signature: (result_ptr) -> void
func hostGetPrevHash(ctx context.Context, mod api.Module, resultPtr uint32) {
	wc := wasmCtxFromContext(ctx)
	if wc == nil {
		return
	}
	hash := wc.GetPrevHash()
	mod.Memory().Write(resultPtr, hash[:])
}

// hostGetCaller writes the immediate sender's address (20 bytes).
// Signature: (result_ptr) -> void
func hostGetCaller(ctx context.Context, mod api.Module, resultPtr uint32) {
	wc := wasmCtxFromContext(ctx)
	if wc == nil {
		return
	}
	addr := wc.GetCaller()
	mod.Memory().Write(resultPtr, addr[:])
}

// hostGetCallToken writes the send block's token standard (10 bytes).
// Signature: (result_ptr) -> void
func hostGetCallToken(ctx context.Context, mod api.Module, resultPtr uint32) {
	wc := wasmCtxFromContext(ctx)
	if wc == nil {
		return
	}
	zts := wc.GetCallToken()
	mod.Memory().Write(resultPtr, zts[:])
}

// hostGetCallAmount writes the send block's amount as 32-byte big-endian.
// Signature: (result_ptr) -> void
func hostGetCallAmount(ctx context.Context, mod api.Module, resultPtr uint32) {
	wc := wasmCtxFromContext(ctx)
	if wc == nil {
		mod.Memory().Write(resultPtr, make([]byte, 32))
		return
	}
	amount := wc.GetCallAmount()
	amountBytes := amount.Bytes()
	result := make([]byte, 32)
	copy(result[32-len(amountBytes):], amountBytes)
	mod.Memory().Write(resultPtr, result)
}

// hostGetAddress writes the contract's own address (20 bytes).
// Signature: (result_ptr) -> void
func hostGetAddress(ctx context.Context, mod api.Module, resultPtr uint32) {
	wc := wasmCtxFromContext(ctx)
	if wc == nil {
		return
	}
	addr := wc.GetAddress()
	mod.Memory().Write(resultPtr, addr[:])
}

// hostGetBlockHash writes the send block's hash (32 bytes).
// Signature: (result_ptr) -> void
func hostGetBlockHash(ctx context.Context, mod api.Module, resultPtr uint32) {
	wc := wasmCtxFromContext(ctx)
	if wc == nil {
		return
	}
	hash := wc.GetBlockHash()
	mod.Memory().Write(resultPtr, hash[:])
}

// hostGetRemainingGas returns the remaining gas. It reads the injected
// $__gas_remaining global, the single authoritative counter that both the
// per-opcode metering and the host-call listener debit (spec §6.3).
// Signature: () -> u64
func hostGetRemainingGas(ctx context.Context, mod api.Module) uint64 {
	if g := mod.ExportedGlobal(GasGlobalName); g != nil {
		return g.Get()
	}
	return 0
}

// --- Events ---

// hostEmitEvent emits an event.
// Signature: (topic_ptr, data_ptr, data_len, indexed) -> i32
// Returns 0=ok, 1=caps exceeded.
func hostEmitEvent(ctx context.Context, mod api.Module, topicPtr, dataPtr, dataLen uint32, indexed uint32) uint32 {
	wc := wasmCtxFromContext(ctx)
	if wc == nil {
		return 1
	}
	if wc.readOnly {
		readOnlyTrap()
	}

	topicBytes, ok := mod.Memory().Read(topicPtr, 32)
	if !ok {
		return 1
	}
	var topic types.Hash
	copy(topic[:], topicBytes)

	var data []byte
	if dataLen > 0 {
		data, ok = mod.Memory().Read(dataPtr, dataLen)
		if !ok {
			return 1
		}
	}

	if err := wc.events.Emit(topic, data, indexed != 0); err != nil {
		return 1
	}
	return 0
}

// --- Control Flow ---

// hostAbort traps immediately, reverting all state changes.
// Signature: (msg_ptr, msg_len, file_ptr, file_len) -> void
// filePtr/fileLen are accepted for AssemblyScript compatibility but unused.
func hostAbort(ctx context.Context, mod api.Module, msgPtr, msgLen, filePtr, fileLen uint32) {
	// Read the message for logging (best-effort).
	if msgLen > 0 && msgLen < 4096 {
		if msg, ok := mod.Memory().Read(msgPtr, msgLen); ok {
			// Log the abort message (non-consensus, best-effort).
			_ = msg
		}
	}
	// Trap — this will cause wazero to return an error from the Call.
	panic("abort")
}

// hostLogMsg is a no-op on the consensus path.
// Signature: (msg_ptr, msg_len) -> void
func hostLogMsg(ctx context.Context, mod api.Module, msgPtr, msgLen uint32) {
	// No-op: log_msg is only active in off-chain debug builds.
}
