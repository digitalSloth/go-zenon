package tests

import (
	"bytes"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"

	"github.com/inconshreveable/log15"

	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/rpc/api"
	"github.com/zenon-network/go-zenon/rpc/api/embedded"
	"github.com/zenon-network/go-zenon/zenon/mock"
)

func activateOffchainExecution(t *testing.T, z mock.MockZenon) {
	saveSporkState(t)
	sporkAPI := embedded.NewSporkApi(z)
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.Spork.Address,
		ToAddress: types.SporkContract,
		Data: definition.ABISpork.PackMethodPanic(definition.SporkCreateMethodName,
			"spork-offchain-execution",              // name
			"activate spork for offchain execution", // description
		),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()

	sporkList, _ := sporkAPI.GetAll(0, 10)
	id := sporkList.List[0].Id

	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.Spork.Address,
		ToAddress: types.SporkContract,
		Data: definition.ABISpork.PackMethodPanic(definition.SporkActivateMethodName,
			id, // id
		),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()
	types.OffchainExecutionSpork.SporkId = id
	types.ImplementedSporksMap[id] = true
	z.InsertMomentumsTo(20)
}

func TestSettlement_RejectedBeforeSpork(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)

	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.SettlementContract,
		Data:          definition.ABISettlement.PackMethodPanic(definition.SettlementReleaseWithdrawalsMethodName, uint32(0)),
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
	}, constants.ErrContractDoesntExist, mock.SkipVmChanges)
	z.InsertNewMomentum()
}

// domainParams mirrors the RegisterDomain inputs.
type domainParams struct {
	DomainId            uint32
	DomainClass         uint8
	RuntimeKind         uint8
	StfSpecHash         types.Hash
	InputSource         uint8
	ExecProfile         uint8
	ForeignProfile      uint8
	ProfileConfig       []byte
	ChainBinding        []byte
	MinExecutors        uint8
	ProposerPolicy      uint8
	ValueCaps           []byte
	FinalityModel       uint8
	Status              uint8
	WithdrawalDelay     uint64
	RuntimeUpgradeDelay uint64
}

// phase1Domain returns the only profile Phase 1 accepts: active, no caps,
// both delays at the withdrawal-delay floor.
func phase1Domain(domainId uint32) domainParams {
	return domainParams{
		DomainId:            domainId,
		DomainClass:         definition.DomainClassExecution,
		RuntimeKind:         definition.RuntimeKindWASM,
		InputSource:         definition.InputSourceL1Native,
		ExecProfile:         definition.ExecProfileAttestation,
		ForeignProfile:      definition.ForeignProfileNone,
		ProfileConfig:       []byte{},
		ChainBinding:        []byte{},
		MinExecutors:        1,
		ProposerPolicy:      definition.ProposerPolicySingle,
		ValueCaps:           []byte{},
		FinalityModel:       definition.FinalityModelDelay,
		Status:              definition.DomainStatusActive,
		WithdrawalDelay:     constants.WithdrawalDelayMin,
		RuntimeUpgradeDelay: constants.WithdrawalDelayMin,
	}
}

// valueCapsWithWindow builds a valueCaps blob: an 8-byte big-endian window
// followed by a tag byte that distinguishes blobs with the same window.
func valueCapsWithWindow(window uint64, tag byte) []byte {
	return append(common.Uint64ToBytes(window), tag)
}

// assetIdFor builds the canonical asset id: 22 zero bytes then the ZTS.
func assetIdFor(zts types.ZenonTokenStandard) types.Hash {
	var id types.Hash
	copy(id[22:], zts.Bytes())
	return id
}

func settlementStep(from types.Address, method string, args ...interface{}) *nom.AccountBlock {
	return &nom.AccountBlock{
		Address:       from,
		ToAddress:     types.SettlementContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
		Data:          definition.ABISettlement.PackMethodPanic(method, args...),
	}
}

func registerDomainStep(admin types.Address, p domainParams) *nom.AccountBlock {
	return settlementStep(admin, definition.SettlementRegisterDomainMethodName,
		p.DomainId,
		p.DomainClass,
		p.RuntimeKind,
		p.StfSpecHash,
		p.InputSource,
		p.ExecProfile,
		p.ForeignProfile,
		p.ProfileConfig,
		p.ChainBinding,
		p.MinExecutors,
		p.ProposerPolicy,
		p.ValueCaps,
		p.FinalityModel,
		p.Status,
		p.WithdrawalDelay,
		p.RuntimeUpgradeDelay,
	)
}

func setDomainConfigStep(admin types.Address, domainId uint32, withdrawalDelay, runtimeUpgradeDelay uint64) *nom.AccountBlock {
	return settlementStep(admin, definition.SettlementSetDomainConfigMethodName, domainId, withdrawalDelay, runtimeUpgradeDelay)
}

func setAssetConfigStep(admin types.Address, domainId uint32, assetId types.Hash, maxBatchWithdrawal *big.Int) *nom.AccountBlock {
	return settlementStep(admin, definition.SettlementSetAssetConfigMethodName, domainId, assetId, maxBatchWithdrawal)
}

func setApprovedDAModesStep(admin types.Address, domainId uint32, modes []byte) *nom.AccountBlock {
	return settlementStep(admin, definition.SettlementSetApprovedDAModesMethodName, domainId, modes)
}

// newSettlementZenon starts a mock chain with the offchain-execution spork
// active and g.User5 as network admin.
func newSettlementZenon(t *testing.T) mock.MockZenon {
	z := mock.NewMockZenon(t)
	t.Cleanup(z.StopPanic)
	activateOffchainExecution(t, z)
	logs := z.SaveLogs(common.EmbeddedLogger)
	t.Cleanup(func() { logs.Equals(t, ``) })
	saveBridgeConstants(t)

	savedAdmin := types.NetworkAdmin
	types.NetworkAdmin = g.User5.Address
	t.Cleanup(func() { types.NetworkAdmin = savedAdmin })
	return z
}

// settlementCall executes the block against the contract and checks the
// receive error.
func settlementCall(t *testing.T, z mock.MockZenon, block *nom.AccountBlock, want error) {
	t.Helper()
	call := z.CallContract(block)
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, want)
}

