# WASM-CONTRACT Devnet Demo

End-to-end proof that the WASM runtime works on a live devnet:

1. **Deploy** a contract
2. **Execute** it
3. **Query** its state and events via RPC
4. **Prove determinism** by comparing `changesHash` across all pillars

## Prerequisites

- Devnet running (`docker compose up -d` from `docker/devnet/`)
- Chain height >= 15 (wasm-runtime spork enforcement)
- `wat2wasm` installed for contract compilation (`brew install wabt`)

## Quick start

```bash
make demo
```

## Individual tools

```bash
# Build
make build

# Deploy a contract
./bin/wasm-cli --index 3 deploy contract/demo.wasm --salt 0000...01 --upgradeable

# Execute
./bin/wasm-cli --index 3 execute <contract-addr>

# View state
./bin/wasm-cli view <contract-addr>

# Contract info
./bin/wasm-cli info <contract-addr>

# Pause/unpause (deployer only)
./bin/wasm-cli pause <contract-addr>
./bin/wasm-cli unpause <contract-addr>

# Halt/unhalt (admin only)
./bin/wasm-cli halt
./bin/wasm-cli unhalt

# Prove determinism
./bin/wasm-replay --addr <contract-addr> --mode cross-pillar
```

## What this proves

| Claim | Tool | Observable |
|-------|------|------------|
| Runtime executes bytecode correctly | `wasm-cli deploy + execute + view` | Counter reads 2 after two executes |
| Effects are recorded and queryable | `wasm-cli info` / `wasm-cli view` | Events and state visible over RPC |
| State transition is deterministic | `wasm-replay --mode cross-pillar` | Identical `changesHash` on every pillar |

## Files

```
wasmclient/     Shared RPC client + block building
wasmtest/       In-process testing harness (Go)
cmd/wasm-cli/   Deploy/execute/query CLI
cmd/wasm-replay/ Determinism prover
contract/       Demo contract (WAT + compiled WASM)
```

## Architecture

All tools are off-chain clients of the runtime. Nothing here touches consensus,
adds a spork, or folds into `ComputeHash`. The CLI builds blocks the same way a
wallet does and submits them via `ledger.publishRawTransaction`; the replay tool
only reads committed `changesHash` values.

See `docs/wasm/WASM_RUNTIME.md` for the full runtime specification and
`docs/wasm/WASM_FOR_DEVELOPERS.md` for the developer guide.
