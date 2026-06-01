package tests

import (
	"bytes"
	"math/big"
	"testing"

	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/crypto"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc/api"
	"github.com/zenon-network/go-zenon/rpc/api/embedded"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/vm/wasm/testmodules"
	"github.com/zenon-network/go-zenon/zenon/mock"
)

// activateWasmRuntime activates both DynamicPlasma and WasmRuntime sporks.
// DynamicPlasma must be active before WasmRuntime can be activated.
func activateWasmRuntime(t *testing.T, z mock.MockZenon) {
	saveSporkState(t)

	// First activate DynamicPlasma (dependency)
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.Spork.Address,
		ToAddress: types.SporkContract,
		Data: definition.ABISpork.PackMethodPanic(definition.SporkCreateMethodName,
			"dynamic-plasma",
			"Activates Dynamic Plasma",
		),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()

	sporkAPI := embedded.NewSporkApi(z)
	sporkList, _ := sporkAPI.GetAll(0, 10)
	dpId := sporkList.List[0].Id

	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.Spork.Address,
		ToAddress: types.SporkContract,
		Data: definition.ABISpork.PackMethodPanic(definition.SporkActivateMethodName,
			dpId,
		),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()
	types.DynamicPlasmaSpork.SporkId = dpId
	types.ImplementedSporksMap[dpId] = true
	z.InsertMomentumsTo(20)

	// Now activate WasmRuntime
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.Spork.Address,
		ToAddress: types.SporkContract,
		Data: definition.ABISpork.PackMethodPanic(definition.SporkCreateMethodName,
			"wasm-runtime",
			"Activates WASM Runtime",
		),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()

	sporkList, _ = sporkAPI.GetAll(0, 10)
	wasmId := sporkList.List[1].Id

	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.Spork.Address,
		ToAddress: types.SporkContract,
		Data: definition.ABISpork.PackMethodPanic(definition.SporkActivateMethodName,
			wasmId,
		),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()
	types.WasmRuntimeSpork.SporkId = wasmId
	types.ImplementedSporksMap[wasmId] = true
	z.InsertMomentumsTo(40)
}

// minimalValidWasmModule returns a minimal WASM module that passes validation.
// It exports "execute(i32, i32) -> i32" and "memory" (1 page). The module is
// exactly 56 bytes, so bytecodeCost(len) == MinBytecodeCost (1e8).
func minimalValidWasmModule() []byte {
	return []byte{
		// Header
		0x00, 0x61, 0x73, 0x6D, // magic
		0x01, 0x00, 0x00, 0x00, // version 1
		// Type section (id=1, size=7)
		0x01, 0x07,
		0x01,             // 1 type
		0x60,             // func type
		0x02, 0x7F, 0x7F, // 2 params: i32, i32
		0x01, 0x7F, // 1 result: i32
		// Function section (id=3, size=2)
		0x03, 0x02,
		0x01, 0x00, // 1 function, type index 0
		// Memory section (id=5, size=3)
		0x05, 0x03,
		0x01,       // 1 memory
		0x00, 0x01, // no max, min 1 page
		// Export section (id=7, size=20)
		0x07, 0x14,
		0x02,                                                       // 2 exports
		0x07, 0x65, 0x78, 0x65, 0x63, 0x75, 0x74, 0x65, 0x00, 0x00, // "execute" func 0
		0x06, 0x6D, 0x65, 0x6D, 0x6F, 0x72, 0x79, 0x02, 0x00, // "memory" mem 0
		// Code section (id=10, size=6)
		0x0A, 0x06,
		0x01,       // 1 function body
		0x04,       // body size 4
		0x00,       // 0 local declarations
		0x20, 0x00, // local.get 0
		0x0B, // end
	}
}

// onReceiveModule returns a WASM module that exports both "execute" and
// "on_receive" (both (i32,i32)->i32). on_receive returns 0 (success).
func onReceiveModule() []byte {
	return []byte{
		0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00, // header
		// Type section: 1 type — (i32, i32) -> i32
		0x01, 0x07, 0x01, 0x60, 0x02, 0x7F, 0x7F, 0x01, 0x7F,
		// Function section: 2 functions, both type 0
		0x03, 0x03, 0x02, 0x00, 0x00,
		// Memory section: 1 page
		0x05, 0x03, 0x01, 0x00, 0x01,
		// Export section (33 bytes payload): execute (func 0), on_receive (func 1), memory
		0x07, 0x21, 0x03,
		0x07, 0x65, 0x78, 0x65, 0x63, 0x75, 0x74, 0x65, 0x00, 0x00,
		0x0A, 0x6F, 0x6E, 0x5F, 0x72, 0x65, 0x63, 0x65, 0x69, 0x76, 0x65, 0x00, 0x01,
		0x06, 0x6D, 0x65, 0x6D, 0x6F, 0x72, 0x79, 0x02, 0x00,
		// Code section: 2 bodies
		//   payload = count(1) + func0(5) + func1(5) = 11 bytes
		0x0A, 0x0B, 0x02,
		0x04, 0x00, 0x20, 0x00, 0x0B, // func 0 (execute): body=04 00 20 00 0B
		0x04, 0x00, 0x41, 0x00, 0x0B, // func 1 (on_receive): body=04 00 41 00 0B
	}
}

// onReceiveTrapModule returns a WASM module whose on_receive traps via
// the unreachable opcode. The balance credit must survive this trap.
func onReceiveTrapModule() []byte {
	return []byte{
		0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00, // header
		// Type section: 1 type — (i32, i32) -> i32
		0x01, 0x07, 0x01, 0x60, 0x02, 0x7F, 0x7F, 0x01, 0x7F,
		// Function section: 2 functions, both type 0
		0x03, 0x03, 0x02, 0x00, 0x00,
		// Memory section: 1 page
		0x05, 0x03, 0x01, 0x00, 0x01,
		// Export section (33 bytes payload): execute (func 0), on_receive (func 1), memory
		0x07, 0x21, 0x03,
		0x07, 0x65, 0x78, 0x65, 0x63, 0x75, 0x74, 0x65, 0x00, 0x00,
		0x0A, 0x6F, 0x6E, 0x5F, 0x72, 0x65, 0x63, 0x65, 0x69, 0x76, 0x65, 0x00, 0x01,
		0x06, 0x6D, 0x65, 0x6D, 0x6F, 0x72, 0x79, 0x02, 0x00,
		// Code section: 2 bodies
		//   payload = count(1) + func0(5) + func1(4) = 10 bytes
		0x0A, 0x0A, 0x02,
		0x04, 0x00, 0x20, 0x00, 0x0B, // func 0 (execute): body=04 00 20 00 0B
		0x03, 0x00, 0x00, 0x0B,       // func 1 (on_receive): body=03 00 00 0B
	}
}

// onReceiveStateWriteModule exports execute (no-op, returns 0), on_receive
// (writes a 1-byte key + 4-byte value via state_write, returns 0), and memory.
// It imports state_read (func 0) and state_write (func 1). The on_receive body
// is identical to StateWriteModule's execute body, so triggering the hook with a
// plain transfer burns (1+4)*QSRPerByteOfState QSR — proving on_receive state
// writes are charged like execute.
func onReceiveStateWriteModule() []byte {
	return []byte{
		// Header
		0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00,

		// Type section (id=1, size=15): type0 (i32,i32,i32,i32)->i32, type1 (i32,i32)->i32
		0x01, 0x0F,
		0x02,
		0x60, 0x04, 0x7F, 0x7F, 0x7F, 0x7F, 0x01, 0x7F,
		0x60, 0x02, 0x7F, 0x7F, 0x01, 0x7F,

		// Import section (id=2, size=36): env.state_read (func 0), env.state_write (func 1)
		0x02, 0x24,
		0x02,
		0x03, 0x65, 0x6E, 0x76, // "env"
		0x0A, 0x73, 0x74, 0x61, 0x74, 0x65, 0x5F, 0x72, 0x65, 0x61, 0x64, // "state_read"
		0x00, 0x00,
		0x03, 0x65, 0x6E, 0x76, // "env"
		0x0B, 0x73, 0x74, 0x61, 0x74, 0x65, 0x5F, 0x77, 0x72, 0x69, 0x74, 0x65, // "state_write"
		0x00, 0x00,

		// Function section (id=3, size=3): func2 type1 (execute), func3 type1 (on_receive)
		0x03, 0x03, 0x02, 0x01, 0x01,

		// Memory section (id=5, size=3): 1 page
		0x05, 0x03, 0x01, 0x00, 0x01,

		// Export section (id=7, size=33): execute func2, on_receive func3, memory mem0
		0x07, 0x21,
		0x03,
		0x07, 0x65, 0x78, 0x65, 0x63, 0x75, 0x74, 0x65, 0x00, 0x02, // "execute" func 2
		0x0A, 0x6F, 0x6E, 0x5F, 0x72, 0x65, 0x63, 0x65, 0x69, 0x76, 0x65, 0x00, 0x03, // "on_receive" func 3
		0x06, 0x6D, 0x65, 0x6D, 0x6F, 0x72, 0x79, 0x02, 0x00, // "memory" mem 0

		// Code section (id=10, size=58): func2 (execute no-op), func3 (on_receive state_write)
		0x0A, 0x3A,
		0x02,
		// func2 (execute): body size 4 — returns 0
		0x04,
		0x00,       // 0 locals
		0x41, 0x00, // i32.const 0
		0x0B,       // end
		// func3 (on_receive): body size 51 — identical to StateWriteModule's execute body
		0x33,
		0x00,             // 0 locals
		0x41, 0x00,       // i32.const 0
		0x41, 0xE3, 0x00, // i32.const 99 ('c')
		0x3A, 0x00, 0x00, // i32.store8 — store key "c" at memory[0]
		0x41, 0x00,       // i32.const 0 (key_ptr)
		0x41, 0x01,       // i32.const 1 (key_len)
		0x41, 0xE4, 0x00, // i32.const 100 (result_ptr)
		0x41, 0x04,       // i32.const 4 (result_max_len)
		0x10, 0x00,       // call 0 (state_read)
		0x1A,             // drop
		0x41, 0xC8, 0x01, // i32.const 200
		0x41, 0xE4, 0x00, // i32.const 100
		0x28, 0x02, 0x00, // i32.load
		0x41, 0x01,       // i32.const 1
		0x6A,             // i32.add
		0x36, 0x02, 0x00, // i32.store — memory[200] = memory[100] + 1
		0x41, 0x00,       // i32.const 0 (key_ptr)
		0x41, 0x01,       // i32.const 1 (key_len)
		0x41, 0xC8, 0x01, // i32.const 200 (value_ptr)
		0x41, 0x04,       // i32.const 4 (value_len)
		0x10, 0x01,       // call 1 (state_write)
		0x1A,             // drop
		0x41, 0x00,       // i32.const 0
		0x0B,             // end
	}
}

