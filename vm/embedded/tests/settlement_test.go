package tests

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/zenon-network/go-zenon/common/crypto"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/trie"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm"
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
`)
	activateOffchainExecution(t, z)

	defer z.CallContract(&nom.AccountBlock{
		Address:       g.User1.Address,
		ToAddress:     types.SettlementContract,
		Data:          definition.ABISettlement.PackMethodPanic(definition.SettlementReleaseWithdrawalsMethodName, uint32(0)),
		TokenStandard: types.ZnnTokenStandard,
		Amount:        big.NewInt(0),
	}).Error(t, constants.ErrNothingToWithdraw)
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

func depositStep(from types.Address, zts types.ZenonTokenStandard, amount int64, domainId uint32, target types.Hash, plasmaLimit uint64) *nom.AccountBlock {
	block := settlementStep(from, definition.SettlementDepositMethodName, domainId, target, plasmaLimit)
	block.TokenStandard = zts
	block.Amount = big.NewInt(amount)
	return block
}

// configureAsset runs the two SetAssetConfig calls that complete the
// time challenge.
func configureAsset(t *testing.T, z mock.MockZenon, domainId uint32, assetId types.Hash) {
	t.Helper()
	step := setAssetConfigStep(g.User5.Address, domainId, assetId, big.NewInt(1000))
	settlementCall(t, z, step, nil)
	advanceMomentums(t, z, constants.MinSoftDelay)
	settlementCall(t, z, setAssetConfigStep(g.User5.Address, domainId, assetId, big.NewInt(1000)), nil)
}

func inputCounterOf(t *testing.T, z mock.MockZenon) uint64 {
	t.Helper()
	counter, err := definition.GetReceivedInputCounter(settlementStorage(t, z))
	common.FailIfErr(t, err)
	return counter.Count
}

func totalDepositedOf(t *testing.T, z mock.MockZenon, domainId uint32, assetId types.Hash) *big.Int {
	t.Helper()
	counters, err := definition.GetConservationCounters(settlementStorage(t, z), domainId, assetId)
	common.FailIfErr(t, err)
	return counters.TotalDeposited
}

func balanceOf(t *testing.T, z mock.MockZenon, address types.Address, zts types.ZenonTokenStandard) int64 {
	t.Helper()
	amount, err := z.Chain().GetFrontierAccountStore(address).GetBalance(zts)
	common.FailIfErr(t, err)
	if amount == nil {
		return 0
	}
	return amount.Int64()
}

func TestSettlement_Deposit_Custody(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	registerDomains(t, z, 1)
	znnAsset := assetIdFor(types.ZnnTokenStandard)
	configureAsset(t, z, 1, znnAsset)
	target := types.Hash{0x07}
	user := g.User1.Address
	start := balanceOf(t, z, user, types.ZnnTokenStandard)

	settlementCall(t, z, depositStep(user, types.ZnnTokenStandard, 100, 1, target, 5000), nil)
	z.ExpectBalance(user, types.ZnnTokenStandard, start-100)
	if got := totalDepositedOf(t, z, 1, znnAsset); got.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("expected TotalDeposited 100, got %v", got)
	}
	record, err := definition.GetAssignedIndexRecord(settlementStorage(t, z), 1, 0)
	common.FailIfErr(t, err)
	if record.Amount.Cmp(big.NewInt(100)) != 0 || record.AssetId != znnAsset {
		t.Fatalf("unexpected record %+v", record)
	}
	total, err := definition.GetCumulativeDepositTotal(settlementStorage(t, z), 1, znnAsset)
	common.FailIfErr(t, err)
	if total.CumulativeDeposited.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("expected cumulative 100, got %v", total.CumulativeDeposited)
	}
	if got := inputCounterOf(t, z); got != 1 {
		t.Fatalf("expected counter 1, got %d", got)
	}

	// accepted, rejected, accepted: indices stay consecutive
	settlementCall(t, z, depositStep(user, types.QsrTokenStandard, 7, 1, target, 5000), constants.ErrInvalidToken)
	autoreceive(t, z, user)
	settlementCall(t, z, depositStep(user, types.ZnnTokenStandard, 5, 1, target, 5000), nil)
	if got := inputCounterOf(t, z); got != 2 {
		t.Fatalf("expected counter 2, got %d", got)
	}
	record, err = definition.GetAssignedIndexRecord(settlementStorage(t, z), 1, 1)
	common.FailIfErr(t, err)
	if record.Amount.Cmp(big.NewInt(5)) != 0 {
		t.Fatalf("expected index 1 amount 5, got %v", record.Amount)
	}
	if _, err := definition.GetAssignedIndexRecord(settlementStorage(t, z), 1, 2); err != constants.ErrDataNonExistent {
		t.Fatalf("expected no record at index 2, got %v", err)
	}
}

func TestSettlement_Deposit_Rejections(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	registerDomains(t, z, 1, 2)
	znnAsset := assetIdFor(types.ZnnTokenStandard)
	configureAsset(t, z, 1, znnAsset)
	target := types.Hash{0x07}
	user := g.User1.Address
	start := balanceOf(t, z, user, types.ZnnTokenStandard)
	startQsr := balanceOf(t, z, user, types.QsrTokenStandard)

	expectUntouched := func() {
		t.Helper()
		autoreceive(t, z, user)
		z.ExpectBalance(user, types.ZnnTokenStandard, start)
		z.ExpectBalance(user, types.QsrTokenStandard, startQsr)
		if got := inputCounterOf(t, z); got != 0 {
			t.Fatalf("expected counter 0, got %d", got)
		}
		if got := totalDepositedOf(t, z, 1, znnAsset); got.Sign() != 0 {
			t.Fatalf("expected TotalDeposited 0, got %v", got)
		}
		if _, err := definition.GetAssignedIndexRecord(settlementStorage(t, z), 1, 0); err != constants.ErrDataNonExistent {
			t.Fatalf("expected no record, got %v", err)
		}
	}

	// unconfigured asset
	settlementCall(t, z, depositStep(user, types.QsrTokenStandard, 7, 1, target, 5000), constants.ErrInvalidToken)
	expectUntouched()

	// unregistered domain
	settlementCall(t, z, depositStep(user, types.ZnnTokenStandard, 7, 9, target, 5000), constants.ErrForbiddenParam)
	expectUntouched()

	// an asset configured for domain 1 only
	settlementCall(t, z, depositStep(user, types.ZnnTokenStandard, 7, 2, target, 5000), constants.ErrInvalidToken)
	expectUntouched()

	// a SetAssetConfig challenge that has started but not completed
	qsrAsset := assetIdFor(types.QsrTokenStandard)
	settlementCall(t, z, setAssetConfigStep(g.User5.Address, 1, qsrAsset, big.NewInt(1000)), nil)
	settlementCall(t, z, depositStep(user, types.QsrTokenStandard, 7, 1, target, 5000), constants.ErrInvalidToken)
	expectUntouched()

	// a paused domain
	paused := phase1Domain(1)
	paused.Status = definition.DomainStatusPaused
	settlementCall(t, z, registerDomainStep(g.User5.Address, paused), nil)
	settlementCall(t, z, depositStep(user, types.ZnnTokenStandard, 7, 1, target, 5000), constants.ErrForbiddenParam)
	expectUntouched()

	settlementCall(t, z, registerDomainStep(g.User5.Address, phase1Domain(1)), nil)
	settlementCall(t, z, depositStep(user, types.ZnnTokenStandard, 7, 1, target, 5000), nil)
	if got := inputCounterOf(t, z); got != 1 {
		t.Fatalf("expected counter 1, got %d", got)
	}
	if _, err := definition.GetAssignedIndexRecord(settlementStorage(t, z), 1, 0); err != nil {
		t.Fatalf("expected record at index 0, got %v", err)
	}
}

func TestSettlement_Deposit_SendValidation(t *testing.T) {
	z := newSettlementZenon(t)
	target := types.Hash{0x07}

	z.InsertSendBlock(depositStep(g.User1.Address, types.ZnnTokenStandard, 0, 1, target, 5000), constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertNewMomentum()

	z.InsertSendBlock(depositStep(g.User1.Address, types.ZnnTokenStandard, 1, 1, target, constants.MaxInputGas+1), constants.ErrForbiddenParam, mock.SkipVmChanges)
	z.InsertNewMomentum()
}

func TestSettlement_Deposit_IgnoresPauseScopes(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	registerDomains(t, z, 1)
	configureAsset(t, z, 1, assetIdFor(types.ZnnTokenStandard))

	settlementCall(t, z, pauseStep(g.User5.Address, 0, true, definition.PauseScopeSubmit, true), nil)
	settlementCall(t, z, pauseStep(g.User5.Address, 0, true, definition.PauseScopeRelay, true), nil)
	settlementCall(t, z, depositStep(g.User1.Address, types.ZnnTokenStandard, 3, 1, types.Hash{0x07}, 5000), nil)
	if got := inputCounterOf(t, z); got != 1 {
		t.Fatalf("expected counter 1, got %d", got)
	}
}

// ============================================================================
// Chunked contract deployment
// ============================================================================

const deployDomain = uint32(1)

// deployPayload returns n deterministic bytes.
func deployPayload(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i*7 + 3)
	}
	return out
}

func deployChunkCount(totalSize uint64) uint32 {
	return uint32((totalSize + constants.MaxDeployChunkData - 1) / constants.MaxDeployChunkData)
}

func deployStartStep(from types.Address, domainId uint32, codeHash types.Hash, totalSize uint64, chunkCount uint32, metadata []byte) *nom.AccountBlock {
	return settlementStep(from, definition.SettlementDeployContractStartMethodName,
		domainId, codeHash, types.NewHash(metadata), totalSize, chunkCount, metadata)
}

func deployChunkStep(from types.Address, domainId uint32, codeHash types.Hash, index uint32, data []byte) *nom.AccountBlock {
	return settlementStep(from, definition.SettlementDeployContractChunkMethodName,
		domainId, codeHash, index, types.NewHash(data), data)
}

func deployFinalizeStep(from types.Address, domainId uint32, codeHash types.Hash, zts types.ZenonTokenStandard, amount int64) *nom.AccountBlock {
	block := settlementStep(from, definition.SettlementDeployContractFinalizeMethodName, domainId, codeHash)
	block.TokenStandard = zts
	block.Amount = big.NewInt(amount)
	return block
}

// startDeploy runs an accepted DeployContractStart for a payload of
// totalSize bytes.
func startDeploy(t *testing.T, z mock.MockZenon, from types.Address, codeHash types.Hash, totalSize uint64) {
	t.Helper()
	settlementCall(t, z, deployStartStep(from, deployDomain, codeHash, totalSize, deployChunkCount(totalSize), nil), nil)
}

// sendChunks runs accepted DeployContractChunk calls for the listed
// indices of payload, in the listed order.
func sendChunks(t *testing.T, z mock.MockZenon, from types.Address, codeHash types.Hash, payload []byte, order ...uint32) {
	t.Helper()
	for _, i := range order {
		settlementCall(t, z, deployChunkStep(from, deployDomain, codeHash, i, deployChunkData(payload, i)), nil)
	}
}

func deployChunkData(payload []byte, index uint32) []byte {
	start := int(index) * constants.MaxDeployChunkData
	end := start + constants.MaxDeployChunkData
	if end > len(payload) {
		end = len(payload)
	}
	return payload[start:end]
}

func deployRecordOf(t *testing.T, z mock.MockZenon, deployer types.Address, codeHash types.Hash) (*definition.DeployRecord, error) {
	t.Helper()
	return definition.GetDeployRecord(settlementStorage(t, z), deployDomain, deployer, codeHash)
}

func expectDeployRecord(t *testing.T, z mock.MockZenon, deployer types.Address, codeHash types.Hash) *definition.DeployRecord {
	t.Helper()
	record, err := deployRecordOf(t, z, deployer, codeHash)
	if err != nil {
		t.Fatalf("expected a deploy record, got %v", err)
	}
	return record
}

func expectNoDeployRecord(t *testing.T, z mock.MockZenon, deployer types.Address, codeHash types.Hash) {
	t.Helper()
	if _, err := deployRecordOf(t, z, deployer, codeHash); err != constants.ErrDataNonExistent {
		t.Fatalf("expected no deploy record, got %v", err)
	}
}

func deployChunkOf(t *testing.T, z mock.MockZenon, deployer types.Address, codeHash types.Hash, startHeight uint64, index uint32) (*definition.DeployChunk, error) {
	t.Helper()
	key := definition.DeployChunkKey{
		DeployRecordKey: definition.DeployRecordKey{DomainId: deployDomain, Deployer: deployer, CodeHash: codeHash},
		StartHeight:     startHeight,
		ChunkIndex:      index,
	}
	return definition.GetDeployChunk(settlementStorage(t, z), key)
}

func expectDeployChunk(t *testing.T, z mock.MockZenon, deployer types.Address, codeHash types.Hash, startHeight uint64, index uint32, want types.Hash) {
	t.Helper()
	chunk, err := deployChunkOf(t, z, deployer, codeHash, startHeight, index)
	if err != nil {
		t.Fatalf("expected chunk entry %d at start height %d, got %v", index, startHeight, err)
	}
	if chunk.ChunkHash != want {
		t.Fatalf("chunk entry %d holds %s, want %s", index, chunk.ChunkHash, want)
	}
}

func expectNoDeployChunk(t *testing.T, z mock.MockZenon, deployer types.Address, codeHash types.Hash, startHeight uint64, index uint32) {
	t.Helper()
	if _, err := deployChunkOf(t, z, deployer, codeHash, startHeight, index); err != constants.ErrDataNonExistent {
		t.Fatalf("expected no chunk entry %d at start height %d, got %v", index, startHeight, err)
	}
}

func expectStartQueue(t *testing.T, z mock.MockZenon, head, tail uint64) {
	t.Helper()
	meta, err := definition.GetDeployStartQueueMeta(settlementStorage(t, z))
	common.FailIfErr(t, err)
	if meta.Head != head || meta.Tail != tail {
		t.Fatalf("expected start queue head %d tail %d, got head %d tail %d", head, tail, meta.Head, meta.Tail)
	}
}

func expectCounter(t *testing.T, z mock.MockZenon, want uint64) {
	t.Helper()
	if got := inputCounterOf(t, z); got != want {
		t.Fatalf("expected input counter %d, got %d", want, got)
	}
}

func feesCollectedOf(t *testing.T, z mock.MockZenon) *big.Int {
	t.Helper()
	counters, err := definition.GetFeeCounters(settlementStorage(t, z))
	common.FailIfErr(t, err)
	return counters.TotalFeesCollected
}

func expectFeesCollected(t *testing.T, z mock.MockZenon, want int64) {
	t.Helper()
	if got := feesCollectedOf(t, z); got.Cmp(big.NewInt(want)) != 0 {
		t.Fatalf("expected TotalFeesCollected %d, got %v", want, got)
	}
}

// setBytecodePrice stores a Periphery config through both calls of its
// time challenge.
func setBytecodePrice(t *testing.T, z mock.MockZenon, price int64) {
	t.Helper()
	constants.MinSoftDelay = 10
	settlementCall(t, z, setPeripheryConfigStep(g.User5.Address, big.NewInt(price), big.NewInt(1)), nil)
	advanceMomentums(t, z, constants.MinSoftDelay)
	settlementCall(t, z, setPeripheryConfigStep(g.User5.Address, big.NewInt(price), big.NewInt(1)), nil)
}

// newDeployZenon starts a settlement chain with domain 1 registered and
// a bytecode price of 3 ZNN base units per byte.
func newDeployZenon(t *testing.T) mock.MockZenon {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	registerDomains(t, z, deployDomain)
	setBytecodePrice(t, z, 3)
	return z
}

var deployCodeHash = types.Hash{0xC0, 0xDE}

func TestSettlement_Deploy_MultiChunkFlow(t *testing.T) {
	z := newDeployZenon(t)
	user := g.User1.Address
	total := uint64(constants.MaxDeployChunkData + 100)
	payload := deployPayload(int(total))
	fee := int64(total) * 3
	znnAsset := assetIdFor(types.ZnnTokenStandard)
	startBalance := balanceOf(t, z, user, types.ZnnTokenStandard)

	startDeploy(t, z, user, deployCodeHash, total)
	record := expectDeployRecord(t, z, user, deployCodeHash)
	if record.TotalSize != total || record.ChunkCount != 2 || record.ReceivedCount != 0 {
		t.Fatalf("unexpected record %+v", record)
	}
	sendChunks(t, z, user, deployCodeHash, payload, 1, 0)
	record = expectDeployRecord(t, z, user, deployCodeHash)
	if record.ReceivedCount != 2 {
		t.Fatalf("expected ReceivedCount 2, got %d", record.ReceivedCount)
	}
	for i := uint32(0); i < 2; i++ {
		expectDeployChunk(t, z, user, deployCodeHash, record.StartHeight, i, types.NewHash(deployChunkData(payload, i)))
	}
	expectCounter(t, z, 3)

	settlementCall(t, z, deployFinalizeStep(user, deployDomain, deployCodeHash, types.ZnnTokenStandard, fee), nil)
	expectNoDeployRecord(t, z, user, deployCodeHash)
	expectNoDeployChunk(t, z, user, deployCodeHash, record.StartHeight, 0)
	expectNoDeployChunk(t, z, user, deployCodeHash, record.StartHeight, 1)
	expectCounter(t, z, 4)
	assigned, err := definition.GetAssignedIndexRecord(settlementStorage(t, z), deployDomain, 3)
	common.FailIfErr(t, err)
	if assigned.Fee == nil || assigned.Fee.Cmp(big.NewInt(fee)) != 0 {
		t.Fatalf("expected assigned record fee %d, got %v", fee, assigned.Fee)
	}
	expectFeesCollected(t, z, fee)
	z.ExpectBalance(user, types.ZnnTokenStandard, startBalance-fee)
	counters, err := definition.GetConservationCounters(settlementStorage(t, z), deployDomain, znnAsset)
	common.FailIfErr(t, err)
	if counters.TotalDeposited.Sign() != 0 || counters.PendingWithdrawalReserve.Sign() != 0 || counters.TotalReleased.Sign() != 0 {
		t.Fatalf("expected untouched conservation counters, got %+v", counters)
	}
	cumulative, err := definition.GetCumulativeDepositTotal(settlementStorage(t, z), deployDomain, znnAsset)
	common.FailIfErr(t, err)
	if cumulative.CumulativeDeposited.Sign() != 0 {
		t.Fatalf("expected untouched cumulative total, got %v", cumulative.CumulativeDeposited)
	}

	// a consumed record frees its key
	settlementCall(t, z, deployFinalizeStep(user, deployDomain, deployCodeHash, types.ZnnTokenStandard, fee), constants.ErrDataNonExistent)
	startDeploy(t, z, user, deployCodeHash, total)
	expectDeployRecord(t, z, user, deployCodeHash)
}

func TestSettlement_Deploy_SingleChunkExactSize(t *testing.T) {
	z := newDeployZenon(t)
	user := g.User1.Address
	payload := deployPayload(100)

	startDeploy(t, z, user, deployCodeHash, 100)
	sendChunks(t, z, user, deployCodeHash, payload, 0)
	settlementCall(t, z, deployFinalizeStep(user, deployDomain, deployCodeHash, types.ZnnTokenStandard, 300), nil)
	expectNoDeployRecord(t, z, user, deployCodeHash)
	expectFeesCollected(t, z, 300)
}

func TestSettlement_Deploy_FullMultipleOfChunkSize(t *testing.T) {
	z := newDeployZenon(t)
	user := g.User1.Address
	total := uint64(2 * constants.MaxDeployChunkData)
	payload := deployPayload(int(total))

	startDeploy(t, z, user, deployCodeHash, total)
	sendChunks(t, z, user, deployCodeHash, payload, 0, 1)
	settlementCall(t, z, deployFinalizeStep(user, deployDomain, deployCodeHash, types.ZnnTokenStandard, int64(total)*3), nil)
	expectNoDeployRecord(t, z, user, deployCodeHash)
}

func TestSettlement_Deploy_MaxCodeSize(t *testing.T) {
	z := newDeployZenon(t)
	user := g.User1.Address
	total := uint64(constants.MaxCodeSize)
	chunks := deployChunkCount(total)
	if chunks > constants.MaxChunkCount {
		t.Fatalf("max code size needs %d chunks, above the %d limit", chunks, constants.MaxChunkCount)
	}
	startDeploy(t, z, user, deployCodeHash, total)
	record := expectDeployRecord(t, z, user, deployCodeHash)
	if record.ChunkCount != chunks {
		t.Fatalf("expected %d chunks, got %d", chunks, record.ChunkCount)
	}
}

func TestSettlement_Deploy_FeeRejections(t *testing.T) {
	z := newDeployZenon(t)
	user := g.User1.Address
	total := uint64(100)
	fee := int64(300)
	payload := deployPayload(int(total))
	startDeploy(t, z, user, deployCodeHash, total)
	sendChunks(t, z, user, deployCodeHash, payload, 0)
	startBalance := balanceOf(t, z, user, types.ZnnTokenStandard)
	startQsr := balanceOf(t, z, user, types.QsrTokenStandard)
	startHeight := expectDeployRecord(t, z, user, deployCodeHash).StartHeight

	// send validation
	z.InsertSendBlock(deployFinalizeStep(user, deployDomain, deployCodeHash, types.ZnnTokenStandard, 0), constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertNewMomentum()
	z.InsertSendBlock(deployFinalizeStep(user, deployDomain, deployCodeHash, types.QsrTokenStandard, fee), constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertNewMomentum()

	// receive validation
	for _, amount := range []int64{fee - 1, fee + 1} {
		settlementCall(t, z, deployFinalizeStep(user, deployDomain, deployCodeHash, types.ZnnTokenStandard, amount), constants.ErrInvalidTokenOrAmount)
		autoreceive(t, z, user)
		z.ExpectBalance(user, types.ZnnTokenStandard, startBalance)
		z.ExpectBalance(user, types.QsrTokenStandard, startQsr)
		expectDeployRecord(t, z, user, deployCodeHash)
		expectDeployChunk(t, z, user, deployCodeHash, startHeight, 0, types.NewHash(payload))
		expectCounter(t, z, 2)
		expectFeesCollected(t, z, 0)
	}

	settlementCall(t, z, deployFinalizeStep(user, deployDomain, deployCodeHash, types.ZnnTokenStandard, fee), nil)
	expectFeesCollected(t, z, fee)
}

func TestSettlement_Deploy_FeeKeptWhenChunksDoNotMatchCodeHash(t *testing.T) {
	z := newDeployZenon(t)
	user := g.User1.Address
	mismatched := types.Hash{0xEE}

	startDeploy(t, z, user, mismatched, 100)
	sendChunks(t, z, user, mismatched, deployPayload(100), 0)
	settlementCall(t, z, deployFinalizeStep(user, deployDomain, mismatched, types.ZnnTokenStandard, 300), nil)
	expectNoDeployRecord(t, z, user, mismatched)
	expectFeesCollected(t, z, 300)
}

func TestSettlement_Deploy_StartRejections(t *testing.T) {
	z := newDeployZenon(t)
	user := g.User1.Address
	total := uint64(100)

	// duplicate live record from the same deployer
	startDeploy(t, z, user, deployCodeHash, total)
	settlementCall(t, z, deployStartStep(user, deployDomain, deployCodeHash, total, 1, nil), constants.ErrForbiddenParam)
	expectCounter(t, z, 1)

	// unregistered domain
	settlementCall(t, z, deployStartStep(user, 9, deployCodeHash, total, 1, nil), constants.ErrForbiddenParam)
	expectCounter(t, z, 1)

	// paused domain
	registerDomains(t, z, 2)
	paused := phase1Domain(2)
	paused.Status = definition.DomainStatusPaused
	settlementCall(t, z, registerDomainStep(g.User5.Address, paused), nil)
	settlementCall(t, z, deployStartStep(user, 2, deployCodeHash, total, 1, nil), constants.ErrForbiddenParam)
	expectCounter(t, z, 1)

	// send validation
	other := types.Hash{0xAB}
	badMetadataHash := settlementStep(user, definition.SettlementDeployContractStartMethodName,
		deployDomain, other, types.Hash{0x01}, total, uint32(1), []byte{})
	longMetadata := deployPayload(constants.MaxContractMetadataSize + 1)
	for name, block := range map[string]*nom.AccountBlock{
		"metadata hash mismatch": badMetadataHash,
		"metadata too long":      deployStartStep(user, deployDomain, other, total, 1, longMetadata),
		"zero total size":        deployStartStep(user, deployDomain, other, 0, 0, nil),
		"total size too large":   deployStartStep(user, deployDomain, other, constants.MaxCodeSize+1, deployChunkCount(constants.MaxCodeSize+1), nil),
		"chunk count too low":    deployStartStep(user, deployDomain, other, constants.MaxDeployChunkData+1, 1, nil),
		"chunk count too high":   deployStartStep(user, deployDomain, other, total, 2, nil),
		"chunk count zero":       deployStartStep(user, deployDomain, other, total, 0, nil),
	} {
		t.Run(name, func(t *testing.T) { settlementRejectedAtSend(z, block) })
	}
	maxMetadata := deployPayload(constants.MaxContractMetadataSize)
	settlementCall(t, z, deployStartStep(user, deployDomain, other, total, 1, maxMetadata), nil)
	expectCounter(t, z, 2)

	withAmount := deployStartStep(user, deployDomain, types.Hash{0xAC}, total, 1, nil)
	withAmount.Amount = big.NewInt(1)
	z.InsertSendBlock(withAmount, constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertNewMomentum()
	expectCounter(t, z, 2)
}

func TestSettlement_Deploy_ChunkRejections(t *testing.T) {
	z := newDeployZenon(t)
	user := g.User1.Address
	total := uint64(constants.MaxDeployChunkData + 100)
	payload := deployPayload(int(total))
	chunk0 := deployChunkData(payload, 0)
	chunk1 := deployChunkData(payload, 1)

	// absent record
	settlementCall(t, z, deployChunkStep(user, deployDomain, deployCodeHash, 0, chunk0), constants.ErrDataNonExistent)
	expectCounter(t, z, 0)

	startDeploy(t, z, user, deployCodeHash, total)
	expectCounter(t, z, 1)

	// a chunk from another address looks up its own key
	settlementCall(t, z, deployChunkStep(g.User2.Address, deployDomain, deployCodeHash, 0, chunk0), constants.ErrDataNonExistent)
	record := expectDeployRecord(t, z, user, deployCodeHash)
	if record.ReceivedCount != 0 {
		t.Fatalf("expected the deployer's ReceivedCount 0, got %d", record.ReceivedCount)
	}
	expectNoDeployChunk(t, z, user, deployCodeHash, record.StartHeight, 0)
	expectCounter(t, z, 1)

	for _, tc := range []struct {
		index uint32
		data  []byte
	}{
		{2, chunk0},                 // index equals chunk count
		{0, chunk0[:len(chunk0)-1]}, // non-last chunk too short
		{1, append(append([]byte{}, chunk1...), 0x01)}, // last chunk too long
		{1, chunk1[:len(chunk1)-1]},                    // last chunk too short
		{0, chunk1},                                    // non-last chunk with the last chunk's length
	} {
		settlementCall(t, z, deployChunkStep(user, deployDomain, deployCodeHash, tc.index, tc.data), constants.ErrForbiddenParam)
		autoreceive(t, z, user)
		expectCounter(t, z, 1)
	}
	record = expectDeployRecord(t, z, user, deployCodeHash)
	if record.ReceivedCount != 0 {
		t.Fatalf("expected ReceivedCount 0, got %d", record.ReceivedCount)
	}
	expectNoDeployChunk(t, z, user, deployCodeHash, record.StartHeight, 0)
	expectNoDeployChunk(t, z, user, deployCodeHash, record.StartHeight, 1)

	// duplicate index
	sendChunks(t, z, user, deployCodeHash, payload, 0)
	expectCounter(t, z, 2)
	settlementCall(t, z, deployChunkStep(user, deployDomain, deployCodeHash, 0, chunk0), constants.ErrForbiddenParam)
	expectCounter(t, z, 2)
	if got := expectDeployRecord(t, z, user, deployCodeHash).ReceivedCount; got != 1 {
		t.Fatalf("expected ReceivedCount 1 after the rejected duplicate, got %d", got)
	}

	// send validation
	badHash := settlementStep(user, definition.SettlementDeployContractChunkMethodName,
		deployDomain, deployCodeHash, uint32(1), types.Hash{0x01}, chunk1)
	settlementRejectedAtSend(z, badHash)
	oversize := deployPayload(constants.MaxDeployChunkData + 1)
	settlementRejectedAtSend(z, deployChunkStep(user, deployDomain, deployCodeHash, 1, oversize))
	settlementRejectedAtSend(z, deployChunkStep(user, deployDomain, deployCodeHash, 1, []byte{}))
	withAmount := deployChunkStep(user, deployDomain, deployCodeHash, 1, chunk1)
	withAmount.Amount = big.NewInt(1)
	z.InsertSendBlock(withAmount, constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertNewMomentum()
	expectCounter(t, z, 2)
}

func TestSettlement_Deploy_FinalizeRejections(t *testing.T) {
	z := newDeployZenon(t)
	user := g.User1.Address
	total := uint64(constants.MaxDeployChunkData + 100)
	payload := deployPayload(int(total))
	fee := int64(total) * 3

	startDeploy(t, z, user, deployCodeHash, total)
	sendChunks(t, z, user, deployCodeHash, payload, 1)
	settlementCall(t, z, deployFinalizeStep(user, deployDomain, deployCodeHash, types.ZnnTokenStandard, fee), constants.ErrForbiddenParam)
	autoreceive(t, z, user)
	expectDeployRecord(t, z, user, deployCodeHash)
	expectCounter(t, z, 2)
	expectFeesCollected(t, z, 0)

	sendChunks(t, z, user, deployCodeHash, payload, 0)
	settlementCall(t, z, deployFinalizeStep(g.User2.Address, deployDomain, deployCodeHash, types.ZnnTokenStandard, fee), constants.ErrDataNonExistent)
	expectDeployRecord(t, z, user, deployCodeHash)
	expectCounter(t, z, 3)
	expectFeesCollected(t, z, 0)

	settlementCall(t, z, deployFinalizeStep(user, deployDomain, deployCodeHash, types.ZnnTokenStandard, fee), nil)
	settlementCall(t, z, deployFinalizeStep(user, deployDomain, deployCodeHash, types.ZnnTokenStandard, fee), constants.ErrDataNonExistent)
	expectCounter(t, z, 4)
	expectFeesCollected(t, z, fee)
}

func TestSettlement_Deploy_FinalizeWithoutPeripheryConfig(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	registerDomains(t, z, deployDomain)
	user := g.User1.Address
	startBalance := balanceOf(t, z, user, types.ZnnTokenStandard)

	startDeploy(t, z, user, deployCodeHash, 100)
	sendChunks(t, z, user, deployCodeHash, deployPayload(100), 0)
	settlementCall(t, z, deployFinalizeStep(user, deployDomain, deployCodeHash, types.ZnnTokenStandard, 300), constants.ErrDataNonExistent)
	autoreceive(t, z, user)
	z.ExpectBalance(user, types.ZnnTokenStandard, startBalance)
	expectDeployRecord(t, z, user, deployCodeHash)
	expectCounter(t, z, 2)
	expectFeesCollected(t, z, 0)
}

// The expiry window spans enough momentums to trigger the periodic
// contract updates, whose debug logs the shared setup would reject.
func newDeployExpiryZenon(t *testing.T) mock.MockZenon {
	z := mock.NewMockZenon(t)
	t.Cleanup(z.StopPanic)
	activateOffchainExecution(t, z)
	saveBridgeConstants(t)
	savedAdmin := types.NetworkAdmin
	types.NetworkAdmin = g.User5.Address
	t.Cleanup(func() { types.NetworkAdmin = savedAdmin })
	registerDomains(t, z, deployDomain)
	setBytecodePrice(t, z, 3)
	return z
}

func TestSettlement_Deploy_Expiry(t *testing.T) {
	z := newDeployExpiryZenon(t)
	user := g.User1.Address
	payload := deployPayload(100)

	startDeploy(t, z, user, deployCodeHash, 100)
	first := expectDeployRecord(t, z, user, deployCodeHash)
	sendChunks(t, z, user, deployCodeHash, payload, 0)
	expectCounter(t, z, 2)

	advanceMomentums(t, z, constants.DeploymentExpiryWindow)
	settlementCall(t, z, deployChunkStep(user, deployDomain, deployCodeHash, 0, payload), constants.ErrDataNonExistent)
	settlementCall(t, z, deployFinalizeStep(user, deployDomain, deployCodeHash, types.ZnnTokenStandard, 300), constants.ErrDataNonExistent)
	settlementCall(t, z, upgradeContractStep(user, deployDomain, types.Hash{0x01}, deployCodeHash, types.ZnnTokenStandard, 300), constants.ErrDataNonExistent)
	expectCounter(t, z, 2)
	// an expired record stays stored until a start sweeps it
	expectDeployRecord(t, z, user, deployCodeHash)

	startDeploy(t, z, user, deployCodeHash, 100)
	second := expectDeployRecord(t, z, user, deployCodeHash)
	if second.StartHeight <= first.StartHeight+constants.DeploymentExpiryWindow-1 {
		t.Fatalf("expected a fresh start height, got %d after %d", second.StartHeight, first.StartHeight)
	}
	if second.ReceivedCount != 0 {
		t.Fatalf("expected ReceivedCount 0 after the restart, got %d", second.ReceivedCount)
	}
	expectNoDeployChunk(t, z, user, deployCodeHash, second.StartHeight, 0)
	expectNoDeployChunk(t, z, user, deployCodeHash, first.StartHeight, 0)
	expectStartQueue(t, z, 1, 2)
	expectCounter(t, z, 3)
}

func TestSettlement_Deploy_ExpirySweep(t *testing.T) {
	z := newDeployExpiryZenon(t)
	u1, u2 := g.User1.Address, g.User2.Address
	payload := deployPayload(100)
	chunkHash := types.NewHash(payload)
	x1, x2, f, y := types.Hash{0xA1}, types.Hash{0xA2}, types.Hash{0xA3}, types.Hash{0xA4}
	k := deployCodeHash

	startDeploy(t, z, u1, x1, 100)
	x1Start := expectDeployRecord(t, z, u1, x1).StartHeight
	sendChunks(t, z, u1, x1, payload, 0)
	startDeploy(t, z, u1, x2, 100)
	startDeploy(t, z, u1, k, 100)
	kOld := expectDeployRecord(t, z, u1, k).StartHeight
	sendChunks(t, z, u1, k, payload, 0)
	startDeploy(t, z, u2, f, 100)
	fStart := expectDeployRecord(t, z, u2, f).StartHeight
	sendChunks(t, z, u2, f, payload, 0)
	settlementCall(t, z, deployFinalizeStep(u2, deployDomain, f, types.ZnnTokenStandard, 300), nil)
	expectCounter(t, z, 8)
	expectStartQueue(t, z, 0, 4)

	advanceMomentums(t, z, constants.DeploymentExpiryWindow)
	settlementCall(t, z, deployStartStep(u1, 9, k, 100, 1, nil), constants.ErrForbiddenParam)
	expectStartQueue(t, z, 0, 4)
	expectDeployRecord(t, z, u1, x1)

	startDeploy(t, z, u1, k, 100)
	expectCounter(t, z, 9)
	expectNoDeployRecord(t, z, u1, x1)
	expectNoDeployChunk(t, z, u1, x1, x1Start, 0)
	expectNoDeployRecord(t, z, u1, x2)
	kNew := expectDeployRecord(t, z, u1, k)
	if kNew.StartHeight <= kOld || kNew.ReceivedCount != 0 {
		t.Fatalf("unexpected restarted record %+v after start height %d", kNew, kOld)
	}
	expectDeployChunk(t, z, u1, k, kOld, 0, chunkHash)
	expectStartQueue(t, z, 2, 5)

	sendChunks(t, z, u1, k, payload, 0)
	expectCounter(t, z, 10)
	if got := expectDeployRecord(t, z, u1, k).ReceivedCount; got != 1 {
		t.Fatalf("expected ReceivedCount 1, got %d", got)
	}

	startDeploy(t, z, u2, y, 100)
	expectCounter(t, z, 11)
	kAfter := expectDeployRecord(t, z, u1, k)
	if kAfter.StartHeight != kNew.StartHeight || kAfter.ReceivedCount != 1 {
		t.Fatalf("unexpected record %+v after the sweep of the predecessor's entry", kAfter)
	}
	expectDeployChunk(t, z, u1, k, kNew.StartHeight, 0, chunkHash)
	expectNoDeployChunk(t, z, u1, k, kOld, 0)
	expectNoDeployRecord(t, z, u2, f)
	expectNoDeployChunk(t, z, u2, f, fStart, 0)
	expectStartQueue(t, z, 4, 6)

	settlementCall(t, z, deployFinalizeStep(u1, deployDomain, k, types.ZnnTokenStandard, 300), nil)
	expectCounter(t, z, 12)
	expectNoDeployRecord(t, z, u1, k)
	expectNoDeployChunk(t, z, u1, k, kNew.StartHeight, 0)
}

func TestSettlement_Deploy_IgnoresPauseScopes(t *testing.T) {
	z := newDeployZenon(t)
	user := g.User1.Address
	payload := deployPayload(100)

	settlementCall(t, z, pauseStep(g.User5.Address, 0, true, definition.PauseScopeSubmit, true), nil)
	settlementCall(t, z, pauseStep(g.User5.Address, 0, true, definition.PauseScopeRelay, true), nil)
	startDeploy(t, z, user, deployCodeHash, 100)
	sendChunks(t, z, user, deployCodeHash, payload, 0)
	settlementCall(t, z, deployFinalizeStep(user, deployDomain, deployCodeHash, types.ZnnTokenStandard, 300), nil)
	expectCounter(t, z, 3)
}

func TestSettlement_Deploy_SameCodeHashTwoDeployers(t *testing.T) {
	z := newDeployZenon(t)
	payload := deployPayload(100)

	startDeploy(t, z, g.User1.Address, deployCodeHash, 100)
	sendChunks(t, z, g.User1.Address, deployCodeHash, payload, 0)
	user1Record := expectDeployRecord(t, z, g.User1.Address, deployCodeHash)
	expectCounter(t, z, 2)

	startDeploy(t, z, g.User2.Address, deployCodeHash, 100)
	expectCounter(t, z, 3)

	after1 := expectDeployRecord(t, z, g.User1.Address, deployCodeHash)
	after2 := expectDeployRecord(t, z, g.User2.Address, deployCodeHash)
	if after1.StartHeight != user1Record.StartHeight {
		t.Fatalf("expected the first deployer's start height to stay %d, got %d", user1Record.StartHeight, after1.StartHeight)
	}
	expectDeployChunk(t, z, g.User1.Address, deployCodeHash, after1.StartHeight, 0, types.NewHash(payload))
	if after2.ReceivedCount != 0 {
		t.Fatalf("expected ReceivedCount 0 for the second deployer, got %d", after2.ReceivedCount)
	}
	expectNoDeployChunk(t, z, g.User2.Address, deployCodeHash, after2.StartHeight, 0)
	if after2.Deployer != g.User2.Address || after1.Deployer != g.User1.Address {
		t.Fatalf("records carry the wrong deployer")
	}
}

func TestSettlement_Deploy_PlasmaPricing(t *testing.T) {
	z := newDeployZenon(t)
	user := g.User1.Address
	full := deployPayload(constants.MaxDeployChunkData)

	start := z.InsertSendBlock(deployStartStep(user, deployDomain, deployCodeHash, 100, 1, nil), nil, mock.SkipVmChanges)
	if start.BasePlasma != uint64(constants.EmbeddedSimplePlasma) {
		t.Fatalf("expected start base plasma %d, got %d", uint64(constants.EmbeddedSimplePlasma), start.BasePlasma)
	}
	z.InsertNewMomentum()

	chunk := z.InsertSendBlock(deployChunkStep(user, deployDomain, deployCodeHash, 0, full), nil, mock.SkipVmChanges)
	want := uint64(constants.AccountBlockBasePlasma + constants.ABByteDataPlasma*len(chunk.Data))
	if len(chunk.Data) != 15556 || chunk.BasePlasma != 1078808 || chunk.BasePlasma != want {
		t.Fatalf("expected chunk base plasma 1078808 for 15556 bytes, got %d for %d bytes", chunk.BasePlasma, len(chunk.Data))
	}
	z.InsertNewMomentum()
	stored, err := z.Chain().GetFrontierMomentumStore().GetAccountBlockByHash(chunk.Hash)
	common.FailIfErr(t, err)
	got, err := vm.CanonicalBasePlasma(z.Chain(), stored)
	common.FailIfErr(t, err)
	if got != chunk.BasePlasma {
		t.Fatalf("expected canonical base plasma %d, got %d", chunk.BasePlasma, got)
	}
	z.InsertNewMomentum()
}

func TestSettlement_Deploy_ChunkSendsExhaustPlasma(t *testing.T) {
	z := newDeployZenon(t)
	user := g.User1.Address
	full := deployPayload(constants.MaxDeployChunkData)

	z.InsertNewMomentum()
	for i := 0; i < 9; i++ {
		z.InsertSendBlock(deployChunkStep(user, deployDomain, deployCodeHash, 0, full), nil, mock.SkipVmChanges)
	}
	z.InsertSendBlock(deployChunkStep(user, deployDomain, deployCodeHash, 0, full), constants.ErrNotEnoughPlasma, mock.SkipVmChanges)
	z.InsertNewMomentum()
	z.InsertNewMomentum()
}

// ============================================================================
// SubmitBatch
// ============================================================================

// batchCommitment encodes a commitment with every version field 1, the
// wire height 0 and the given asset flow summary.
func batchCommitment(domainId uint32, batchId, first, last uint64, pre, post types.Hash, executor types.Address, summary []byte) []byte {
	var out []byte
	for i := 0; i < 9; i++ {
		out = binary.BigEndian.AppendUint16(out, 1)
	}
	out = binary.BigEndian.AppendUint32(out, domainId)
	out = binary.BigEndian.AppendUint64(out, batchId)
	out = binary.BigEndian.AppendUint64(out, first)
	out = binary.BigEndian.AppendUint64(out, last)
	out = append(out, pre.Bytes()...)
	out = append(out, post.Bytes()...)
	for i := 0; i < 5; i++ {
		out = append(out, hashFromByteForTest(byte(0x30+i)).Bytes()...)
	}
	out = append(out, 0)
	out = append(out, executor.Bytes()...)
	out = binary.BigEndian.AppendUint64(out, 0)
	out = append(out, summary...)
	return binary.BigEndian.AppendUint32(out, 0)
}

func znnDepositSummary(amount int64) []byte {
	out := binary.BigEndian.AppendUint32(nil, 1)
	out = append(out, assetIdFor(types.ZnnTokenStandard).Bytes()...)
	for _, v := range []int64{amount, 0} {
		var word [32]byte
		big.NewInt(v).FillBytes(word[:])
		out = append(out, word[:]...)
	}
	return out
}

func submitBatchStep(from types.Address, commitment []byte) *nom.AccountBlock {
	return settlementStep(from, definition.SettlementSubmitBatchMethodName, commitment)
}

func registerExecutorFully(t *testing.T, z mock.MockZenon, pillar string, executor types.Address) {
	t.Helper()
	settlementCall(t, z, registerExecutorStep(g.User5.Address, pillar, executor), nil)
	advanceMomentums(t, z, constants.MinAdministratorDelay)
	settlementCall(t, z, registerExecutorStep(g.User5.Address, pillar, executor), nil)
}

func TestSettlement_SubmitBatch_AcceptanceAndChaining(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	constants.MinAdministratorDelay = 10
	savedMin := constants.WithdrawalDelayMin
	constants.WithdrawalDelayMin = 10
	t.Cleanup(func() { constants.WithdrawalDelayMin = savedMin })

	registerDomains(t, z, 1)
	settlementCall(t, z, setApprovedDAModesStep(g.User5.Address, 1, []byte{0}), nil)
	advanceMomentums(t, z, constants.MinSoftDelay)
	settlementCall(t, z, setApprovedDAModesStep(g.User5.Address, 1, []byte{0}), nil)
	configureAsset(t, z, 1, assetIdFor(types.ZnnTokenStandard))
	registerExecutorFully(t, z, g.Pillar1Name, g.User1.Address)

	target := types.Hash{0x07}
	deposit := func(amount int64) {
		settlementCall(t, z, depositStep(g.User1.Address, types.ZnnTokenStandard, amount, 1, target, 5000), nil)
	}
	root := func(b byte) types.Hash { return hashFromByteForTest(b) }
	pre1 := root(0x51)

	deposit(100)
	deposit(5)
	settlementCall(t, z, submitBatchStep(g.User1.Address, batchCommitment(1, 0, 0, 1, types.ZeroHash, pre1, g.User1.Address, znnDepositSummary(105))), nil)

	entry, err := definition.GetBatchEntry(settlementStorage(t, z), 1, 0)
	common.FailIfErr(t, err)
	if entry.ExecutorPillar != g.Pillar1Name || entry.State != definition.BatchStateSubmitted || entry.PostStateRoot != pre1 || entry.SubmittedAtHeight == 0 {
		t.Fatalf("unexpected entry %+v", entry)
	}
	if entry.FinalizeAtHeight-entry.SubmittedAtHeight != 10 {
		t.Fatalf("expected a delay of 10, got %d", entry.FinalizeAtHeight-entry.SubmittedAtHeight)
	}
	cursor, err := definition.GetInputSequenceCursor(settlementStorage(t, z), 1)
	common.FailIfErr(t, err)
	if cursor.ConsumedInputCount != 2 || cursor.NextBatchId != 1 {
		t.Fatalf("unexpected cursor %+v", cursor)
	}

	deposit(1)
	pre2 := root(0x52)
	settlementCall(t, z, submitBatchStep(g.User1.Address, batchCommitment(1, 1, 2, 2, pre1, pre2, g.User1.Address, znnDepositSummary(1))), nil)

	registerExecutorFully(t, z, g.Pillar2Name, g.User2.Address)
	deposit(2)
	settlementCall(t, z, submitBatchStep(g.User2.Address, batchCommitment(1, 2, 3, 3, root(0x99), root(0x53), g.User2.Address, znnDepositSummary(2))), constants.ErrForbiddenParam)
	settlementCall(t, z, submitBatchStep(g.User1.Address, batchCommitment(1, 2, 3, 3, pre2, root(0x53), g.User1.Address, znnDepositSummary(2))), constants.ErrPermissionDenied)
	settlementCall(t, z, submitBatchStep(g.User2.Address, batchCommitment(1, 2, 3, 3, pre2, root(0x53), g.User2.Address, znnDepositSummary(2))), nil)
	entry, err = definition.GetBatchEntry(settlementStorage(t, z), 1, 2)
	common.FailIfErr(t, err)
	if entry.ExecutorPillar != g.Pillar2Name {
		t.Fatalf("expected pillar 2, got %q", entry.ExecutorPillar)
	}

	deposit(3)
	settlementCall(t, z, pauseStep(g.User5.Address, 1, false, definition.PauseScopeSubmit, true), nil)
	settlementCall(t, z, submitBatchStep(g.User2.Address, batchCommitment(1, 3, 4, 4, root(0x53), root(0x54), g.User2.Address, znnDepositSummary(3))), constants.ErrSettlementSubmitPaused)
}

// ============================================================================
// RelayMessage and ReleaseWithdrawals
// ============================================================================

// outboxMessage encodes a 133-byte kind 1 outbox message of ZNN with an
// empty payload. A zero source marks a runtime-emitted refund.
func outboxMessage(source types.Hash, input uint64, outbox uint32, to types.Address, amount int64) []byte {
	out := append([]byte{}, source.Bytes()...)
	out = binary.BigEndian.AppendUint64(out, input)
	out = binary.BigEndian.AppendUint32(out, outbox)
	out = append(out, 1)
	out = append(out, to.Bytes()...)
	out = append(out, assetIdFor(types.ZnnTokenStandard).Bytes()...)
	var word [32]byte
	big.NewInt(amount).FillBytes(word[:])
	out = append(out, word[:]...)
	return binary.BigEndian.AppendUint32(out, 0)
}

// outboxTree builds the three-leaf SPEC 27.3 tree: the third leaf is
// promoted past the first level and carries a one-step proof.
func outboxTree(leaves [][]byte) (root types.Hash, proofs [][]byte) {
	h := func(prefix byte, parts ...[]byte) []byte {
		return crypto.Hash(append([][]byte{{prefix}}, parts...)...)
	}
	l0, l1, l2 := h(0, leaves[0]), h(0, leaves[1]), h(0, leaves[2])
	n01 := h(1, l0, l1)
	rootBytes := h(1, n01, l2)
	step := func(side byte, sibling []byte) []byte { return append([]byte{side}, sibling...) }
	proof := func(steps ...[]byte) []byte {
		out := binary.BigEndian.AppendUint32(nil, uint32(len(steps)))
		for _, s := range steps {
			out = append(out, s...)
		}
		return out
	}
	return types.BytesToHashPanic(rootBytes), [][]byte{
		proof(step(1, l1), step(1, l2)),
		proof(step(0, l0), step(1, l2)),
		proof(step(0, n01)),
	}
}

func relayMessageStep(from types.Address, domainId uint32, batchId uint64, message, proof []byte) *nom.AccountBlock {
	return settlementStep(from, definition.SettlementRelayMessageMethodName, domainId, batchId, message, proof)
}

func releaseWithdrawalsStep(from types.Address, domainId uint32) *nom.AccountBlock {
	return settlementStep(from, definition.SettlementReleaseWithdrawalsMethodName, domainId)
}

// withdrawalFlowSummary is the ZNN summary of a batch that credits
// deposits and debits the withdrawals its outbox carries.
func withdrawalFlowSummary(credit, debit int64) []byte {
	out := binary.BigEndian.AppendUint32(nil, 1)
	out = append(out, assetIdFor(types.ZnnTokenStandard).Bytes()...)
	for _, v := range []int64{credit, debit} {
		var word [32]byte
		big.NewInt(v).FillBytes(word[:])
		out = append(out, word[:]...)
	}
	return out
}

type withdrawalSetup struct {
	z      mock.MockZenon
	leaves [][]byte
	proofs [][]byte
}

// newWithdrawalZenon registers domain 1 with ZNN (cap 1000) and an
// executor, takes deposits of 100 ZNN from User2 (input 0) and 50 ZNN
// from User3 (input 1), and submits batch 0 over both with an outbox of
// three messages: a full refund of 100 to User2, a contract withdrawal
// of 10 to User4 and a partial refund of 30 to User3.
func newWithdrawalZenon(t *testing.T) *withdrawalSetup {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	constants.MinAdministratorDelay = 10
	savedMin := constants.WithdrawalDelayMin
	constants.WithdrawalDelayMin = 10
	t.Cleanup(func() { constants.WithdrawalDelayMin = savedMin })

	registerDomains(t, z, 1)
	settlementCall(t, z, setApprovedDAModesStep(g.User5.Address, 1, []byte{0}), nil)
	advanceMomentums(t, z, constants.MinSoftDelay)
	settlementCall(t, z, setApprovedDAModesStep(g.User5.Address, 1, []byte{0}), nil)
	configureAsset(t, z, 1, assetIdFor(types.ZnnTokenStandard))
	registerExecutorFully(t, z, g.Pillar1Name, g.User1.Address)

	target := types.Hash{0x07}
	settlementCall(t, z, depositStep(g.User2.Address, types.ZnnTokenStandard, 100, 1, target, 5000), nil)
	settlementCall(t, z, depositStep(g.User3.Address, types.ZnnTokenStandard, 50, 1, target, 5000), nil)

	leaves := [][]byte{
		outboxMessage(types.ZeroHash, 0, 0, g.User2.Address, 100),
		outboxMessage(hashFromByteForTest(0x77), 1, 0, g.User4.Address, 10),
		outboxMessage(types.ZeroHash, 1, 1, g.User3.Address, 30),
	}
	root, proofs := outboxTree(leaves)
	commitment := batchCommitment(1, 0, 0, 1, types.ZeroHash, hashFromByteForTest(0x51), g.User1.Address, withdrawalFlowSummary(150, 140))
	copy(commitment[206:238], root.Bytes())
	settlementCall(t, z, submitBatchStep(g.User1.Address, commitment), nil)
	return &withdrawalSetup{z: z, leaves: leaves, proofs: proofs}
}

func (w *withdrawalSetup) relay(t *testing.T, i int, want error) {
	t.Helper()
	settlementCall(t, w.z, relayMessageStep(g.User1.Address, 1, 0, w.leaves[i], w.proofs[i]), want)
}

func (w *withdrawalSetup) finalize(t *testing.T) {
	t.Helper()
	entry, err := definition.GetBatchEntry(settlementStorage(t, w.z), 1, 0)
	common.FailIfErr(t, err)
	frMom, err := w.z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	if frMom.Height < entry.FinalizeAtHeight {
		advanceMomentums(t, w.z, entry.FinalizeAtHeight-frMom.Height)
	}
}

func (w *withdrawalSetup) counters(t *testing.T) *definition.ConservationCounters {
	t.Helper()
	c, err := definition.GetConservationCounters(settlementStorage(t, w.z), 1, assetIdFor(types.ZnnTokenStandard))
	common.FailIfErr(t, err)
	return c
}

func (w *withdrawalSetup) queued(t *testing.T) uint64 {
	t.Helper()
	meta, err := definition.GetReleaseQueueMeta(settlementStorage(t, w.z), 1)
	common.FailIfErr(t, err)
	return meta.Tail - meta.Head
}

func TestSettlement_RelayMessage_RegistersAndReleasesRefundsAndWithdrawals(t *testing.T) {
	w := newWithdrawalZenon(t)
	z := w.z

	// A batch that is still SUBMITTED cannot be relayed.
	w.relay(t, 0, constants.ErrForbiddenParam)
	if w.queued(t) != 0 {
		t.Fatal("a relay against a SUBMITTED batch queued a withdrawal")
	}

	w.finalize(t)
	for i := range w.leaves {
		w.relay(t, i, nil)
	}
	if w.queued(t) != 3 {
		t.Fatalf("expected 3 queued withdrawals, got %d", w.queued(t))
	}
	if c := w.counters(t); c.PendingWithdrawalReserve.Int64() != 140 || c.TotalReleased.Int64() != 0 || c.TotalDeposited.Int64() != 150 {
		t.Fatalf("counters after registration: %+v", c)
	}
	for i := range w.leaves {
		w.relay(t, i, constants.ErrSettlementOutboxProcessed)
	}
	if w.queued(t) != 3 {
		t.Fatal("a replay changed the queue")
	}

	// PAUSE_SUBMIT does not stop the release of a finalized batch.
	settlementCall(t, z, pauseStep(g.User5.Address, 1, false, definition.PauseScopeSubmit, true), nil)
	before := map[types.Address]int64{
		g.User2.Address: balanceOf(t, z, g.User2.Address, types.ZnnTokenStandard),
		g.User3.Address: balanceOf(t, z, g.User3.Address, types.ZnnTokenStandard),
		g.User4.Address: balanceOf(t, z, g.User4.Address, types.ZnnTokenStandard),
	}
	settlementCall(t, z, releaseWithdrawalsStep(g.User1.Address, 1), nil)
	for _, user := range []types.Address{g.User2.Address, g.User3.Address, g.User4.Address} {
		autoreceive(t, z, user)
	}
	z.InsertNewMomentum()
	for user, want := range map[types.Address]int64{g.User2.Address: 100, g.User3.Address: 30, g.User4.Address: 10} {
		if got := balanceOf(t, z, user, types.ZnnTokenStandard) - before[user]; got != want {
			t.Fatalf("balance delta of %s: got %d want %d", user, got, want)
		}
	}
	if c := w.counters(t); c.PendingWithdrawalReserve.Sign() != 0 || c.TotalReleased.Int64() != 140 {
		t.Fatalf("counters after release: %+v", c)
	}
	if w.queued(t) != 0 {
		t.Fatal("the queue is not drained")
	}

	// The empty queue fails before the rate limit applies, and a
	// registered message stays registered.
	settlementCall(t, z, releaseWithdrawalsStep(g.User1.Address, 1), constants.ErrNothingToWithdraw)
	w.relay(t, 0, constants.ErrSettlementOutboxProcessed)
}

func TestSettlement_RelayMessage_RelayPauseHaltsRegistrationAndRelease(t *testing.T) {
	w := newWithdrawalZenon(t)
	z := w.z
	w.finalize(t)
	w.relay(t, 0, nil)
	w.relay(t, 1, nil)

	settlementCall(t, z, pauseStep(g.User5.Address, 1, false, definition.PauseScopeRelay, true), nil)
	w.relay(t, 2, constants.ErrSettlementRelayPaused)
	settlementCall(t, z, releaseWithdrawalsStep(g.User1.Address, 1), constants.ErrSettlementRelayPaused)
	if w.queued(t) != 2 {
		t.Fatalf("expected 2 queued withdrawals, got %d", w.queued(t))
	}

	settlementCall(t, z, pauseStep(g.User5.Address, 1, false, definition.PauseScopeRelay, false), nil)
	// The rejected relay left its id free.
	w.relay(t, 2, nil)

	order := []types.Address{g.User2.Address, g.User4.Address, g.User3.Address}
	call := z.CallContract(releaseWithdrawalsStep(g.User1.Address, 1))
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	call.Error(t, nil)

	// The descendant sends appear in registration order.
	sends := 0
	var heights []uint64
	for i, user := range order {
		unreceived, err := api.NewLedgerApi(z).GetUnreceivedBlocksByAddress(user, 0, 50)
		common.FailIfErr(t, err)
		for _, b := range unreceived.List {
			if b.AccountBlock.Address == types.SettlementContract {
				sends++
				heights = append(heights, b.AccountBlock.Height)
				want := []int64{100, 10, 30}[i]
				if b.AccountBlock.Amount.Int64() != want {
					t.Fatalf("send to %s: amount %v want %d", user, b.AccountBlock.Amount, want)
				}
			}
		}
	}
	if sends != 3 {
		t.Fatalf("expected 3 sends, got %d", sends)
	}
	if !(heights[0] < heights[1] && heights[1] < heights[2]) {
		t.Fatalf("send heights %v do not rise in registration order", heights)
	}
	if c := w.counters(t); c.PendingWithdrawalReserve.Sign() != 0 || c.TotalReleased.Int64() != 140 {
		t.Fatalf("counters after release: %+v", c)
	}
}

func TestSettlement_RelayMessage_RejectsBadInputs(t *testing.T) {
	w := newWithdrawalZenon(t)
	z := w.z
	w.finalize(t)

	// Unregistrable messages are rejected at send validation.
	embedded := outboxMessage(hashFromByteForTest(0x77), 5, 0, types.SettlementContract, 5)
	zero := outboxMessage(hashFromByteForTest(0x77), 6, 0, g.User4.Address, 0)
	for _, m := range [][]byte{embedded, zero, embedded[:132]} {
		settlementRejectedAtSend(z, relayMessageStep(g.User1.Address, 1, 0, m, []byte{0, 0, 0, 0}))
	}
	attached := relayMessageStep(g.User1.Address, 1, 0, w.leaves[0], w.proofs[0])
	attached.Amount = big.NewInt(1)
	z.InsertSendBlock(attached, constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertNewMomentum()

	// A valid proof relayed under another batch or domain, a wrong
	// sibling and a count above the maximum depth are rejected.
	settlementCall(t, z, relayMessageStep(g.User1.Address, 1, 1, w.leaves[0], w.proofs[0]), constants.ErrForbiddenParam)
	settlementCall(t, z, relayMessageStep(g.User1.Address, 2, 0, w.leaves[0], w.proofs[0]), constants.ErrForbiddenParam)
	wrong := append([]byte{}, w.proofs[0]...)
	wrong[10] ^= 1
	settlementCall(t, z, relayMessageStep(g.User1.Address, 1, 0, w.leaves[0], wrong), constants.ErrForbiddenParam)
	settlementCall(t, z, relayMessageStep(g.User1.Address, 1, 0, w.leaves[0], append(append([]byte{}, w.proofs[0]...), 0)), constants.ErrForbiddenParam)
	settlementCall(t, z, relayMessageStep(g.User1.Address, 1, 0, w.leaves[0], binary.BigEndian.AppendUint32(nil, 33)), constants.ErrForbiddenParam)
	if w.queued(t) != 0 {
		t.Fatal("a rejected relay queued a withdrawal")
	}
	w.relay(t, 0, nil)
}

func TestSettlement_ReleaseWithdrawals_EmptyQueueIsRejected(t *testing.T) {
	w := newWithdrawalZenon(t)
	settlementCall(t, w.z, releaseWithdrawalsStep(g.User1.Address, 1), constants.ErrNothingToWithdraw)
	settlementCall(t, w.z, releaseWithdrawalsStep(g.User1.Address, 2), constants.ErrNothingToWithdraw)
}

// ============================================================================
// CallL2, UpgradeContract, TransferContractOwnership and BuyStorage
// ============================================================================

func callL2Step(from types.Address, zts types.ZenonTokenStandard, amount int64, domainId uint32, target types.Hash, plasmaLimit uint64, payload []byte) *nom.AccountBlock {
	block := settlementStep(from, definition.SettlementCallL2MethodName, domainId, target, plasmaLimit, payload)
	block.TokenStandard = zts
	block.Amount = big.NewInt(amount)
	return block
}

func upgradeContractStep(from types.Address, domainId uint32, contractId, newCodeHash types.Hash, zts types.ZenonTokenStandard, amount int64) *nom.AccountBlock {
	block := settlementStep(from, definition.SettlementUpgradeContractMethodName, domainId, contractId, newCodeHash)
	block.TokenStandard = zts
	block.Amount = big.NewInt(amount)
	return block
}

func transferOwnershipStep(from types.Address, domainId uint32, contractId types.Hash, newOwner types.Address, amount int64) *nom.AccountBlock {
	block := settlementStep(from, definition.SettlementTransferContractOwnershipMethodName, domainId, contractId, newOwner)
	block.Amount = big.NewInt(amount)
	return block
}

func buyStorageStep(from types.Address, domainId uint32, contractId types.Hash, size uint64, zts types.ZenonTokenStandard, amount int64) *nom.AccountBlock {
	block := settlementStep(from, definition.SettlementBuyStorageMethodName, domainId, contractId, size)
	block.TokenStandard = zts
	block.Amount = big.NewInt(amount)
	return block
}

func pauseDomain(t *testing.T, z mock.MockZenon, domainId uint32) {
	t.Helper()
	paused := phase1Domain(domainId)
	paused.Status = definition.DomainStatusPaused
	settlementCall(t, z, registerDomainStep(g.User5.Address, paused), nil)
}

// expectAssignedInput checks the asset, amount and fee of an assigned
// index record. A nil stored amount or fee counts as zero.
func expectAssignedInput(t *testing.T, z mock.MockZenon, domainId uint32, index uint64, asset types.Hash, amount, fee int64) {
	t.Helper()
	record, err := definition.GetAssignedIndexRecord(settlementStorage(t, z), domainId, index)
	common.FailIfErr(t, err)
	if record.AssetId != asset {
		t.Fatalf("index %d: expected asset %s, got %s", index, asset, record.AssetId)
	}
	gotAmount := big.NewInt(0)
	if record.Amount != nil {
		gotAmount = record.Amount
	}
	if gotAmount.Cmp(big.NewInt(amount)) != 0 {
		t.Fatalf("index %d: expected amount %d, got %v", index, amount, record.Amount)
	}
	gotFee := big.NewInt(0)
	if record.Fee != nil {
		gotFee = record.Fee
	}
	if gotFee.Cmp(big.NewInt(fee)) != 0 {
		t.Fatalf("index %d: expected fee %d, got %v", index, fee, record.Fee)
	}
}

func TestSettlement_CallL2_WithAndWithoutValue(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	registerDomains(t, z, 1)
	znnAsset := assetIdFor(types.ZnnTokenStandard)
	configureAsset(t, z, 1, znnAsset)
	target := types.Hash{0x07}
	user := g.User1.Address
	start := balanceOf(t, z, user, types.ZnnTokenStandard)

	settlementCall(t, z, callL2Step(user, types.ZnnTokenStandard, 0, 1, target, 5000, []byte("call")), nil)
	expectCounter(t, z, 1)
	expectAssignedInput(t, z, 1, 0, types.ZeroHash, 0, 0)
	if got := totalDepositedOf(t, z, 1, znnAsset); got.Sign() != 0 {
		t.Fatalf("expected TotalDeposited 0, got %v", got)
	}
	z.ExpectBalance(user, types.ZnnTokenStandard, start)

	settlementCall(t, z, callL2Step(user, types.ZnnTokenStandard, 100, 1, target, 5000, []byte("call")), nil)
	expectCounter(t, z, 2)
	expectAssignedInput(t, z, 1, 1, znnAsset, 100, 0)
	z.ExpectBalance(user, types.ZnnTokenStandard, start-100)
	if got := totalDepositedOf(t, z, 1, znnAsset); got.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("expected TotalDeposited 100, got %v", got)
	}
	total, err := definition.GetCumulativeDepositTotal(settlementStorage(t, z), 1, znnAsset)
	common.FailIfErr(t, err)
	if total.CumulativeDeposited.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("expected cumulative 100, got %v", total.CumulativeDeposited)
	}
}

func TestSettlement_CallL2_ValueLessSkipsAssetConfig(t *testing.T) {
	z := newSettlementZenon(t)
	registerDomains(t, z, 1)

	// no asset is configured, and a QSR token standard carries no value
	settlementCall(t, z, callL2Step(g.User1.Address, types.QsrTokenStandard, 0, 1, types.Hash{0x07}, 0, nil), nil)
	expectCounter(t, z, 1)
	expectAssignedInput(t, z, 1, 0, types.ZeroHash, 0, 0)
}

func TestSettlement_CallL2_Rejections(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	registerDomains(t, z, 1)
	znnAsset := assetIdFor(types.ZnnTokenStandard)
	configureAsset(t, z, 1, znnAsset)
	target := types.Hash{0x07}
	user := g.User1.Address
	start := balanceOf(t, z, user, types.ZnnTokenStandard)
	startQsr := balanceOf(t, z, user, types.QsrTokenStandard)

	expectUntouched := func() {
		t.Helper()
		autoreceive(t, z, user)
		z.ExpectBalance(user, types.ZnnTokenStandard, start)
		z.ExpectBalance(user, types.QsrTokenStandard, startQsr)
		expectCounter(t, z, 0)
		if got := totalDepositedOf(t, z, 1, znnAsset); got.Sign() != 0 {
			t.Fatalf("expected TotalDeposited 0, got %v", got)
		}
		if _, err := definition.GetAssignedIndexRecord(settlementStorage(t, z), 1, 0); err != constants.ErrDataNonExistent {
			t.Fatalf("expected no record, got %v", err)
		}
	}

	settlementCall(t, z, callL2Step(user, types.QsrTokenStandard, 7, 1, target, 5000, nil), constants.ErrInvalidToken)
	expectUntouched()

	settlementCall(t, z, callL2Step(user, types.ZnnTokenStandard, 0, 9, target, 5000, nil), constants.ErrForbiddenParam)
	expectUntouched()

	pauseDomain(t, z, 1)
	settlementCall(t, z, callL2Step(user, types.ZnnTokenStandard, 7, 1, target, 5000, nil), constants.ErrForbiddenParam)
	expectUntouched()
	settlementCall(t, z, callL2Step(user, types.ZnnTokenStandard, 0, 1, target, 5000, nil), constants.ErrForbiddenParam)
	expectUntouched()
}

func TestSettlement_CallL2_SendValidation(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	registerDomains(t, z, 1)
	user := g.User1.Address
	target := types.Hash{0x07}

	settlementRejectedAtSend(z, callL2Step(user, types.ZnnTokenStandard, 0, 1, target, 5000, make([]byte, constants.MaxCallPayload+1)))
	settlementRejectedAtSend(z, callL2Step(user, types.ZnnTokenStandard, 0, 1, target, constants.MaxInputGas+1, nil))
	expectCounter(t, z, 0)

	exact := z.InsertSendBlock(callL2Step(user, types.ZnnTokenStandard, 0, 1, target, 0, make([]byte, constants.MaxCallPayload)), nil, mock.SkipVmChanges)
	want := uint64(constants.AccountBlockBasePlasma + constants.ABByteDataPlasma*len(exact.Data))
	if exact.BasePlasma != want {
		t.Fatalf("expected base plasma %d for %d bytes, got %d", want, len(exact.Data), exact.BasePlasma)
	}
	z.InsertNewMomentum()
	z.InsertNewMomentum()

	// an empty payload and the maximum plasma limit are accepted
	settlementCall(t, z, callL2Step(user, types.ZnnTokenStandard, 0, 1, target, constants.MaxInputGas, nil), nil)
	expectCounter(t, z, 2)
}

func TestSettlement_CallL2_IgnoresPauseScopes(t *testing.T) {
	z := newSettlementZenon(t)
	registerDomains(t, z, 1)

	settlementCall(t, z, pauseStep(g.User5.Address, 0, true, definition.PauseScopeSubmit, true), nil)
	settlementCall(t, z, pauseStep(g.User5.Address, 0, true, definition.PauseScopeRelay, true), nil)
	settlementCall(t, z, callL2Step(g.User1.Address, types.ZnnTokenStandard, 0, 1, types.Hash{0x07}, 1, nil), nil)
	expectCounter(t, z, 1)
}

// completeDeploy starts a deployment of 100 bytes and sends its chunk.
func completeDeploy(t *testing.T, z mock.MockZenon, from types.Address, codeHash types.Hash) {
	t.Helper()
	startDeploy(t, z, from, codeHash, 100)
	sendChunks(t, z, from, codeHash, deployPayload(100), 0)
}

func TestSettlement_UpgradeContract_ConsumesOwnRecord(t *testing.T) {
	z := newDeployZenon(t)
	user := g.User1.Address
	total := uint64(constants.MaxDeployChunkData + 100)
	payload := deployPayload(int(total))
	fee := int64(total) * 3
	znnAsset := assetIdFor(types.ZnnTokenStandard)
	contractId := types.Hash{0x55}
	startBalance := balanceOf(t, z, user, types.ZnnTokenStandard)

	startDeploy(t, z, user, deployCodeHash, total)
	sendChunks(t, z, user, deployCodeHash, payload, 0, 1)
	startHeight := expectDeployRecord(t, z, user, deployCodeHash).StartHeight
	expectCounter(t, z, 3)

	settlementCall(t, z, upgradeContractStep(user, deployDomain, contractId, deployCodeHash, types.ZnnTokenStandard, fee), nil)
	expectNoDeployRecord(t, z, user, deployCodeHash)
	expectNoDeployChunk(t, z, user, deployCodeHash, startHeight, 0)
	expectNoDeployChunk(t, z, user, deployCodeHash, startHeight, 1)
	expectCounter(t, z, 4)
	expectAssignedInput(t, z, deployDomain, 3, types.ZeroHash, 0, fee)
	expectFeesCollected(t, z, fee)
	z.ExpectBalance(user, types.ZnnTokenStandard, startBalance-fee)
	if got := totalDepositedOf(t, z, deployDomain, znnAsset); got.Sign() != 0 {
		t.Fatalf("expected TotalDeposited 0, got %v", got)
	}

	settlementCall(t, z, deployFinalizeStep(user, deployDomain, deployCodeHash, types.ZnnTokenStandard, fee), constants.ErrDataNonExistent)
	expectCounter(t, z, 4)
}

func TestSettlement_UpgradeContract_Rejections(t *testing.T) {
	z := newDeployZenon(t)
	user := g.User1.Address
	fee := int64(300)
	contractId := types.Hash{0x55}
	hash := deployCodeHash
	startBalance := balanceOf(t, z, user, types.ZnnTokenStandard)

	expectUnchanged := func(counter uint64) {
		t.Helper()
		autoreceive(t, z, user)
		z.ExpectBalance(user, types.ZnnTokenStandard, startBalance)
		expectCounter(t, z, counter)
		expectFeesCollected(t, z, 0)
	}

	// no record
	settlementCall(t, z, upgradeContractStep(user, deployDomain, contractId, hash, types.ZnnTokenStandard, fee), constants.ErrDataNonExistent)
	expectUnchanged(0)

	// only another deployer holds a complete record
	completeDeploy(t, z, g.User2.Address, hash)
	settlementCall(t, z, upgradeContractStep(user, deployDomain, contractId, hash, types.ZnnTokenStandard, fee), constants.ErrDataNonExistent)
	if _, err := deployRecordOf(t, z, g.User2.Address, hash); err != nil {
		t.Fatalf("expected the other deployer's record to stay, got %v", err)
	}
	expectUnchanged(2)

	// incomplete record
	total := uint64(constants.MaxDeployChunkData + 100)
	incomplete := types.Hash{0xAA}
	startDeploy(t, z, user, incomplete, total)
	sendChunks(t, z, user, incomplete, deployPayload(int(total)), 1)
	settlementCall(t, z, upgradeContractStep(user, deployDomain, contractId, incomplete, types.ZnnTokenStandard, int64(total)*3), constants.ErrForbiddenParam)
	expectDeployRecord(t, z, user, incomplete)
	expectUnchanged(4)

	// wrong fee
	completeDeploy(t, z, user, hash)
	settlementCall(t, z, upgradeContractStep(user, deployDomain, contractId, hash, types.ZnnTokenStandard, fee-1), constants.ErrInvalidTokenOrAmount)
	expectDeployRecord(t, z, user, hash)
	expectUnchanged(6)

	// QSR is rejected at send
	z.InsertSendBlock(upgradeContractStep(user, deployDomain, contractId, hash, types.QsrTokenStandard, fee), constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertNewMomentum()
	z.InsertSendBlock(upgradeContractStep(user, deployDomain, contractId, hash, types.ZnnTokenStandard, 0), constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertNewMomentum()
	expectDeployRecord(t, z, user, hash)

	// a record consumed by finalize
	settlementCall(t, z, deployFinalizeStep(user, deployDomain, hash, types.ZnnTokenStandard, fee), nil)
	settlementCall(t, z, upgradeContractStep(user, deployDomain, contractId, hash, types.ZnnTokenStandard, fee), constants.ErrDataNonExistent)
	expectCounter(t, z, 7)
	expectFeesCollected(t, z, fee)

	// a paused domain
	other := types.Hash{0xBB}
	completeDeploy(t, z, user, other)
	pauseDomain(t, z, deployDomain)
	settlementCall(t, z, upgradeContractStep(user, deployDomain, contractId, other, types.ZnnTokenStandard, fee), constants.ErrForbiddenParam)
	expectDeployRecord(t, z, user, other)
	expectCounter(t, z, 9)
	expectFeesCollected(t, z, fee)
}

func TestSettlement_UpgradeContract_WithoutPeripheryConfig(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	registerDomains(t, z, deployDomain)
	user := g.User1.Address
	startBalance := balanceOf(t, z, user, types.ZnnTokenStandard)

	completeDeploy(t, z, user, deployCodeHash)
	settlementCall(t, z, upgradeContractStep(user, deployDomain, types.Hash{0x55}, deployCodeHash, types.ZnnTokenStandard, 300), constants.ErrDataNonExistent)
	autoreceive(t, z, user)
	z.ExpectBalance(user, types.ZnnTokenStandard, startBalance)
	expectDeployRecord(t, z, user, deployCodeHash)
	expectCounter(t, z, 2)
	expectFeesCollected(t, z, 0)
}

func TestSettlement_TransferContractOwnership(t *testing.T) {
	z := newSettlementZenon(t)
	registerDomains(t, z, 1)
	user := g.User1.Address
	contractId := types.Hash{0x66}
	start := balanceOf(t, z, user, types.ZnnTokenStandard)

	settlementCall(t, z, transferOwnershipStep(user, 1, contractId, g.User2.Address, 0), nil)
	expectCounter(t, z, 1)
	expectAssignedInput(t, z, 1, 0, types.ZeroHash, 0, 0)

	// a zero new owner renounces ownership
	settlementCall(t, z, transferOwnershipStep(user, 1, contractId, types.Address{}, 0), nil)
	expectCounter(t, z, 2)
	expectAssignedInput(t, z, 1, 1, types.ZeroHash, 0, 0)

	// an attached amount is rejected at send
	z.InsertSendBlock(transferOwnershipStep(user, 1, contractId, g.User2.Address, 1), constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertNewMomentum()
	expectCounter(t, z, 2)

	settlementCall(t, z, transferOwnershipStep(user, 9, contractId, g.User2.Address, 0), constants.ErrForbiddenParam)
	pauseDomain(t, z, 1)
	settlementCall(t, z, transferOwnershipStep(user, 1, contractId, g.User2.Address, 0), constants.ErrForbiddenParam)
	expectCounter(t, z, 2)
	z.ExpectBalance(user, types.ZnnTokenStandard, start)
}

func TestSettlement_BuyStorage(t *testing.T) {
	z := newDeployZenon(t)
	user := g.User1.Address
	contractId := types.Hash{0x77}
	znnAsset := assetIdFor(types.ZnnTokenStandard)
	start := balanceOf(t, z, user, types.ZnnTokenStandard)
	startQsr := balanceOf(t, z, user, types.QsrTokenStandard)

	settlementCall(t, z, buyStorageStep(user, deployDomain, contractId, 50, types.ZnnTokenStandard, 50), nil)
	expectCounter(t, z, 1)
	expectAssignedInput(t, z, deployDomain, 0, types.ZeroHash, 0, 50)
	expectFeesCollected(t, z, 50)
	z.ExpectBalance(user, types.ZnnTokenStandard, start-50)
	if got := totalDepositedOf(t, z, deployDomain, znnAsset); got.Sign() != 0 {
		t.Fatalf("expected TotalDeposited 0, got %v", got)
	}

	// the bytecode price is not the state price
	settlementCall(t, z, buyStorageStep(user, deployDomain, contractId, 50, types.ZnnTokenStandard, 150), constants.ErrInvalidTokenOrAmount)
	autoreceive(t, z, user)
	z.ExpectBalance(user, types.ZnnTokenStandard, start-50)
	expectCounter(t, z, 1)
	expectFeesCollected(t, z, 50)

	// send validation
	z.InsertSendBlock(buyStorageStep(user, deployDomain, contractId, 50, types.QsrTokenStandard, 50), constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertNewMomentum()
	z.InsertSendBlock(buyStorageStep(user, deployDomain, contractId, 50, types.ZnnTokenStandard, 0), constants.ErrInvalidTokenOrAmount, mock.SkipVmChanges)
	z.InsertNewMomentum()
	z.InsertSendBlock(buyStorageStep(user, deployDomain, contractId, 0, types.ZnnTokenStandard, 50), constants.ErrForbiddenParam, mock.SkipVmChanges)
	z.InsertNewMomentum()
	z.ExpectBalance(user, types.QsrTokenStandard, startQsr)
	expectCounter(t, z, 1)

	// an unregistered domain
	settlementCall(t, z, buyStorageStep(user, 9, contractId, 50, types.ZnnTokenStandard, 50), constants.ErrForbiddenParam)
	expectCounter(t, z, 1)

	pauseDomain(t, z, deployDomain)
	settlementCall(t, z, buyStorageStep(user, deployDomain, contractId, 50, types.ZnnTokenStandard, 50), constants.ErrForbiddenParam)
	autoreceive(t, z, user)
	z.ExpectBalance(user, types.ZnnTokenStandard, start-50)
	expectCounter(t, z, 1)
	expectFeesCollected(t, z, 50)
}

func TestSettlement_BuyStorage_WithoutPeripheryConfig(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	registerDomains(t, z, deployDomain)
	user := g.User1.Address
	start := balanceOf(t, z, user, types.ZnnTokenStandard)

	settlementCall(t, z, buyStorageStep(user, deployDomain, types.Hash{0x77}, 50, types.ZnnTokenStandard, 50), constants.ErrDataNonExistent)
	autoreceive(t, z, user)
	z.ExpectBalance(user, types.ZnnTokenStandard, start)
	expectCounter(t, z, 0)
	expectFeesCollected(t, z, 0)
}

// sendSettlementInput inserts the send block, lets Settlement receive
// it, and returns the hash of the inserted send.
func sendSettlementInput(t *testing.T, z mock.MockZenon, block *nom.AccountBlock) types.Hash {
	t.Helper()
	sent := z.InsertSendBlock(block, nil, mock.SkipVmChanges)
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	return sent.Hash
}

func TestSettlement_OrdinalEntryNamesSend(t *testing.T) {
	z := newDeployZenon(t)
	configureAsset(t, z, deployDomain, assetIdFor(types.ZnnTokenStandard))
	user := g.User1.Address
	target := types.Hash{0x07}
	codeA := types.Hash{0xA1}
	codeB := types.Hash{0xB1}

	var hashes []types.Hash
	send := func(block *nom.AccountBlock) {
		hashes = append(hashes, sendSettlementInput(t, z, block))
	}
	send(depositStep(user, types.ZnnTokenStandard, 100, deployDomain, target, 5000))
	send(callL2Step(user, types.ZnnTokenStandard, 50, deployDomain, target, 5000, []byte("call")))
	send(callL2Step(user, types.QsrTokenStandard, 0, deployDomain, target, 0, nil))
	send(deployStartStep(user, deployDomain, codeA, 100, 1, nil))
	send(deployChunkStep(user, deployDomain, codeA, 0, deployPayload(100)))
	send(deployFinalizeStep(user, deployDomain, codeA, types.ZnnTokenStandard, 300))
	send(deployStartStep(user, deployDomain, codeB, 100, 1, nil))
	send(deployChunkStep(user, deployDomain, codeB, 0, deployPayload(100)))
	send(upgradeContractStep(user, deployDomain, types.Hash{0x55}, codeB, types.ZnnTokenStandard, 300))
	send(transferOwnershipStep(user, deployDomain, types.Hash{0x66}, g.User2.Address, 0))
	send(buyStorageStep(user, deployDomain, types.Hash{0x77}, 50, types.ZnnTokenStandard, 50))

	seen := map[types.Hash]bool{}
	for _, h := range hashes {
		if seen[h] {
			t.Fatalf("send hash %s repeats", h)
		}
		seen[h] = true
	}
	for k, want := range hashes {
		o, err := definition.GetDomainInputOrdinal(settlementStorage(t, z), deployDomain, uint64(k))
		common.FailIfErr(t, err)
		if o.GlobalInputIndex != uint64(k) {
			t.Fatalf("ordinal %d: global index %d want %d", k, o.GlobalInputIndex, k)
		}
		if o.SourceAccountBlockHash != want {
			t.Fatalf("ordinal %d: send hash %s want %s", k, o.SourceAccountBlockHash, want)
		}
	}

	settlementCall(t, z, transferOwnershipStep(user, 9, types.Hash{0x66}, g.User2.Address, 0), constants.ErrForbiddenParam)
	if _, err := definition.GetDomainInputOrdinal(settlementStorage(t, z), 9, 0); err != constants.ErrDataNonExistent {
		t.Fatalf("rejected send: expected ErrDataNonExistent for domain 9, got %v", err)
	}
	if _, err := definition.GetDomainInputOrdinal(settlementStorage(t, z), deployDomain, uint64(len(hashes))); err != constants.ErrDataNonExistent {
		t.Fatalf("rejected send: expected ErrDataNonExistent past the last ordinal, got %v", err)
	}
}

func TestSettlement_GetProofVerifiesOrdinalEntry(t *testing.T) {
	z := mock.NewMockZenon(t)
	t.Cleanup(z.StopPanic)
	activateStateRoot(t, z)
	activateSporkByName(t, z, "spork-offchain-execution", types.OffchainExecutionSpork)
	advanceMomentums(t, z, 10)

	savedAdmin := types.NetworkAdmin
	types.NetworkAdmin = g.User5.Address
	t.Cleanup(func() { types.NetworkAdmin = savedAdmin })

	registerDomains(t, z, 1)
	h := sendSettlementInput(t, z, transferOwnershipStep(g.User1.Address, 1, types.Hash{0x66}, g.User2.Address, 0))

	ledgerApi := api.NewLedgerApi(z)
	m, err := ledgerApi.GetFrontierMomentum()
	common.FailIfErr(t, err)
	if m.Version != nom.StateRootMomentumVersion {
		t.Fatalf("frontier momentum version %d want %d", m.Version, nom.StateRootMomentumVersion)
	}

	rawKey := common.JoinBytes([]byte{0x03}, types.SettlementContract.Bytes(), []byte{0x04}, definition.DomainInputOrdinalKey(1, 0))
	resp, err := ledgerApi.GetProof(m.Height, rawKey)
	common.FailIfErr(t, err)
	if resp.Root != m.StateRoot {
		t.Fatalf("proof root %s != header StateRoot %s", resp.Root, m.StateRoot)
	}
	if len(resp.Value) == 0 {
		t.Fatalf("proof carries no value")
	}
	ok, err := trie.VerifyProofByPath(m.StateRoot, trie.Path(types.NewHash(rawKey)), resp.Value, resp.Proof)
	common.FailIfErr(t, err)
	if !ok {
		t.Fatalf("proof does not verify against the header StateRoot")
	}

	o := new(definition.DomainInputOrdinal)
	common.FailIfErr(t, definition.ABISettlement.UnpackVariable(o, definition.DomainInputOrdinalVariableName, resp.Value))
	if o.GlobalInputIndex != 0 || o.SourceAccountBlockHash != h {
		t.Fatalf("decoded entry %+v, want global index 0 and send hash %s", o, h)
	}

	tampered := append([]byte(nil), resp.Value...)
	tampered[len(tampered)-1] ^= 0x01
	if ok, _ := trie.VerifyProofByPath(m.StateRoot, trie.Path(types.NewHash(rawKey)), tampered, resp.Proof); ok {
		t.Fatalf("proof verifies a tampered value")
	}
}
