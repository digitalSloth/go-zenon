package main

import (
	"flag"
	"fmt"
	"math/big"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/docker/demos/wasm/wasmclient"
)

// cmdFund sends a plain, data-less QSR transfer to a deployed (0x02) contract.
// Such a transfer is auto-received and credited to the contract's balance
// (see vm.generateEmbeddedReceive: a data-less send to a WASM address just
// AddBalances), giving the contract spendable QSR. That spendable QSR is what
// state_write locks as a per-key storage deposit — without it, state_write
// returns "insufficient deposit" and the contract cannot persist any state.
func cmdFund(args []string) error {
	fs := flag.NewFlagSet("fund", flag.ExitOnError)
	qsr := fs.String("qsr", "100000000", "QSR amount in base units to send (1 QSR = 1e8)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: fund <contract-address> [--qsr amount]")
	}

	contractAddr, err := parseAddr(fs.Arg(0))
	if err != nil {
		return err
	}

	amount, err := bigQSR(*qsr)
	if err != nil {
		return err
	}
	if amount.Sign() <= 0 {
		return fmt.Errorf("--qsr must be positive")
	}

	kp, err := deriveKey()
	if err != nil {
		return err
	}

	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()

	var block *nom.AccountBlock
	if *powFlag {
		block, err = wasmclient.BuildBlockWithPoW(c, kp, contractAddr,
			amount, types.QsrTokenStandard, []byte{}, new(big.Int).SetUint64(*diffFlag))
	} else {
		block, err = wasmclient.BuildBlock(c, kp, contractAddr,
			amount, types.QsrTokenStandard, []byte{}, wasmclient.BasePlasma)
	}
	if err != nil {
		return err
	}

	if *dryRunFlag {
		fmt.Printf("[dry-run] Fund send built, hash: %s\n", block.Hash)
		return nil
	}

	if err := wasmclient.PublishBlock(c, block); err != nil {
		return err
	}
	fmt.Printf("Fund send published, hash: %s (%s QSR base units)\n", block.Hash, amount.String())
	return nil
}