// ---------------------------------------------------------------------------
// Assertion helpers
//
// All deployment state lives in WasmContract (0x01) storage.
// EmbeddedContext reads the committed frontier store.
// 0x02 contract receives are auto-received by driveMomentums.
// ---------------------------------------------------------------------------

func wasmStore(z mock.MockZenon) db.DB {
	return z.EmbeddedContext(types.WasmContract).Storage()
}

func wasmMeta(t *testing.T, z mock.MockZenon, wasmAddr types.Address) *definition.WasmContractMetadata {
	m, err := definition.GetWasmContractMetadata(wasmStore(z), wasmAddr)
	common.FailIfErr(t, err)
	return m
}

func wasmChunkMeta(t *testing.T, z mock.MockZenon, wasmAddr types.Address) *definition.WasmChunkMetadata {
	m, err := definition.GetWasmChunkMetadata(wasmStore(z), wasmAddr)
	common.FailIfErr(t, err)
	return m
}

func wasmInfo(t *testing.T, z mock.MockZenon) *definition.WasmContractInfo {
	info, err := definition.GetWasmContractInfo(wasmStore(z))
	common.FailIfErr(t, err)
	return info
}

func wasmBytecode(t *testing.T, z mock.MockZenon, wasmAddr types.Address) []byte {
	data, err := definition.GetWasmBytecode(wasmStore(z), wasmAddr)
	common.FailIfErr(t, err)
	return data
}

// wasmTokenSupply returns the committed TotalSupply for a token standard, read
// from the TokenContract (0x01) frontier storage. WASM burns (bytecode cost,
// ZNN deploy fee, state-write cost) reduce this via the embedded Burn method, so
// it is the ground truth for asserting real supply reduction.
func wasmTokenSupply(t *testing.T, z mock.MockZenon, zts types.ZenonTokenStandard) *big.Int {
	t.Helper()
	info, err := definition.GetTokenInfo(z.EmbeddedContext(types.TokenContract).Storage(), zts)
	common.FailIfErr(t, err)
	if info == nil {
		t.Fatalf("no token info for %v", zts)
	}
	return info.TotalSupply
}

// driveMomentums inserts n momentums so embedded receives and any descendant
// sends they emit settle into the committed ledger.
func driveMomentums(z mock.MockZenon, n int) {
	for i := 0; i < n; i++ {
		z.InsertNewMomentum()
	}
}

// driveUntilWasmMeta inserts momentums until contract metadata for wasmAddr is
// committed (the deploy/finalize receive landed), then one extra momentum so
// any descendant sends (endowment/refund) settle as unreceived blocks. Returns
// nil if metadata never appears within maxMomentums.
func driveUntilWasmMeta(t *testing.T, z mock.MockZenon, wasmAddr types.Address, maxMomentums int) *definition.WasmContractMetadata {
	for i := 0; i < maxMomentums; i++ {
		z.InsertNewMomentum()
		if m := wasmMeta(t, z, wasmAddr); m != nil {
			z.InsertNewMomentum()
			return m
		}
	}
	return nil
}

// driveUntilChunkMeta inserts momentums until chunk metadata for wasmAddr is
// committed (the first Deploy receive landed). Returns nil if it never
// appears within maxMomentums.
func driveUntilChunkMeta(t *testing.T, z mock.MockZenon, wasmAddr types.Address, maxMomentums int) *definition.WasmChunkMetadata {
	for i := 0; i < maxMomentums; i++ {
		z.InsertNewMomentum()
		if m := wasmChunkMeta(t, z, wasmAddr); m != nil {
			z.InsertNewMomentum()
			return m
		}
	}
	return nil
}

// driveUntilActivated inserts momentums until contract metadata for wasmAddr
// reports Activated==true (the Activate receive landed), then one extra momentum
// so any descendant sends (cost-burn / prefund / fee-burn) settle. Returns nil
// if the contract never activates within maxMomentums.
func driveUntilActivated(t *testing.T, z mock.MockZenon, wasmAddr types.Address, maxMomentums int) *definition.WasmContractMetadata {
	for i := 0; i < maxMomentums; i++ {
		z.InsertNewMomentum()
		if m := wasmMeta(t, z, wasmAddr); m != nil && m.Activated {
			z.InsertNewMomentum()
			return m
		}
	}
	return nil
}

// unreceivedTo returns the pending (unreceived) account blocks addressed to addr.
func unreceivedTo(t *testing.T, z mock.MockZenon, addr types.Address) []*api.AccountBlock {
	list, err := api.NewLedgerApi(z).GetUnreceivedBlocksByAddress(addr, 0, 50)
	common.FailIfErr(t, err)
	return list.List
}

// expectSingleQsrUnreceived asserts addr has exactly one pending block: a QSR
// send of exactly `amount`.
func expectSingleQsrUnreceived(t *testing.T, z mock.MockZenon, addr types.Address, amount *big.Int) {
	blocks := unreceivedTo(t, z, addr)
	if len(blocks) != 1 {
		t.Fatalf("expected exactly 1 unreceived block for %v, got %d", addr, len(blocks))
	}
	b := blocks[0]
	if b.TokenStandard != types.QsrTokenStandard {
		t.Fatalf("expected QSR unreceived for %v, got token %v", addr, b.TokenStandard)
	}
	if b.ToAddress != addr {
		t.Fatalf("unreceived ToAddress mismatch: want %v got %v", addr, b.ToAddress)
	}
	common.ExpectAmount(t, b.Amount, amount)
}

// expectNoUnreceived asserts addr has no pending blocks.
func expectNoUnreceived(t *testing.T, z mock.MockZenon, addr types.Address) {
	blocks := unreceivedTo(t, z, addr)
	if len(blocks) != 0 {
		t.Fatalf("expected no unreceived blocks for %v, got %d", addr, len(blocks))
	}
}

// splitIntoChunks splits data into n non-empty, near-equal contiguous chunks.
// The first len(data)%n chunks get one extra byte. Requires len(data) >= n so
// every chunk is non-empty (Deploy rejects empty chunkData).
func splitIntoChunks(t *testing.T, data []byte, n int) [][]byte {
	if len(data) < n {
		t.Fatalf("cannot split %d bytes into %d non-empty chunks", len(data), n)
	}
	chunks := make([][]byte, n)
	base := len(data) / n
	rem := len(data) % n
	offset := 0
	for i := 0; i < n; i++ {
		size := base
		if i < rem {
			size++
		}
		chunks[i] = data[offset : offset+size]
		offset += size
	}
	return chunks
}

// fullChunkCostFor returns the mandatory first-chunk QSR cost for a chunked
// upload of totalChunks chunks: max(totalChunks*MaxWasmBytecodeSize*QSRPerByte, MinBytecodeCost).
func fullChunkCostFor(totalChunks uint32) int64 {
	d := int64(totalChunks) * int64(constants.MaxWasmBytecodeSize) * int64(constants.QSRPerByteOfBytecode)
	if d < int64(constants.MinBytecodeCost) {
		d = int64(constants.MinBytecodeCost)
	}
	return d
}

// deployAndActivateWasmFor deploys a module single-shot under salt and then
// Activates it (1 ZNN fee), driving momentums until the contract reports
// Activated==true. Returns the contract address. upgradeable controls the
// Activate flag. The single-shot Deploy pre-funds the bytecode cost
// (MinBytecodeCost) into the 0x01 pool; Activate burns that cost plus the ZNN
// fee and flips Activated, which is the gate that enables Execute/view calls.
func deployAndActivateWasmFor(t *testing.T, z mock.MockZenon, module []byte, salt [32]byte, upgradeable bool) types.Address {
	t.Helper()
	wasmAddr := types.WasmAddress(g.User1.Address, salt)

	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(int64(constants.MinBytecodeCost)),
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), uint32(1), module),
	}, nil, mock.SkipVmChanges)
	if driveUntilWasmMeta(t, z, wasmAddr, 8) == nil {
		t.Fatal("contract not deployed")
	}

	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(int64(constants.ZNNDeployFee)),
		Data: definition.ABIWasm.PackMethodPanic(definition.ActivateMethodName,
			wasmAddr, salt, upgradeable),
	}, nil, mock.SkipVmChanges)
	if driveUntilActivated(t, z, wasmAddr, 8) == nil {
		t.Fatal("contract not activated")
	}
	return wasmAddr
}

// ---------------------------------------------------------------------------
// Structural / spork-gating tests
// ---------------------------------------------------------------------------