// settlementRejectedAtSend checks that send validation rejects the block.
func settlementRejectedAtSend(z mock.MockZenon, block *nom.AccountBlock) {
	z.InsertSendBlock(block, constants.ErrForbiddenParam, mock.SkipVmChanges)
	z.InsertNewMomentum()
}

func settlementStorage(t *testing.T, z mock.MockZenon) db.DB {
	t.Helper()
	_, context, err := api.GetFrontierContext(z.Chain(), types.SettlementContract)
	common.FailIfErr(t, err)
	return context.Storage()
}

func advanceMomentums(t *testing.T, z mock.MockZenon, n uint64) {
	t.Helper()
	frMom, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	z.InsertMomentumsTo(frMom.Height + n)
}

// challengeHash returns the pending params hash of a time-challenge scope,
// or the zero hash when none is pending.
func challengeHash(t *testing.T, z mock.MockZenon, scope string) types.Hash {
	t.Helper()
	info, err := definition.GetTimeChallengeInfoVariable(settlementStorage(t, z), scope)
	common.FailIfErr(t, err)
	if info == nil {
		return types.ZeroHash
	}
	return info.ParamsHash
}

func domainRecordOf(t *testing.T, z mock.MockZenon, domainId uint32) *definition.DomainRecord {
	t.Helper()
	record, err := definition.GetDomainRecord(settlementStorage(t, z), domainId)
	common.FailIfErr(t, err)
	return record
}

func domainConfigOf(t *testing.T, z mock.MockZenon, domainId uint32) *definition.DomainConfig {
	t.Helper()
	config, err := definition.GetDomainConfig(settlementStorage(t, z), domainId)
	common.FailIfErr(t, err)
	return config
}

func registerDomains(t *testing.T, z mock.MockZenon, ids ...uint32) {
	t.Helper()
	for _, id := range ids {
		settlementCall(t, z, registerDomainStep(g.User5.Address, phase1Domain(id)), nil)
	}
}

func pauseStep(admin types.Address, domainId uint32, isGlobal bool, scope uint8, active bool) *nom.AccountBlock {
	return &nom.AccountBlock{
		Address:       admin,
		ToAddress:     types.SettlementContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
		Data: definition.ABISettlement.PackMethodPanic(definition.SettlementPauseMethodName,
			domainId,
			isGlobal,
			scope,
			active,
		),
	}
}

func initiateEscapeHatchStep(admin types.Address, domainId uint32) *nom.AccountBlock {
	return &nom.AccountBlock{
		Address:       admin,
		ToAddress:     types.SettlementContract,
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
		Data: definition.ABISettlement.PackMethodPanic(definition.SettlementInitiateEscapeHatchMethodName,
			domainId,
		),
	}
}

func TestSettlement_AdminGate_RejectsNonNetworkAdmin(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateOffchainExecution(t, z)
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)

	settlementCall(t, z, registerDomainStep(g.User1.Address, phase1Domain(1)), constants.ErrPermissionDenied)
	settlementCall(t, z, setDomainConfigStep(g.User1.Address, 1, constants.WithdrawalDelayMin, constants.WithdrawalDelayMin), constants.ErrPermissionDenied)
	settlementCall(t, z, setAssetConfigStep(g.User1.Address, 1, assetIdFor(types.ZnnTokenStandard), big.NewInt(1000)), constants.ErrPermissionDenied)
	settlementCall(t, z, setApprovedDAModesStep(g.User1.Address, 1, []byte{definition.DAModeBestEffort}), constants.ErrPermissionDenied)

	call := z.CallContract(pauseStep(g.User1.Address, 0, true, definition.PauseScopeSubmit, true))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, constants.ErrPermissionDenied)

	call = z.CallContract(initiateEscapeHatchStep(g.User1.Address, 1))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, constants.ErrPermissionDenied)
}

func TestSettlement_RegisterDomain_Phase1Profile(t *testing.T) {
	z := newSettlementZenon(t)

	settlementCall(t, z, registerDomainStep(g.User5.Address, phase1Domain(1)), nil)

	record := domainRecordOf(t, z, 1)
	if record.DomainClass != definition.DomainClassExecution || record.RuntimeKind != definition.RuntimeKindWASM ||
		record.StfSpecHash != types.ZeroHash || record.InputSource != definition.InputSourceL1Native ||
		record.ExecProfile != definition.ExecProfileAttestation || record.ForeignProfile != definition.ForeignProfileNone ||
		len(record.ProfileConfig) != 0 || len(record.ChainBinding) != 0 || record.MinExecutors != 1 ||
		record.ProposerPolicy != definition.ProposerPolicySingle || len(record.ValueCaps) != 0 ||
		record.FinalityModel != definition.FinalityModelDelay || record.Status != definition.DomainStatusActive {
		t.Fatalf("unexpected domain record: %+v", record)
	}
	config := domainConfigOf(t, z, 1)
	if config.WithdrawalDelay != constants.WithdrawalDelayMin || config.RuntimeUpgradeDelay != constants.WithdrawalDelayMin {
		t.Fatalf("unexpected domain config: %+v", config)
	}
	if last, err := definition.GetLastDomainId(settlementStorage(t, z)); err != nil || last != 1 {
		t.Fatalf("expected last domain id 1, got %d err %v", last, err)
	}

	settlementCall(t, z, registerDomainStep(g.User5.Address, phase1Domain(2)), nil)
	if last, err := definition.GetLastDomainId(settlementStorage(t, z)); err != nil || last != 2 {
		t.Fatalf("expected last domain id 2, got %d err %v", last, err)
	}
}

