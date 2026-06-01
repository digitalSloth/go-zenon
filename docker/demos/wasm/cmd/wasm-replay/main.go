package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/zenon-network/go-zenon/docker/demos/wasm/wasmclient"
)

var (
	urlFlag     = flag.String("url", "http://127.0.0.1:35997", "Primary node RPC URL")
	addrFlag    = flag.String("addr", "", "Contract address to audit")
	modeFlag    = flag.String("mode", "cross-pillar", "Mode: cross-pillar | local")
	pillarsFlag = flag.String("pillars", "znnd-pillar1,znnd-pillar2,znnd-pillar3", "Pillar container names (comma-separated)")
	fromFlag    = flag.Uint64("from", 0, "From momentum height")
	toFlag      = flag.Uint64("to", 0, "To momentum height (0 = latest)")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "wasm-replay — WASM determinism prover\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  wasm-replay --addr <contract> --mode cross-pillar [--from N] [--to N]\n\n")
		fmt.Fprintf(os.Stderr, "Modes:\n")
		fmt.Fprintf(os.Stderr, "  cross-pillar  Compare changesHash across all pillars (primary proof)\n")
		fmt.Fprintf(os.Stderr, "  local         Re-derive changesHash locally (requires datadir)\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	if *addrFlag == "" {
		fmt.Fprintf(os.Stderr, "error: --addr is required\n")
		os.Exit(1)
	}

	var err error
	switch *modeFlag {
	case "cross-pillar":
		err = crossPillar()
	case "local":
		err = localReexec()
	default:
		fmt.Fprintf(os.Stderr, "unknown mode: %s\n", *modeFlag)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func pillarURLs() []string {
	names := strings.Split(*pillarsFlag, ",")
	urls := make([]string, len(names))
	for i, name := range names {
		// Each pillar container exposes RPC on port 35997 internally
		urls[i] = fmt.Sprintf("http://%s:35997", strings.TrimSpace(name))
	}
	return urls
}

func connectPrimary() (*wasmclient.Client, error) {
	return wasmclient.Dial(*urlFlag)
}