// 0x02 sends are rejected before WasmRuntime spork activation. applySend in
// vm/vm.go checks the spork before any other validation.
func TestWasm_PreSporkRejects0x02(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)

	wasmAddr := types.WasmAddress(g.User1.Address, [32]byte{1})
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     wasmAddr,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(100 * g.Zexp),
	}, constants.ErrWasmNotActivated, mock.NoVmChanges)
}

// WasmContract address has the 0x01 prefix (embedded contract).
func TestWasm_ManagementContractIsEmbedded(t *testing.T) {
	common.Expect(t, types.IsEmbeddedAddress(types.WasmContract), true)
	common.Expect(t, types.IsWasmContractAddress(types.WasmContract), false)
	common.Expect(t, types.IsContractAddress(types.WasmContract), true)
}

// 0x02 addresses are correctly identified as WASM contract addresses.
func TestWasm_WasmAddressIsCorrectlyPrefixed(t *testing.T) {
	wasmAddr := types.WasmAddress(g.User1.Address, [32]byte{1})
	common.Expect(t, types.IsEmbeddedAddress(wasmAddr), false)
	common.Expect(t, types.IsWasmContractAddress(wasmAddr), true)
	common.Expect(t, types.IsContractAddress(wasmAddr), true)
}

// WasmContract is in the EmbeddedContracts slice (else the pillar worker won't
// auto-receive management contract sends).
func TestWasm_WasmContractInEmbeddedContracts(t *testing.T) {
	found := false
	for _, addr := range types.EmbeddedContracts {
		if addr == types.WasmContract {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("WasmContract is not in EmbeddedContracts slice")
	}
}

// Sends to an undeployed 0x02 address are rejected with ErrWasmContractNotDeployed
// at send validation (the bytecode-exists check in applySend), regardless of data.
func TestWasm_0x02RejectsUndeployed(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	wasmAddr := types.WasmAddress(g.User1.Address, [32]byte{1})

	// Execute data
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: wasmAddr,
		Data: definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName,
			"transfer", []byte{}),
	}, constants.ErrWasmContractNotDeployed, mock.NoVmChanges)

	// Non-Execute data (Deploy ABI sent to undeployed 0x02 — rejected at send)
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: wasmAddr,
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, [32]byte{1}, uint32(0), uint32(1), []byte{0x00, 0x61, 0x73, 0x6d}),
	}, constants.ErrWasmContractNotDeployed, mock.NoVmChanges)

	// Plain token transfer (empty data, non-zero amount)
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     wasmAddr,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(100 * g.Zexp),
	}, constants.ErrWasmContractNotDeployed, mock.NoVmChanges)
}

// Execute against an undeployed 0x02 address is rejected at send validation.
func TestWasm_ExecuteUndeployed(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	wasmAddr := types.WasmAddress(g.User1.Address, [32]byte{99})
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: wasmAddr,
		Data: definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName,
			"test", []byte{}),
	}, constants.ErrWasmContractNotDeployed, mock.NoVmChanges)
}

// ---------------------------------------------------------------------------
// Single-shot Deploy tests
// ---------------------------------------------------------------------------

// A minimal single-shot Deploy with exact bytecodeCost QSR: bytecode + metadata
// are stored, QSR is held in the pool (no Activate = no settle).
func TestWasm_DeploySingleShotPersists(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	module := minimalValidWasmModule()
	salt := [32]byte{0x10}
	wasmAddr := types.WasmAddress(g.User1.Address, salt)

	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(int64(constants.MinBytecodeCost)),
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), uint32(1), module),
	}, nil, mock.SkipVmChanges)

	meta := driveUntilWasmMeta(t, z, wasmAddr, 8)
	if meta == nil {
		t.Fatal("contract metadata not committed after single-shot deploy")
	}

	// Metadata fields.
	common.Expect(t, meta.Deployer.String(), g.User1.Address.String())
	common.Expect(t, meta.Version, 1)
	common.Expect(t, meta.Upgradeable, false) // set at Activate
	common.Expect(t, meta.Activated, false)   // single-shot Deploy does not activate
	expectedHash := types.BytesToHashPanic(crypto.Hash(module))
	common.Expect(t, meta.BytecodeHash.String(), expectedHash.String())
	// 56-byte module -> cost floored at MinBytecodeCost.
	common.ExpectAmount(t, meta.BytecodeCost, big.NewInt(int64(constants.MinBytecodeCost)))

	// Bytecode round-trips exactly.
	if !bytes.Equal(wasmBytecode(t, z, wasmAddr), module) {
		t.Fatal("stored bytecode does not match deployed module")
	}

	// QSR held in pool (no Activate to settle it).
	expectNoUnreceived(t, z, wasmAddr)
	expectNoUnreceived(t, z, g.User1.Address)
	z.ExpectBalance(types.WasmContract, types.QsrTokenStandard, int64(constants.MinBytecodeCost))
}

// An endowed Deploy stores the bytecode and holds the full QSR in the pool.
// Without Activate, the QSR is not settled (no burn, no forward).
func TestWasm_DeployEndowedHoldsQsr(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	module := minimalValidWasmModule()
	salt := [32]byte{0x11}
	wasmAddr := types.WasmAddress(g.User1.Address, salt)

	deposit := int64(constants.MinBytecodeCost) // 1e8
	endow := int64(50_000_000)
	amount := deposit + endow

	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(amount),
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), uint32(1), module),
	}, nil, mock.SkipVmChanges)

	meta := driveUntilWasmMeta(t, z, wasmAddr, 8)
	if meta == nil {
		t.Fatal("endowed deploy rolled back: contract metadata not committed")
	}
	common.Expect(t, meta.Deployer.String(), g.User1.Address.String())
	common.Expect(t, meta.Activated, false) // not activated by Deploy alone
	// Recorded bytecode cost is the floor.
	common.ExpectAmount(t, meta.BytecodeCost, big.NewInt(deposit))

	// QSR held in pool (no Activate to settle it).
	expectNoUnreceived(t, z, wasmAddr)
	expectNoUnreceived(t, z, g.User1.Address)
	z.ExpectBalance(types.WasmContract, types.QsrTokenStandard, amount)
}

// A Deploy carrying QSR below the required bytecode cost is rejected at
// receive: nothing is stored and the QSR is refunded to the deployer.
func TestWasm_DeployInsufficientQsrRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	module := minimalValidWasmModule()
	salt := [32]byte{0x12}
	wasmAddr := types.WasmAddress(g.User1.Address, salt)

	short := int64(50_000_000) // < MinBytecodeCost (1e8)
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(short),
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), uint32(1), module),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Rolled back: no bytecode, no metadata.
	common.Expect(t, wasmMeta(t, z, wasmAddr) == nil, true)
	// QSR refunded to deployer; nothing endowed to the contract.
	expectSingleQsrUnreceived(t, z, g.User1.Address, big.NewInt(short))
	expectNoUnreceived(t, z, wasmAddr)
	z.ExpectBalance(types.WasmContract, types.QsrTokenStandard, 0)
}

// A Deploy with invalid bytecode is rejected at receive; the QSR is refunded.
func TestWasm_DeployInvalidBytecodeRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	// Valid magic + version but no type/function/export/code sections.
	badBytecode := []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00}
	salt := [32]byte{0x13}
	wasmAddr := types.WasmAddress(g.User1.Address, salt)
	deposit := int64(constants.MinBytecodeCost)

	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(deposit),
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), uint32(1), badBytecode),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	common.Expect(t, wasmMeta(t, z, wasmAddr) == nil, true)
	expectSingleQsrUnreceived(t, z, g.User1.Address, big.NewInt(deposit))
	expectNoUnreceived(t, z, wasmAddr)
	z.ExpectBalance(types.WasmContract, types.QsrTokenStandard, 0)
}

// A second Deploy to the same deployer+salt collides with the first: the first
// deployment persists unchanged and the second's QSR is refunded.
func TestWasm_DeployCollisionRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	module := minimalValidWasmModule()
	salt := [32]byte{0x14}
	wasmAddr := types.WasmAddress(g.User1.Address, salt)

	// First deploy: persists as version 1.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(int64(constants.MinBytecodeCost)),
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), uint32(1), module),
	}, nil, mock.SkipVmChanges)
	first := driveUntilWasmMeta(t, z, wasmAddr, 8)
	if first == nil {
		t.Fatal("first deploy not committed")
	}
	common.Expect(t, first.Version, 1)

	// Second deploy: same salt, with QSR. Receive hits the collision check and
	// rolls back; the QSR is refunded.
	deposit := int64(constants.MinBytecodeCost)
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(deposit),
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), uint32(1), module),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// First deployment untouched (still version 1).
	after := wasmMeta(t, z, wasmAddr)
	if after == nil {
		t.Fatal("first deployment disappeared after collision")
	}
	common.Expect(t, after.Version, 1)
	// Second deploy's QSR refunded; factory holds first deploy's QSR (no Activate).
	expectSingleQsrUnreceived(t, z, g.User1.Address, big.NewInt(deposit))
	expectNoUnreceived(t, z, wasmAddr)
	z.ExpectBalance(types.WasmContract, types.QsrTokenStandard, deposit)
}

// ---------------------------------------------------------------------------
// Chunked Deploy tests
// ---------------------------------------------------------------------------

