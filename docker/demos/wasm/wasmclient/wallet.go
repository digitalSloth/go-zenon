package wasmclient

import (
	"github.com/tyler-smith/go-bip39"

	"github.com/zenon-network/go-zenon/wallet"
)

const (
	// DevMnemonic is the committed devnet BIP-39 mnemonic.
	DevMnemonic = "abstract affair idle position alien fluid board ordinary exist afraid chapter wood wood guide sun walnut crew perfect place firm poverty model side million"
	// DevPassword is the BIP-39 seed passphrase (empty for devnet).
	DevPassword = ""
)

// DeriveKeyPair derives a KeyPair from a mnemonic, password, and account index.
func DeriveKeyPair(mnemonic, password string, index uint32) (*wallet.KeyPair, error) {
	seed := bip39.NewSeed(mnemonic, password)
	return wallet.DeriveWithIndex(index, seed)
}

// DevKeyPair derives a devnet KeyPair at the given index.
func DevKeyPair(index uint32) (*wallet.KeyPair, error) {
	return DeriveKeyPair(DevMnemonic, DevPassword, index)
}
