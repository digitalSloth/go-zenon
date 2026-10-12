package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"

	g "github.com/zenon-network/go-zenon/chain/genesis/mock"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc/api"
	"github.com/zenon-network/go-zenon/rpc/api/embedded"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/zenon/mock"
)

// expectRpcJson marshals got compactly and compares it with the golden
// JSON in want; whitespace in want is ignored.
func expectRpcJson(t *testing.T, got interface{}, err error, want string) {
	t.Helper()
	common.FailIfErr(t, err)
	gotBytes, mErr := json.Marshal(got)
	common.FailIfErr(t, mErr)
	var compact bytes.Buffer
	if cErr := json.Compact(&compact, []byte(want)); cErr != nil {
		t.Fatalf("invalid golden JSON: %v\n%s", cErr, want)
	}
	if string(gotBytes) != compact.String() {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", gotBytes, compact.String())
	}
}

func rpcFrontierHeight(t *testing.T, z mock.MockZenon) uint64 {
	t.Helper()
	m, err := z.Chain().GetFrontierMomentumStore().GetFrontierMomentum()
	common.FailIfErr(t, err)
	return m.Height
}

func TestSettlementRpc_EmptyBeforeActivation(t *testing.T) {
	z := mock.NewMockZenon(t)
	defer z.StopPanic()
	a := embedded.NewSettlementApi(z)

	d1, err := a.GetDomainInfo(1)
	expectRpcJson(t, d1, err, `null`)
	d0, err := a.GetDomainInfo(0)
	expectRpcJson(t, d0, err, `null`)
	all, err := a.GetAllDomains(0, 10)
	expectRpcJson(t, all, err, `{"count":0,"list":[]}`)
	b, err := a.GetBatch(1, 0)
	expectRpcJson(t, b, err, `null`)
	bs, err := a.GetBatches(1, 0, 10)
	expectRpcJson(t, bs, err, `{"count":0,"list":[]}`)
	dr, err := a.GetDeployRecord(1, g.User1.Address, types.Hash{0x01})
	expectRpcJson(t, dr, err, `null`)
	drs, err := a.GetDeployRecordsByDeployer(1, g.User1.Address, 0, 10)
	expectRpcJson(t, drs, err, `{"count":0,"list":[]}`)
	ps, err := a.GetPauseState()
	expectRpcJson(t, ps, err, `{"global":{"submitPaused":false,"relayPaused":false},"domains":[]}`)
	pc, err := a.GetPeripheryConfig()
	expectRpcJson(t, pc, err, `null`)
	es, err := a.GetExecutorSet()
	expectRpcJson(t, es, err, `{"count":0,"list":[]}`)
	fi, err := a.GetFeeInfo()
	expectRpcJson(t, fi, err, `{"totalFeesCollected":"0","totalFeesBurned":"0","totalFeesCredited":"0","totalRewardsCollected":"0","pendingEpochHead":null}`)
	ai, err := a.GetAssetInfo(1, assetIdFor(types.ZnnTokenStandard))
	expectRpcJson(t, ai, err, `null`)
	rq, err := a.GetReleaseQueue(1, 0, 10)
	expectRpcJson(t, rq, err, `{"head":0,"tail":0,"count":0,"list":[]}`)
	op, err := a.IsOutboxProcessed(types.Hash{0x01})
	expectRpcJson(t, op, err, `false`)
	tc, err := a.GetTimeChallengesInfo()
	expectRpcJson(t, tc, err, `{"count":0,"list":[]}`)
}

