package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"math/big"
	"os"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/docker/demos/wasm/wasmclient"
	"github.com/zenon-network/go-zenon/vm/constants"
)

func cmdDeploy(args []string) error {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	saltHex := fs.String("salt", "", "32-byte salt (hex)")
	upgradeable := fs.Bool("upgradeable", false, "Mark contract as upgradeable")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: deploy <file.wasm> --salt <hex> [--upgradeable]")
	}
	if *saltHex == "" {
		return fmt.Errorf("--salt is required")
	}

	var salt [32]byte
	saltBytes, err := hex.DecodeString(*saltHex)
	if err != nil || len(saltBytes) != 32 {
		if len(saltBytes) > 0 && len(saltBytes) <= 32 {
			copy(salt[32-len(saltBytes):], saltBytes)
		} else {
			return fmt.Errorf("invalid salt: must be 32 bytes hex")
		}
	} else {
		copy(salt[:], saltBytes)
	}

	bytecode, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("read bytecode: %w", err)
	}

	kp, err := deriveKey()
	if err != nil {
		return err
	}

	wasmAddr := wasmclient.WasmAddress(kp.Address, salt)
	fmt.Printf("Deployer:  %s\n", kp.Address)
	fmt.Printf("Salt:      %s\n", hex.EncodeToString(salt[:]))
	fmt.Printf("Contract:  %s\n", wasmAddr)
	fmt.Printf("Bytecode:  %d bytes\n", len(bytecode))

	cost := int64(len(bytecode)) * int64(constants.QSRPerByteOfBytecode)
	if cost < int64(constants.MinBytecodeCost) {
		cost = int64(constants.MinBytecodeCost)
	}
	fmt.Printf("Cost:      %d base units (%.2f QSR)\n", cost, float64(cost)/1e8)

	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()

	// Step 1: Deploy (upload bytecode)
	if len(bytecode) <= constants.MaxWasmBytecodeSize {
		// Single-shot
		data := wasmclient.PackDeploy(wasmAddr, salt, 0, 1, bytecode)
		block, err := wasmclient.BuildBlock(c, kp, wasmclient.WasmContract,
			big.NewInt(cost), types.QsrTokenStandard, data, wasmclient.EmbeddedWasmDeploy)
		if err != nil {
			return err
		}
		if *dryRunFlag {
			fmt.Printf("[dry-run] Deploy send built, hash: %s\n", block.Hash)
			return nil
		}
		if err := wasmclient.PublishBlock(c, block); err != nil {
			return err
		}
		fmt.Printf("Deploy send published, hash: %s\n", block.Hash)
	} else {
		// Chunked
		chunks := wasmclient.ChunkBytecode(bytecode, constants.MaxArgsBytes)
		if len(chunks) > constants.MaxChunkCount {
			return fmt.Errorf("bytecode too large: %d bytes requires %d chunks (max %d)",
				len(bytecode), len(chunks), constants.MaxChunkCount)
		}
		fmt.Printf("Chunked deploy: %d chunks\n", len(chunks))

		// Compute worst-case cost for chunk 0
		worstCost := int64(len(chunks)) * int64(constants.MaxWasmBytecodeSize) * int64(constants.QSRPerByteOfBytecode)
		if worstCost < int64(constants.MinBytecodeCost) {
			worstCost = int64(constants.MinBytecodeCost)
		}

		for i, chunk := range chunks {
			data := wasmclient.PackDeploy(wasmAddr, salt, uint32(i), uint32(len(chunks)), chunk)

			var amount *big.Int
			if i == 0 {
				amount = big.NewInt(worstCost)
			} else {
				amount = big.NewInt(0)
			}

			block, err := wasmclient.BuildBlock(c, kp, wasmclient.WasmContract,
				amount, types.QsrTokenStandard, data, wasmclient.EmbeddedWasmDeploy)
			if err != nil {
				return fmt.Errorf("chunk %d: %w", i, err)
			}
			if *dryRunFlag {
				fmt.Printf("[dry-run] Chunk %d send built, hash: %s\n", i, block.Hash)
				continue
			}
			if err := wasmclient.PublishBlock(c, block); err != nil {
				return fmt.Errorf("chunk %d: %w", i, err)
			}
			fmt.Printf("Chunk %d send published, hash: %s\n", i, block.Hash)
		}
	}

	// Step 2: Activate (settle + finalize)
	activateData := wasmclient.PackActivate(wasmAddr, salt, *upgradeable)
	znnFee := big.NewInt(int64(constants.ZNNDeployFee))
	block, err := wasmclient.BuildBlock(c, kp, wasmclient.WasmContract,
		znnFee, types.ZnnTokenStandard, activateData, wasmclient.EmbeddedWasmDeploy)
	if err != nil {
		return err
	}
	if *dryRunFlag {
		fmt.Printf("[dry-run] Activate send built, hash: %s\n", block.Hash)
		return nil
	}
	if err := wasmclient.PublishBlock(c, block); err != nil {
		return err
	}
	fmt.Printf("Activate send published, hash: %s\n", block.Hash)

	return nil
}