func TestSettlement_RegisterDomain_RejectsReservedValues(t *testing.T) {
	z := newSettlementZenon(t)
	d := constants.WithdrawalDelayMin

	cases := []struct {
		name   string
		mutate func(*domainParams)
	}{
		{"domainId 0", func(p *domainParams) { p.DomainId = 0 }},
		{"inputSource relayed", func(p *domainParams) { p.InputSource = 1 }},
		{"inputSource retired", func(p *domainParams) { p.InputSource = 2 }},
		{"proposerPolicy 1", func(p *domainParams) { p.ProposerPolicy = 1 }},
		{"proposerPolicy 2", func(p *domainParams) { p.ProposerPolicy = 2 }},
		{"domainClass 1", func(p *domainParams) { p.DomainClass = 1 }},
		{"domainClass 2", func(p *domainParams) { p.DomainClass = 2 }},
		{"execProfile 1", func(p *domainParams) { p.ExecProfile = 1 }},
		{"execProfile 2", func(p *domainParams) { p.ExecProfile = 2 }},
		{"foreignProfile 1", func(p *domainParams) { p.ForeignProfile = 1 }},
		{"foreignProfile 2", func(p *domainParams) { p.ForeignProfile = 2 }},
		{"foreignProfile 3", func(p *domainParams) { p.ForeignProfile = 3 }},
		{"runtimeKind 1", func(p *domainParams) { p.RuntimeKind = 1 }},
		{"finalityModel 1", func(p *domainParams) { p.FinalityModel = 1 }},
		{"finalityModel 2", func(p *domainParams) { p.FinalityModel = 2 }},
		{"minExecutors 0", func(p *domainParams) { p.MinExecutors = 0 }},
		{"minExecutors 2", func(p *domainParams) { p.MinExecutors = 2 }},
		{"status 2", func(p *domainParams) { p.Status = 2 }},
		{"chainBinding set", func(p *domainParams) { p.ChainBinding = []byte{1} }},
		{"profileConfig set", func(p *domainParams) { p.ProfileConfig = []byte{1} }},
		{"valueCaps 1 byte", func(p *domainParams) { p.ValueCaps = []byte{1} }},
		{"valueCaps 7 bytes", func(p *domainParams) { p.ValueCaps = make([]byte, 7) }},
		{"window below withdrawalDelay", func(p *domainParams) {
			p.WithdrawalDelay, p.RuntimeUpgradeDelay = d+5, d+5
			p.ValueCaps = valueCapsWithWindow(d, 0)
		}},
		{"withdrawalDelay below min", func(p *domainParams) { p.WithdrawalDelay = d - 1 }},
		{"withdrawalDelay above max", func(p *domainParams) {
			p.WithdrawalDelay = constants.WithdrawalDelayMax + 1
			p.RuntimeUpgradeDelay = constants.WithdrawalDelayMax + 1
		}},
		{"runtimeUpgradeDelay above max", func(p *domainParams) {
			p.RuntimeUpgradeDelay = math.MaxUint64
		}},
		{"runtimeUpgradeDelay below withdrawalDelay", func(p *domainParams) {
			p.WithdrawalDelay, p.RuntimeUpgradeDelay = d+1, d
		}},
	}
	for _, c := range cases {
		p := phase1Domain(1)
		c.mutate(&p)
		settlementRejectedAtSend(z, registerDomainStep(g.User5.Address, p))
	}
}

func TestSettlement_RegisterDomain_RejectsSkippedIdChangedDelaysAndNoOp(t *testing.T) {
	z := newSettlementZenon(t)
	d := constants.WithdrawalDelayMin

	settlementCall(t, z, registerDomainStep(g.User5.Address, phase1Domain(2)), constants.ErrForbiddenParam)
	if last, err := definition.GetLastDomainId(settlementStorage(t, z)); err != nil || last != 0 {
		t.Fatalf("expected last domain id 0, got %d err %v", last, err)
	}

	registerDomains(t, z, 1)

	changed := phase1Domain(1)
	changed.WithdrawalDelay, changed.RuntimeUpgradeDelay = d+1, d+1
	settlementCall(t, z, registerDomainStep(g.User5.Address, changed), constants.ErrForbiddenParam)

	settlementCall(t, z, registerDomainStep(g.User5.Address, phase1Domain(1)), constants.ErrForbiddenParam)

	if h := challengeHash(t, z, "RegisterDomain/1"); !h.IsZero() {
		t.Fatalf("expected no pending challenge, got %v", h)
	}
}

func TestSettlement_RegisterDomain_ValueCapsChangeIsTimeChallenged(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinAdministratorDelay = 20
	d := constants.WithdrawalDelayMin
	scope := "RegisterDomain/1"

	oldCaps := valueCapsWithWindow(d, 1)
	newCaps := valueCapsWithWindow(d, 2)
	p := phase1Domain(1)
	p.ValueCaps = oldCaps
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), nil)

	// 1. new caps with PAUSED: status applies at once, caps stay pending
	p.ValueCaps, p.Status = newCaps, definition.DomainStatusPaused
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), nil)
	record := domainRecordOf(t, z, 1)
	if record.Status != definition.DomainStatusPaused || !bytes.Equal(record.ValueCaps, oldCaps) {
		t.Fatalf("expected PAUSED with old caps, got %+v", record)
	}
	h := challengeHash(t, z, scope)
	if h.IsZero() {
		t.Fatal("expected a pending challenge")
	}

	// 2. status-only flip keeps the pending challenge
	p.ValueCaps, p.Status = oldCaps, definition.DomainStatusActive
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), nil)
	if record := domainRecordOf(t, z, 1); record.Status != definition.DomainStatusActive {
		t.Fatalf("expected ACTIVE, got %+v", record)
	}
	if got := challengeHash(t, z, scope); got != h {
		t.Fatalf("expected challenge hash %v to be kept, got %v", h, got)
	}

	// 3. premature completion fails and rolls its status back
	p.ValueCaps, p.Status = newCaps, definition.DomainStatusPaused
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), constants.ErrTimeChallengeNotDue)
	if record := domainRecordOf(t, z, 1); record.Status != definition.DomainStatusActive {
		t.Fatalf("expected ACTIVE after rolled-back call, got %+v", record)
	}

	// 4. completion applies the caps and its own status
	advanceMomentums(t, z, constants.MinAdministratorDelay)
	p.ValueCaps, p.Status = newCaps, definition.DomainStatusActive
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), nil)
	record = domainRecordOf(t, z, 1)
	if record.Status != definition.DomainStatusActive || !bytes.Equal(record.ValueCaps, newCaps) {
		t.Fatalf("expected ACTIVE with new caps, got %+v", record)
	}
}

