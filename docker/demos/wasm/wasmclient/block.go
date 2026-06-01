package wasmclient

import (
	"fmt"
	"math/big"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/pow"
	"github.com/zenon-network/go-zenon/wallet"
)

const DevChainIdentifier uint64 = 69

// Plasma constants
const (
	BasePlasma              = 21000
	EmbeddedSimplePlasma    = 52500
	EmbeddedWasmDeploy      = 210000
	EmbeddedWasmExecute     = 105000
	PoWDifficultyPerPlasma  = 1500
)

// BuildBlock constructs, hashes, and signs a send block.
func BuildBlock(c *Client, kp *wallet.KeyPair, toAddress types.Address, amount *big.Int, tokenStandard types.ZenonTokenStandard, data []byte, fusedPlasma uint64) (*nom.AccountBlock, error) {
	if amount == nil {
		amount = big.NewInt(0)
	}

	// Fetch sender frontier
	frontier, err := c.GetFrontierAccountBlock(kp.Address)
	if err != nil {
		return nil, err
	}

	var height uint64
	var previousHash types.Hash
	if frontier != nil {
		height = frontier.Height + 1
		previousHash = frontier.Hash
	} else {
		height = 1
	}

	// Fetch chain frontier for momentum acknowledgement
	momentum, err := c.GetFrontierMomentum()
	if err != nil {
		return nil, err
	}
	if momentum == nil {
		return nil, fmt.Errorf("no frontier momentum available")
	}

	block := &nom.AccountBlock{
		Version:         1,
		ChainIdentifier: DevChainIdentifier,
		BlockType:       nom.BlockTypeUserSend,
		PreviousHash:    previousHash,
		Height:          height,
		MomentumAcknowledged: types.HashHeight{
			Hash:   momentum.Hash,
			Height: momentum.Height,
		},
		Address:       kp.Address,
		ToAddress:     toAddress,
		Amount:        amount,
		TokenStandard: tokenStandard,
		Data:          data,
		FusedPlasma:   fusedPlasma,
	}

	block.Hash = block.ComputeHash()
	block.PublicKey = kp.Public
	block.Signature = kp.Sign(block.Hash.Bytes())

	return block, nil
}

// BuildBlockWithPoW builds a block and computes a PoW nonce.
func BuildBlockWithPoW(c *Client, kp *wallet.KeyPair, toAddress types.Address, amount *big.Int, tokenStandard types.ZenonTokenStandard, data []byte, difficulty *big.Int) (*nom.AccountBlock, error) {
	block, err := BuildBlock(c, kp, toAddress, amount, tokenStandard, data, 0)
	if err != nil {
		return nil, err
	}

	block.Difficulty = difficulty.Uint64()
	dataHash := pow.GetAccountBlockHash(block)
	nonce := pow.GetPoWNonce(difficulty, dataHash)
	copy(block.Nonce.Data[:], nonce[:8])

	// Re-hash and re-sign with nonce included
	block.Hash = block.ComputeHash()
	block.Signature = kp.Sign(block.Hash.Bytes())

	return block, nil
}

// PublishBlock sends a signed block to the node.
func PublishBlock(c *Client, block *nom.AccountBlock) error {
	return c.rpc.Call(nil, "ledger.publishRawTransaction", block)
}

// DecodeViewU64 decodes a little-endian u64 from a view return. The runtime's
// callView already strips the contract's [u32 len] envelope (runtime.go reads
// the prefix and returns only the inner bytes), so `data` here is the raw
// value — for this contract, the 8-byte LE count.
func DecodeViewU64(data []byte) (uint64, bool) {
	if len(data) < 8 {
		return 0, false
	}
	v := data[:8]
	return uint64(v[0]) | uint64(v[1])<<8 | uint64(v[2])<<16 | uint64(v[3])<<24 |
		uint64(v[4])<<32 | uint64(v[5])<<40 | uint64(v[6])<<48 | uint64(v[7])<<56, true
}
