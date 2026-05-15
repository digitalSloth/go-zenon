package tests

import (
	"testing"
	"time"

	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc/api/embedded"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/zenon/mock"
)

// overrideVestedPillarConstants sets short voting and grace periods for testing.
// Returns a restore function to reset the original values.
func overrideVestedPillarConstants() func() {
	origThreshold := constants.VestedPillarVoteAcceptanceThreshold
	origVoting := constants.VestedPillarVotingPeriod
	origGrace := constants.VestedPillarApprovalGracePeriod
	constants.VestedPillarVoteAcceptanceThreshold = 33
	constants.VestedPillarVotingPeriod = 20       // 20 seconds = 2 momentums
	constants.VestedPillarApprovalGracePeriod = 1000 // 1000 seconds = 100 momentums
	return func() {
		constants.VestedPillarVoteAcceptanceThreshold = origThreshold
		constants.VestedPillarVotingPeriod = origVoting
		constants.VestedPillarApprovalGracePeriod = origGrace
	}
}

// updatePillarContract explicitly calls UpdateEmbeddedPillar on the pillar contract
func updatePillarContract(z mock.MockZenon) {
	z.CallContract(&nom.AccountBlock{
		Address:   g.Pillar1.Address,
		ToAddress: types.PillarContract,
		Data:      definition.ABIPillars.PackMethodPanic(definition.UpdateMethodName),
	})
	z.InsertNewMomentum()
	z.InsertNewMomentum()
}

// applyVestedPillar sends an ApplyVestedPillar transaction from the given address.
// Returns the application ID (hash of the send block).
func applyVestedPillar(z mock.MockZenon, address types.Address, title, description, url string) types.Hash {
	block := z.InsertSendBlock(&nom.AccountBlock{
		Address:       address,
		ToAddress:     types.PillarContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        constants.VestedPillarApplicationFee,
		Data: definition.ABIPillars.PackMethodPanic(definition.ApplyVestedPillarMethodName,
			title, description, url,
		),
	}, nil, mock.SkipVmChanges)
	return block.Hash
}

// voteYesByProdAddress votes yes on a vested pillar application
func voteYesByProdAddress(z mock.MockZenon, pillarAddress types.Address, applicationId types.Hash) {
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   pillarAddress,
		ToAddress: types.PillarContract,
		Data: definition.ABIPillars.PackMethodPanic(definition.VoteByProdAddressMethodName,
			applicationId,
			definition.VoteYes,
		),
	}, nil, mock.SkipVmChanges)
}

// voteNoByProdAddress votes no on a vested pillar application
func voteNoByProdAddress(z mock.MockZenon, pillarAddress types.Address, applicationId types.Hash) {
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   pillarAddress,
		ToAddress: types.PillarContract,
		Data: definition.ABIPillars.PackMethodPanic(definition.VoteByProdAddressMethodName,
			applicationId,
			definition.VoteNo,
		),
	}, nil, mock.SkipVmChanges)
}

// registerVested sends a RegisterVested transaction
func registerVested(z mock.MockZenon, address types.Address, applicationId types.Hash, name string, producerAddress, rewardAddress types.Address) {
	z.CallContract(&nom.AccountBlock{
		Address:   address,
		ToAddress: types.PillarContract,
		Data: definition.ABIPillars.PackMethodPanic(definition.RegisterVestedMethodName,
			applicationId,
			name, producerAddress, rewardAddress, uint8(0), uint8(100),
		),
	})
	z.InsertNewMomentum()
	z.InsertNewMomentum()
}

// TestVestedPillar_HappyPath tests the full apply → vote → approve → register flow
func TestVestedPillar_HappyPath(t *testing.T) {
	restore := overrideVestedPillarConstants()
	defer restore()

	z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
	pillarApi := embedded.NewPillarApi(z, true)
	defer z.StopPanic()

	// Record QSR cost before
	qsrCostBefore, err := pillarApi.GetQsrRegistrationCost()
	common.FailIfErr(t, err)

	// Pillar4 applies with 15k ZNN
	z.ExpectBalance(g.Pillar4.Address, types.ZnnTokenStandard, 16000*g.Zexp)
	appId := applyVestedPillar(z, g.Pillar4.Address, "Vested Test", "A test vested pillar application", "test.com")
	z.InsertNewMomentum()

	// ZNN is now held by the contract
	z.ExpectBalance(g.Pillar4.Address, types.ZnnTokenStandard, 1000*g.Zexp)

	// All 3 genesis pillars vote yes
	voteYesByProdAddress(z, g.Pillar1.Address, appId)
	voteYesByProdAddress(z, g.Pillar2.Address, appId)
	voteYesByProdAddress(z, g.Pillar3.Address, appId)
	z.InsertNewMomentum()

	// Advance past the voting period (20 seconds) and UpdateMinNumMomentums (300)
	z.InsertMomentumsTo(400)

	// Explicitly trigger the pillar update to transition application to ApprovedStatus
	updatePillarContract(z)

	// Register the vested pillar
	registerVested(z, g.Pillar4.Address, appId, "vested-pillar", g.Pillar4.Address, g.Pillar4.Address)

	// Verify pillar was registered with VestedPillarType
	pillarInfo, err := pillarApi.GetByName("vested-pillar")
	common.FailIfErr(t, err)
	if pillarInfo == nil {
		t.Fatal("vested pillar not found after registration")
	}
	if pillarInfo.Type != definition.VestedPillarType {
		t.Fatalf("expected pillar type %d but got %d", definition.VestedPillarType, pillarInfo.Type)
	}

	// Verify QSR cost is unchanged
	qsrCostAfter, err := pillarApi.GetQsrRegistrationCost()
	common.FailIfErr(t, err)
	if qsrCostBefore != qsrCostAfter {
		t.Fatalf("QSR cost changed: before=%s after=%s", qsrCostBefore, qsrCostAfter)
	}
}