func TestSettlement_RegisterDomain_StfSpecHashBumpUsesRuntimeUpgradeDelay(t *testing.T) {
	z := newSettlementZenon(t)
	savedMin := constants.WithdrawalDelayMin
	constants.WithdrawalDelayMin = 10
	t.Cleanup(func() { constants.WithdrawalDelayMin = savedMin })

	p := phase1Domain(1)
	p.RuntimeUpgradeDelay = 40
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), nil)

	bumped := hashFromByteForTest(7)
	p.StfSpecHash = bumped
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), nil)
	if h := domainRecordOf(t, z, 1).StfSpecHash; h != types.ZeroHash {
		t.Fatalf("expected old hash while pending, got %v", h)
	}

	advanceMomentums(t, z, 20)
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), constants.ErrTimeChallengeNotDue)

	advanceMomentums(t, z, 40)
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), nil)
	if h := domainRecordOf(t, z, 1).StfSpecHash; h != bumped {
		t.Fatalf("expected bumped hash, got %v", h)
	}
}

func hashFromByteForTest(b byte) types.Hash {
	var h types.Hash
	for i := range h {
		h[i] = b
	}
	return h
}

func TestSettlement_SetDomainConfig_Bounds(t *testing.T) {
	z := newSettlementZenon(t)
	d := constants.WithdrawalDelayMin

	settlementRejectedAtSend(z, setDomainConfigStep(g.User5.Address, 1, d-1, constants.WithdrawalDelayMax))
	settlementRejectedAtSend(z, setDomainConfigStep(g.User5.Address, 1, constants.WithdrawalDelayMax+1, constants.WithdrawalDelayMax+1))
	settlementRejectedAtSend(z, setDomainConfigStep(g.User5.Address, 1, d+100, d+99))
	settlementRejectedAtSend(z, setDomainConfigStep(g.User5.Address, 1, d, math.MaxUint64))

	settlementCall(t, z, setDomainConfigStep(g.User5.Address, 1, d, d), constants.ErrForbiddenParam)
}

func TestSettlement_SetDomainConfig_TimeChallenge(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	d := constants.WithdrawalDelayMin
	registerDomains(t, z, 1)

	settlementCall(t, z, setDomainConfigStep(g.User5.Address, 1, d+5, d+6), nil)
	if got := domainConfigOf(t, z, 1); got.WithdrawalDelay != d || got.RuntimeUpgradeDelay != d {
		t.Fatalf("expected unchanged config while pending, got %+v", got)
	}

	settlementCall(t, z, setDomainConfigStep(g.User5.Address, 1, d+5, d+6), constants.ErrTimeChallengeNotDue)

	advanceMomentums(t, z, constants.MinSoftDelay)
	settlementCall(t, z, setDomainConfigStep(g.User5.Address, 1, d+5, d+6), nil)
	if got := domainConfigOf(t, z, 1); got.WithdrawalDelay != d+5 || got.RuntimeUpgradeDelay != d+6 {
		t.Fatalf("expected new config, got %+v", got)
	}

	// later reconfigurations must carry the new delays
	p := phase1Domain(1)
	p.Status = definition.DomainStatusPaused
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), constants.ErrForbiddenParam)
	p.WithdrawalDelay, p.RuntimeUpgradeDelay = d+5, d+6
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), nil)
	if record := domainRecordOf(t, z, 1); record.Status != definition.DomainStatusPaused {
		t.Fatalf("expected PAUSED, got %+v", record)
	}
}

func TestSettlement_SetDomainConfig_RejectsDelayAboveValueCapWindow(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 30
	constants.MinAdministratorDelay = 10
	d := constants.WithdrawalDelayMin
	window := d + 10

	p1 := phase1Domain(1)
	p1.ValueCaps = valueCapsWithWindow(window, 1)
	settlementCall(t, z, registerDomainStep(g.User5.Address, p1), nil)
	p2 := phase1Domain(2)
	p2.ValueCaps = valueCapsWithWindow(window, 1)
	settlementCall(t, z, registerDomainStep(g.User5.Address, p2), nil)

	// a delay above the window is rejected; a delay equal to it is accepted
	settlementCall(t, z, setDomainConfigStep(g.User5.Address, 1, window+1, window+1), constants.ErrForbiddenParam)
	settlementCall(t, z, setDomainConfigStep(g.User5.Address, 1, window, window), nil)
	advanceMomentums(t, z, constants.MinSoftDelay)
	settlementCall(t, z, setDomainConfigStep(g.User5.Address, 1, window, window), nil)
	if got := domainConfigOf(t, z, 1); got.WithdrawalDelay != window {
		t.Fatalf("expected delay %d, got %+v", window, got)
	}

	// domain 2: a pending delay is invalidated by a later window shrink
	settlementCall(t, z, setDomainConfigStep(g.User5.Address, 2, d+5, d+5), nil)
	p2.ValueCaps = valueCapsWithWindow(d, 2)
	settlementCall(t, z, registerDomainStep(g.User5.Address, p2), nil)
	advanceMomentums(t, z, constants.MinAdministratorDelay)
	settlementCall(t, z, registerDomainStep(g.User5.Address, p2), nil)
	if record := domainRecordOf(t, z, 2); !bytes.Equal(record.ValueCaps, p2.ValueCaps) {
		t.Fatalf("expected shrunk window to be stored, got %+v", record)
	}

	advanceMomentums(t, z, constants.MinSoftDelay)
	settlementCall(t, z, setDomainConfigStep(g.User5.Address, 2, d+5, d+5), constants.ErrForbiddenParam)
	if got := domainConfigOf(t, z, 2); got.WithdrawalDelay != d {
		t.Fatalf("expected stored delays unchanged, got %+v", got)
	}
}

