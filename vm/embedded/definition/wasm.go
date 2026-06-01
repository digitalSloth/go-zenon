package definition

import (
	"encoding/binary"
	"math/big"
	"strings"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/abi"
	"github.com/zenon-network/go-zenon/vm/constants"
)

const (
	jsonWasm = `
	[
		{"type":"function","name":"Deploy","inputs":[
			{"name":"wasmAddr","type":"address"},
			{"name":"salt","type":"bytes32"},
			{"name":"chunkIndex","type":"uint32"},
			{"name":"totalChunks","type":"uint32"},
			{"name":"chunkData","type":"bytes"}
		]},

		{"type":"function","name":"Activate","inputs":[
			{"name":"wasmAddr","type":"address"},
			{"name":"salt","type":"bytes32"},
			{"name":"upgradeable","type":"bool"}
		]},

		{"type":"function","name":"DiscardChunks","inputs":[
			{"name":"wasmAddr","type":"address"},
			{"name":"salt","type":"bytes32"}
		]},

		{"type":"function","name":"Halt","inputs":[]},

		{"type":"function","name":"Unhalt","inputs":[]},

		{"type":"function","name":"ChangeAdministrator","inputs":[
			{"name":"newAdmin","type":"address"}
		]},

		{"type":"function","name":"Execute","inputs":[
			{"name":"function","type":"string"},
			{"name":"args","type":"bytes"}
		]},

		{"type":"function","name":"Revoke","inputs":[
			{"name":"wasmAddr","type":"address"}
		]},

		{"type":"function","name":"Pause","inputs":[
			{"name":"wasmAddr","type":"address"}
		]},

		{"type":"function","name":"Unpause","inputs":[
			{"name":"wasmAddr","type":"address"}
		]},

		{"type":"function","name":"SetWasmVariables","inputs":[
			{"name":"executionGasLimit","type":"uint64"},
			{"name":"onReceiveGasLimit","type":"uint64"},
			{"name":"maxDescendantBlocks","type":"uint64"},
			{"name":"maxEventsPerExecute","type":"uint64"},
			{"name":"maxEventDataPerEvent","type":"uint64"},
			{"name":"maxEventBytesPerExecute","type":"uint64"},
			{"name":"maxViewReturnSize","type":"uint64"},
			{"name":"qsrPerByteOfBytecode","type":"uint64"},
			{"name":"minBytecodeCost","type":"uint64"},
			{"name":"qsrPerByteOfState","type":"uint64"},
			{"name":"maxWasmBytecodeSize","type":"uint64"},
			{"name":"maxChunkCount","type":"uint64"},
			{"name":"chunkTTLMomentums","type":"uint64"}
		]},

		{"type":"variable","name":"wasmContractInfo","inputs":[
			{"name":"halted","type":"bool"},
			{"name":"administrator","type":"address"}
		]},

		{"type":"variable","name":"wasmContractMetadata","inputs":[
			{"name":"deployer","type":"address"},
			{"name":"version","type":"uint32"},
			{"name":"upgradeable","type":"bool"},
			{"name":"activated","type":"bool"},
			{"name":"bytecodeHash","type":"hash"},
			{"name":"bytecodeCost","type":"uint256"}
		]},

		{"type":"variable","name":"wasmChunkMetadata","inputs":[
			{"name":"totalChunks","type":"uint32"},
			{"name":"firstChunkHeight","type":"uint64"},
			{"name":"collectedQsr","type":"uint256"},
			{"name":"uploader","type":"address"},
			{"name":"isUpgrade","type":"bool"}
		]},

		{"type":"variable","name":"wasmVariables","inputs":[
			{"name":"executionGasLimit","type":"uint64"},
			{"name":"onReceiveGasLimit","type":"uint64"},
			{"name":"maxDescendantBlocks","type":"uint64"},
			{"name":"maxEventsPerExecute","type":"uint64"},
			{"name":"maxEventDataPerEvent","type":"uint64"},
			{"name":"maxEventBytesPerExecute","type":"uint64"},
			{"name":"maxViewReturnSize","type":"uint64"},
			{"name":"qsrPerByteOfBytecode","type":"uint64"},
			{"name":"minBytecodeCost","type":"uint64"},
			{"name":"qsrPerByteOfState","type":"uint64"},
			{"name":"maxWasmBytecodeSize","type":"uint64"},
			{"name":"maxChunkCount","type":"uint64"},
			{"name":"chunkTTLMomentums","type":"uint64"}
		]}
	]`

	// Method names
	DeployMethodName                  = "Deploy"
	ActivateMethodName                = "Activate"
	DiscardChunksMethodName           = "DiscardChunks"
	WasmHaltMethodName                = "Halt"
	WasmUnhaltMethodName              = "Unhalt"
	WasmChangeAdministratorMethodName = "ChangeAdministrator"
	ExecuteMethodName                 = "Execute"
	WasmRevokeMethodName              = "Revoke"
	WasmPauseMethodName               = "Pause"
	WasmUnpauseMethodName             = "Unpause"
	SetWasmVariablesMethodName        = "SetWasmVariables"

	// Variable names
	wasmContractInfoVariableName     = "wasmContractInfo"
	wasmContractMetadataVariableName = "wasmContractMetadata"
	wasmChunkMetadataVariableName    = "wasmChunkMetadata"
	wasmVariablesVariableName        = "wasmVariables"
)