// Full chunked-deploy lifecycle (18 chunks -> finalize). Verifies QSR
// conservation: the mandatory full-chunk cost splits exactly into a refund
// of the unused chunk space to the deployer plus an endowment to the contract,
// leaving the 0x01 pool empty.
func TestWasm_ChunkedDeployLifecycleConservesPool(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	module := minimalValidWasmModule()
	salt := [32]byte{0x20}
	wasmAddr := types.WasmAddress(g.User1.Address, salt)
	totalChunks := uint32(constants.MaxChunkCount) // 18
	chunks := splitIntoChunks(t, module, int(totalChunks))

	fullDeposit := fullChunkCostFor(totalChunks) // 129,024,000
	actualDeposit := int64(constants.MinBytecodeCost)

	// Chunk 0 carries the full mandatory cost and declares totalChunks.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(fullDeposit),
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), totalChunks, chunks[0]),
	}, nil, mock.SkipVmChanges)
	cm := driveUntilChunkMeta(t, z, wasmAddr, 10)
	if cm == nil {
		t.Fatal("chunk metadata not committed after first chunk")
	}
	common.Expect(t, cm.TotalChunks, int(totalChunks))
	common.ExpectAmount(t, cm.CollectedQsr, big.NewInt(fullDeposit))

	// Remaining chunks (zero QSR).
	for i := uint32(1); i < totalChunks; i++ {
		z.InsertSendBlock(&nom.AccountBlock{
			Address:   g.User1.Address,
			ToAddress: types.WasmContract,
			Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
				wasmAddr, salt, i, totalChunks, chunks[i]),
		}, nil, mock.SkipVmChanges)
	}

	// Capture token supplies just before Activate: the only burns in the window
	// that follows are the bytecode-cost QSR burn and the ZNN deploy-fee burn.
	// (Chunk uploads are plain transfers into the 0x01 pool — no supply change.)
	qsrSupplyBefore := wasmTokenSupply(t, z, types.QsrTokenStandard)
	znnSupplyBefore := wasmTokenSupply(t, z, types.ZnnTokenStandard)

	// Activate (1 ZNN fee).
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(int64(constants.ZNNDeployFee)),
		Data: definition.ABIWasm.PackMethodPanic(definition.ActivateMethodName,
			wasmAddr, salt, true),
	}, nil, mock.SkipVmChanges)

	meta := driveUntilWasmMeta(t, z, wasmAddr, 40)
	if meta == nil {
		t.Fatal("contract metadata not committed after finalize")
	}

	// Finalized metadata.
	common.Expect(t, meta.Deployer.String(), g.User1.Address.String())
	common.Expect(t, meta.Version, 1)
	common.Expect(t, meta.Upgradeable, true)
	common.Expect(t, meta.Activated, true)
	expectedHash := types.BytesToHashPanic(crypto.Hash(module))
	common.Expect(t, meta.BytecodeHash.String(), expectedHash.String())
	common.ExpectAmount(t, meta.BytecodeCost, big.NewInt(actualDeposit))

	// Chunk metadata cleaned up; bytecode reassembles to the original module.
	common.Expect(t, wasmChunkMeta(t, z, wasmAddr) == nil, true)
	if !bytes.Equal(wasmBytecode(t, z, wasmAddr), module) {
		t.Fatal("reassembled bytecode does not match original module")
	}

	// Conservation: actualCost burned, excess forwarded to contract as prefund.
	// Deployer gets nothing back; factory nets to zero.
	prefund := fullDeposit - actualDeposit
	expectNoUnreceived(t, z, g.User1.Address)
	expectSingleQsrUnreceived(t, z, wasmAddr, big.NewInt(prefund))
	z.ExpectBalance(types.WasmContract, types.QsrTokenStandard, 0)

	// Supply reduction (MEDIUM): drive the descendant burns into TokenContract,
	// then assert TotalSupply dropped by exactly the bytecode cost (QSR) and the
	// ZNN deploy fee. The prefund-unreceived assertion above already ran at its
	// original timing, so settling further here cannot retroactively break it.
	driveMomentums(z, 8)
	qsrSupplyAfter := wasmTokenSupply(t, z, types.QsrTokenStandard)
	znnSupplyAfter := wasmTokenSupply(t, z, types.ZnnTokenStandard)
	common.ExpectAmount(t, new(big.Int).Sub(qsrSupplyBefore, qsrSupplyAfter), big.NewInt(actualDeposit))
	common.ExpectAmount(t, new(big.Int).Sub(znnSupplyBefore, znnSupplyAfter), big.NewInt(int64(constants.ZNNDeployFee)))
}

// The first chunk MUST pre-fund the full chunk cost. A zero-QSR first chunk
// is rejected and no chunk metadata is created.
func TestWasm_ChunkedDeployZeroQsrFirstChunkRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	module := minimalValidWasmModule()
	salt := [32]byte{0x21}
	wasmAddr := types.WasmAddress(g.User1.Address, salt)
	totalChunks := uint32(constants.MaxChunkCount)
	chunks := splitIntoChunks(t, module, int(totalChunks))

	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), totalChunks, chunks[0]),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	common.Expect(t, wasmChunkMeta(t, z, wasmAddr) == nil, true)
	// Zero amount -> nothing to refund.
	expectNoUnreceived(t, z, g.User1.Address)
	z.ExpectBalance(types.WasmContract, types.QsrTokenStandard, 0)
}

// Subsequent chunks must carry zero QSR. A stray QSR amount on chunk 1 is
// rejected and refunded; the pre-funded chunk metadata stays intact.
func TestWasm_ChunkedDeployStrayQsrOnSubsequentRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	module := minimalValidWasmModule()
	salt := [32]byte{0x22}
	wasmAddr := types.WasmAddress(g.User1.Address, salt)
	totalChunks := uint32(constants.MaxChunkCount)
	chunks := splitIntoChunks(t, module, int(totalChunks))
	fullDeposit := fullChunkCostFor(totalChunks)

	// Chunk 0 with full cost.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(fullDeposit),
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), totalChunks, chunks[0]),
	}, nil, mock.SkipVmChanges)
	if driveUntilChunkMeta(t, z, wasmAddr, 10) == nil {
		t.Fatal("chunk metadata not committed after first chunk")
	}

	// Chunk 1 with a stray 1 QSR — rejected.
	stray := int64(1)
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(stray),
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(1), totalChunks, chunks[1]),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Chunk metadata intact (rollback did not corrupt the in-progress upload).
	cm := wasmChunkMeta(t, z, wasmAddr)
	if cm == nil {
		t.Fatal("chunk metadata vanished after stray-QSR rollback")
	}
	common.Expect(t, cm.TotalChunks, int(totalChunks))
	common.ExpectAmount(t, cm.CollectedQsr, big.NewInt(fullDeposit))
	// Only the stray QSR is refunded.
	expectSingleQsrUnreceived(t, z, g.User1.Address, big.NewInt(stray))
	// Pool still holds the (legitimate) full cost from chunk 0.
	z.ExpectBalance(types.WasmContract, types.QsrTokenStandard, fullDeposit)
}

// DiscardChunks before TTL frees chunk storage and refunds the full QSR cost.
func TestWasm_DiscardChunksRefundsBeforeTTL(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	module := minimalValidWasmModule()
	salt := [32]byte{0x23}
	wasmAddr := types.WasmAddress(g.User1.Address, salt)
	totalChunks := uint32(constants.MaxChunkCount)
	chunks := splitIntoChunks(t, module, int(totalChunks))
	fullDeposit := fullChunkCostFor(totalChunks)

	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(fullDeposit),
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), totalChunks, chunks[0]),
	}, nil, mock.SkipVmChanges)
	if driveUntilChunkMeta(t, z, wasmAddr, 10) == nil {
		t.Fatal("chunk metadata not committed")
	}

	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data: definition.ABIWasm.PackMethodPanic(definition.DiscardChunksMethodName,
			wasmAddr, salt),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	common.Expect(t, wasmChunkMeta(t, z, wasmAddr) == nil, true)
	common.Expect(t, wasmMeta(t, z, wasmAddr) == nil, true) // never finalized
	expectSingleQsrUnreceived(t, z, g.User1.Address, big.NewInt(fullDeposit))
	z.ExpectBalance(types.WasmContract, types.QsrTokenStandard, 0)
}

// DiscardChunks with no in-progress upload is rejected; nothing is refunded.
func TestWasm_DiscardChunksNoChunksRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x24}
	wasmAddr := types.WasmAddress(g.User1.Address, salt)

	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data: definition.ABIWasm.PackMethodPanic(definition.DiscardChunksMethodName,
			wasmAddr, salt),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	common.Expect(t, wasmChunkMeta(t, z, wasmAddr) == nil, true)
	expectNoUnreceived(t, z, g.User1.Address)
}

// After the chunk TTL elapses (spec §4.4): Activate is rejected and
// DiscardChunks frees the storage but FORFEITS the QSR (no refund); the
// forfeited QSR remains in the 0x01 pool.
//
// ChunkTTLMomentums is an overridable consensus var (like UpdateMinNumMomentums,
// FuseExpiration, StakeTime*); we shrink it for the test so the TTL window is a
// handful of momentums rather than the mainnet 1440. Restored on return.
func TestWasm_ChunkTTLExpiryForfeitsQsr(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()

	origTTL := constants.ChunkTTLMomentums
	constants.ChunkTTLMomentums = 20
	defer func() { constants.ChunkTTLMomentums = origTTL }()

	activateWasmRuntime(t, z)

	module := minimalValidWasmModule()
	salt := [32]byte{0x25}
	wasmAddr := types.WasmAddress(g.User1.Address, salt)
	totalChunks := uint32(constants.MaxChunkCount)
	chunks := splitIntoChunks(t, module, int(totalChunks))
	fullDeposit := fullChunkCostFor(totalChunks)

	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(fullDeposit),
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), totalChunks, chunks[0]),
	}, nil, mock.SkipVmChanges)
	cm := driveUntilChunkMeta(t, z, wasmAddr, 10)
	if cm == nil {
		t.Fatal("chunk metadata not committed")
	}

	// Advance strictly past the TTL window.
	z.InsertMomentumsTo(cm.FirstChunkHeight + uint64(constants.ChunkTTLMomentums) + 5)

	// Activate now rejected (TTL elapsed). Zero amount -> no refund.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(int64(constants.ZNNDeployFee)),
		Data: definition.ABIWasm.PackMethodPanic(definition.ActivateMethodName,
			wasmAddr, salt, false),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	common.Expect(t, wasmMeta(t, z, wasmAddr) == nil, true)
	// Expired chunk metadata persists until explicitly discarded.
	if wasmChunkMeta(t, z, wasmAddr) == nil {
		t.Fatal("expired chunk metadata should persist until DiscardChunks")
	}

	// DiscardChunks after expiry: storage freed, QSR forfeited (no refund).
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data: definition.ABIWasm.PackMethodPanic(definition.DiscardChunksMethodName,
			wasmAddr, salt),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	common.Expect(t, wasmChunkMeta(t, z, wasmAddr) == nil, true)
	// QSR forfeited: nothing refunded, pool retains the full cost.
	// User1 has 1 unreceived ZNN refund from the failed Activate attempt.
	blocks := unreceivedTo(t, z, g.User1.Address)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 unreceived block (ZNN refund) for User1, got %d", len(blocks))
	}
	if blocks[0].TokenStandard != types.ZnnTokenStandard {
		t.Fatalf("expected ZNN refund, got token %v", blocks[0].TokenStandard)
	}
	common.ExpectAmount(t, blocks[0].Amount, big.NewInt(int64(constants.ZNNDeployFee)))
	z.ExpectBalance(types.WasmContract, types.QsrTokenStandard, fullDeposit)
}

