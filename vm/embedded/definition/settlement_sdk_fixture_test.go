package definition

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/crypto"
	"github.com/zenon-network/go-zenon/common/trie"
	"github.com/zenon-network/go-zenon/common/types"
)

// TestDumpSettlementSdkFixture writes the packed calldata and time-challenge
// params hashes the TypeScript SDK asserts against. It runs only when
// SETTLEMENT_SDK_FIXTURE_OUT names an output file.
func TestDumpSettlementSdkFixture(t *testing.T) {
	out := os.Getenv("SETTLEMENT_SDK_FIXTURE_OUT")
	if out == "" {
		t.Skip("SETTLEMENT_SDK_FIXTURE_OUT not set")
	}

	hashA := types.NewHash([]byte("a"))
	hashB := types.NewHash([]byte("b"))
	addr := types.ParseAddressPanic("z1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqsggv2f")
	other := types.ParseAddressPanic("z1qxemdeddedxplasmaxxxxxxxxxxxxxxxxsctrp")
	big1 := new(big.Int).Mul(big.NewInt(123456789), big.NewInt(1_000_000_000_000))
	big2 := big.NewInt(42)
	bytesA := []byte{0x01, 0x02, 0x03, 0xff}
	bytesB := []byte{}

	type call struct {
		Method string        `json:"method"`
		Args   []interface{} `json:"-"`
		Data   string        `json:"data"`
	}
	calls := []call{
		{Method: "Update", Args: nil},
		{Method: "CollectReward", Args: nil},
		{Method: "ReleaseWithdrawals", Args: []interface{}{uint32(7)}},
		{Method: "RegisterExecutor", Args: []interface{}{"pillar-one", addr}},
		{Method: "SetPeripheryConfig", Args: []interface{}{big1, big2}},
		{Method: "RegisterDomain", Args: []interface{}{uint32(3), uint8(0), uint8(0), hashA, uint8(0), uint8(2), uint8(0), bytesA, bytesB, uint8(2), uint8(0), uint8(2), uint8(0), uint64(100800), uint64(201600)}},
		{Method: "SetDomainConfig", Args: []interface{}{uint32(3), uint64(100800), uint64(201600)}},
		{Method: "SetAssetConfig", Args: []interface{}{uint32(3), hashB, big1}},
		{Method: "SetApprovedDAModes", Args: []interface{}{uint32(3), []byte{0}}},
		{Method: "Pause", Args: []interface{}{uint32(3), true, uint8(1), false}},
		{Method: "InitiateEscapeHatch", Args: []interface{}{uint32(3)}},
		{Method: "Deposit", Args: []interface{}{uint32(3), hashA, uint64(500000)}},
		{Method: "RelayMessage", Args: []interface{}{uint32(3), uint64(9), bytesA, bytesA}},
		{Method: "DeployContractStart", Args: []interface{}{uint32(3), hashA, types.NewHash(bytesA), uint64(40000), uint32(3), bytesA}},
		{Method: "DeployContractChunk", Args: []interface{}{uint32(3), hashA, uint32(2), types.NewHash(bytesA), bytesA}},
		{Method: "DeployContractFinalize", Args: []interface{}{uint32(3), hashA}},
		{Method: "CallL2", Args: []interface{}{uint32(3), hashA, uint64(500000), bytesA}},
		{Method: "UpgradeContract", Args: []interface{}{uint32(3), hashA, hashB}},
		{Method: "TransferContractOwnership", Args: []interface{}{uint32(3), hashA, other}},
		{Method: "BuyStorage", Args: []interface{}{uint32(3), hashA, uint64(1024)}},
	}
	for i := range calls {
		data, err := ABISettlement.PackMethod(calls[i].Method, calls[i].Args...)
		if err != nil {
			t.Fatalf("%s: %v", calls[i].Method, err)
		}
		calls[i].Data = hex.EncodeToString(data)
	}

	hashHex := func(parts ...[]byte) string { return hex.EncodeToString(crypto.Hash(parts...)) }
	paramsHashes := map[string]string{
		"RegisterDomain": hashHex(
			common.Uint32ToBytes(3), hashA.Bytes(), []byte{2},
			common.Uint64ToBytes(100800), common.Uint64ToBytes(201600)),
		"SetDomainConfig": hashHex(
			common.Uint32ToBytes(3), common.Uint64ToBytes(100800), common.Uint64ToBytes(201600)),
		"SetAssetConfig":      hashHex(common.Uint32ToBytes(3), hashB.Bytes(), common.BigIntToBytes(big1)),
		"SetApprovedDAModes":  hashHex(common.Uint32ToBytes(3), []byte{0}),
		"InitiateEscapeHatch": hashHex(common.Uint32ToBytes(3)),
		"RegisterExecutor":    hashHex([]byte("pillar-one"), addr.Bytes()),
		"SetPeripheryConfig":  hashHex(common.BigIntToBytes(big1), common.BigIntToBytes(big2)),
	}

	fixture := map[string]interface{}{
		"calls":        calls,
		"paramsHashes": paramsHashes,
	}
	raw, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

type sdkVariable struct {
	Name   string        `json:"name"`
	Args   []interface{} `json:"args"`
	Inputs []string      `json:"types"`
	Data   string        `json:"data"`
}

type sdkProofEntry struct {
	Label  string `json:"label"`
	Key    string `json:"key"`
	Value  string `json:"value"`
	Proof  string `json:"proof"`
	Absent bool   `json:"absent"`
}

// TestDumpSettlementSdkStateFixture writes every Settlement variable packed
// with deterministic values, and a small state tree with inclusion and
// absence proofs over Settlement storage keys. It runs only when
// SETTLEMENT_SDK_STATE_FIXTURE_OUT names an output file.
func TestDumpSettlementSdkStateFixture(t *testing.T) {
	out := os.Getenv("SETTLEMENT_SDK_STATE_FIXTURE_OUT")
	if out == "" {
		t.Skip("SETTLEMENT_SDK_STATE_FIXTURE_OUT not set")
	}

	var entries []struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		Inputs []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"inputs"`
	}
	if err := json.NewDecoder(strings.NewReader(jsonSettlement)).Decode(&entries); err != nil {
		t.Fatal(err)
	}

	sample := func(typ string, i int) (interface{}, interface{}) {
		n := uint8(i + 3)
		switch typ {
		case "uint8":
			return n, uint64(n)
		case "uint16":
			return uint16(n), uint64(n)
		case "uint32":
			return uint32(n) + 1000, uint64(n) + 1000
		case "uint64":
			return uint64(n) + 1_000_000, uint64(n) + 1_000_000
		case "uint256":
			v := new(big.Int).Mul(big.NewInt(int64(n)), big.NewInt(1_000_000_000_000_000))
			return v, v.String()
		case "bool":
			return true, true
		case "string":
			return "pillar-" + string(rune('a'+i)), "pillar-" + string(rune('a'+i))
		case "hash":
			h := types.NewHash([]byte{n})
			return h, h.String()
		case "address":
			return types.ParseAddressPanic("z1qxemdeddedxplasmaxxxxxxxxxxxxxxxxsctrp"), "z1qxemdeddedxplasmaxxxxxxxxxxxxxxxxsctrp"
		case "bytes":
			b := []byte{n, n + 1, n + 2}
			return b, hex.EncodeToString(b)
		}
		t.Fatalf("unsupported type %s", typ)
		return nil, nil
	}

	var variables []sdkVariable
	packed := map[string][]byte{}
	for _, e := range entries {
		if e.Type != "variable" {
			continue
		}
		var args, expect []interface{}
		var typs []string
		for i, in := range e.Inputs {
			a, x := sample(in.Type, i)
			args = append(args, a)
			expect = append(expect, x)
			typs = append(typs, in.Name+":"+in.Type)
		}
		data, err := ABISettlement.PackVariable(e.Name, args...)
		if err != nil {
			t.Fatalf("%s: %v", e.Name, err)
		}
		packed[e.Name] = data
		variables = append(variables, sdkVariable{Name: e.Name, Args: expect, Inputs: typs, Data: hex.EncodeToString(data)})
	}

	treeKey := func(k []byte) []byte {
		return common.JoinBytes([]byte{0x03}, types.SettlementContract.Bytes(), []byte{0x04}, k)
	}
	challengeData, err := ABICommon.PackVariable(timeChallengeInfoVariableName, "SetDomainConfig/3", types.NewHash([]byte("p")), uint64(77))
	if err != nil {
		t.Fatal(err)
	}
	type kv struct {
		label string
		key   []byte
		value []byte
	}
	state := []kv{
		{"domainRecord/3", treeKey(common.JoinBytes(domainRecordPrefix, uint32ToBytesBE(3))), packed[DomainRecordVariableName]},
		{"domainConfig/3", treeKey(common.JoinBytes(domainConfigPrefix, uint32ToBytesBE(3))), packed[domainConfigVariableName]},
		{"batchEntry/3/0", treeKey(common.JoinBytes(batchEntryPrefix, uint32ToBytesBE(3), uint64ToBytesBE(0))), packed[BatchEntryVariableName]},
		{"processedOutbox", treeKey(common.JoinBytes(processedOutboxPrefix, types.NewHash([]byte("o")).Bytes())), []byte{0x01}},
		{"timeChallenge/SetDomainConfig/3", treeKey(common.JoinBytes([]byte{135}, crypto.Hash([]byte("SetDomainConfig/3")))), challengeData},
		{"other", common.JoinBytes([]byte{0x03}, types.PlasmaContract.Bytes(), []byte{0x04}, []byte("x")), []byte("unrelated")},
	}
	var paths []trie.Path
	var values [][]byte
	for _, s := range state {
		paths = append(paths, trie.Path(types.NewHash(s.key)))
		values = append(values, s.value)
	}
	root, err := trie.RootOfLeaves(paths, values)
	if err != nil {
		t.Fatal(err)
	}

	var proofs []sdkProofEntry
	for i, s := range state {
		present, value, proof, err := trie.ProveByPath(paths, values, paths[i])
		if err != nil || !present {
			t.Fatalf("%s: present=%v err=%v", s.label, present, err)
		}
		proofs = append(proofs, sdkProofEntry{Label: s.label, Key: hex.EncodeToString(s.key), Value: hex.EncodeToString(value), Proof: hex.EncodeToString(proof)})
	}
	absentKey := treeKey(common.JoinBytes(domainRecordPrefix, uint32ToBytesBE(99)))
	present, _, proof, err := trie.ProveByPath(paths, values, trie.Path(types.NewHash(absentKey)))
	if err != nil || present {
		t.Fatalf("absent: present=%v err=%v", present, err)
	}
	proofs = append(proofs, sdkProofEntry{Label: "domainRecord/99", Key: hex.EncodeToString(absentKey), Proof: hex.EncodeToString(proof), Absent: true})

	raw, err := json.MarshalIndent(map[string]interface{}{
		"variables": variables,
		"root":      root.String(),
		"proofs":    proofs,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