var (
	ABIWasm = abi.JSONToABIContract(strings.NewReader(jsonWasm))

	// DB key prefixes in WasmContract's Storage() namespace (spec §8.2).
	// The halted flag and administrator are stored together as the WasmContractInfo
	// struct at WasmInfoKeyPrefix (§12.5), not as separate keys.
	WasmInfoKeyPrefix = []byte{1}
	// WasmBytecodePrefix is the single source of truth for the bytecode key,
	// shared with the verifier and the VM's has-bytecode check
	// (constants.WasmBytecodeKeyPrefix). They MUST stay identical — a divergence
	// would split send-validation between the producer and the verifier — so this
	// references the constant rather than re-declaring the literal byte.
	WasmBytecodePrefix       = constants.WasmBytecodeKeyPrefix
	WasmMetadataPrefix       = []byte{3}
	WasmChunkPrefix          = []byte{4}
	WasmChunkMetaPrefix      = []byte{5}
	WasmAdminChallengePrefix = []byte{8}  // ChangeAdministrator time-challenge state (§12.5)
	WasmRevokedPrefix        = []byte{9}  // per-contract revoked flag (Revoke)
	WasmPausedPrefix         = []byte{10} // per-contract paused flag; value = deployer address
	WasmVariablesPrefix      = []byte{11} // singleton WasmVariables (governance-tunable params)
)

// WasmContractInfo is the singleton state of the WASM management contract.
type WasmContractInfo struct {
	Halted        bool          `json:"halted"`
	Administrator types.Address `json:"administrator"`
}

func (info *WasmContractInfo) Save(context db.DB) error {
	data, err := ABIWasm.PackVariable(wasmContractInfoVariableName, info.Halted, info.Administrator)
	if err != nil {
		return err
	}
	return context.Put(WasmInfoKeyPrefix, data)
}

func GetWasmContractInfo(context db.DB) (*WasmContractInfo, error) {
	data, err := context.Get(WasmInfoKeyPrefix)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return &WasmContractInfo{
				Halted:        false,
				Administrator: constants.InitialWasmAdministrator,
			}, nil
		}
		return nil, err
	}
	return parseWasmContractInfo(data)
}

func parseWasmContractInfo(data []byte) (*WasmContractInfo, error) {
	if len(data) > 0 {
		info := new(WasmContractInfo)
		if err := ABIWasm.UnpackVariable(info, wasmContractInfoVariableName, data); err != nil {
			return nil, err
		}
		return info, nil
	}
	return &WasmContractInfo{
		Halted:        false,
		Administrator: constants.InitialWasmAdministrator,
	}, nil
}

