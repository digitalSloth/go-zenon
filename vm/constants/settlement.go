package constants

import "math/big"

// Fixed Settlement protocol constants (sizes in bytes unless noted).
const (
	MaxOnChainCommitmentSize       = 1024 * 15 // 15 KiB
	MaxCallPayload                 = 1024 * 8  // 8 KiB
	MaxExecutionResult             = 1024 * 32 // 32 KiB
	MaxBatchInputs                 = 64
	MaxDeployChunkData             = 1024 * 15  // 15 KiB
	MaxCodeSize                    = 1024 * 256 // 256 KiB (pre-instrumentation)
	MaxChunkCount                  = 18
	DeploymentExpiryWindow         = 2880 // momentum heights
	MaxContractMetadataSize        = 256
	MaxAssetsPerBatch              = 16
	MaxWithdrawalsPerUpdate        = 16
	ReleaseIntervalMomentums       = 1  // minimum momentums between a domain's accepted ReleaseWithdrawals calls
	MaxMerkleProofDepth            = 32 // steps in one MerkleProof, for any batch root
	MaxDeploySweepPerStart         = 2  // expired deploy starts swept per accepted start
	MaxRecordSweepBatchesPerSubmit = 2  // finalized batches whose input records one accepted SubmitBatch deletes
	FeeBurnShare                   = 30 // percent of each settled fee burned
)

// Phase-1 batch commitment version fields, in wire order.
const (
	SettlementProtocolVersion        uint16 = 1
	SettlementRuntimeProfileVersion  uint16 = 1
	SettlementMeteringVersion        uint16 = 1
	SettlementSerializationVersion   uint16 = 1
	SettlementStateDiffVersion       uint16 = 1
	SettlementExecutionResultVersion uint16 = 1
	SettlementReceiptVersion         uint16 = 1
	SettlementOutboxVersion          uint16 = 1
	SettlementDAModeVersion          uint16 = 1
)

// Hard bounds on Settlement's configurable parameters and input budgets.
// The ceilings are placeholders to finalize before mainnet; the floors, and
// the enforcement of every bound, are fixed.
var (
	// Withdrawal-delay bounds (momentum heights).
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

	// Reserved for MINTED custody, symmetric to MaxBatchWithdrawal. Nothing reads
	// it while ESCROW is the only custody mode.
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

// IsWithdrawalDelayInBounds reports whether a withdrawal delay (momentum
// heights) is within the enforced hard bounds.
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

// IsMaxBatchMintInBounds reports whether a MaxBatchMint value is within the
// enforced hard bounds. Reserved for MINTED custody; nothing calls it while
// ESCROW is the only custody mode.
func IsMaxBatchMintInBounds(v *big.Int) bool {
	if v == nil {
		return false
	}
	return v.Cmp(MaxBatchMintFloor) >= 0 && v.Cmp(MaxBatchMintCeiling) <= 0
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
