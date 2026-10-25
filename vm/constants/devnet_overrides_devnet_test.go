//go:build devnet

package constants_test

import (
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/constants"
)

func TestDevnetBuildOverrides(t *testing.T) {
	admin := types.ParseAddressPanic("z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d")
	if types.NetworkAdmin != admin {
		t.Errorf("NetworkAdmin is %s, want %s", types.NetworkAdmin, admin)
	}
	if types.NetworkAdmin == types.CommunitySporkAddress {
		t.Errorf("NetworkAdmin equals the community spork address %s", types.CommunitySporkAddress)
	}
	if constants.MinAdministratorDelay != 12 {
		t.Errorf("MinAdministratorDelay is %d, want 12", constants.MinAdministratorDelay)
	}
	if constants.MinSoftDelay != 6 {
		t.Errorf("MinSoftDelay is %d, want 6", constants.MinSoftDelay)
	}
	if constants.WithdrawalDelayMin != 30 {
		t.Errorf("WithdrawalDelayMin is %d, want 30", constants.WithdrawalDelayMin)
	}
	if constants.FeeEpochMomentums != 60 {
		t.Errorf("FeeEpochMomentums is %d, want 60", constants.FeeEpochMomentums)
	}
	if constants.UpdateMinNumMomentums != 30 {
		t.Errorf("UpdateMinNumMomentums is %d, want 30", constants.UpdateMinNumMomentums)
	}
}
