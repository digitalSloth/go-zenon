package constants

import "math/big"

// Fixed Settlement protocol constants (sizes in bytes unless noted).
const (
	MaxOnChainCommitmentSize = 1024 * 15 // 15 KiB
	MaxCallPayload           = 1024 * 8  // 8 KiB
	MaxReturnData            = 1024 * 4  // 4 KiB
	MaxExecutionResult       = 1024 * 32 // 32 KiB
	MaxBatchInputs           = 64
	MaxDAChunkSize           = 1024 * 16  // 16 KiB (off-chain DA only)
	MaxDeployChunkData       = 1024 * 15  // 15 KiB
	MaxCodeSize              = 1024 * 256 // 256 KiB (pre-instrumentation)
	MaxChunkCount            = 18
	DeploymentExpiryWindow   = 2880       // momentum heights
	MaxMemory                = 64 * 65536 // 4 MiB (64 WASM pages)
	MaxStackDepth            = 512
	MaxInstrumentedCodeSize  = 1024 * 1024 // 1 MiB post-instrumentation ceiling
	MaxStateKeySize          = 32
	MaxStateValueSize        = 1024 * 16 // 16 KiB
	MaxEventsPerExecution    = 64
	MaxEventDataSize         = 1024 // 1 KiB
	MaxTopicsPerEvent        = 4
	MaxOutboxPerExecution    = 32
	MaxOutboxPayloadSize     = 1024 * 8 // 8 KiB
	MaxAssetsPerBatch        = 16
	MaxWithdrawalsPerUpdate  = 16
	FeeBurnShare             = 30 // percent of each settled fee burned
)

// Hard-bound holders for pre-mainnet parameters. Concrete ceilings are
// placeholders finalized by a later parameterization ticket; floors and the
// existence/enforcement of the bounds themselves are normative.
var (
	// Withdrawal-delay / challenge-window bounds (momentum heights).
	// Floor per MomentumsPerEpoch; ceiling is a pre-mainnet placeholder (~14 days).
	WithdrawalDelayMin = uint64(MomentumsPerEpoch)
	// pre-mainnet; finalize before mainnet
	WithdrawalDelayMax = uint64(MomentumsPerEpoch * 14)

	// MaxBatchWithdrawal floor/ceiling per (domain, asset).
	// Non-zero floor; ceiling is a pre-mainnet placeholder that also sizes the bond.
	MaxBatchWithdrawalFloor = big.NewInt(1)
	// pre-mainnet; finalize before mainnet
	MaxBatchWithdrawalCeiling = new(big.Int).Mul(big.NewInt(1_000_000), big.NewInt(Decimals))

	// MaxInputGas is the ceiling on a CallL2 or Deposit plasma_limit, and so on the
	// L2 gas budget one input can authorize. It is sized from the batch rather than
	// the input: an honest executor's worst case is MaxBatchInputs * MaxInputGas.
	//
	// It stays below math.MaxInt64 so every admissible budget fits a signed 64-bit
	// metering ledger, which is what lets the runtime forbid clamping: a budget it
	// cannot represent is one Settlement would not have admitted.
	//
	// pre-mainnet; finalize before mainnet
	MaxInputGas = uint64(1_000_000_000)

	// Reserved (MINTED only) — symmetric to MaxBatchWithdrawal, unused in Phase 1.
	MaxBatchMintFloor = big.NewInt(1)
	// pre-mainnet; finalize before mainnet
	MaxBatchMintCeiling = new(big.Int).Set(MaxBatchWithdrawalCeiling)

	// Developer-fee price bounds, in ZNN base units per byte.
	ZnnPerByteOfBytecodeFloor = big.NewInt(1)
	// pre-mainnet; finalize before mainnet
	ZnnPerByteOfBytecodeCeiling = big.NewInt(Decimals)
	ZnnPerByteOfStateFloor      = big.NewInt(1)
	// pre-mainnet; finalize before mainnet
	ZnnPerByteOfStateCeiling = big.NewInt(Decimals)
)

// IsWithdrawalDelayInBounds reports whether a withdrawal-delay/challenge-window
// duration (momentum heights) is within the enforced hard bounds.
func IsWithdrawalDelayInBounds(heights uint64) bool {
	return heights >= WithdrawalDelayMin && heights <= WithdrawalDelayMax
}

// IsMaxBatchWithdrawalInBounds reports whether a MaxBatchWithdrawal value is
// within the enforced hard bounds.
func IsMaxBatchWithdrawalInBounds(v *big.Int) bool {
	if v == nil {
		return false
	}
	return v.Cmp(MaxBatchWithdrawalFloor) >= 0 && v.Cmp(MaxBatchWithdrawalCeiling) <= 0
}

// IsRuntimeUpgradeDelayValid reports whether a runtime-upgrade (stfSpecHash
// bump) delay satisfies the mandatory floor relative to the domain's active
// withdrawal delay and stays within WithdrawalDelayMax, which keeps the
// time-challenge due-height sum from wrapping uint64.
func IsRuntimeUpgradeDelayValid(upgradeDelay, activeWithdrawalDelay uint64) bool {
	return upgradeDelay >= activeWithdrawalDelay && upgradeDelay <= WithdrawalDelayMax
}

// IsInputGasInBounds reports whether a CallL2 or Deposit plasma_limit is within
// the ceiling on one input's L2 gas budget. A send exceeding it is rejected at
// send validation and never becomes an input.
func IsInputGasInBounds(plasmaLimit uint64) bool {
	return plasmaLimit <= MaxInputGas
}

// IsValueCapWindowValid reports whether a valueCaps rolling-window duration
// (momentum heights) satisfies the mandatory floor relative to the withdrawal
// delay in force.
//
// The floor is the active withdrawal delay rather than a fixed constant, so a
// long withdrawal delay cannot be paired with a short window to spread
// withdrawals across micro-windows and bypass the cap. Symmetric to
// IsRuntimeUpgradeDelayValid, and evaluated against the withdrawal delay in
// force after the configuring call: when both values are set in the same call,
// against the delay being set, not the one it replaces.
func IsValueCapWindowValid(window, activeWithdrawalDelay uint64) bool {
	return window >= activeWithdrawalDelay
}

// IsZnnPerByteOfBytecodeInBounds reports whether a bytecode fee price is
// within the enforced hard bounds.
func IsZnnPerByteOfBytecodeInBounds(v *big.Int) bool {
	if v == nil {
		return false
	}
	return v.Cmp(ZnnPerByteOfBytecodeFloor) >= 0 && v.Cmp(ZnnPerByteOfBytecodeCeiling) <= 0
}

// IsZnnPerByteOfStateInBounds reports whether a state fee price is within
// the enforced hard bounds.
func IsZnnPerByteOfStateInBounds(v *big.Int) bool {
	if v == nil {
		return false
	}
	return v.Cmp(ZnnPerByteOfStateFloor) >= 0 && v.Cmp(ZnnPerByteOfStateCeiling) <= 0
}
