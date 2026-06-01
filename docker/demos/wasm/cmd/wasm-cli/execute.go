package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"math/big"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/docker/demos/wasm/wasmclient"
)

func cmdExecute(args []string) error {
	fs := flag.NewFlagSet("execute", flag.ExitOnError)
	functionName := fs.String("function", "", "Function name to call")
	argsHex := fs.String("args", "", "Arguments (hex)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: execute <contract-address> [--function name] [--args hex]")
	}

	contractAddr, err := parseAddr(fs.Arg(0))
	if err != nil {
		return err
	}

	var fn string
	if *functionName != "" {
		fn = *functionName
	}

	var fnArgs []byte
	if *argsHex != "" {
		fnArgs, err = hex.DecodeString(*argsHex)
		if err != nil {
			return fmt.Errorf("invalid args hex: %w", err)
		}
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

	data := wasmclient.PackExecute(fn, fnArgs)

	var block *nom.AccountBlock
	if *powFlag {
		block, err = wasmclient.BuildBlockWithPoW(c, kp, contractAddr,
			big.NewInt(0), types.ZenonTokenStandard{}, data, new(big.Int).SetUint64(*diffFlag))
	} else {
		block, err = wasmclient.BuildBlock(c, kp, contractAddr,
			big.NewInt(0), types.ZenonTokenStandard{}, data, wasmclient.EmbeddedWasmExecute)
	}
	if err != nil {
		return err
	}

	if *dryRunFlag {
		fmt.Printf("[dry-run] Execute send built, hash: %s\n", block.Hash)
		return nil
	}

	if err := wasmclient.PublishBlock(c, block); err != nil {
		return err
	}
	fmt.Printf("Execute send published, hash: %s\n", block.Hash)
	return nil
}
