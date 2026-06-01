package main

import (
	"fmt"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/docker/demos/wasm/wasmclient"
)

// crossPillar compares changesHash for Execute receive blocks across all pillars.
func crossPillar() error {
	addr, err := types.ParseAddress(*addrFlag)
	if err != nil {
		return err
	}

	primary, err := connectPrimary()
	if err != nil {
		return err
	}
	defer primary.Close()

	// Determine height range
	fromHeight := *fromFlag
	toHeight := *toFlag
	if toHeight == 0 {
		m, err := primary.GetFrontierMomentum()
		if err != nil {
			return err
		}
		toHeight = m.Height
	}

	fmt.Printf("Auditing contract %s from height %d to %d\n", addr, fromHeight, toHeight)

	// Connect to each pillar
	urls := pillarURLs()
	clients := make([]*wasmclient.Client, len(urls))
	for i, url := range urls {
		c, err := wasmclient.Dial(url)
		if err != nil {
			return fmt.Errorf("connect pillar %d (%s): %w", i, url, err)
		}
		defer c.Close()
		clients[i] = c
	}

	// Walk the contract's account blocks and compare changesHash across pillars.
	// The primary node's account blocks at each height are the source of truth;
	// for each block, fetch the same block from each pillar and compare changesHash.
	pass := true
	blockHeight := uint64(1)

	type blockEntry struct {
		Hash         types.Hash `json:"hash"`
		Height       uint64     `json:"height"`
		ChangesHash  types.Hash `json:"changesHash"`
		BlockType    uint64     `json:"blockType"`
	}

	for blockHeight <= toHeight {
		var blocks []blockEntry
		err := primary.Call(&blocks, "ledger.getAccountBlocksByHeight", addr.String(), blockHeight, uint32(50))
		if err != nil || len(blocks) == 0 {
			break
		}

		for _, block := range blocks {
			if block.Height > toHeight {
				break
			}
			if block.Height < fromHeight {
				continue
			}
			// Only check receive blocks (contract receives carry changesHash)
			if block.BlockType != 5 { // ContractReceive
				continue
			}

			fmt.Printf("Height %d, block %s: changesHash=%s\n", block.Height, block.Hash, block.ChangesHash)

			// Compare across pillars
			for i, c := range clients {
				var pillarBlock blockEntry
				err := c.Call(&pillarBlock, "ledger.getAccountBlockByHash", block.Hash.String())
				if err != nil {
					fmt.Printf("  pillar %d: ERROR fetching block: %v\n", i, err)
					pass = false
					continue
				}
				if pillarBlock.ChangesHash != block.ChangesHash {
					fmt.Printf("  pillar %d: MISMATCH (got %s)\n", i, pillarBlock.ChangesHash)
					pass = false
				}
			}
		}

		blockHeight += 50
	}

	if pass {
		fmt.Println("\nWASM-CONTRACT cross-pillar determinism: PASS")
	} else {
		fmt.Println("\nWASM-CONTRACT cross-pillar determinism: FAIL")
	}
	return nil
}