func TestSettlement_SetAssetConfig_NeverFalls(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	registerDomains(t, z, 1, 2)
	assetA := assetIdFor(types.ZnnTokenStandard)
	assetB := assetIdFor(types.QsrTokenStandard)

	maxOf := func(domainId uint32, assetId types.Hash) *big.Int {
		config, err := definition.GetAssetConfig(settlementStorage(t, z), domainId, assetId)
		common.FailIfErr(t, err)
		return config.MaxBatchWithdrawal
	}
	set := func(domainId uint32, assetId types.Hash, v int64, want error) {
		settlementCall(t, z, setAssetConfigStep(g.User5.Address, domainId, assetId, big.NewInt(v)), want)
	}
	setAndComplete := func(domainId uint32, assetId types.Hash, v int64) {
		set(domainId, assetId, v, nil)
		advanceMomentums(t, z, constants.MinSoftDelay)
		set(domainId, assetId, v, nil)
	}

	var nonCanonical types.Hash
	nonCanonical[0] = 0x01
	copy(nonCanonical[22:], types.ZnnTokenStandard.Bytes())
	settlementRejectedAtSend(z, setAssetConfigStep(g.User5.Address, 1, nonCanonical, big.NewInt(1000)))

	overCeiling := new(big.Int).Add(constants.MaxBatchWithdrawalCeiling, big.NewInt(1))
	settlementRejectedAtSend(z, setAssetConfigStep(g.User5.Address, 1, assetA, overCeiling))
	settlementRejectedAtSend(z, setAssetConfigStep(g.User5.Address, 1, assetA, big.NewInt(0)))

	setAndComplete(1, assetA, 1000)
	if maxOf(1, assetA).Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("expected 1000, got %v", maxOf(1, assetA))
	}

	set(1, assetA, 999, constants.ErrForbiddenParam)

	paused := phase1Domain(1)
	paused.Status = definition.DomainStatusPaused
	settlementCall(t, z, registerDomainStep(g.User5.Address, paused), nil)
	if domainRecordOf(t, z, 1).Status != definition.DomainStatusPaused {
		t.Fatal("expected domain 1 PAUSED")
	}
	set(1, assetA, 999, constants.ErrForbiddenParam)

	setAndComplete(1, assetA, 2000)
	if maxOf(1, assetA).Cmp(big.NewInt(2000)) != 0 {
		t.Fatalf("expected 2000, got %v", maxOf(1, assetA))
	}

	// a fresh asset and a fresh domain accept a lower value
	setAndComplete(1, assetB, 5)
	setAndComplete(2, assetA, 5)
	if maxOf(1, assetB).Cmp(big.NewInt(5)) != 0 || maxOf(2, assetA).Cmp(big.NewInt(5)) != 0 {
		t.Fatal("expected independent asset configs")
	}

	// different scopes run independently
	set(1, assetA, 3000, nil)
	set(1, assetB, 6, nil)
	advanceMomentums(t, z, constants.MinSoftDelay)
	set(1, assetA, 3000, nil)
	set(1, assetB, 6, nil)
	if maxOf(1, assetA).Cmp(big.NewInt(3000)) != 0 || maxOf(1, assetB).Cmp(big.NewInt(6)) != 0 {
		t.Fatalf("expected both pending changes to complete, got %v and %v", maxOf(1, assetA), maxOf(1, assetB))
	}
}

func TestSettlement_SetApprovedDAModes(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10

	settlementRejectedAtSend(z, setApprovedDAModesStep(g.User5.Address, 1, []byte{}))
	settlementRejectedAtSend(z, setApprovedDAModesStep(g.User5.Address, 1, []byte{1}))
	settlementRejectedAtSend(z, setApprovedDAModesStep(g.User5.Address, 1, []byte{0, 0}))

	settlementCall(t, z, setApprovedDAModesStep(g.User5.Address, 1, []byte{0}), constants.ErrForbiddenParam)

	registerDomains(t, z, 1)
	settlementCall(t, z, setApprovedDAModesStep(g.User5.Address, 1, []byte{0}), nil)
	set, err := definition.GetApprovedDAModeSet(settlementStorage(t, z), 1)
	common.FailIfErr(t, err)
	if len(set.Modes) != 0 {
		t.Fatalf("expected empty set while pending, got %v", set.Modes)
	}

	advanceMomentums(t, z, constants.MinSoftDelay)
	settlementCall(t, z, setApprovedDAModesStep(g.User5.Address, 1, []byte{0}), nil)
	set, err = definition.GetApprovedDAModeSet(settlementStorage(t, z), 1)
	common.FailIfErr(t, err)
	if !bytes.Equal(set.Modes, []byte{definition.DAModeBestEffort}) {
		t.Fatalf("expected [0], got %v", set.Modes)
	}
}
func TestSettlement_Pause_StateMachine(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateOffchainExecution(t, z)
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)

	savedAdmin := types.NetworkAdmin
	types.NetworkAdmin = g.User5.Address
	defer func() { types.NetworkAdmin = savedAdmin }()

	getStorage := func() db.DB {
		_, context, err := api.GetFrontierContext(z.Chain(), types.SettlementContract)
		common.FailIfErr(t, err)
		return context.Storage()
	}

	// global SUBMIT pause
	call := z.CallContract(pauseStep(g.User5.Address, 0, true, definition.PauseScopeSubmit, true))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	storage := getStorage()
	if paused, err := definition.IsSubmitPaused(storage, 0); err != nil || !paused {
		t.Fatalf("expected SUBMIT paused globally, got %v err %v", paused, err)
	}
	if paused, err := definition.IsRelayPaused(storage, 0); err != nil || paused {
		t.Fatalf("expected RELAY unaffected, got %v err %v", paused, err)
	}

	// global RELAY pause too (independence)
	call = z.CallContract(pauseStep(g.User5.Address, 0, true, definition.PauseScopeRelay, true))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	storage = getStorage()
	if paused, err := definition.IsSubmitPaused(storage, 0); err != nil || !paused {
		t.Fatalf("expected SUBMIT still paused, got %v err %v", paused, err)
	}
	if paused, err := definition.IsRelayPaused(storage, 0); err != nil || !paused {
		t.Fatalf("expected RELAY paused, got %v err %v", paused, err)
	}

	// deactivate SUBMIT, RELAY unaffected
	call = z.CallContract(pauseStep(g.User5.Address, 0, true, definition.PauseScopeSubmit, false))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	storage = getStorage()
	if paused, err := definition.IsSubmitPaused(storage, 0); err != nil || paused {
		t.Fatalf("expected SUBMIT deactivated, got %v err %v", paused, err)
	}
	if paused, err := definition.IsRelayPaused(storage, 0); err != nil || !paused {
		t.Fatalf("expected RELAY still paused, got %v err %v", paused, err)
	}

	// deactivate RELAY, SUBMIT unaffected (already off)
	call = z.CallContract(pauseStep(g.User5.Address, 0, true, definition.PauseScopeRelay, false))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	storage = getStorage()
	if paused, err := definition.IsSubmitPaused(storage, 0); err != nil || paused {
		t.Fatalf("expected SUBMIT still deactivated, got %v err %v", paused, err)
	}
	if paused, err := definition.IsRelayPaused(storage, 0); err != nil || paused {
		t.Fatalf("expected RELAY deactivated, got %v err %v", paused, err)
	}

	// per-domain SUBMIT pause only affects that domain
	call = z.CallContract(pauseStep(g.User5.Address, 7, false, definition.PauseScopeSubmit, true))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	storage = getStorage()
	if paused, err := definition.IsSubmitPaused(storage, 7); err != nil || !paused {
		t.Fatalf("expected SUBMIT paused for domain 7, got %v err %v", paused, err)
	}
	if paused, err := definition.IsSubmitPaused(storage, 8); err != nil || paused {
		t.Fatalf("expected SUBMIT unaffected for domain 8, got %v err %v", paused, err)
	}
}