func TestSettlementRpc_Domains(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	constants.MinAdministratorDelay = 20
	a := embedded.NewSettlementApi(z)

	registerDomains(t, z, 1, 2)
	settlementCall(t, z, setApprovedDAModesStep(g.User5.Address, 1, []byte{0}), nil)
	advanceMomentums(t, z, constants.MinSoftDelay)
	settlementCall(t, z, setApprovedDAModesStep(g.User5.Address, 1, []byte{0}), nil)
	settlementCall(t, z, pauseStep(g.User5.Address, 1, false, definition.PauseScopeSubmit, true), nil)
	settlementCall(t, z, pauseStep(g.User5.Address, 7, false, definition.PauseScopeRelay, true), nil)
	settlementCall(t, z, initiateEscapeHatchStep(g.User5.Address, 1), nil)
	advanceMomentums(t, z, constants.MinAdministratorDelay)
	settlementCall(t, z, initiateEscapeHatchStep(g.User5.Address, 1), nil)

	hatch, err := definition.GetEscapeHatchRecord(settlementStorage(t, z), 1)
	common.FailIfErr(t, err)
	if hatch.TriggeredAtHeight == 0 {
		t.Fatal("expected a triggered escape hatch")
	}

	domain := func(id uint32, submitPaused bool, modes, escapeHatch string) string {
		return fmt.Sprintf(`{
			"domainId": %d,
			"domainClass": %d,
			"runtimeKind": %d,
			"stfSpecHash": %q,
			"inputSource": %d,
			"execProfile": %d,
			"foreignProfile": %d,
			"profileConfig": "",
			"chainBinding": "",
			"minExecutors": 1,
			"proposerPolicy": %d,
			"valueCaps": "",
			"finalityModel": %d,
			"status": %d,
			"withdrawalDelay": %d,
			"runtimeUpgradeDelay": %d,
			"approvedDAModes": %s,
			"pauseState": {"submitPaused": %t, "relayPaused": false},
			"inputCursor": {"consumedInputCount": 0, "nextBatchId": 0, "recordsSweptThrough": 0, "sweptInputCount": 0},
			"receivedInputCount": 0,
			"escapeHatch": %s
		}`, id, definition.DomainClassExecution, definition.RuntimeKindWASM, types.ZeroHash.String(),
			definition.InputSourceL1Native, definition.ExecProfileAttestation, definition.ForeignProfileNone,
			definition.ProposerPolicySingle, definition.FinalityModelDelay, definition.DomainStatusActive,
			constants.WithdrawalDelayMin, constants.WithdrawalDelayMin, modes, submitPaused, escapeHatch)
	}
	domain1 := domain(1, true, `[0]`, fmt.Sprintf(`{"triggeredAtHeight": %d}`, hatch.TriggeredAtHeight))
	domain2 := domain(2, false, `[]`, `null`)

	d1, err := a.GetDomainInfo(1)
	expectRpcJson(t, d1, err, domain1)
	d2, err := a.GetDomainInfo(2)
	expectRpcJson(t, d2, err, domain2)
	d0, err := a.GetDomainInfo(0)
	expectRpcJson(t, d0, err, `null`)
	d3, err := a.GetDomainInfo(3)
	expectRpcJson(t, d3, err, `null`)

	p0, err := a.GetAllDomains(0, 1)
	expectRpcJson(t, p0, err, fmt.Sprintf(`{"count":2,"list":[%s]}`, domain1))
	p1, err := a.GetAllDomains(1, 1)
	expectRpcJson(t, p1, err, fmt.Sprintf(`{"count":2,"list":[%s]}`, domain2))
	p2, err := a.GetAllDomains(2, 1)
	expectRpcJson(t, p2, err, `{"count":2,"list":[]}`)
	zero, err := a.GetAllDomains(0, 0)
	expectRpcJson(t, zero, err, `{"count":2,"list":[]}`)
	if _, err := a.GetAllDomains(0, api.RpcMaxPageSize+1); err != api.ErrPageSizeParamTooBig {
		t.Fatalf("expected ErrPageSizeParamTooBig, got %v", err)
	}

	ps, err := a.GetPauseState()
	expectRpcJson(t, ps, err, `{
		"global": {"submitPaused": false, "relayPaused": false},
		"domains": [
			{"domainId": 1, "submitPaused": true, "relayPaused": false},
			{"domainId": 7, "submitPaused": false, "relayPaused": true}
		]
	}`)
}

// batchesFixture returns the withdrawal fixture plus a second batch
// (id 1) chained on batch 0.
func batchesFixture(t *testing.T) *withdrawalSetup {
	w := newWithdrawalZenon(t)
	w.finalize(t)
	settlementCall(t, w.z, depositStep(g.User2.Address, types.ZnnTokenStandard, 7, 1, types.Hash{0x07}, 5000), nil)
	commitment := batchCommitment(1, 1, 2, 2, hashFromByteForTest(0x51), hashFromByteForTest(0x52), g.User1.Address, znnDepositSummary(7))
	settlementCall(t, w.z, submitBatchStep(g.User1.Address, commitment), nil)
	return w
}

