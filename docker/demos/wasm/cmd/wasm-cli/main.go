package main

import (
	"flag"
	"fmt"
	"math/big"
	"os"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/docker/demos/wasm/wasmclient"
	"github.com/zenon-network/go-zenon/wallet"
)

var (
	urlFlag      = flag.String("url", "http://127.0.0.1:35997", "Node RPC URL")
	mnemonicFlag = flag.String("mnemonic", wasmclient.DevMnemonic, "BIP-39 mnemonic")
	passwordFlag = flag.String("password", wasmclient.DevPassword, "Mnemonic password")
	indexFlag    = flag.Uint("index", 3, "Account derivation index")
	chainIDFlag  = flag.Uint64("chain-id", wasmclient.DevChainIdentifier, "Chain identifier")
	dryRunFlag   = flag.Bool("dry-run", false, "Build block but do not submit")
	powFlag      = flag.Bool("pow", false, "Use PoW instead of fused plasma")
	diffFlag     = flag.Uint64("difficulty", 78750000, "PoW difficulty (when --pow)")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "wasm-cli — Zenon WASM contract tool\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  wasm-cli <command> [args] [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  deploy <file.wasm> --salt <hex> [--upgradeable]\n")
		fmt.Fprintf(os.Stderr, "  execute <addr> [--args <hex>] [--function <name>]\n")
		fmt.Fprintf(os.Stderr, "  fund <addr> --qsr <amount>\n")
		fmt.Fprintf(os.Stderr, "  view <addr> [--args <hex>] [--function <name>]\n")
		fmt.Fprintf(os.Stderr, "  info <addr>\n")
		fmt.Fprintf(os.Stderr, "  halt\n")
		fmt.Fprintf(os.Stderr, "  unhalt\n")
		fmt.Fprintf(os.Stderr, "  pause <addr>\n")
		fmt.Fprintf(os.Stderr, "  unpause <addr>\n")
		fmt.Fprintf(os.Stderr, "  fuse-plasma [--qsr <amount>]\n")
		fmt.Fprintf(os.Stderr, "  stats\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()
	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		os.Exit(1)
	}

	cmd := args[0]
	cmdArgs := args[1:]

	var err error
	switch cmd {
	case "deploy":
		err = cmdDeploy(cmdArgs)
	case "execute":
		err = cmdExecute(cmdArgs)
	case "fund":
		err = cmdFund(cmdArgs)
	case "view":
		err = cmdView(cmdArgs)
	case "info":
		err = cmdInfo(cmdArgs)
	case "halt":
		err = cmdHalt(cmdArgs)
	case "unhalt":
		err = cmdUnhalt(cmdArgs)
	case "pause":
		err = cmdPause(cmdArgs)
	case "unpause":
		err = cmdUnpause(cmdArgs)
	case "fuse-plasma":
		err = cmdFusePlasma(cmdArgs)
	case "stats":
		err = cmdStats()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		flag.Usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func connect() (*wasmclient.Client, error) {
	return wasmclient.Dial(*urlFlag)
}

func deriveKey() (*wallet.KeyPair, error) {
	return wasmclient.DeriveKeyPair(*mnemonicFlag, *passwordFlag, uint32(*indexFlag))
}

func parseAddr(s string) (types.Address, error) {
	return types.ParseAddress(s)
}

func publishOrDry(c *wasmclient.Client, block interface{}, label string) error {
	if *dryRunFlag {
		fmt.Printf("[dry-run] %s block built successfully\n", label)
		return nil
	}
	return c.Call(nil, "ledger.publishRawTransaction", block)
}

// bigQSR parses a QSR amount in base units (1 QSR = 1e8).
func bigQSR(s string) (*big.Int, error) {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("invalid amount: %s", s)
	}
	return v, nil
}