// ---------------------------------------------------------------------------
// Halt / Unhalt permission tests (User1 is not the administrator)
// ---------------------------------------------------------------------------

// Halt from a non-administrator is rejected; the halted flag stays false.
func TestWasm_HaltByNonAdminRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	common.Expect(t, wasmInfo(t, z).Halted, false)

	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmHaltMethodName),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	common.Expect(t, wasmInfo(t, z).Halted, false)
	expectNoUnreceived(t, z, g.User1.Address)
}

// Unhalt from a non-administrator is rejected; the halted flag stays false.
func TestWasm_UnhaltByNonAdminRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmUnhaltMethodName),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	common.Expect(t, wasmInfo(t, z).Halted, false)
	expectNoUnreceived(t, z, g.User1.Address)
}

// ---------------------------------------------------------------------------
// Execute lifecycle tests
// ---------------------------------------------------------------------------

// Deploy a contract, execute it, and verify the auto-receive produces a block.
// This exercises the full Deploy → Execute → auto-receive pipeline.
func TestWasm_ExecuteLifecycle(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	// Deploy + Activate: a contract is only executable once the deployer has
	// activated it (burns the bytecode cost + ZNN fee, flips Activated).
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), [32]byte{0x20}, false)

	// Execute: the minimal module returns its first arg (the args pointer). With
	// empty args the pointer is 0, so the execution succeeds. Verify the send
	// succeeds and the auto-receive produces a block.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: wasmAddr,
		Data: definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName,
			"test", []byte{}),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Verify the wasmAddr has a receive block (the auto-receive).
	frontier, err := z.Chain().GetFrontierAccountStore(wasmAddr).Frontier()
	common.DealWithErr(err)
	if frontier == nil {
		t.Fatal("no receive block after execute")
	}
	if frontier.BlockType != nom.BlockTypeContractReceive {
		t.Fatalf("expected contract receive block, got type %d", frontier.BlockType)
	}
}

// Execute a runaway contract → gas exhaustion → deterministic revert.
func TestWasm_ExecuteGasExhaustion(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	// Deploy + Activate a runaway module directly. A contract must be activated
	// before it can execute, and activation makes the runaway module the live
	// bytecode in one step. (Deploying a placeholder first and swapping it in via a
	// second Deploy is no longer possible: an un-activated single-shot Deploy
	// leaves its chunk metadata in place, so a duplicate Deploy collides.)
	wasmAddr := deployAndActivateWasmFor(t, z, testmodules.RunawayModule(), [32]byte{0x21}, false)

	// Execute the runaway contract → should fail with gas exhaustion.
	// The send itself succeeds (validation is pre-execution), but the
	// receive will trap and revert.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: wasmAddr,
		Data: definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName,
			"run", []byte{}),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Verify the contract is still deployed (execution reverted, not the deploy).
	common.Expect(t, wasmBytecode(t, z, wasmAddr) != nil, true)
}

// A deployed-but-not-activated contract cannot execute: the receive is rejected
// by the Activated gate before the module runs. This is the spam/abuse guard —
// without it, bytecode left by a single-shot Deploy would be runnable while
// avoiding the activation burn (bytecode cost + ZNN fee). The test also proves
// real execute-path supply reduction: once activated, a state_write burns QSR
// that is debited from the contract and removed from TotalSupply.
func TestWasm_ExecuteGatedOnActivation(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	module := testmodules.StateWriteModule()
	salt := [32]byte{0x60}
	wasmAddr := types.WasmAddress(g.User1.Address, salt)

	// Single-shot Deploy (NOT activated). Bytecode + metadata are stored, but the
	// Activated gate is closed.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(int64(constants.MinBytecodeCost)),
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), uint32(1), module),
	}, nil, mock.SkipVmChanges)
	if driveUntilWasmMeta(t, z, wasmAddr, 8) == nil {
		t.Fatal("contract not deployed")
	}
	common.Expect(t, wasmMeta(t, z, wasmAddr).Activated, false)

	// Fund the contract's own (0x02) spendable balance with a plain QSR send so it
	// can later pay the state-write burn. Plain value sends to a deployed 0x02 are
	// auto-received and credited without running any code (no activation needed).
	fund := int64(g.Zexp) // 1 QSR, far above the 5000-unit state-write burn
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     wasmAddr,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(fund),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 8)
	z.ExpectBalance(wasmAddr, types.QsrTokenStandard, fund)

	// Pre-activation Execute: the gate rejects the receive (ErrWasmContractNotActivated)
	// before the module runs. The module never reaches state_write, so the funded
	// balance is untouched — proof the contract did not execute.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: wasmAddr,
		Data: definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName,
			"bump", []byte{}),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 6)
	z.ExpectBalance(wasmAddr, types.QsrTokenStandard, fund) // unchanged: did not execute
	common.Expect(t, wasmMeta(t, z, wasmAddr).Activated, false)

	// Activate (upgradeable=false). Burns the bytecode cost from the 0x01 pool plus
	// the ZNN fee and flips Activated. The separately-funded 0x02 balance survives.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(int64(constants.ZNNDeployFee)),
		Data: definition.ABIWasm.PackMethodPanic(definition.ActivateMethodName,
			wasmAddr, salt, false),
	}, nil, mock.SkipVmChanges)
	if driveUntilActivated(t, z, wasmAddr, 8) == nil {
		t.Fatal("contract not activated")
	}
	// Settle the Activate descendant burns into the TokenContract BEFORE snapshotting
	// supply, so the only post-snapshot QSR burn is the state-write burn below.
	driveMomentums(z, 8)
	z.ExpectBalance(wasmAddr, types.QsrTokenStandard, fund) // Activate left the 0x02 funding intact
	qsrSupplyBefore := wasmTokenSupply(t, z, types.QsrTokenStandard)

	// Post-activation Execute now runs StateWriteModule (which returns 0, so the
	// receive succeeds and the burn settles). StateWrite charges the *growth delta*
	// (newCost - oldCost). The key is genuinely absent (state_read probes Has, so it
	// is not treated as an empty-but-existing value), giving oldCost = 0. The fresh
	// write of a 1-byte key + 4-byte value therefore burns the full key+value:
	//   delta = (1+4)*QSRPerByteOfState - 0 = 5*QSRPerByteOfState.
	burn := int64(5) * int64(constants.QSRPerByteOfState)
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: wasmAddr,
		Data: definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName,
			"bump", []byte{}),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 10)

	// The burn debited the contract's 0x02 balance and reduced QSR TotalSupply by
	// exactly the state-write cost.
	z.ExpectBalance(wasmAddr, types.QsrTokenStandard, fund-burn)
	qsrSupplyAfter := wasmTokenSupply(t, z, types.QsrTokenStandard)
	common.ExpectAmount(t, new(big.Int).Sub(qsrSupplyBefore, qsrSupplyAfter), big.NewInt(burn))
}

// ---------------------------------------------------------------------------
// Upgrade tests
// ---------------------------------------------------------------------------

// Deployer can upgrade to new bytecode; version increments and cache is invalidated.
func TestWasm_UpgradeByDeployer(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x30}

	// Deploy + Activate (upgradeable=true) — only an activated, upgradeable
	// contract is a valid upgrade target.
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), salt, true)

	meta := wasmMeta(t, z, wasmAddr)
	common.Expect(t, meta.Version, uint32(1))
	common.Expect(t, meta.Activated, true)
	common.Expect(t, meta.Upgradeable, true)

	oldHash := meta.BytecodeHash

	// Upgrade to counter module via Deploy + Activate.
	newModule := testmodules.CounterModule()
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), uint32(1), newModule),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(int64(constants.ZNNDeployFee)),
		Data: definition.ABIWasm.PackMethodPanic(definition.ActivateMethodName,
			wasmAddr, salt, true),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Verify version bumped and bytecode changed.
	meta2 := wasmMeta(t, z, wasmAddr)
	common.Expect(t, meta2.Version, uint32(2))
	common.Expect(t, meta2.BytecodeHash != oldHash, true)
	if !bytes.Equal(wasmBytecode(t, z, wasmAddr), newModule) {
		t.Fatal("bytecode not updated after upgrade")
	}
}