func TestSettlementRpc_Batches(t *testing.T) {
	w := batchesFixture(t)
	z := w.z
	a := embedded.NewSettlementApi(z)

	b0, err := definition.GetBatchEntry(settlementStorage(t, z), 1, 0)
	common.FailIfErr(t, err)
	b1, err := definition.GetBatchEntry(settlementStorage(t, z), 1, 1)
	common.FailIfErr(t, err)

	batch := func(e *definition.BatchEntry, finalized bool) string {
		return fmt.Sprintf(`{
			"domainId": 1,
			"batchId": %d,
			"protocolVersion": 1,
			"firstInputSeq": %d,
			"lastInputSeq": %d,
			"preStateRoot": %q,
			"postStateRoot": %q,
			"inputRoot": %q,
			"receiptRoot": %q,
			"eventRoot": %q,
			"outboxRoot": %q,
			"daHash": %q,
			"daMode": 0,
			"executorPillar": %q,
			"submittedAtHeight": %d,
			"finalizeAtHeight": %d,
			"assetFlowSummary": %q,
			"proofData": %q,
			"state": %d,
			"isFinalized": %t
		}`, e.BatchId, e.FirstInputSeq, e.LastInputSeq, e.PreStateRoot, e.PostStateRoot, e.InputRoot,
			e.ReceiptRoot, e.EventRoot, e.OutboxRoot, e.DaHash, g.Pillar1Name, e.SubmittedAtHeight,
			e.FinalizeAtHeight, base64Of(e.AssetFlowSummary), base64Of(e.ProofData), e.State, finalized)
	}

	if b0.BatchId != 0 || b1.BatchId != 1 || b1.FinalizeAtHeight <= b0.FinalizeAtHeight {
		t.Fatalf("unexpected fixture batches %+v %+v", b0, b1)
	}

	// batch 0 finalizes before batch 1
	if rpcFrontierHeight(t, z) < b0.FinalizeAtHeight {
		t.Fatal("expected batch 0 to be finalized at the fixture frontier")
	}

	list, err := a.GetBatches(1, 0, 1)
	expectRpcJson(t, list, err, fmt.Sprintf(`{"count":2,"list":[%s]}`, batch(b1, false)))
	list, err = a.GetBatches(1, 1, 1)
	expectRpcJson(t, list, err, fmt.Sprintf(`{"count":2,"list":[%s]}`, batch(b0, true)))
	list, err = a.GetBatches(1, 2, 1)
	expectRpcJson(t, list, err, `{"count":2,"list":[]}`)
	if _, err := a.GetBatches(1, 0, api.RpcMaxPageSize+1); err != api.ErrPageSizeParamTooBig {
		t.Fatalf("expected ErrPageSizeParamTooBig, got %v", err)
	}
	empty, err := a.GetBatches(9, 0, 10)
	expectRpcJson(t, empty, err, `{"count":0,"list":[]}`)

	one, err := a.GetBatch(1, 1)
	expectRpcJson(t, one, err, batch(b1, false))
	missing, err := a.GetBatch(1, 9)
	expectRpcJson(t, missing, err, `null`)

	z.InsertMomentumsTo(b1.FinalizeAtHeight - 1)
	one, err = a.GetBatch(1, 1)
	common.FailIfErr(t, err)
	if one.IsFinalized {
		t.Fatalf("expected batch 1 unfinalized at height %d", b1.FinalizeAtHeight-1)
	}
	z.InsertNewMomentum()
	one, err = a.GetBatch(1, 1)
	common.FailIfErr(t, err)
	if !one.IsFinalized {
		t.Fatalf("expected batch 1 finalized at height %d", b1.FinalizeAtHeight)
	}
	// the stored state is unchanged by finalization
	if one.State != definition.BatchStateSubmitted {
		t.Fatalf("expected the stored state %d, got %d", definition.BatchStateSubmitted, one.State)
	}
}

func base64Of(b []byte) string {
	out, _ := json.Marshal(b)
	return strings.Trim(string(out), `"`)
}

