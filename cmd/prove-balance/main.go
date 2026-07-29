// Command prove-balance is a standalone example light-client: it asks a znnd node for a
// momentum and a Merkle proof of an account's token balance, then verifies that proof against
// the momentum's StateRoot using the storage-free verifier in common/trie — no trust in the
// node beyond the signed momentum header.
//
// Usage:
//
//	go run ./cmd/prove-balance -address z1q... [-token zts1...] [-height N] [-url http://127.0.0.1:35997]
//
// -height 0 (default) uses the frontier momentum. -token defaults to ZNN.
package main

import (
	"flag"
	"fmt"
	"math/big"
	"os"

	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/trie"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/rpc/server"
	"github.com/zenon-network/go-zenon/wallet"
)

// momentumHeader is the subset of the ledger.Momentum RPC response we need.
type momentumHeader struct {
	Hash      types.Hash `json:"hash"`
	Height    uint64     `json:"height"`
	StateRoot types.Hash `json:"stateRoot"`
	PublicKey []byte     `json:"publicKey"`
	Signature []byte     `json:"signature"`
}

type momentumList struct {
	List []momentumHeader `json:"list"`
}

// stateProof mirrors rpc/api.StateProof (the ledger.getProof response).
type stateProof struct {
	Value []byte     `json:"value"` // nil if the key is absent
	Proof []byte     `json:"proof"`
	Root  types.Hash `json:"root"`
}

func main() {
	url := flag.String("url", "http://127.0.0.1:35997", "znnd HTTP RPC endpoint")
	addrStr := flag.String("address", "", "account address (z1...)")
	tokenStr := flag.String("token", "zts1znnxxxxxxxxxxxxx9z4ulx", "token standard (zts1...), default ZNN")
	height := flag.Uint64("height", 0, "momentum height to prove against (0 = frontier)")
	flag.Parse()

	if *addrStr == "" {
		fail("missing -address")
	}
	addr, err := types.ParseAddress(*addrStr)
	if err != nil {
		fail("bad -address: %v", err)
	}
	zts, err := types.ParseZTS(*tokenStr)
	if err != nil {
		fail("bad -token: %v", err)
	}

	client, err := server.Dial(*url)
	if err != nil {
		fail("dial %s: %v", *url, err)
	}
	defer client.Close()

	// 1. Fetch the momentum whose StateRoot we will prove against.
	m, err := fetchMomentum(client, *height)
	if err != nil {
		fail("fetch momentum: %v", err)
	}
	fmt.Printf("momentum     height=%d hash=%s\n", m.Height, m.Hash)
	fmt.Printf("state root   %s\n", m.StateRoot)
	if m.StateRoot.IsZero() {
		fail("StateRoot is zero at height %d — the state-root spork is not active here; nothing to prove", m.Height)
	}

	// 2. The momentum is signed by its producing pillar. Verify the signature binds this hash.
	//    (A full light client would additionally confirm the signer is an eligible pillar for
	//    this height and recompute momentum.ComputeHash() to bind the StateRoot into that hash;
	//    both need more chain context than this example pulls.)
	if ok, err := wallet.VerifySignature(m.PublicKey, m.Hash.Bytes(), m.Signature); err != nil || !ok {
		fail("momentum signature invalid: ok=%v err=%v", ok, err)
	}
	fmt.Printf("signature    valid (signed by %s)\n", types.PubKeyToAddress(m.PublicKey))

	// 3. The balance key in the state tree: {3}|address|{3}|zts.
	key := common.JoinBytes([]byte{0x03}, addr.Bytes(), []byte{0x03}, zts.Bytes())

	// 4. Ask the node for the proof at this height.
	var proof stateProof
	if err := client.Call(&proof, "ledger.getProof", m.Height, key); err != nil {
		fail("ledger.getProof: %v", err)
	}
	// 5. The proof must reconstruct to the *signed* StateRoot — not whatever the node claims.
	if proof.Root != m.StateRoot {
		fail("served proof root %s != signed StateRoot %s", proof.Root, m.StateRoot)
	}

	// 6. Verify, storage-free, against the StateRoot.
	balance := big.NewInt(0)
	if proof.Value == nil {
		// Key absent: the account never held this token ⇒ balance 0.
		ok, err := trie.VerifyAbsence(m.StateRoot, key, proof.Proof)
		if err != nil || !ok {
			fail("absence proof failed: ok=%v err=%v", ok, err)
		}
		fmt.Printf("proof        VERIFIED (absent — never held)\n")
	} else {
		ok, err := trie.VerifyProof(m.StateRoot, key, proof.Value, proof.Proof)
		if err != nil || !ok {
			fail("inclusion proof failed: ok=%v err=%v", ok, err)
		}
		balance = common.BytesToBigInt(proof.Value)
		fmt.Printf("proof        VERIFIED (inclusion)\n")
	}

	fmt.Printf("\n%s holds %s of %s at height %d (proven against StateRoot %s)\n",
		addr, balance.String(), zts, m.Height, m.StateRoot)
	fmt.Printf("(amount is in the token's smallest unit — ZNN/QSR have 8 decimals)\n")
}

func fetchMomentum(client *server.Client, height uint64) (*momentumHeader, error) {
	if height == 0 {
		var m momentumHeader
		if err := client.Call(&m, "ledger.getFrontierMomentum"); err != nil {
			return nil, err
		}
		return &m, nil
	}
	var list momentumList
	if err := client.Call(&list, "ledger.getMomentumsByHeight", height, uint64(1)); err != nil {
		return nil, err
	}
	if len(list.List) == 0 {
		return nil, fmt.Errorf("no momentum at height %d", height)
	}
	return &list.List[0], nil
}

func fail(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
