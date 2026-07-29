package app

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/urfave/cli/v2"

	"github.com/zenon-network/go-zenon/chain"
	"github.com/zenon-network/go-zenon/chain/genesis"
	"github.com/zenon-network/go-zenon/chain/store"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/trie"
)

var (
	StateTreeDataDirFlag = &cli.StringFlag{
		Name:     "data-dir",
		Usage:    "Path to a frozen copy of the node's data folder (the one containing the nom database) to build the state tree against",
		Required: true,
	}
	StateTreeOutFlag = &cli.StringFlag{
		Name:  "statetree-out",
		Usage: "Directory the built statetree database is written to. Defaults to <data-dir>/statetree",
	}

	stateTreeCommand = &cli.Command{
		Name:     "state-tree",
		Usage:    "State-tree maintenance commands",
		Category: "MISCELLANEOUS COMMANDS",
		Subcommands: []*cli.Command{
			stateTreePrebuildCommand,
		},
	}

	stateTreePrebuildCommand = &cli.Command{
		Action: stateTreePrebuildAction,
		Name:   "prebuild",
		Usage:  "Build the state tree from a frozen copy of the chain database ahead of an upgrade",
		Flags: []cli.Flag{
			StateTreeDataDirFlag,
			StateTreeOutFlag,
			GenesisFileFlag,
		},
		ArgsUsage: " ",
	}
)

// stateTreePrebuildAction builds a statetree database from a frozen copy of a node's data folder,
// using the same build engine (chain.PrebuildStateTree) the live node uses at startup and in its
// background build. It opens the frozen copy's nom database read-only and does not open its cache
// database at all (the build engine never reads it), but opening a leveldb read-only is still a
// physical open of that database's files (LOCK, LOG, journal recovery) — see the operator
// procedure doc for what that means for how the frozen copy must be produced. On upgrade, the node
// started against the live data folder resumes the prebuilt statetree database from its persisted
// frontier and folds only the momentums produced since the copy was taken.
func stateTreePrebuildAction(ctx *cli.Context) (actionErr error) {
	defer func() {
		// Every leveldb open below either returns its error normally or, for a handful of
		// operations `common.DealWithErr` still guards deeper in the db packages, panics. Recover
		// so an operator gets a clean CLI error instead of a stack trace.
		if r := recover(); r != nil {
			actionErr = fmt.Errorf("state tree prebuild failed: %v", r)
		}
	}()

	dataDir := ctx.String(StateTreeDataDirFlag.Name)
	nomDir := path.Join(dataDir, "nom")

	if info, err := os.Stat(nomDir); err != nil || !info.IsDir() {
		return fmt.Errorf("--data-dir %q does not contain a nom database (expected a directory at %q); is this a node data folder?", dataDir, nomDir)
	}

	stateTreeDir := ctx.String(StateTreeOutFlag.Name)
	if stateTreeDir == "" {
		stateTreeDir = path.Join(dataDir, "statetree")
	}

	if err := validateStateTreeOutDir(stateTreeDir, dataDir); err != nil {
		return err
	}

	genesisConfig, err := loadStateTreePrebuildGenesis(ctx)
	if err != nil {
		return err
	}

	chainManager, err := db.NewReadOnlyLevelDBManager(nomDir)
	if err != nil {
		return fmt.Errorf("can't open the nom database at %q: %w", nomDir, err)
	}
	defer chainManager.Stop()

	c := chain.NewChain(chainManager, nil, genesisConfig, stateTreeDir, false)

	fmt.Printf("Building the state tree at %v from the frozen chain database at %v\n", stateTreeDir, dataDir)

	frontier, err := chain.PrebuildStateTree(c, stateTreeDir, false)
	if err != nil {
		return err
	}
	if frontier.Height == 0 {
		return fmt.Errorf("no momentums found in %q — is this a node data folder?", nomDir)
	}

	fmt.Printf("State tree prebuild complete. Frontier: height %v, hash %v\n", frontier.Height, frontier.Hash)
	fmt.Println("Copy this statetree database into the live data folder before starting the upgraded znnd; it will fold only the momentums produced since this copy was taken.")
	return nil
}

// validateStateTreeOutDir rejects a --statetree-out that would land on the frozen copy's own
// databases, and rejects a non-empty target that is not already a state-tree database — closing
// the path where a typo'd or copy-pasted --statetree-out silently wipes an arbitrary leveldb (the
// state-tree open path treats any non-empty, non-state-tree leveldb as a format mismatch and wipes
// it).
func validateStateTreeOutDir(stateTreeDir, dataDir string) error {
	target, err := filepath.Abs(stateTreeDir)
	if err != nil {
		return fmt.Errorf("can't resolve --statetree-out %q: %w", stateTreeDir, err)
	}
	dataDirAbs, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("can't resolve --data-dir %q: %w", dataDir, err)
	}

	for _, forbidden := range []string{dataDirAbs, filepath.Join(dataDirAbs, "nom"), filepath.Join(dataDirAbs, "cache")} {
		if target == forbidden {
			return fmt.Errorf("--statetree-out %q must not be the data folder or its nom/cache databases", stateTreeDir)
		}
	}

	entries, err := os.ReadDir(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("can't inspect --statetree-out %q: %w", stateTreeDir, err)
	}
	if len(entries) == 0 {
		return nil
	}

	ldb, err := leveldb.OpenFile(target, &opt.Options{ReadOnly: true, ErrorIfMissing: true})
	if err != nil {
		return fmt.Errorf("--statetree-out %q is a non-empty directory that isn't a state-tree database: %w", stateTreeDir, err)
	}
	defer ldb.Close()
	if _, err := trie.NewNodeTree(ldb); err != nil {
		return fmt.Errorf("--statetree-out %q is a non-empty directory that isn't a state-tree database", stateTreeDir)
	}
	return nil
}

func loadStateTreePrebuildGenesis(ctx *cli.Context) (store.Genesis, error) {
	if genesisFile := ctx.String(GenesisFileFlag.Name); ctx.IsSet(GenesisFileFlag.Name) && genesisFile != "" {
		return genesis.ReadGenesisConfigFromFile(genesisFile)
	}
	genesisConfig, err := genesis.MakeEmbeddedGenesisConfig()
	if err == genesis.ErrNoEmbeddedGenesis {
		return nil, fmt.Errorf("no embedded genesis found; provide --genesis")
	}
	return genesisConfig, err
}