func TestSettlementRpc_Custody(t *testing.T) {
	w := newWithdrawalZenon(t)
	z := w.z
	a := embedded.NewSettlementApi(z)
	w.finalize(t)
	for i := range w.leaves {
		w.relay(t, i, nil)
	}

	storage := settlementStorage(t, z)
	entries := make([]*definition.ReleaseQueueEntry, 3)
	for i := range entries {
		e, err := definition.GetReleaseQueueEntry(storage, 1, uint64(i))
		common.FailIfErr(t, err)
		entries[i] = e
	}
	item := func(i int) string {
		e := entries[i]
		return fmt.Sprintf(`{"registrationIndex":%d,"recipient":%q,"assetId":%q,"amount":%q,"outboxId":%q}`,
			e.RegistrationIndex, e.Recipient.String(), e.AssetId.String(), e.Amount.String(), e.OutboxId.String())
	}

	q, err := a.GetReleaseQueue(1, 0, 2)
	expectRpcJson(t, q, err, fmt.Sprintf(`{"head":0,"tail":3,"count":3,"list":[%s,%s]}`, item(0), item(1)))
	q, err = a.GetReleaseQueue(1, 1, 2)
	expectRpcJson(t, q, err, fmt.Sprintf(`{"head":0,"tail":3,"count":3,"list":[%s]}`, item(2)))
	if _, err := a.GetReleaseQueue(1, 0, api.RpcMaxPageSize+1); err != api.ErrPageSizeParamTooBig {
		t.Fatalf("expected ErrPageSizeParamTooBig, got %v", err)
	}
	other, err := a.GetReleaseQueue(2, 0, 10)
	expectRpcJson(t, other, err, `{"head":0,"tail":0,"count":0,"list":[]}`)

	processed, err := a.IsOutboxProcessed(entries[0].OutboxId)
	expectRpcJson(t, processed, err, `true`)
	processed, err = a.IsOutboxProcessed(types.Hash{0x01})
	expectRpcJson(t, processed, err, `false`)

	znn := assetIdFor(types.ZnnTokenStandard)
	info, err := a.GetAssetInfo(1, znn)
	expectRpcJson(t, info, err, fmt.Sprintf(`{
		"domainId": 1,
		"assetId": %q,
		"maxBatchWithdrawal": "1000",
		"cumulativeDeposited": "150",
		"totalDeposited": "150",
		"pendingWithdrawalReserve": "140",
		"totalReleased": "0"
	}`, znn.String()))
	unconfigured, err := a.GetAssetInfo(1, assetIdFor(types.QsrTokenStandard))
	expectRpcJson(t, unconfigured, err, `null`)

	settlementCall(t, z, releaseWithdrawalsStep(g.User1.Address, 1), nil)
	q, err = a.GetReleaseQueue(1, 0, 10)
	expectRpcJson(t, q, err, `{"head":3,"tail":3,"count":0,"list":[]}`)
	info, err = a.GetAssetInfo(1, znn)
	common.FailIfErr(t, err)
	if info.PendingWithdrawalReserve != "0" || info.TotalReleased != "140" {
		t.Fatalf("unexpected asset counters after release: %+v", info)
	}
}