// WasmVariables holds governance-tunable runtime parameters. Mirrors the
// Dynamic Plasma pattern: stored as a singleton in WasmContract storage,
// read from storage per-call, defaults returned when storage is empty.
type WasmVariables struct {
	ExecutionGasLimit       uint64 `abi:"executionGasLimit"       json:"executionGasLimit"`
	OnReceiveGasLimit       uint64 `abi:"onReceiveGasLimit"       json:"onReceiveGasLimit"`
	MaxDescendantBlocks     uint64 `abi:"maxDescendantBlocks"     json:"maxDescendantBlocks"`
	MaxEventsPerExecute     uint64 `abi:"maxEventsPerExecute"     json:"maxEventsPerExecute"`
	MaxEventDataPerEvent    uint64 `abi:"maxEventDataPerEvent"    json:"maxEventDataPerEvent"`
	MaxEventBytesPerExecute uint64 `abi:"maxEventBytesPerExecute" json:"maxEventBytesPerExecute"`
	MaxViewReturnSize       uint64 `abi:"maxViewReturnSize"       json:"maxViewReturnSize"`
	QSRPerByteOfBytecode    uint64 `abi:"qsrPerByteOfBytecode"    json:"qsrPerByteOfBytecode"`
	MinBytecodeCost         uint64 `abi:"minBytecodeCost"         json:"minBytecodeCost"`
	QSRPerByteOfState       uint64 `abi:"qsrPerByteOfState"       json:"qsrPerByteOfState"`
	MaxWasmBytecodeSize     uint64 `abi:"maxWasmBytecodeSize"     json:"maxWasmBytecodeSize"`
	MaxChunkCount           uint64 `abi:"maxChunkCount"           json:"maxChunkCount"`
	ChunkTTLMomentums       uint64 `abi:"chunkTTLMomentums"       json:"chunkTTLMomentums"`
}

// Minimum bounds for SetWasmVariables — prevents bricking the runtime.
const (
	WasmVarExecutionGasLimitMin       = 10_000
	WasmVarOnReceiveGasLimitMin       = 1_000
	WasmVarMaxDescendantBlocksMin     = 1
	WasmVarMaxEventsPerExecuteMin     = 1
	WasmVarMaxEventDataPerEventMin    = 256
	WasmVarMaxEventBytesPerExecuteMin = 4_096
	WasmVarMaxViewReturnSizeMin       = 1_024
	WasmVarQSRPerByteOfBytecodeMin    = 100
	WasmVarMinBytecodeCostMin         = 1_000_000
	WasmVarQSRPerByteOfStateMin       = 100
	WasmVarMaxWasmBytecodeSizeMin     = 1_024
	WasmVarMaxChunkCountMin           = 1
	WasmVarChunkTTLMomentumsMin       = 100
)

// DefaultWasmVariables returns the hardcoded defaults (matching the constants
// in vm/constants/embedded.go). Used when storage is empty.
func DefaultWasmVariables() *WasmVariables {
	return &WasmVariables{
		ExecutionGasLimit:       uint64(constants.WasmExecutionGasLimit),
		OnReceiveGasLimit:       uint64(constants.WasmOnReceiveGasLimit),
		MaxDescendantBlocks:     uint64(constants.MaxDescendantBlocksPerExecute),
		MaxEventsPerExecute:     uint64(constants.MaxEventsPerExecute),
		MaxEventDataPerEvent:    uint64(constants.MaxEventDataPerEvent),
		MaxEventBytesPerExecute: uint64(constants.MaxEventBytesPerExecute),
		MaxViewReturnSize:       uint64(constants.WasmMaxViewReturnSize),
		QSRPerByteOfBytecode:    uint64(constants.QSRPerByteOfBytecode),
		MinBytecodeCost:         uint64(constants.MinBytecodeCost),
		QSRPerByteOfState:       uint64(constants.QSRPerByteOfState),
		MaxWasmBytecodeSize:     uint64(constants.MaxWasmBytecodeSize),
		MaxChunkCount:           uint64(constants.MaxChunkCount),
		ChunkTTLMomentums:       uint64(constants.ChunkTTLMomentums),
	}
}

