//go:build devnet

package types

// The devnet build makes dev address index 1 of the devnet mnemonic
// (docker/devnet/README.md) the network admin. Its key is public: never
// run a devnet build on any other network.
func init() {
	NetworkAdmin = ParseAddressPanic("z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d")
}