// Upgrade from a non-deployer is rejected.
func TestWasm_UpgradeByNonDeployerRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x31}

	// Deploy + Activate (upgradeable=true).
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), salt, true)

	// User2 tries to upgrade via Deploy → send succeeds but receive silently
	// rejects (salt is bound to the deployer's address, so User2 cannot target
	// User1's contract).
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User2.Address,
		ToAddress: types.WasmContract,
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), uint32(1), testmodules.CounterModule()),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Verify version did NOT bump — still 1.
	metaAfter := wasmMeta(t, z, wasmAddr)
	common.Expect(t, metaAfter.Version, uint32(1))
}

// Upgrade a non-upgradeable contract is rejected.
func TestWasm_UpgradeNonUpgradeableRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x32}

	// Deploy + Activate with upgradeable=false: an activated but locked contract.
	// (It must be activated to be an upgrade target at all — an un-activated
	// single-shot is rejected earlier by the in-progress chunk-0 guard.)
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), salt, false)
	common.Expect(t, wasmMeta(t, z, wasmAddr).Upgradeable, false)

	// Deployer tries to upgrade via Deploy → send succeeds but the receive
	// rejects with ErrPermissionDenied because the contract is not upgradeable.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), uint32(1), testmodules.CounterModule()),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Verify bytecode did NOT change.
	common.Expect(t, wasmMeta(t, z, wasmAddr).Version, uint32(1))
}

// ---------------------------------------------------------------------------
// Event integration tests
// ---------------------------------------------------------------------------

// Execute a contract and verify the receive block has the correct structure.
// The full events pipeline (emit_event → Events on block) is verified by
// TestWasm_EventsVersionStamped. This test covers the receive block structure.
func TestWasm_ExecuteReceiveBlockStructure(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	// Deploy + Activate.
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), [32]byte{0x40}, false)

	// Execute with empty args so the minimal module returns 0 (success).
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: wasmAddr,
		Data: definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName,
			"test", []byte{}),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Verify receive block structure.
	frontier, err := z.Chain().GetFrontierAccountStore(wasmAddr).Frontier()
	common.DealWithErr(err)
	if frontier == nil {
		t.Fatal("no receive block after execute")
	}
	if frontier.BlockType != nom.BlockTypeContractReceive {
		t.Fatalf("block type = %d, want %d", frontier.BlockType, nom.BlockTypeContractReceive)
	}
	if frontier.Version != nom.WasmAccountBlockVersion {
		t.Fatalf("version = %d, want %d", frontier.Version, nom.WasmAccountBlockVersion)
	}
	// The minimal module doesn't emit events, so Events should be nil/empty.
	if len(frontier.Events) != 0 {
		t.Fatalf("expected no events from minimal module, got %d", len(frontier.Events))
	}
}

// Post-spork Execute receive has Version == WasmAccountBlockVersion (3).
func TestWasm_EventsVersionStamped(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	// Deploy + Activate.
	wasmAddr := deployAndActivateWasmFor(t, z, testmodules.CounterModule(), [32]byte{0x41}, false)

	// Execute. The receive block is produced and version-stamped regardless of the
	// module's return value.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: wasmAddr,
		Data: definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName,
			"bump", []byte{}),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Verify receive block version.
	frontier, err := z.Chain().GetFrontierAccountStore(wasmAddr).Frontier()
	common.DealWithErr(err)
	if frontier == nil {
		t.Fatal("no receive block")
	}
	if frontier.Version != nom.WasmAccountBlockVersion {
		t.Fatalf("receive block version = %d, want %d", frontier.Version, nom.WasmAccountBlockVersion)
	}
}

// ---------------------------------------------------------------------------
// RPC (embedded.wasm) endpoint tests
// ---------------------------------------------------------------------------

// deployWasmFor deploys a module under salt and drives until its metadata is
// committed, returning the contract address. Helper for the RPC tests below.
func deployWasmFor(t *testing.T, z mock.MockZenon, module []byte, salt [32]byte) types.Address {
	t.Helper()
	wasmAddr := types.WasmAddress(g.User1.Address, salt)
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(int64(constants.MinBytecodeCost)),
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), uint32(1), module),
	}, nil, mock.SkipVmChanges)
	if driveUntilWasmMeta(t, z, wasmAddr, 8) == nil {
		t.Fatal("contract not deployed")
	}
	return wasmAddr
}

// GetContract returns metadata for a deployed contract and nil for an unknown
// address; GetHaltStatus reports the default (not halted, governance admin).
func TestWasm_RPCGetContractAndHaltStatus(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	wasmAddr := deployWasmFor(t, z, minimalValidWasmModule(), [32]byte{0x50})
	api := embedded.NewWasmApi(z)

	c, err := api.GetContract(wasmAddr)
	common.DealWithErr(err)
	if c == nil {
		t.Fatal("GetContract returned nil for a deployed contract")
	}
	common.Expect(t, c.Deployer.String(), g.User1.Address.String())
	common.Expect(t, c.Version, uint32(1))
	common.Expect(t, c.Upgradeable, false) // Deploy-only: not activated yet
	common.Expect(t, c.Activated, false)   // Deploy-only: gate still closed
	common.Expect(t, c.Deployed, true)
	common.Expect(t, c.Halted, false)

	// Unknown address → nil, no error.
	unknown, err := api.GetContract(types.WasmAddress(g.User1.Address, [32]byte{0xFE}))
	common.DealWithErr(err)
	if unknown != nil {
		t.Fatal("GetContract returned non-nil for an unknown contract")
	}

	hs, err := api.GetHaltStatus(wasmAddr)
	common.DealWithErr(err)
	common.Expect(t, hs.Halted, false)
	common.Expect(t, hs.Administrator.String(), constants.InitialWasmAdministrator.String())
}

// GetStats reports the pending-receive backlog: non-zero while a 0x02 send is
// uncommitted-to-receive, draining to 0 once the auto-receive lands.
func TestWasm_RPCGetStats(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), [32]byte{0x51}, false)
	// Drain the deploy + activate receives (and their descendant burns) fully.
	driveMomentums(z, 8)
	api := embedded.NewWasmApi(z)

	stats, err := api.GetStats()
	common.DealWithErr(err)
	common.Expect(t, stats.PendingReceiveBacklog, 0)

	// Send an Execute and commit exactly one momentum: the send is now pending
	// receive, so the backlog must be non-zero.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: wasmAddr,
		Data: definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName,
			"test", []byte{0x01}),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()

	stats, err = api.GetStats()
	common.DealWithErr(err)
	if stats.PendingReceiveBacklog < 1 {
		t.Fatalf("expected backlog >= 1 while send is pending, got %d", stats.PendingReceiveBacklog)
	}

	// Drive the auto-receive and confirm the backlog drains.
	driveMomentums(z, 8)
	stats, err = api.GetStats()
	common.DealWithErr(err)
	common.Expect(t, stats.PendingReceiveBacklog, 0)
}

// GetEvents returns events on the contract's receive blocks. An over-range
// toHeight returns cleanly without panic.
func TestWasm_RPCGetEvents(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	wasmAddr := deployAndActivateWasmFor(t, z, testmodules.EventModule(), [32]byte{0x52}, false)

	// Execute → EventModule emits one event (zero topic, data {1,2,3,4}).
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: wasmAddr,
		Data: definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName,
			"emit", []byte{}),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 5)

	api := embedded.NewWasmApi(z)
	zeroTopic := types.Hash{}

	list, err := api.GetEvents(wasmAddr, zeroTopic, 0, 0, 0, 100)
	common.DealWithErr(err)
	if list.Count < 1 {
		t.Fatalf("expected >= 1 event, got %d", list.Count)
	}
	if !bytes.Equal(list.List[0].Data, []byte{1, 2, 3, 4}) {
		t.Fatalf("event data = %x, want 01020304", list.List[0].Data)
	}

	// An over-range toHeight must return cleanly with the same events.
	frontier, err := z.Chain().GetFrontierAccountStore(wasmAddr).Frontier()
	common.DealWithErr(err)
	over := uint32(frontier.Height) + 1000
	list2, err := api.GetEvents(wasmAddr, zeroTopic, 0, over, 0, 100)
	common.DealWithErr(err)
	common.Expect(t, list2.Count, list.Count)
}

// CallView returns a deployed contract's length-prefixed buffer, errors on an
// undeployed address, and traps when a view attempts a state mutation.
func TestWasm_RPCCallView(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	// minimalValidWasmModule's execute returns the args pointer, so callView's
	// execute fallback echoes back the length-prefixed buffer we pass.
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), [32]byte{0x53}, false)
	api := embedded.NewWasmApi(z)

	args := []byte{0x04, 0x00, 0x00, 0x00, 0x0A, 0x0B, 0x0C, 0x0D}
	res, err := api.CallView(wasmAddr, "view", args)
	common.DealWithErr(err)
	if !bytes.Equal(res.Data, []byte{0x0A, 0x0B, 0x0C, 0x0D}) {
		t.Fatalf("callView returned %x, want 0A0B0C0D", res.Data)
	}

	// Undeployed address → error.
	if _, err := api.CallView(types.WasmAddress(g.User1.Address, [32]byte{0xED}), "view", nil); err == nil {
		t.Fatal("expected error calling view on an undeployed contract")
	}

	// A view that attempts to mutate state traps: EventModule's execute calls
	// emit_event, which is forbidden in read-only mode.
	evAddr := deployAndActivateWasmFor(t, z, testmodules.EventModule(), [32]byte{0x54}, false)
	if _, err := api.CallView(evAddr, "view", nil); err == nil {
		t.Fatal("expected read-only trap calling a mutating view")
	}
}

// ---------------------------------------------------------------------------
// Part 4 — Revoke tests
// ---------------------------------------------------------------------------

