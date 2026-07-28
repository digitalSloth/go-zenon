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
)

// Hard-bound holders for pre-mainnet parameters. Concrete ceilings are
// placeholders finalized by a later parameterization ticket; floors and the
// existence/enforcement of the bounds themselves are normative.
var (
	// Withdrawal-delay / challenge-window bounds (momentum heights).
	// Floor per MomentumsPerEpoch; ceiling is a pre-mainnet placeholder (~14 days).
	WithdrawalDelayMin = uint64(MomentumsPerEpoch)
	// pre-mainnet; finalize before mainnet
	WithdrawalDelayMax = uint64(MomentumsPerEpoch * 24 * 14)

	// MaxBatchWithdrawal floor/ceiling per (domain, asset).
	// Non-zero floor; ceiling is a pre-mainnet placeholder that also sizes the bond.
	MaxBatchWithdrawalFloor = big.NewInt(1)
	// pre-mainnet; finalize before mainnet
	MaxBatchWithdrawalCeiling = new(big.Int).Mul(big.NewInt(1_000_000), big.NewInt(Decimals))

	// Reserved (MINTED only) — symmetric to MaxBatchWithdrawal, unused in Phase 1.
	MaxBatchMintFloor = big.NewInt(1)
	// pre-mainnet; finalize before mainnet
	MaxBatchMintCeiling = new(big.Int).Set(MaxBatchWithdrawalCeiling)

	// valueCaps rolling-window floor, must be >= WithdrawalDelay.
	ValueCapWindowFloor = WithdrawalDelayMin
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
// withdrawal delay.
func IsRuntimeUpgradeDelayValid(upgradeDelay, activeWithdrawalDelay uint64) bool {
	return upgradeDelay >= activeWithdrawalDelay
}

// IsValueCapWindowInBounds reports whether a valueCaps rolling-window
// duration (momentum heights) is within the enforced hard bound.
func IsValueCapWindowInBounds(window uint64) bool {
	return window >= ValueCapWindowFloor
}