// TestVestedPillar_RejectionRefund tests that rejected applications get a ZNN refund
func TestVestedPillar_RejectionRefund(t *testing.T) {
	restore := overrideVestedPillarConstants()
	defer restore()

	z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
	defer z.StopPanic()

	// Pillar4 applies
	appId := applyVestedPillar(z, g.Pillar4.Address, "Reject Test", "Will be rejected", "reject.com")
	z.InsertNewMomentum()
	z.ExpectBalance(g.Pillar4.Address, types.ZnnTokenStandard, 1000*g.Zexp)

	// All 3 genesis pillars vote no
	voteNoByProdAddress(z, g.Pillar1.Address, appId)
	voteNoByProdAddress(z, g.Pillar2.Address, appId)
	voteNoByProdAddress(z, g.Pillar3.Address, appId)
	z.InsertNewMomentum()

	// Advance past voting period
	z.InsertMomentumsTo(400)

	// Explicitly trigger the pillar update to transition application to RejectedStatus + emit refund
	updatePillarContract(z)

	// Autoreceive the refund
	autoreceive(t, z, g.Pillar4.Address)

	// Pillar4 should have their 15k ZNN back
	z.ExpectBalance(g.Pillar4.Address, types.ZnnTokenStandard, 16000*g.Zexp)
}

// TestVestedPillar_ExpiryRefund tests that approved applications that expire get a ZNN refund
func TestVestedPillar_ExpiryRefund(t *testing.T) {
	restore := overrideVestedPillarConstants()
	defer restore()

	z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
	defer z.StopPanic()

	// Pillar4 applies
	appId := applyVestedPillar(z, g.Pillar4.Address, "Expiry Test", "Will expire", "expiry.com")
	z.InsertNewMomentum()

	// All 3 vote yes — application will be approved
	voteYesByProdAddress(z, g.Pillar1.Address, appId)
	voteYesByProdAddress(z, g.Pillar2.Address, appId)
	voteYesByProdAddress(z, g.Pillar3.Address, appId)
	z.InsertNewMomentum()

	// Advance past voting period and epoch boundary
	z.InsertMomentumsTo(400)
	// Explicitly trigger the pillar update to transition application to ApprovedStatus
	updatePillarContract(z)

	// Now advance past the approval grace period (1000 seconds = 100 momentums).
	// Approval happens here, grace expires 1000s later.
	// We also need to pass UpdateMinNumMomentums (300) from the last update.
	z.InsertMomentumsTo(750)
	// Explicitly trigger the pillar update to transition application to ExpiredStatus + emit refund
	updatePillarContract(z)

	// Autoreceive the refund
	autoreceive(t, z, g.Pillar4.Address)

	// Pillar4 should have their 15k ZNN back
	z.ExpectBalance(g.Pillar4.Address, types.ZnnTokenStandard, 16000*g.Zexp)
}

// TestVestedPillar_OneOpenPerAddress tests that an address cannot have two open applications
func TestVestedPillar_OneOpenPerAddress(t *testing.T) {
	z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
	defer z.StopPanic()

	// First application — should succeed
	applyVestedPillar(z, g.Pillar4.Address, "First App", "First application", "first.com")
	z.InsertNewMomentum()

	// Transfer ZNN from Pillar5 to Pillar4 so Pillar4 has enough for a second attempt
	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.Pillar5.Address,
		ToAddress:     g.Pillar4.Address,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        constants.VestedPillarApplicationFee,
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	autoreceive(t, z, g.Pillar4.Address)

	// Second application from same address — should fail with ErrVestedAppAlreadyExists
	defer z.CallContract(&nom.AccountBlock{
		Address:       g.Pillar4.Address,
		ToAddress:     types.PillarContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        constants.VestedPillarApplicationFee,
		Data: definition.ABIPillars.PackMethodPanic(definition.ApplyVestedPillarMethodName,
			"Second App", "Second application", "second.com",
		),
	}).Error(t, constants.ErrVestedAppAlreadyExists)
	z.InsertNewMomentum()
}