func TestSettlementRpc_DeployRecords(t *testing.T) {
	z := newDeployExpiryZenon(t)
	a := embedded.NewSettlementApi(z)
	registerDomains(t, z, 2)
	u1, u2 := g.User1.Address, g.User2.Address
	hashA, hashB, hashC := types.Hash{0xA1}, types.Hash{0xA2}, types.Hash{0xA3}

	start := func(user types.Address, domain uint32, codeHash types.Hash) {
		settlementCall(t, z, deployStartStep(user, domain, codeHash, 100, deployChunkCount(100), nil), nil)
	}
	start(u1, 1, hashB)
	start(u1, 1, hashA)
	start(u1, 2, hashC)
	start(u2, 1, hashA)

	record := func(deployer types.Address, domain uint32, codeHash types.Hash, expired bool) string {
		r, err := definition.GetDeployRecord(settlementStorage(t, z), domain, deployer, codeHash)
		common.FailIfErr(t, err)
		return fmt.Sprintf(`{
			"domainId": %d,
			"deployer": %q,
			"codeHash": %q,
			"metadataHash": %q,
			"totalSize": 100,
			"chunkCount": %d,
			"startHeight": %d,
			"receivedCount": 0,
			"expiresAtHeight": %d,
			"isExpired": %t
		}`, domain, deployer.String(), codeHash.String(), r.MetadataHash.String(), deployChunkCount(100),
			r.StartHeight, r.StartHeight+constants.DeploymentExpiryWindow, expired)
	}

	list, err := a.GetDeployRecordsByDeployer(1, u1, 0, 10)
	expectRpcJson(t, list, err, fmt.Sprintf(`{"count":2,"list":[%s,%s]}`,
		record(u1, 1, hashA, false), record(u1, 1, hashB, false)))
	page, err := a.GetDeployRecordsByDeployer(1, u1, 1, 1)
	expectRpcJson(t, page, err, fmt.Sprintf(`{"count":2,"list":[%s]}`, record(u1, 1, hashB, false)))
	if _, err := a.GetDeployRecordsByDeployer(1, u1, 0, api.RpcMaxPageSize+1); err != api.ErrPageSizeParamTooBig {
		t.Fatalf("expected ErrPageSizeParamTooBig, got %v", err)
	}
	none, err := a.GetDeployRecordsByDeployer(1, g.User3.Address, 0, 10)
	expectRpcJson(t, none, err, `{"count":0,"list":[]}`)

	advanceMomentums(t, z, constants.DeploymentExpiryWindow)
	list, err = a.GetDeployRecordsByDeployer(1, u1, 0, 10)
	expectRpcJson(t, list, err, fmt.Sprintf(`{"count":2,"list":[%s,%s]}`,
		record(u1, 1, hashA, true), record(u1, 1, hashB, true)))

	one, err := a.GetDeployRecord(1, u2, hashA)
	expectRpcJson(t, one, err, record(u2, 1, hashA, true))
	missing, err := a.GetDeployRecord(1, u2, types.Hash{0xEE})
	expectRpcJson(t, missing, err, `null`)
}

func TestSettlementRpc_NetworkState(t *testing.T) {
	z := newDeployZenon(t)
	constants.MinAdministratorDelay = 10
	a := embedded.NewSettlementApi(z)

	registerExecutorFully(t, z, g.Pillar1Name, g.User1.Address)
	completeDeploy(t, z, g.User1.Address, deployCodeHash)
	settlementCall(t, z, deployFinalizeStep(g.User1.Address, deployDomain, deployCodeHash, types.ZnnTokenStandard, 300), nil)

	pc, err := a.GetPeripheryConfig()
	expectRpcJson(t, pc, err, `{"znnPerByteOfBytecode":"3","znnPerByteOfState":"1"}`)

	set, err := definition.GetExecutorSet(settlementStorage(t, z))
	common.FailIfErr(t, err)
	if len(set) != 1 {
		t.Fatalf("expected one executor, got %d", len(set))
	}
	es, err := a.GetExecutorSet()
	expectRpcJson(t, es, err, fmt.Sprintf(`{"count":1,"list":[{"pillarName":%q,"executorAddress":%q,"registrationHeight":%d,"status":0}]}`,
		g.Pillar1Name, g.User1.Address.String(), set[0].RegistrationHeight))

	fi, err := a.GetFeeInfo()
	expectRpcJson(t, fi, err, `{"totalFeesCollected":"300","totalFeesBurned":"0","totalFeesCredited":"0","totalRewardsCollected":"0","pendingEpochHead":null}`)
}

func timeChallengeByPrefix(t *testing.T, a *embedded.SettlementApi, prefix string) *embedded.SettlementTimeChallenge {
	t.Helper()
	list, err := a.GetTimeChallengesInfo()
	common.FailIfErr(t, err)
	var found *embedded.SettlementTimeChallenge
	for _, c := range list.List {
		if strings.HasPrefix(c.MethodName, prefix) {
			if found != nil {
				t.Fatalf("two challenges match %q", prefix)
			}
			found = c
		}
	}
	return found
}