func TestSettlement_InitiateEscapeHatch_Stub(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateOffchainExecution(t, z)
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)
	saveBridgeConstants(t)
	constants.MinAdministratorDelay = 20

	savedAdmin := types.NetworkAdmin
	types.NetworkAdmin = g.User5.Address
	defer func() { types.NetworkAdmin = savedAdmin }()

	domainId := uint32(3)

	call := z.CallContract(initiateEscapeHatchStep(g.User5.Address, domainId))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	_, context, err := api.GetFrontierContext(z.Chain(), types.SettlementContract)
	common.FailIfErr(t, err)
	if _, err := definition.GetEscapeHatchRecord(context.Storage(), domainId); err != constants.ErrDataNonExistent {
		t.Fatalf("expected no record before challenge completes, got err %v", err)
	}

	frMom, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	z.InsertMomentumsTo(frMom.Height + constants.MinAdministratorDelay)

	call = z.CallContract(initiateEscapeHatchStep(g.User5.Address, domainId))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	_, context, err = api.GetFrontierContext(z.Chain(), types.SettlementContract)
	common.FailIfErr(t, err)
	record, err := definition.GetEscapeHatchRecord(context.Storage(), domainId)
	common.FailIfErr(t, err)
	if record.DomainId != domainId {
		t.Fatalf("expected domainId %d, got %d", domainId, record.DomainId)
	}
	completionMom, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	if record.TriggeredAtHeight > completionMom.Height {
		t.Fatalf("expected triggeredAtHeight <= frontier height, got %d > %d", record.TriggeredAtHeight, completionMom.Height)
	}
}

func TestSettlement_DispatchAfterSpork(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, `
t=2001-09-09T01:46:50+0000 lvl=dbug msg=created module=embedded contract=spork spork="&{Id:bed2a35fd610e53f6a6c0ffaacc6638fef0588003588cff784b226d12aad4027 Name:spork-offchain-execution Description:activate spork for offchain execution Activated:false EnforcementHeight:0}"
t=2001-09-09T01:47:00+0000 lvl=dbug msg=activated module=embedded contract=spork spork="&{Id:bed2a35fd610e53f6a6c0ffaacc6638fef0588003588cff784b226d12aad4027 Name:spork-offchain-execution Description:activate spork for offchain execution Activated:true EnforcementHeight:9}"
t=2001-09-09T01:50:00+0000 lvl=dbug msg="settlement method dispatched (not implemented)" module=embedded contract=settlement method=ReleaseWithdrawals address=z1qzal6c5s9rjnnxd2z7dvdhjxpmmj4fmw56a0mz
`)
	activateOffchainExecution(t, z)

	defer z.CallContract(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.SettlementContract,
		Data:          definition.ABISettlement.PackMethodPanic(definition.SettlementReleaseWithdrawalsMethodName, uint32(0)),
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
	}).Error(t, constants.ErrNotImplemented)
	z.InsertNewMomentum()
	z.InsertNewMomentum()
}

// Update and ReleaseWithdrawals carry no payment of their own, so an
// attached amount is always a mistake and is rejected at send validation.
func TestSettlement_Update_RejectsAttachedAmount(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateOffchainExecution(t, z)
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)

	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.SettlementContract,
		Data:          definition.ABISettlement.PackMethodPanic(definition.SettlementUpdateMethodName),
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(1),
	}, constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertNewMomentum()
}

func TestSettlement_ReleaseWithdrawals_RejectsAttachedAmount(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateOffchainExecution(t, z)
	defer z.SaveLogs(common.EmbeddedLogger).Equals(t, ``)

	z.InsertSendBlock(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.SettlementContract,
		Data:          definition.ABISettlement.PackMethodPanic(definition.SettlementReleaseWithdrawalsMethodName, uint32(0)),
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(1),
	}, constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertNewMomentum()
}