func (v *WasmVariables) Save(context db.DB) error {
	data, err := ABIWasm.PackVariable(wasmVariablesVariableName,
		v.ExecutionGasLimit, v.OnReceiveGasLimit, v.MaxDescendantBlocks,
		v.MaxEventsPerExecute, v.MaxEventDataPerEvent, v.MaxEventBytesPerExecute,
		v.MaxViewReturnSize, v.QSRPerByteOfBytecode, v.MinBytecodeCost,
		v.QSRPerByteOfState, v.MaxWasmBytecodeSize, v.MaxChunkCount,
		v.ChunkTTLMomentums)
	if err != nil {
		return err
	}
	return context.Put(WasmVariablesPrefix, data)
}

func GetWasmVariables(context db.DB) (*WasmVariables, error) {
	data, err := context.Get(WasmVariablesPrefix)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return DefaultWasmVariables(), nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return DefaultWasmVariables(), nil
	}
	v := new(WasmVariables)
	if err := ABIWasm.UnpackVariable(v, wasmVariablesVariableName, data); err != nil {
		return nil, err
	}
	return v, nil
}

// WasmContractMetadata stores per-contract deployment metadata.
type WasmContractMetadata struct {
	Deployer     types.Address `json:"deployer"`
	Version      uint32        `json:"version"`
	Upgradeable  bool          `json:"upgradeable"`
	Activated    bool          `json:"activated"`
	BytecodeHash types.Hash    `json:"bytecodeHash"`
	BytecodeCost *big.Int      `json:"bytecodeCost"`
}

func GetWasmContractMetadata(context db.DB, wasmAddr types.Address) (*WasmContractMetadata, error) {
	key := common.JoinBytes(WasmMetadataPrefix, wasmAddr.Bytes())
	data, err := context.Get(key)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	return parseWasmContractMetadata(data)
}

func (m *WasmContractMetadata) Save(context db.DB, wasmAddr types.Address) error {
	data, err := ABIWasm.PackVariable(wasmContractMetadataVariableName,
		m.Deployer, m.Version, m.Upgradeable, m.Activated, m.BytecodeHash, m.BytecodeCost)
	if err != nil {
		return err
	}
	return context.Put(common.JoinBytes(WasmMetadataPrefix, wasmAddr.Bytes()), data)
}

func parseWasmContractMetadata(data []byte) (*WasmContractMetadata, error) {
	if len(data) > 0 {
		m := new(WasmContractMetadata)
		if err := ABIWasm.UnpackVariable(m, wasmContractMetadataVariableName, data); err != nil {
			return nil, err
		}
		return m, nil
	}
	return nil, nil
}

// WasmMetadataKey returns the storage key for a contract's deployment metadata.
func WasmMetadataKey(wasmAddr types.Address) []byte {
	return common.JoinBytes(WasmMetadataPrefix, wasmAddr.Bytes())
}

// DeleteWasmContractMetadata removes a contract's deployment metadata. Used to
// clean up the artifacts of an un-activated single-shot Deploy when it is
// abandoned via DiscardChunks.
func DeleteWasmContractMetadata(context db.DB, wasmAddr types.Address) error {
	return context.Delete(WasmMetadataKey(wasmAddr))
}

// WasmBytecodeKey returns the storage key for a deployed contract's bytecode.
func WasmBytecodeKey(wasmAddr types.Address) []byte {
	return common.JoinBytes(WasmBytecodePrefix, wasmAddr.Bytes())
}

// WasmHasBytecode checks if bytecode exists for the given address.
func WasmHasBytecode(context db.DB, wasmAddr types.Address) bool {
	has, err := context.Has(WasmBytecodeKey(wasmAddr))
	if err != nil {
		return false
	}
	return has
}

