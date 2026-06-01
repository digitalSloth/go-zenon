package wasmclient

import (
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
)

// WasmContract is the factory contract address (0x01 prefix).
var WasmContract = types.WasmContract

// WasmAddress derives a deployed contract's address (0x02 prefix).
func WasmAddress(deployer types.Address, salt [32]byte) types.Address {
	return types.WasmAddress(deployer, salt)
}

// --- Packing helpers ---

// PackDeploy packs a Deploy call (5-arg: wasmAddr, salt, chunkIndex, totalChunks, chunkData).
func PackDeploy(wasmAddr types.Address, salt [32]byte, chunkIndex, totalChunks uint32, chunkData []byte) []byte {
	return definition.ABIWasm.PackMethodPanic(definition.DeployMethodName, wasmAddr, salt, chunkIndex, totalChunks, chunkData)
}

// PackActivate packs an Activate call (3-arg: wasmAddr, salt, upgradeable).
func PackActivate(wasmAddr types.Address, salt [32]byte, upgradeable bool) []byte {
	return definition.ABIWasm.PackMethodPanic(definition.ActivateMethodName, wasmAddr, salt, upgradeable)
}

func PackDiscardChunks(wasmAddr types.Address, salt [32]byte) []byte {
	return definition.ABIWasm.PackMethodPanic(definition.DiscardChunksMethodName, wasmAddr, salt)
}

func PackExecute(function string, args []byte) []byte {
	return definition.ABIWasm.PackMethodPanic(definition.ExecuteMethodName, function, args)
}

func PackHalt() []byte {
	return definition.ABIWasm.PackMethodPanic(definition.WasmHaltMethodName)
}

func PackUnhalt() []byte {
	return definition.ABIWasm.PackMethodPanic(definition.WasmUnhaltMethodName)
}

func PackPause(wasmAddr types.Address) []byte {
	return definition.ABIWasm.PackMethodPanic(definition.WasmPauseMethodName, wasmAddr)
}

func PackUnpause(wasmAddr types.Address) []byte {
	return definition.ABIWasm.PackMethodPanic(definition.WasmUnpauseMethodName, wasmAddr)
}

func PackChangeAdministrator(newAdmin types.Address) []byte {
	return definition.ABIWasm.PackMethodPanic(definition.WasmChangeAdministratorMethodName, newAdmin)
}

func PackFuse(beneficiary types.Address) []byte {
	return definition.ABIPlasma.PackMethodPanic("Fuse", beneficiary)
}

// ChunkBytecode splits bytecode into chunks of at most maxChunkSize bytes.
func ChunkBytecode(bytecode []byte, maxChunkSize int) [][]byte {
	if len(bytecode) <= maxChunkSize {
		return [][]byte{bytecode}
	}
	var chunks [][]byte
	for i := 0; i < len(bytecode); i += maxChunkSize {
		end := i + maxChunkSize
		if end > len(bytecode) {
			end = len(bytecode)
		}
		chunks = append(chunks, bytecode[i:end])
	}
	return chunks
}

// MaxDeployBytecodeSize is the max single-shot deploy bytecode size.
var MaxDeployBytecodeSize = constants.MaxWasmBytecodeSize

// MaxChunkDataSize is the per-chunk data limit.
const MaxChunkDataSize = 15800 // MaxArgsBytes
