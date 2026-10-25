//go:build !devnet

package constants_test

import (
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/constants"
)

func TestDefaultBuildHasNoDevnetOverrides(t *testing.T) {
	if types.NetworkAdmin != types.CommunitySporkAddress {
		t.Errorf("NetworkAdmin is %s, want the community spork address %s", types.NetworkAdmin, types.CommunitySporkAddress)
	}
	if want := uint64(2 * constants.MomentumsPerEpoch); constants.MinAdministratorDelay != want {
		t.Errorf("MinAdministratorDelay is %d, want %d", constants.MinAdministratorDelay, want)
	}
	if want := uint64(constants.MomentumsPerEpoch); constants.MinSoftDelay != want {
		t.Errorf("MinSoftDelay is %d, want %d", constants.MinSoftDelay, want)
	}
	if want := uint64(constants.MomentumsPerEpoch); constants.WithdrawalDelayMin != want {
		t.Errorf("WithdrawalDelayMin is %d, want %d", constants.WithdrawalDelayMin, want)
	}
	if want := uint64(constants.MomentumsPerEpoch); constants.FeeEpochMomentums != want {
		t.Errorf("FeeEpochMomentums is %d, want %d", constants.FeeEpochMomentums, want)
	}
	if want := uint64(constants.MomentumsPerHour * 5 / 6); constants.UpdateMinNumMomentums != want {
		t.Errorf("UpdateMinNumMomentums is %d, want %d", constants.UpdateMinNumMomentums, want)
	}
}