// GetWasmBytecode reads deployed bytecode from WasmContract's storage.
func GetWasmBytecode(context db.DB, wasmAddr types.Address) ([]byte, error) {
	data, err := context.Get(WasmBytecodeKey(wasmAddr))
	if err != nil {
		if err == leveldb.ErrNotFound {
			return nil, constants.ErrWasmContractNotDeployed
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, constants.ErrWasmContractNotDeployed
	}
	return data, nil
}

// WasmRevokedKey returns the storage key for a contract's revoked flag.
func WasmRevokedKey(wasmAddr types.Address) []byte {
	return common.JoinBytes(WasmRevokedPrefix, wasmAddr.Bytes())
}

func IsWasmRevoked(context db.DB, wasmAddr types.Address) (bool, error) {
	has, err := context.Has(WasmRevokedKey(wasmAddr))
	if err != nil {
		return false, err
	}
	return has, nil
}

func SetWasmRevoked(context db.DB, wasmAddr types.Address) error {
	return context.Put(WasmRevokedKey(wasmAddr), []byte{1})
}

// WasmPausedKey returns the storage key for a contract's paused flag.
// The value stored is the deployer's address (20 bytes).
func WasmPausedKey(wasmAddr types.Address) []byte {
	return common.JoinBytes(WasmPausedPrefix, wasmAddr.Bytes())
}

func GetWasmPausedDeployer(store db.DB, wasmAddr types.Address) (*types.Address, error) {
	data, err := store.Get(WasmPausedKey(wasmAddr))
	if err != nil {
		if err == leveldb.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	addr, err := types.BytesToAddress(data)
	if err != nil {
		return nil, err
	}
	return &addr, nil
}

func SetWasmPaused(store db.DB, wasmAddr, deployer types.Address) error {
	return store.Put(WasmPausedKey(wasmAddr), deployer.Bytes())
}

func DeleteWasmPaused(store db.DB, wasmAddr types.Address) error {
	return store.Delete(WasmPausedKey(wasmAddr))
}

func IsWasmPaused(store db.DB, wasmAddr types.Address) (bool, error) {
	has, err := store.Has(WasmPausedKey(wasmAddr))
	if err != nil {
		return false, err
	}
	return has, nil
}

// WasmChunkMetadata tracks in-progress chunked deployments.
type WasmChunkMetadata struct {
	TotalChunks      uint32        `json:"totalChunks"`
	FirstChunkHeight uint64        `json:"firstChunkHeight"`
	CollectedQsr     *big.Int      `json:"collectedQsr"`
	Uploader         types.Address `json:"uploader"`
	IsUpgrade        bool          `json:"isUpgrade"`
}

func WasmChunkMetaKey(wasmAddr types.Address) []byte {
	return common.JoinBytes(WasmChunkMetaPrefix, wasmAddr.Bytes())
}

func WasmChunkDataKey(wasmAddr types.Address, chunkIndex uint32) []byte {
	indexBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(indexBytes, chunkIndex)
	return common.JoinBytes(WasmChunkPrefix, wasmAddr.Bytes(), indexBytes)
}

func (m *WasmChunkMetadata) Save(context db.DB, wasmAddr types.Address) error {
	data, err := ABIWasm.PackVariable(wasmChunkMetadataVariableName,
		m.TotalChunks, m.FirstChunkHeight, m.CollectedQsr, m.Uploader, m.IsUpgrade)
	if err != nil {
		return err
	}
	return context.Put(WasmChunkMetaKey(wasmAddr), data)
}

func (m *WasmChunkMetadata) Delete(context db.DB, wasmAddr types.Address) error {
	return context.Delete(WasmChunkMetaKey(wasmAddr))
}

func GetWasmChunkMetadata(context db.DB, wasmAddr types.Address) (*WasmChunkMetadata, error) {
	data, err := context.Get(WasmChunkMetaKey(wasmAddr))
	if err != nil {
		if err == leveldb.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	m := new(WasmChunkMetadata)
	if err := ABIWasm.UnpackVariable(m, wasmChunkMetadataVariableName, data); err != nil {
		return nil, err
	}
	return m, nil
}
