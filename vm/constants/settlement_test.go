package constants

import (
	"math"
	"math/big"
	"testing"
)

func TestIsWithdrawalDelayInBounds(t *testing.T) {
	if !IsWithdrawalDelayInBounds(WithdrawalDelayMin) {
		t.Fatal("expected floor to be in bounds")
	}
	if !IsWithdrawalDelayInBounds(WithdrawalDelayMax) {
		t.Fatal("expected ceiling to be in bounds")
	}
	if IsWithdrawalDelayInBounds(WithdrawalDelayMin - 1) {
		t.Fatal("expected below floor to be rejected")
	}
	if IsWithdrawalDelayInBounds(WithdrawalDelayMax + 1) {
		t.Fatal("expected above ceiling to be rejected")
	}
}

func TestIsMaxBatchWithdrawalInBounds(t *testing.T) {
	if !IsMaxBatchWithdrawalInBounds(MaxBatchWithdrawalFloor) {
		t.Fatal("expected floor to be in bounds")
	}
	if !IsMaxBatchWithdrawalInBounds(MaxBatchWithdrawalCeiling) {
		t.Fatal("expected ceiling to be in bounds")
	}
	belowFloor := new(big.Int).Sub(MaxBatchWithdrawalFloor, big.NewInt(1))
	if IsMaxBatchWithdrawalInBounds(belowFloor) {
		t.Fatal("expected below floor to be rejected")
	}
	aboveCeiling := new(big.Int).Add(MaxBatchWithdrawalCeiling, big.NewInt(1))
	if IsMaxBatchWithdrawalInBounds(aboveCeiling) {
		t.Fatal("expected above ceiling to be rejected")
	}
}

func TestIsRuntimeUpgradeDelayValid(t *testing.T) {
	if !IsRuntimeUpgradeDelayValid(100, 100) {
		t.Fatal("expected equal delay to be valid")
	}
	if !IsRuntimeUpgradeDelayValid(101, 100) {
		t.Fatal("expected greater delay to be valid")
	}
	if IsRuntimeUpgradeDelayValid(99, 100) {
		t.Fatal("expected upgradeDelay < activeWithdrawalDelay to be rejected")
	}
	if !IsRuntimeUpgradeDelayValid(WithdrawalDelayMax, 100) {
		t.Fatal("expected WithdrawalDelayMax to be valid")
	}
	if IsRuntimeUpgradeDelayValid(WithdrawalDelayMax+1, 100) {
		t.Fatal("expected a delay above WithdrawalDelayMax to be rejected")
	}
}

func TestIsValueCapWindowValid(t *testing.T) {
	if !IsValueCapWindowValid(100, 100) {
		t.Fatal("expected window equal to the active withdrawal delay to be valid")
	}
	if !IsValueCapWindowValid(101, 100) {
		t.Fatal("expected window above the active withdrawal delay to be valid")
	}
	if IsValueCapWindowValid(99, 100) {
		t.Fatal("expected window < activeWithdrawalDelay to be rejected")
	}

	// The floor tracks the delay in force: the same window that is valid against
	// a short delay is rejected against a longer one.
	window := WithdrawalDelayMin
	if !IsValueCapWindowValid(window, WithdrawalDelayMin) {
		t.Fatal("expected window to be valid against the minimum delay")
	}
	if IsValueCapWindowValid(window, WithdrawalDelayMin*2) {
		t.Fatal("expected the same window to be rejected against a longer active delay")
	}
}

func TestIsInputGasInBounds(t *testing.T) {
	if !IsInputGasInBounds(0) {
		t.Fatal("expected a zero budget to be in bounds")
	}
	if !IsInputGasInBounds(MaxInputGas) {
		t.Fatal("expected the ceiling itself to be in bounds")
	}
	if IsInputGasInBounds(MaxInputGas + 1) {
		t.Fatal("expected a budget above the ceiling to be rejected")
	}

	// The ceiling keeps every admissible budget inside a signed 64-bit metering
	// ledger, which is what lets the runtime treat a larger budget as impossible
	// rather than clamping it.
	if MaxInputGas >= uint64(math.MaxInt64) {
		t.Fatalf("MaxInputGas = %d, must stay below math.MaxInt64", MaxInputGas)
	}

	// A worst-case batch is MaxBatchInputs inputs each at the ceiling; that product
	// must not wrap.
	if MaxInputGas > math.MaxUint64/uint64(MaxBatchInputs) {
		t.Fatalf("MaxBatchInputs * MaxInputGas overflows uint64")
	}
}