func TestSettlement_CollectReward_RejectsAttachedAmount(t *testing.T) {
	z := newSettlementZenon(t)
	block := settlementStep(g.User1.Address, definition.SettlementCollectRewardMethodName)
	block.Amount = big.NewInt(1)
	z.InsertSendBlock(block, constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertNewMomentum()
}

func registerExecutorStep(admin types.Address, pillarName string, executor types.Address) *nom.AccountBlock {
	return settlementStep(admin, definition.SettlementRegisterExecutorMethodName, pillarName, executor)
}

func setPeripheryConfigStep(admin types.Address, bytecode, state *big.Int) *nom.AccountBlock {
	return settlementStep(admin, definition.SettlementSetPeripheryConfigMethodName, bytecode, state)
}

func TestSettlement_RegisterExecutor_AdminGateAndUnknownPillar(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinAdministratorDelay = 10

	settlementCall(t, z, registerExecutorStep(g.User1.Address, g.Pillar1Name, g.User1.Address), constants.ErrPermissionDenied)
	settlementCall(t, z, registerExecutorStep(g.User5.Address, "no-such-pillar", g.User1.Address), constants.ErrForbiddenParam)

	if set, err := definition.GetExecutorSet(settlementStorage(t, z)); err != nil || len(set) != 0 {
		t.Fatalf("expected an empty executor set, got %v err %v", set, err)
	}
}

func TestSettlement_RegisterExecutor_TimeChallengeAndReplacement(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinAdministratorDelay = 10

	settlementCall(t, z, registerExecutorStep(g.User5.Address, g.Pillar1Name, g.User1.Address), nil)
	if set, err := definition.GetExecutorSet(settlementStorage(t, z)); err != nil || len(set) != 0 {
		t.Fatalf("expected no member while the challenge is pending, got %v err %v", set, err)
	}
	settlementCall(t, z, registerExecutorStep(g.User5.Address, g.Pillar1Name, g.User1.Address), constants.ErrTimeChallengeNotDue)

	advanceMomentums(t, z, constants.MinAdministratorDelay)
	settlementCall(t, z, registerExecutorStep(g.User5.Address, g.Pillar1Name, g.User1.Address), nil)
	set, err := definition.GetExecutorSet(settlementStorage(t, z))
	common.FailIfErr(t, err)
	if len(set) != 1 || set[0].PillarName != g.Pillar1Name || set[0].ExecutorAddress != g.User1.Address ||
		set[0].Status != definition.ExecutorStatusActive || set[0].RegistrationHeight == 0 {
		t.Fatalf("expected one active member for pillar 1, got %+v", set)
	}

	// a second registration replaces the member
	settlementCall(t, z, registerExecutorStep(g.User5.Address, g.Pillar2Name, g.User2.Address), nil)
	advanceMomentums(t, z, constants.MinAdministratorDelay)
	settlementCall(t, z, registerExecutorStep(g.User5.Address, g.Pillar2Name, g.User2.Address), nil)
	set, err = definition.GetExecutorSet(settlementStorage(t, z))
	common.FailIfErr(t, err)
	if len(set) != 1 || set[0].PillarName != g.Pillar2Name || set[0].ExecutorAddress != g.User2.Address {
		t.Fatalf("expected exactly the replacement member, got %+v", set)
	}
}

func TestSettlement_RegisterExecutor_SamePillarNewAddressReplacesAddress(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinAdministratorDelay = 10

	for _, executor := range []types.Address{g.User1.Address, g.User2.Address} {
		settlementCall(t, z, registerExecutorStep(g.User5.Address, g.Pillar1Name, executor), nil)
		advanceMomentums(t, z, constants.MinAdministratorDelay)
		settlementCall(t, z, registerExecutorStep(g.User5.Address, g.Pillar1Name, executor), nil)
	}
	set, err := definition.GetExecutorSet(settlementStorage(t, z))
	common.FailIfErr(t, err)
	if len(set) != 1 || set[0].ExecutorAddress != g.User2.Address {
		t.Fatalf("expected one member with the new address, got %+v", set)
	}
}

func TestSettlement_SetPeripheryConfig_BoundsAndAdminGate(t *testing.T) {
	z := newSettlementZenon(t)
	floor := constants.ZnnPerByteOfBytecodeFloor
	ceiling := constants.ZnnPerByteOfBytecodeCeiling
	ok := big.NewInt(5)

	settlementRejectedAtSend(z, setPeripheryConfigStep(g.User5.Address, new(big.Int).Sub(floor, big.NewInt(1)), ok))
	settlementRejectedAtSend(z, setPeripheryConfigStep(g.User5.Address, ok, new(big.Int).Sub(constants.ZnnPerByteOfStateFloor, big.NewInt(1))))
	settlementRejectedAtSend(z, setPeripheryConfigStep(g.User5.Address, new(big.Int).Add(ceiling, big.NewInt(1)), ok))
	settlementRejectedAtSend(z, setPeripheryConfigStep(g.User5.Address, ok, new(big.Int).Add(constants.ZnnPerByteOfStateCeiling, big.NewInt(1))))

	settlementCall(t, z, setPeripheryConfigStep(g.User1.Address, ok, ok), constants.ErrPermissionDenied)
}

func TestSettlement_SetPeripheryConfig_TimeChallenge(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	bytecode, state := big.NewInt(5), big.NewInt(9)

	settlementCall(t, z, setPeripheryConfigStep(g.User5.Address, bytecode, state), nil)
	if _, err := definition.GetPeripheryConfig(settlementStorage(t, z)); err != constants.ErrDataNonExistent {
		t.Fatalf("expected no config while pending, got err %v", err)
	}
	settlementCall(t, z, setPeripheryConfigStep(g.User5.Address, bytecode, state), constants.ErrTimeChallengeNotDue)

	advanceMomentums(t, z, constants.MinSoftDelay)
	settlementCall(t, z, setPeripheryConfigStep(g.User5.Address, bytecode, state), nil)
	config, err := definition.GetPeripheryConfig(settlementStorage(t, z))
	common.FailIfErr(t, err)
	if config.ZnnPerByteOfBytecode.Cmp(bytecode) != 0 || config.ZnnPerByteOfState.Cmp(state) != 0 {
		t.Fatalf("expected the new config, got %+v", config)
	}
}

func TestSettlement_CollectReward_NothingToWithdraw(t *testing.T) {
	z := newSettlementZenon(t)
	settlementCall(t, z, settlementStep(g.User1.Address, definition.SettlementCollectRewardMethodName), constants.ErrNothingToWithdraw)

	// a SUBMIT-only pause does not block collection
	settlementCall(t, z, pauseStep(g.User5.Address, 7, false, definition.PauseScopeSubmit, true), nil)
	settlementCall(t, z, settlementStep(g.User1.Address, definition.SettlementCollectRewardMethodName), constants.ErrNothingToWithdraw)
}

func TestSettlement_CollectReward_BlockedByRelayPauseOnOneDomain(t *testing.T) {
	z := newSettlementZenon(t)
	settlementCall(t, z, pauseStep(g.User5.Address, 7, false, definition.PauseScopeRelay, true), nil)
	settlementCall(t, z, settlementStep(g.User1.Address, definition.SettlementCollectRewardMethodName), constants.ErrSettlementRelayPaused)

	settlementCall(t, z, pauseStep(g.User5.Address, 7, false, definition.PauseScopeRelay, false), nil)
	settlementCall(t, z, settlementStep(g.User1.Address, definition.SettlementCollectRewardMethodName), constants.ErrNothingToWithdraw)
}

// activateSporkByName creates and activates a spork, then registers its id
// on target. The pillar worker returns at the first embedded contract that
// cannot be updated, so the automatic Settlement crank only runs once the
// sporks gating the other updatable contracts are active too.
func activateSporkByName(t *testing.T, z mock.MockZenon, name string, target *types.ImplementedSpork) {
	t.Helper()
	sporkAPI := embedded.NewSporkApi(z)
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.Spork.Address,
		ToAddress: types.SporkContract,
		Data:      definition.ABISpork.PackMethodPanic(definition.SporkCreateMethodName, name, name),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()

	sporkList, err := sporkAPI.GetAll(0, 10)
	common.FailIfErr(t, err)
	var id types.Hash
	for _, spork := range sporkList.List {
		if spork.Name == name {
			id = spork.Id
		}
	}
	if id.IsZero() {
		t.Fatalf("spork %s not created", name)
	}
	z.InsertSendBlock(&nom.AccountBlock{
		Address:   g.Spork.Address,
		ToAddress: types.SporkContract,
		Data:      definition.ABISpork.PackMethodPanic(definition.SporkActivateMethodName, id),
	}, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()
	target.SporkId = id
	types.ImplementedSporksMap[id] = true
}

// The pillar worker cranks Update automatically once the rate limit allows
// it, so a manual Update sent straight after is too recent.
func TestSettlement_Update_AutomaticCrankConsumesRateLimit(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	activateOffchainExecution(t, z)
	activateSporkByName(t, z, "spork-accelerator", types.AcceleratorSpork)
	activateSporkByName(t, z, "spork-bridge-liquidity", types.BridgeAndLiquiditySpork)

	before, err := definition.GetLastUpdate(settlementStorage(t, z))
	common.FailIfErr(t, err)
	if before.Height != 0 {
		t.Fatalf("expected no update before the crank, got height %d", before.Height)
	}

	advanceMomentums(t, z, constants.UpdateMinNumMomentums+10)
	advanceMomentums(t, z, 5)

	after, err := definition.GetLastUpdate(settlementStorage(t, z))
	common.FailIfErr(t, err)
	if after.Height == 0 {
		t.Fatal("expected a pillar to have cranked Update")
	}

	settlementCall(t, z, settlementStep(g.User1.Address, definition.SettlementUpdateMethodName), constants.ErrUpdateTooRecent)
}

// Settlement is the last embedded contract the worker visits, so the
// other updatable contracts must be active for the loop to reach it. With
// the offchain-execution spork inactive the worker skips it silently.
func TestSettlement_Update_WorkerSkipsBeforeSpork(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	saveSporkState(t)
	logs := new(bytes.Buffer)
	common.PillarLogger.SetHandler(log15.StreamHandler(logs, log15.LogfmtFormat()))
	activateSporkByName(t, z, "spork-accelerator", types.AcceleratorSpork)
	activateSporkByName(t, z, "spork-bridge-liquidity", types.BridgeAndLiquiditySpork)

	advanceMomentums(t, z, constants.UpdateMinNumMomentums+10)

	out := logs.String()
	if !strings.Contains(out, "contract-address="+types.AcceleratorContract.String()) {
		t.Fatal("expected the worker to reach the Accelerator update")
	}
	if strings.Contains(out, "contract-address="+types.SettlementContract.String()) {
		t.Fatal("worker produced an Update block for Settlement before the spork")
	}
	if strings.Contains(out, "failed to update contracts") {
		t.Fatal("worker failed while updating contracts before the spork")
	}
	last, err := definition.GetLastUpdate(settlementStorage(t, z))
	common.FailIfErr(t, err)
	if last.Height != 0 {
		t.Fatalf("expected no Settlement update before the spork, got height %d", last.Height)
	}
}

func TestSettlement_RegisterDomain_CapsAndSpecChangeUsesLongerDelay(t *testing.T) {
	z := newSettlementZenon(t)
	savedMin := constants.WithdrawalDelayMin
	constants.WithdrawalDelayMin = 10
	constants.MinAdministratorDelay = 10
	t.Cleanup(func() { constants.WithdrawalDelayMin = savedMin })

	oldCaps := valueCapsWithWindow(10, 1)
	newCaps := valueCapsWithWindow(10, 2)
	p := phase1Domain(1)
	p.RuntimeUpgradeDelay = 40
	p.ValueCaps = oldCaps
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), nil)

	bumped := hashFromByteForTest(9)
	p.ValueCaps, p.StfSpecHash = newCaps, bumped
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), nil)

	// past the caps delay but before the runtime upgrade delay
	advanceMomentums(t, z, 20)
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), constants.ErrTimeChallengeNotDue)
	record := domainRecordOf(t, z, 1)
	if !bytes.Equal(record.ValueCaps, oldCaps) || record.StfSpecHash != types.ZeroHash {
		t.Fatalf("expected old caps and hash while pending, got %+v", record)
	}

	advanceMomentums(t, z, 40)
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), nil)
	record = domainRecordOf(t, z, 1)
	if !bytes.Equal(record.ValueCaps, newCaps) || record.StfSpecHash != bumped {
		t.Fatalf("expected new caps and hash, got %+v", record)
	}
}

func TestSettlement_RegisterDomain_StatusCallWithOtherCapsRestartsChallenge(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinAdministratorDelay = 20
	scope := "RegisterDomain/1"
	d := constants.WithdrawalDelayMin

	oldCaps := valueCapsWithWindow(d, 1)
	p := phase1Domain(1)
	p.ValueCaps = oldCaps
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), nil)

	p.ValueCaps = valueCapsWithWindow(d, 2)
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), nil)
	h := challengeHash(t, z, scope)
	if h.IsZero() {
		t.Fatal("expected a pending challenge")
	}

	// a status change carrying caps other than the stored ones is a different
	// change: it applies its status and replaces the pending challenge
	p.ValueCaps, p.Status = valueCapsWithWindow(d, 3), definition.DomainStatusPaused
	settlementCall(t, z, registerDomainStep(g.User5.Address, p), nil)
	if record := domainRecordOf(t, z, 1); record.Status != definition.DomainStatusPaused || !bytes.Equal(record.ValueCaps, oldCaps) {
		t.Fatalf("expected PAUSED with old caps, got %+v", record)
	}
	if got := challengeHash(t, z, scope); got.IsZero() || got == h {
		t.Fatalf("expected a restarted challenge, old %v new %v", h, got)
	}
}