// TestVestedPillar_QsrCostInvariant tests that GetQsrCostForNextPillar is unaffected by vested registrations
func TestVestedPillar_QsrCostInvariant(t *testing.T) {
	restore := overrideVestedPillarConstants()
	defer restore()

	z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
	pillarApi := embedded.NewPillarApi(z, true)
	defer z.StopPanic()

	costBefore, err := pillarApi.GetQsrRegistrationCost()
	common.FailIfErr(t, err)

	// Apply, vote, approve, register a vested pillar
	appId := applyVestedPillar(z, g.Pillar4.Address, "QSR Test", "QSR cost invariant", "qsr.com")
	z.InsertNewMomentum()
	voteYesByProdAddress(z, g.Pillar1.Address, appId)
	voteYesByProdAddress(z, g.Pillar2.Address, appId)
	voteYesByProdAddress(z, g.Pillar3.Address, appId)
	z.InsertNewMomentum()
	z.InsertMomentumsTo(400)
	updatePillarContract(z)

	registerVested(z, g.Pillar4.Address, appId, "qsr-pillar", g.Pillar4.Address, g.Pillar4.Address)

	costAfter, err := pillarApi.GetQsrRegistrationCost()
	common.FailIfErr(t, err)
	if costBefore != costAfter {
		t.Fatalf("QSR cost changed after vested registration: before=%s after=%s", costBefore, costAfter)
	}
}

// TestVestedPillar_RegisterGuardRails tests error conditions for RegisterVested
func TestVestedPillar_RegisterGuardRails(t *testing.T) {
	t.Run("wrong sender", func(t *testing.T) {
		restore := overrideVestedPillarConstants()
		defer restore()

		z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
		defer z.StopPanic()

		appId := applyVestedPillar(z, g.Pillar4.Address, "Guard Test", "Wrong sender", "guard1.com")
		z.InsertNewMomentum()
		voteYesByProdAddress(z, g.Pillar1.Address, appId)
		voteYesByProdAddress(z, g.Pillar2.Address, appId)
		voteYesByProdAddress(z, g.Pillar3.Address, appId)
		z.InsertNewMomentum()
		z.InsertMomentumsTo(400)
		updatePillarContract(z)

		// Try to register from a different address — should fail
		defer z.CallContract(&nom.AccountBlock{
			Address:   g.Pillar5.Address,
			ToAddress: types.PillarContract,
			Data: definition.ABIPillars.PackMethodPanic(definition.RegisterVestedMethodName,
				appId,
				"guard-pillar", g.Pillar5.Address, g.Pillar5.Address, uint8(0), uint8(100),
			),
		}).Error(t, constants.ErrPermissionDenied)
		z.InsertNewMomentum()
	})

	t.Run("application not approved", func(t *testing.T) {
		z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
		defer z.StopPanic()

		appId := applyVestedPillar(z, g.Pillar4.Address, "NotApproved", "Not approved yet", "guard2.com")
		z.InsertNewMomentum()

		// Try to register before voting period ends (still in VotingStatus)
		defer z.CallContract(&nom.AccountBlock{
			Address:   g.Pillar4.Address,
			ToAddress: types.PillarContract,
			Data: definition.ABIPillars.PackMethodPanic(definition.RegisterVestedMethodName,
				appId,
				"not-approved", g.Pillar4.Address, g.Pillar4.Address, uint8(0), uint8(100),
			),
		}).Error(t, constants.ErrVestedAppNotApproved)
		z.InsertNewMomentum()
	})

	t.Run("expired approval", func(t *testing.T) {
		restore := overrideVestedPillarConstants()
		defer restore()

		z := mock.NewMockZenonWithCustomEpochDuration(t, time.Hour)
		defer z.StopPanic()

		appId := applyVestedPillar(z, g.Pillar4.Address, "Expired", "Will expire before register", "guard3.com")
		z.InsertNewMomentum()
		voteYesByProdAddress(z, g.Pillar1.Address, appId)
		voteYesByProdAddress(z, g.Pillar2.Address, appId)
		voteYesByProdAddress(z, g.Pillar3.Address, appId)
		z.InsertNewMomentum()

		// Advance past voting period + grace period
		// Approval at ~360, grace expires at ~460
		z.InsertMomentumsTo(500)

		// Try to register after expiry
		defer z.CallContract(&nom.AccountBlock{
			Address:   g.Pillar4.Address,
			ToAddress: types.PillarContract,
			Data: definition.ABIPillars.PackMethodPanic(definition.RegisterVestedMethodName,
				appId,
				"expired-pillar", g.Pillar4.Address, g.Pillar4.Address, uint8(0), uint8(100),
			),
		}).Error(t, constants.ErrVestedAppExpired)
		z.InsertNewMomentum()
	})
}