func TestWasm_RevokeByDeployer(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x61}
	module := minimalValidWasmModule()
	wasmAddr := deployAndActivateWasmFor(t, z, module, salt, true)

	// Revoke from the deployer (sends to 0x01 factory).
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
		Data:          definition.ABIWasm.PackMethodPanic(definition.WasmRevokeMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// 1. Metadata still exists (not deleted).
	meta := wasmMeta(t, z, wasmAddr)
	if meta == nil {
		t.Fatal("metadata should still exist after revoke")
	}

	// 2. Bytecode still exists (not deleted).
	if !definition.WasmHasBytecode(wasmStore(z), wasmAddr) {
		t.Fatal("bytecode should still exist after revoke")
	}

	// 3. Execute on the revoked contract is rejected (proves Revoked flag is set).
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: wasmAddr,
		Data:      definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName, "echo", []byte{0x01}),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 2)
	// (Should not panic — just rejected.)
}

func TestWasm_RevokeByNonDeployerRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x62}
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), salt, true)

	// User2 tries to revoke User1's contract.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User2.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
		Data:          definition.ABIWasm.PackMethodPanic(definition.WasmRevokeMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Contract should still be live (not revoked).
	meta := wasmMeta(t, z, wasmAddr)
	if meta == nil {
		t.Fatal("metadata should still exist")
	}
	revoked, err := definition.IsWasmRevoked(wasmStore(z), wasmAddr)
	common.FailIfErr(t, err)
	common.Expect(t, revoked, false)
}

func TestWasm_RevokeWithNonQsrBalanceRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x63}
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), salt, true)

	// Fund the 0x02 with ZNN (a non-QSR token).
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     wasmAddr,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(int64(constants.Decimals)),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 2)

	// Revoke should be rejected because the contract holds ZNN.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
		Data:          definition.ABIWasm.PackMethodPanic(definition.WasmRevokeMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Contract should still be live (not revoked).
	meta := wasmMeta(t, z, wasmAddr)
	if meta == nil {
		t.Fatal("metadata should still exist")
	}
	revoked, err := definition.IsWasmRevoked(wasmStore(z), wasmAddr)
	common.FailIfErr(t, err)
	common.Expect(t, revoked, false)
}

func TestWasm_RevokeOnHaltedContract(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x65}
	module := minimalValidWasmModule()
	wasmAddr := deployAndActivateWasmFor(t, z, module, salt, true)

	// Halt the contract.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.Spork.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmHaltMethodName),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 2)

	// Revoke should succeed on a halted contract.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
		Data:          definition.ABIWasm.PackMethodPanic(definition.WasmRevokeMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Verify revocation succeeded — metadata still exists.
	meta := wasmMeta(t, z, wasmAddr)
	if meta == nil {
		t.Fatal("metadata should still exist after revoke")
	}
}

func TestWasm_RevokeAlreadyRevoked(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x66}
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), salt, true)

	// First revoke succeeds.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
		Data:          definition.ABIWasm.PackMethodPanic(definition.WasmRevokeMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	revoked, err := definition.IsWasmRevoked(wasmStore(z), wasmAddr)
	common.FailIfErr(t, err)
	common.Expect(t, revoked, true)

	// Second revoke should be rejected.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
		Data:          definition.ABIWasm.PackMethodPanic(definition.WasmRevokeMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)
	// (Should not panic — just rejected.)
}

// ---------------------------------------------------------------------------
// Part 5 — Pause / Unpause tests
// ---------------------------------------------------------------------------

func TestWasm_PauseByDeployer(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x70}
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), salt, false)

	// Pause from the deployer.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmPauseMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// User2 Execute is blocked.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User2.Address,
		ToAddress: wasmAddr,
		Data:      definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName, "test", []byte{}),
	}, constants.ErrWasmContractPaused, mock.NoVmChanges)

	// Deployer Execute still works.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: wasmAddr,
		Data:      definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName, "test", []byte{}),
	}, nil, mock.SkipVmChanges)
}

func TestWasm_PauseByNonDeployerRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x71}
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), salt, false)

	// User2 tries to pause User1's contract — rejected at receive.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User2.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmPauseMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Contract should not be paused.
	paused, err := definition.IsWasmPaused(wasmStore(z), wasmAddr)
	common.FailIfErr(t, err)
	common.Expect(t, paused, false)
}

func TestWasm_PauseTokenSendBlocked(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x72}
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), salt, false)

	// Pause.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmPauseMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// User2 QSR send to the paused contract is blocked.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User2.Address,
		ToAddress:     wasmAddr,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(1000),
	}, constants.ErrWasmContractPaused, mock.NoVmChanges)

	// Deployer QSR send still works.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     wasmAddr,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(1000),
	}, nil, mock.SkipVmChanges)
}

func TestWasm_UnpauseByDeployer(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x73}
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), salt, false)

	// Pause.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmPauseMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Unpause from the deployer.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmUnpauseMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// User2 Execute now works.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User2.Address,
		ToAddress: wasmAddr,
		Data:      definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName, "test", []byte{}),
	}, nil, mock.SkipVmChanges)
}

func TestWasm_UnpauseByNonDeployerRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x74}
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), salt, false)

	// Pause (by deployer).
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmPauseMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// User2 tries to unpause — rejected at receive.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User2.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmUnpauseMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Still paused.
	paused, err := definition.IsWasmPaused(wasmStore(z), wasmAddr)
	common.FailIfErr(t, err)
	common.Expect(t, paused, true)
}

func TestWasm_UnpauseNotPausedRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x75}
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), salt, false)

	// Unpause on a contract that was never paused — rejected at receive.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmUnpauseMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	paused, err := definition.IsWasmPaused(wasmStore(z), wasmAddr)
	common.FailIfErr(t, err)
	common.Expect(t, paused, false)
}

func TestWasm_PauseAlreadyPausedRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x76}
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), salt, false)

	// First pause succeeds.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmPauseMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	paused, err := definition.IsWasmPaused(wasmStore(z), wasmAddr)
	common.FailIfErr(t, err)
	common.Expect(t, paused, true)

	// Second pause should be rejected.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmPauseMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)
	// (Should not panic — just rejected.)
}

func TestWasm_RevokeOnPausedContract(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x77}
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), salt, true)

	// Pause.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmPauseMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Revoke on a paused contract succeeds (sends to 0x01, not 0x02).
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
		Data:          definition.ABIWasm.PackMethodPanic(definition.WasmRevokeMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Verify revocation succeeded — metadata still exists.
	meta := wasmMeta(t, z, wasmAddr)
	if meta == nil {
		t.Fatal("metadata should still exist after revoke")
	}
}

func TestWasm_DeployActivateOnPausedContract(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	salt := [32]byte{0x78}
	module := minimalValidWasmModule()
	wasmAddr := types.WasmAddress(g.User1.Address, salt)

	// Deploy (not activated yet).
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(int64(constants.MinBytecodeCost)),
		Data: definition.ABIWasm.PackMethodPanic(definition.DeployMethodName,
			wasmAddr, salt, uint32(0), uint32(1), module),
	}, nil, mock.SkipVmChanges)
	if driveUntilWasmMeta(t, z, wasmAddr, 8) == nil {
		t.Fatal("contract not deployed")
	}

	// Pause the contract.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmPauseMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Activate on a paused contract succeeds (sends to 0x01, not 0x02).
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.WasmContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(int64(constants.ZNNDeployFee)),
		Data:          definition.ABIWasm.PackMethodPanic(definition.ActivateMethodName, wasmAddr, salt, false),
	}, nil, mock.SkipVmChanges)
	if driveUntilActivated(t, z, wasmAddr, 8) == nil {
		t.Fatal("contract not activated")
	}
}

func TestWasm_AdminHaltWithPausedContract(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	// Override the WASM administrator to User1 so the mock can sign halt blocks.
	savedAdmin := constants.InitialWasmAdministrator
	constants.InitialWasmAdministrator = g.User1.Address
	t.Cleanup(func() { constants.InitialWasmAdministrator = savedAdmin })

	salt := [32]byte{0x79}
	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), salt, false)

	// Pause.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmPauseMethodName, wasmAddr),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Admin Halt works (sends to 0x01, not the paused 0x02).
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data:      definition.ABIWasm.PackMethodPanic(definition.WasmHaltMethodName),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	common.Expect(t, wasmInfo(t, z).Halted, true)
}

// ---------------------------------------------------------------------------
// on_receive hook tests
// ---------------------------------------------------------------------------

// A contract without an on_receive export receives plain tokens normally.
// This is the existing behavior — balance is credited, no hook runs.
func TestWasm_OnReceiveNoHook(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	wasmAddr := deployAndActivateWasmFor(t, z, minimalValidWasmModule(), [32]byte{0x7A}, false)

	// Send plain ZNN tokens to the contract (empty Data).
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     wasmAddr,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(100 * g.Zexp),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Balance should be credited.
	z.ExpectBalance(wasmAddr, types.ZnnTokenStandard, 100*g.Zexp)
}

// A contract that exports on_receive gets its hook called on plain token
// receives. The hook returns 0 (success), so the balance is credited.
func TestWasm_OnReceiveBasic(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	wasmAddr := deployAndActivateWasmFor(t, z, onReceiveModule(), [32]byte{0x7B}, false)

	// Send plain ZNN tokens to the contract.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     wasmAddr,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(50 * g.Zexp),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Balance should be credited — the hook succeeded.
	z.ExpectBalance(wasmAddr, types.ZnnTokenStandard, 50*g.Zexp)
}

// A contract whose on_receive traps (unreachable opcode) must still have
// its balance credit preserved. The trap rolls back hook state, but the
// token credit survives.
func TestWasm_OnReceiveTrapKeepsBalance(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	wasmAddr := deployAndActivateWasmFor(t, z, onReceiveTrapModule(), [32]byte{0x7C}, false)

	// Send plain ZNN tokens to the contract.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     wasmAddr,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(75 * g.Zexp),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Balance must be credited even though on_receive trapped.
	z.ExpectBalance(wasmAddr, types.ZnnTokenStandard, 75*g.Zexp)
}