func TestSettlementRpc_TimeChallenges(t *testing.T) {
	z := newSettlementZenon(t)
	constants.MinSoftDelay = 10
	constants.MinAdministratorDelay = 20
	a := embedded.NewSettlementApi(z)
	registerDomains(t, z, 1)

	// 1. RegisterExecutor and RegisterDomain challenges
	settlementCall(t, z, registerExecutorStep(g.User5.Address, g.Pillar1Name, g.User1.Address), nil)
	caps := phase1Domain(1)
	caps.ValueCaps = valueCapsWithWindow(constants.WithdrawalDelayMin, 1)
	settlementCall(t, z, registerDomainStep(g.User5.Address, caps), nil)

	// 2. SetAssetConfig challenge, started last so the apply height
	// stays ahead of the frontier
	assetId := assetIdFor(types.ZnnTokenStandard)
	assetBlock := func() *nom.AccountBlock { return setAssetConfigStep(g.User5.Address, 1, assetId, big.NewInt(1000)) }
	settlementCall(t, z, assetBlock(), nil)

	executor := timeChallengeByPrefix(t, a, definition.SettlementRegisterExecutorMethodName)
	domain := timeChallengeByPrefix(t, a, definition.SettlementRegisterDomainMethodName+"/1")
	asset := timeChallengeByPrefix(t, a, definition.SettlementSetAssetConfigMethodName+"/1/")
	if executor == nil || domain == nil || asset == nil {
		t.Fatalf("expected three pending challenges, got %+v %+v %+v", executor, domain, asset)
	}
	if executor.DueHeight != executor.ChallengeStartHeight+21 {
		t.Fatalf("RegisterExecutor dueHeight %d, start %d", executor.DueHeight, executor.ChallengeStartHeight)
	}
	runtimeUpgradeDelay := domainConfigOf(t, z, 1).RuntimeUpgradeDelay
	wantDomainDelay := max(uint64(20), runtimeUpgradeDelay)
	if domain.DueHeight != domain.ChallengeStartHeight+wantDomainDelay+1 {
		t.Fatalf("RegisterDomain dueHeight %d, start %d, delay %d", domain.DueHeight, domain.ChallengeStartHeight, wantDomainDelay)
	}
	if asset.DueHeight != asset.ChallengeStartHeight+11 {
		t.Fatalf("SetAssetConfig dueHeight %d, start %d", asset.DueHeight, asset.ChallengeStartHeight)
	}
	if !strings.HasSuffix(asset.MethodName, assetId.String()) {
		t.Fatalf("expected the asset id in %q", asset.MethodName)
	}
	list, err := a.GetTimeChallengesInfo()
	common.FailIfErr(t, err)
	if list.Count != 3 || len(list.List) != 3 {
		t.Fatalf("expected 3 challenges, got %+v", list)
	}

	// 3. the apply is refused at dueHeight-1 and passes at dueHeight
	due := asset.DueHeight
	if rpcFrontierHeight(t, z) > due-2 {
		t.Fatalf("frontier %d is past dueHeight-2 = %d", rpcFrontierHeight(t, z), due-2)
	}
	z.InsertMomentumsTo(due - 2)
	probe := z.CallContract(assetBlock())
	z.InsertNewMomentum()
	apply := z.CallContract(assetBlock())
	z.InsertNewMomentum()
	z.InsertNewMomentum()
	probe.Error(t, constants.ErrTimeChallengeNotDue)
	apply.Error(t, nil)

	// 4. the applied entry is gone, the others stay
	if gone := timeChallengeByPrefix(t, a, definition.SettlementSetAssetConfigMethodName+"/1/"); gone != nil {
		t.Fatalf("expected the applied challenge to be omitted, got %+v", gone)
	}
	list, err = a.GetTimeChallengesInfo()
	common.FailIfErr(t, err)
	if list.Count != 2 || len(list.List) != 2 {
		t.Fatalf("expected 2 challenges, got %+v", list)
	}
	if timeChallengeByPrefix(t, a, definition.SettlementRegisterExecutorMethodName) == nil ||
		timeChallengeByPrefix(t, a, definition.SettlementRegisterDomainMethodName+"/1") == nil {
		t.Fatal("expected the unapplied challenges to remain")
	}
}
