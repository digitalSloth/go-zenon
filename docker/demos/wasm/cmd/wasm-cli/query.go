package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"

	"github.com/zenon-network/go-zenon/docker/demos/wasm/wasmclient"
)

func cmdView(args []string) error {
	fs := flag.NewFlagSet("view", flag.ExitOnError)
	argsHex := fs.String("args", "", "Arguments (hex)")
	functionName := fs.String("function", "view", "View function name")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: view <contract-address> [--function name] [--args hex]")
	}

	contractAddr, err := parseAddr(fs.Arg(0))
	if err != nil {
		return err
	}

	var fnArgs []byte
	if *argsHex != "" {
		fnArgs, err = hex.DecodeString(*argsHex)
		if err != nil {
			return fmt.Errorf("invalid args hex: %w", err)
		}
	}

	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()

	result, err := c.CallView(contractAddr, *functionName, fnArgs)
	if err != nil {
		return err
	}

	fmt.Printf("Raw return:  %s\n", hex.EncodeToString(result))
	if val, ok := wasmclient.DecodeViewU64(result); ok {
		fmt.Printf("Decoded u64: %d\n", val)
	}
	return nil
}

func cmdInfo(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: info <contract-address>")
	}

	addr, err := parseAddr(args[0])
	if err != nil {
		return err
	}

	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()

	contract, err := c.GetContract(addr)
	if err != nil {
		return err
	}

	data, _ := json.MarshalIndent(contract, "", "  ")
	fmt.Println(string(data))
	return nil
}

func cmdStats() error {
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()

	stats, err := c.GetStats()
	if err != nil {
		return err
	}

	data, _ := json.MarshalIndent(stats, "", "  ")
	fmt.Println(string(data))
	return nil
}