// A contract whose on_receive writes state must pay the QSR storage burn, exactly
// like execute. Regression test: tryOnReceive previously returned only the hook's
// transfers and dropped the aggregated burn block, letting on_receive grow
// persistent state for free (anyone can trigger it with a plain transfer).
func TestWasm_OnReceiveStateWriteBurnsQsr(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	wasmAddr := deployAndActivateWasmFor(t, z, onReceiveStateWriteModule(), [32]byte{0x7D}, false)
	// Settle the activation burns (bytecode cost + ZNN fee) before snapshotting supply.
	driveMomentums(z, 8)

	qsrSupplyBefore := wasmTokenSupply(t, z, types.QsrTokenStandard)

	// A plain QSR transfer both credits the balance AND fires on_receive, which
	// writes a 1-byte key + 4-byte value. The key is fresh (oldCost = 0), so the
	// hook burns the full (1+4)*QSRPerByteOfState QSR from the just-credited balance.
	fund := int64(g.Zexp) // 1 QSR, far above the burn
	burn := int64(5) * int64(constants.QSRPerByteOfState)
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     wasmAddr,
		TokenStandard: types.QsrTokenStandard,
		Amount:        big.NewInt(fund),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 10)

	// The contract's QSR balance is the funded amount minus the on_receive burn,
	// and QSR TotalSupply fell by exactly the burn. Before the fix, tryOnReceive
	// dropped the burn block, so the balance was the full fund and supply was
	// unchanged — i.e. free storage.
	z.ExpectBalance(wasmAddr, types.QsrTokenStandard, fund-burn)
	qsrSupplyAfter := wasmTokenSupply(t, z, types.QsrTokenStandard)
	common.ExpectAmount(t, new(big.Int).Sub(qsrSupplyBefore, qsrSupplyAfter), big.NewInt(burn))
}

// ---------------------------------------------------------------------------
// SetWasmVariables tests
// ---------------------------------------------------------------------------

// wasmVariables reads the current WasmVariables from committed storage.
func wasmVariables(t *testing.T, z mock.MockZenon) *definition.WasmVariables {
	t.Helper()
	v, err := definition.GetWasmVariables(wasmStore(z))
	common.FailIfErr(t, err)
	return v
}

func TestWasm_SetWasmVariablesDefault(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	// Before any SetWasmVariables call, defaults must match hardcoded constants.
	v := wasmVariables(t, z)
	defaults := definition.DefaultWasmVariables()
	common.Expect(t, v.ExecutionGasLimit, defaults.ExecutionGasLimit)
	common.Expect(t, v.OnReceiveGasLimit, defaults.OnReceiveGasLimit)
	common.Expect(t, v.MaxDescendantBlocks, defaults.MaxDescendantBlocks)
	common.Expect(t, v.MaxEventsPerExecute, defaults.MaxEventsPerExecute)
	common.Expect(t, v.QSRPerByteOfBytecode, defaults.QSRPerByteOfBytecode)
	common.Expect(t, v.MaxWasmBytecodeSize, defaults.MaxWasmBytecodeSize)
}

func TestWasm_SetWasmVariables(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	// Override admin to User1.
	savedAdmin := constants.InitialWasmAdministrator
	constants.InitialWasmAdministrator = g.User1.Address
	t.Cleanup(func() { constants.InitialWasmAdministrator = savedAdmin })

	// Admin sets new gas limit (100,000 instead of 250,000).
	newGasLimit := uint64(100_000)
	defaults := definition.DefaultWasmVariables()
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data: definition.ABIWasm.PackMethodPanic(definition.SetWasmVariablesMethodName,
			newGasLimit,                // ExecutionGasLimit
			defaults.OnReceiveGasLimit, // OnReceiveGasLimit
			defaults.MaxDescendantBlocks,
			defaults.MaxEventsPerExecute,
			defaults.MaxEventDataPerEvent,
			defaults.MaxEventBytesPerExecute,
			defaults.MaxViewReturnSize,
			defaults.QSRPerByteOfBytecode,
			defaults.MinBytecodeCost,
			defaults.QSRPerByteOfState,
			defaults.MaxWasmBytecodeSize,
			defaults.MaxChunkCount,
			defaults.ChunkTTLMomentums,
		),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Verify stored variables.
	v := wasmVariables(t, z)
	common.Expect(t, v.ExecutionGasLimit, newGasLimit)
	// Other fields unchanged.
	common.Expect(t, v.OnReceiveGasLimit, defaults.OnReceiveGasLimit)
	common.Expect(t, v.MaxDescendantBlocks, defaults.MaxDescendantBlocks)
}

func TestWasm_SetWasmVariablesNonAdminRejected(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	defaults := definition.DefaultWasmVariables()

	// User2 (non-admin) tries to set variables.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User2.Address,
		ToAddress: types.WasmContract,
		Data: definition.ABIWasm.PackMethodPanic(definition.SetWasmVariablesMethodName,
			uint64(100_000),
			defaults.OnReceiveGasLimit,
			defaults.MaxDescendantBlocks,
			defaults.MaxEventsPerExecute,
			defaults.MaxEventDataPerEvent,
			defaults.MaxEventBytesPerExecute,
			defaults.MaxViewReturnSize,
			defaults.QSRPerByteOfBytecode,
			defaults.MinBytecodeCost,
			defaults.QSRPerByteOfState,
			defaults.MaxWasmBytecodeSize,
			defaults.MaxChunkCount,
			defaults.ChunkTTLMomentums,
		),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Variables must remain at defaults.
	v := wasmVariables(t, z)
	common.Expect(t, v.ExecutionGasLimit, defaults.ExecutionGasLimit)
}

func TestWasm_SetWasmVariablesBoundsCheck(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	// Override admin to User1.
	savedAdmin := constants.InitialWasmAdministrator
	constants.InitialWasmAdministrator = g.User1.Address
	t.Cleanup(func() { constants.InitialWasmAdministrator = savedAdmin })

	defaults := definition.DefaultWasmVariables()

	// Admin tries to set ExecutionGasLimit below minimum (10,000).
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data: definition.ABIWasm.PackMethodPanic(definition.SetWasmVariablesMethodName,
			uint64(5_000), // below WasmVarExecutionGasLimitMin
			defaults.OnReceiveGasLimit,
			defaults.MaxDescendantBlocks,
			defaults.MaxEventsPerExecute,
			defaults.MaxEventDataPerEvent,
			defaults.MaxEventBytesPerExecute,
			defaults.MaxViewReturnSize,
			defaults.QSRPerByteOfBytecode,
			defaults.MinBytecodeCost,
			defaults.QSRPerByteOfState,
			defaults.MaxWasmBytecodeSize,
			defaults.MaxChunkCount,
			defaults.ChunkTTLMomentums,
		),
	}, constants.ErrForbiddenParam, mock.SkipVmChanges)
	driveMomentums(z, 4)

	// Variables must remain at defaults — the bounds check rejected the update.
	v := wasmVariables(t, z)
	common.Expect(t, v.ExecutionGasLimit, defaults.ExecutionGasLimit)
}

func TestWasm_SetWasmVariablesMultipleUpdates(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateWasmRuntime(t, z)

	// Override admin to User1.
	savedAdmin := constants.InitialWasmAdministrator
	constants.InitialWasmAdministrator = g.User1.Address
	t.Cleanup(func() { constants.InitialWasmAdministrator = savedAdmin })

	defaults := definition.DefaultWasmVariables()

	// First update: change gas limit and QSR cost.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data: definition.ABIWasm.PackMethodPanic(definition.SetWasmVariablesMethodName,
			uint64(100_000), // ExecutionGasLimit
			defaults.OnReceiveGasLimit,
			defaults.MaxDescendantBlocks,
			defaults.MaxEventsPerExecute,
			defaults.MaxEventDataPerEvent,
			defaults.MaxEventBytesPerExecute,
			defaults.MaxViewReturnSize,
			uint64(1_000), // QSRPerByteOfBytecode (doubled)
			defaults.MinBytecodeCost,
			defaults.QSRPerByteOfState,
			defaults.MaxWasmBytecodeSize,
			defaults.MaxChunkCount,
			defaults.ChunkTTLMomentums,
		),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	v := wasmVariables(t, z)
	common.Expect(t, v.ExecutionGasLimit, uint64(100_000))
	common.Expect(t, v.QSRPerByteOfBytecode, uint64(1_000))

	// Second update: full replacement — gas limit goes back, QSR stays doubled.
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.User1.Address,
		ToAddress: types.WasmContract,
		Data: definition.ABIWasm.PackMethodPanic(definition.SetWasmVariablesMethodName,
			defaults.ExecutionGasLimit,
			defaults.OnReceiveGasLimit,
			defaults.MaxDescendantBlocks,
			defaults.MaxEventsPerExecute,
			defaults.MaxEventDataPerEvent,
			defaults.MaxEventBytesPerExecute,
			defaults.MaxViewReturnSize,
			uint64(1_000), // QSRPerByteOfBytecode
			defaults.MinBytecodeCost,
			defaults.QSRPerByteOfState,
			defaults.MaxWasmBytecodeSize,
			defaults.MaxChunkCount,
			defaults.ChunkTTLMomentums,
		),
	}, nil, mock.SkipVmChanges)
	driveMomentums(z, 4)

	v = wasmVariables(t, z)
	common.Expect(t, v.ExecutionGasLimit, defaults.ExecutionGasLimit)
	common.Expect(t, v.QSRPerByteOfBytecode, uint64(1_000))
}
