package main

import (
	"flag"
	"fmt"
	"math/big"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/docker/demos/wasm/wasmclient"
)

func cmdHalt(args []string) error {
	kp, err := deriveKey()
	if err != nil {
		return err
	}
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()

	data := wasmclient.PackHalt()
	block, err := wasmclient.BuildBlock(c, kp, wasmclient.WasmContract,
		big.NewInt(0), types.ZenonTokenStandard{}, data, wasmclient.EmbeddedSimplePlasma)
	if err != nil {
		return err
	}
	if *dryRunFlag {
		fmt.Printf("[dry-run] Halt send built, hash: %s\n", block.Hash)
		return nil
	}
	if err := wasmclient.PublishBlock(c, block); err != nil {
		return err
	}
	fmt.Printf("Halt send published, hash: %s\n", block.Hash)
	return nil
}

func cmdUnhalt(args []string) error {
	kp, err := deriveKey()
	if err != nil {
		return err
	}
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()

	data := wasmclient.PackUnhalt()
	block, err := wasmclient.BuildBlock(c, kp, wasmclient.WasmContract,
		big.NewInt(0), types.ZenonTokenStandard{}, data, wasmclient.EmbeddedSimplePlasma)
	if err != nil {
		return err
	}
	if *dryRunFlag {
		fmt.Printf("[dry-run] Unhalt send built, hash: %s\n", block.Hash)
		return nil
	}
	if err := wasmclient.PublishBlock(c, block); err != nil {
		return err
	}
	fmt.Printf("Unhalt send published, hash: %s\n", block.Hash)
	return nil
}

func cmdPause(args []string) error {
	fs := flag.NewFlagSet("pause", flag.ExitOnError)
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: pause <contract-address>")
	}

	contractAddr, err := parseAddr(fs.Arg(0))
	if err != nil {
		return err
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

	data := wasmclient.PackPause(contractAddr)
	block, err := wasmclient.BuildBlock(c, kp, wasmclient.WasmContract,
		big.NewInt(0), types.ZenonTokenStandard{}, data, wasmclient.EmbeddedSimplePlasma)
	if err != nil {
		return err
	}
	if *dryRunFlag {
		fmt.Printf("[dry-run] Pause send built, hash: %s\n", block.Hash)
		return nil
	}
	if err := wasmclient.PublishBlock(c, block); err != nil {
		return err
	}
	fmt.Printf("Pause send published, hash: %s\n", block.Hash)
	return nil
}

func cmdUnpause(args []string) error {
	fs := flag.NewFlagSet("unpause", flag.ExitOnError)
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: unpause <contract-address>")
	}

	contractAddr, err := parseAddr(fs.Arg(0))
	if err != nil {
		return err
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

	data := wasmclient.PackUnpause(contractAddr)
	block, err := wasmclient.BuildBlock(c, kp, wasmclient.WasmContract,
		big.NewInt(0), types.ZenonTokenStandard{}, data, wasmclient.EmbeddedSimplePlasma)
	if err != nil {
		return err
	}
	if *dryRunFlag {
		fmt.Printf("[dry-run] Unpause send built, hash: %s\n", block.Hash)
		return nil
	}
	if err := wasmclient.PublishBlock(c, block); err != nil {
		return err
	}
	fmt.Printf("Unpause send published, hash: %s\n", block.Hash)
	return nil
}

func cmdFusePlasma(args []string) error {
	fs := flag.NewFlagSet("fuse-plasma", flag.ExitOnError)
	qsr := fs.String("qsr", "1000000000", "QSR amount in base units (default 10 QSR)")
	fs.Parse(args)

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

	if err := c.FusePlasma(kp, amount); err != nil {
		return err
	}
	fmt.Printf("Fused %s base units QSR for %s\n", amount.String(), kp.Address)
	return nil
}
