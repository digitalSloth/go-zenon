package constants

import (
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
}

func TestIsValueCapWindowInBounds(t *testing.T) {
	if !IsValueCapWindowInBounds(ValueCapWindowFloor) {
		t.Fatal("expected floor to be in bounds")
	}
	if !IsValueCapWindowInBounds(ValueCapWindowFloor + 1) {
		t.Fatal("expected above floor to be in bounds")
	}
	if IsValueCapWindowInBounds(ValueCapWindowFloor - 1) {
		t.Fatal("expected below floor to be rejected")
	}
}
