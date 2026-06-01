<pre>
  ZIP: <TBD>
  Title: WASM-CONTRACT — WebAssembly Smart Contract Runtime
  Type: Standards Track (Consensus)
  Layer: Consensus / Execution
  Status: Draft
  Requires: DynamicPlasmaSpork
  Activation: WasmRuntimeSpork
</pre>

# WASM-CONTRACT: A WebAssembly Smart Contract Runtime for Zenon

> This document is the Zenon Improvement Proposal for the WASM runtime. It is an overview of what has been built, with the technical detail, architecture, code examples, and test coverage needed to evaluate it. The full normative behaviour — every constant, signature, and hashing rule — is specified in [`WASM_RUNTIME.md`](./WASM_RUNTIME.md), which this proposal references rather than restates.

---

## 1. Abstract

WASM-CONTRACT adds a general-purpose smart-contract layer to Zenon. Today the network runs a fixed set of *embedded* contracts compiled into the node binary; changing or adding one requires shipping a new node. WASM-CONTRACT lets any account deploy arbitrary contracts as WebAssembly bytecode. Each deployed contract becomes a first-class chain account with its own address, token balances, persistent state, and account-block history.

The runtime is built around one non-negotiable property — **determinism** — and is gated behind a spork plus an administrator-controlled emergency halt, so it can be introduced and, if needed, paused without a hard fork.

---

## 2. Motivation

Zenon's embedded contracts are powerful but closed. The set of things the chain can do is the set of things a core developer has compiled in. This is safe but slow: every new on-chain primitive is a protocol upgrade.

A WASM execution layer changes that economy:

- **Permissionless innovation.** Developers ship contracts without touching the node or coordinating a network upgrade.
- **Mainstream toolchains.** WebAssembly is a compile target for Rust, C/C++, AssemblyScript, TinyGo, and more — developers bring existing languages and tooling.
- **A safe sandbox.** WASM is a small, well-specified, sandboxed instruction set with linear memory and no ambient authority. That makes it far more tractable to constrain for deterministic consensus than a general-purpose VM.

The challenge is that "general-purpose execution" and "byte-identical across every node forever" are in tension. The bulk of this proposal is the machinery that resolves that tension: an interpreter-only engine, a static gas-metering pass, a determinism-rejecting validator, and an economic model that prices on-chain storage.

---

## 3. Overview of What Has Been Built

The runtime is complete across its first-release scope. The major pieces:

| Subsystem | What it does |
|---|---|
| **Address model** | A new `0x02` address class for deployed contracts, derived deterministically from `(deployer, salt)`; predicates that keep WASM contracts distinct from privileged built-ins. |
| **Factory contract** | An embedded contract (`WasmContract`, `0x01`) that owns deployment (single-shot and chunked), upgrade, emergency halt, administrator rotation, and the `Execute` relay. |
| **Execution engine** | wazero in interpreter mode, pinned and stripped of all non-determinism (no JIT, floats, SIMD, WASI, clock, or RNG). |
| **Gas system** | A single authoritative counter fed by static per-opcode instrumentation and per-host-call costs, with an out-of-gas trap and a wall-clock runaway watchdog. |
| **Host ABI** | 18 host functions for state, balances, transfers, context, events, and diagnostics. |
| **Validator** | A bytecode validator that rejects every non-deterministic or unbounded construct (a determinism deny-list) and bounds-checks all indices before deployment, backstopped by wazero's own structural validation. |
| **Storage** | Per-contract state priced in QSR that is **burned** at the point of each bytecode or state write — no deposit ledger, no locked balance, no refund on deletion. |
| **Events** | A capped, order-sensitive event model folded into block hashing via a new account-block version. |
| **View calls** | Off-consensus, read-only contract queries exposed over RPC. |
| **`on_receive` hook** | Optional contract export that fires on plain token receives with a 25,000-gas budget; events and transfers allowed; trap preserves the balance credit. |
| **Governance-set variables** | 13 runtime parameters (gas limits, event caps, QSR rates, size limits, chunk TTL) are admin-modifiable via `SetWasmVariables`; mirrors the Dynamic Plasma pattern; changes take effect per-momentum with no caching. |

The remainder of this document walks the architecture and the most important mechanisms, with code, and then the test suite that pins them down.

---

## 4. Architecture

### 4.1 Where the runtime sits

```
                 account block to a contract address
                                 │
                    ┌────────────┴────────────┐
              prefix 0x01                 prefix 0x02
                    │                          │
          embedded dispatcher          WASM runtime (this ZIP)
                    │                          │
        ┌───────────┴──────────┐               │
   Pillar, Plasma, ...    WasmContract ────────┤  Execute relay
                          (the factory)        │
                                               ▼
                                   ┌───────────────────────┐
                                   │  wazero interpreter    │
                                   │  + injected gas meter  │
                                   │  + 18 host functions   │
                                   └───────────┬───────────┘
                                               │
                       state writes • token transfers • events
                                               │
                                          ChangesHash
```

Dispatch is by address prefix. A block to a `0x02` address routes into the WASM runtime; the factory `WasmContract` (`0x01`) is the embedded contract that deploys those `0x02` contracts and relays `Execute` calls into them.

### 4.2 The execution pipeline

```
bytecode ──▶ validate ──▶ inject gas metering ──▶ compile (wazero) ──▶ cache
                                                                         │
   Execute(send) ──▶ instantiate ──▶ seed __gas_remaining=250k ──▶ run execute(args)
                                                                         │
                              ┌──────────────────────────────────────────┤
                              ▼                    ▼                      ▼
                       host-call costs      opcode gas (static)     wall-clock watchdog
                              └──────────── __gas_remaining ─────────┘   (1s safety net)
```

Validation and instrumentation happen **once per distinct bytecode** and are cached; instantiation and execution happen per call. Gas accounting is entirely inside the bytecode and host hooks, so it is identical on every node.

### 4.3 Determinism, by construction

Determinism is not a runtime check; it is the sum of design choices that make non-determinism unrepresentable:

1. **Interpreter only.** wazero's interpreter is pinned in `go.mod`. No compiling backend, whose codegen could differ across CPUs, is used.
2. **No floats, ever.** Floating-point types and instructions are rejected at validation. IEEE-754 results can differ across platforms; they cannot enter consensus.
3. **No ambient non-determinism.** No WASI, no filesystem, no host clock, no RNG. Time and height come from the *momentum*, not the wall clock.
4. **Deterministic gas.** Costs are fixed at instrumentation time, so every node charges the same gas for the same execution and halts at the same instruction.
5. **Pinned version as consensus.** Bumping wazero is treated as a consensus change requiring a behavioural cross-check.

---

## 5. Technical Specification (Highlights)

This section summarizes the mechanisms most relevant to reviewers. See `WASM_RUNTIME.md` for the complete, normative version.

### 5.1 Address derivation

A deployed contract's address binds its deployer and a 32-byte salt:

```
address = 0x02 || SHA3-256(deployer.Bytes() || salt)[0:19]
```

```go
func WasmAddress(deployer Address, salt [32]byte) Address {
    input := append(deployer.Bytes(), salt[:]...)
    hash := sha3.Sum256(input)
    var addr Address
    addr[0] = WasmContractAddrByte            // 0x02
    copy(addr[1:], hash[0:AddressCoreSize])   // 19 bytes
    return addr
}
```

The salt is bound into the deployment, so a contract can only finalize at the address its salt derives, and collisions require breaking SHA3-256.

### 5.2 The gas system

All gas lives in one exported mutable `i64` global, `__gas_remaining`. It is instrumented to start at 0 and is **seeded by the host** to `WasmExecutionGasLimit` (250,000) at the start of each call. Two feeds decrement it.

**Static opcode metering.** Before a module runs, an instrumentation pass rewrites the code section so the head of each basic block charges that block's cost. Conceptually:

```wat
;; ── before ──                         ;; ── after instrumentation ──
(func $work (param $n i32)              (func $work (param $n i32)
  local.get $n                           i64.const 7          ;; total cost of this block
  i32.const 1                            call $__consume_gas  ;; charge it up-front
  i32.add                                local.get $n
  ...)                                    i32.const 1
                                         i32.add
                                         ...)
```

The injected helper subtracts the cost and traps on underflow, after zeroing the counter so out-of-gas is observable as `remaining == 0`:

```wat
(func $__consume_gas (param $cost i64)
  global.get $__gas_remaining
  local.get $cost
  i64.lt_u                       ;; would this underflow?
  if
    i64.const 0
    global.set $__gas_remaining  ;; zero it, so the trap reads as out-of-gas
    unreachable                  ;; trap
  end
  global.get $__gas_remaining
  local.get $cost
  i64.sub
  global.set $__gas_remaining)
```

The instrumentation uses canonical LEB128 encodings, so the instrumented bytecode is itself reproducible byte-for-byte.

**Per-opcode costs** (charged statically): basic/constant 1; comparison/control 2; arithmetic/load 3; store/call 5; division 10; `call_indirect` 20.

**Host-call costs** (charged by a wazero listener hook): each function's own cost — e.g. `state_read` 200, `state_write` 5,000, `transfer` 9,000, `emit_event` 375 + 8/byte, context accessors 50.

A wall-clock watchdog of 1 second backstops anything gas has not yet caught; it is a safety net, not a budget.

### 5.3 The host ABI

Exactly 18 functions are bound under the import module `env`. Pointers/lengths are `i32` offsets into the module's exported memory; integers are little-endian; token amounts and balances are 32-byte big-endian.

```
State:     state_read, state_write, state_delete, state_has
Balances:  balance_get, transfer
Context:   get_height, get_timestamp, get_prev_hash, get_caller,
           get_address, get_block_hash, get_remaining_gas,
           get_call_token, get_call_amount
Events:    emit_event
Other:     abort, log_msg
```

`get_caller` returns the address of the account that submitted the `Execute` send block. In Phase 1 this is always a user address (`0x00` prefix), since contract-to-contract `Execute` calls are not supported (see §16). `get_call_token` and `get_call_amount` expose the token standard and amount from the incoming `Execute` call; both return zero values in a view context.

In a read-only (view) context, the four mutating functions — `state_write`, `state_delete`, `transfer`, `emit_event` — trap. Read accessors stay available.

### 5.4 The storage model

On-chain bytes are paid for in QSR, which is **burned** (permanently destroyed) at the point of the storage write. There is no deposit ledger, no locked balance, and no refund on deletion.

```
bytecodeCost = max(size × 500, 100_000_000)          // floor: 1 QSR
stateCost    = (len(key) + len(value)) × 1000        // per write, growth delta only
```

Bytecode cost is burned at `Activate` time. State writes charge only the growth delta relative to the prior occupant of the key — overwriting with the same-sized or smaller value costs nothing additional. The delta QSR is burned from the contract's own balance at write time. Deleting a key frees the storage bytes but recovers no QSR. A contract that has insufficient spendable QSR for a write gets an error return from `state_write`; it does not trap.

### 5.5 Events and block versioning

Events are recorded as `nom.AccountBlockEvent{ContractAddress, Topic, Indexed, Data}` and capped (256 events/execute, 4 KiB/event, 64 KiB total/execute). They are folded into consensus via an `EventsHash`:

```
EventsHash = SHA3-256( for each event:
    Topic || ContractAddress || Indexed(1 byte) || len(Data) as u32-LE || Data )
```

The hash is order-sensitive and empty events hash to zero. It enters `ComputeHash` only for blocks at `WasmAccountBlockVersion` (3) — post-spork contract-receive blocks — so user sends and embedded descendants (version 1) are unaffected.

### 5.6 The `on_receive` hook

A contract may export an optional `on_receive(args_ptr, args_len) -> i32` function to react to incoming plain token transfers. If the contract exports `on_receive`, the runtime calls it after crediting the balance; contracts without the export keep the existing silent-receive behavior.

`on_receive` uses the same calling convention as `execute` but runs on a 25,000 gas budget (`WasmOnReceiveGasLimit`) — 10× cheaper than `Execute`. The runtime writes a 62-byte args buffer into guest memory containing the token standard (10 bytes), amount (32 bytes big-endian), and sender address (20 bytes).

The hook may call `transfer` and `emit_event` using the same host functions as `execute`. Transfers queued from `on_receive` become descendant send blocks processed after the hook completes. **If `on_receive` traps or runs out of gas, the receive still succeeds and the tokens are still credited** — the trap rolls back the hook's state side effects but not the balance credit. A contract cannot reject or fail an incoming transfer.

A halted or revoked contract's `on_receive` is skipped. A paused contract blocks the send at the VM level before `on_receive` fires.

### 5.7 Validation

The validator rejects, by an explicit deny-list, floats, SIMD (`0xFD`), atomics (`0xFE`), exceptions (`0x06`–`0x08`), reference types (`0xD0`–`0xD2`), the start section, and `_start`; under the `0xFC` prefix only `memory.copy`/`memory.fill` are allowed. Imports are an allow-list (§7), and every global/function index is bounds-checked so gas instrumentation cannot be subverted. It enforces structural caps (imports ≤ 256, functions ≤ 10,000, memory ≤ 256 pages, locals ≤ 50,000/fn, instructions ≤ 100,000/fn, a combined complexity bound ≤ 2,000,000, and more) and requires an `execute (i32,i32)->i32` export plus an exported memory.

### 5.8 Governance-set variables

Every runtime constant that might need tuning after launch — gas budgets, event caps, QSR pricing, size limits, chunk TTL — lives in a single `WasmVariables` struct stored as a singleton in `WasmContract` storage under prefix `0x0B`. The administrator can replace the entire struct in one transaction via the `SetWasmVariables` method, following the same pattern Dynamic Plasma uses for its `SetVariables` governance call.

The method is **admin-only** (rejected for any other caller), performs a **full replacement** (not a partial merge), and **bounds-checks** every field against hard minimums before accepting the write. When storage is empty — before the first call, or if a future operation clears it — every read returns the compiled-in defaults, so the runtime always has sane values even if governance has never touched the parameters.

Reads happen per-call directly from storage with no in-process cache, so a new momentum picks up changed values immediately without any restart or cache invalidation.

The 13 tunable fields:

| Field | Default | Minimum | What it controls |
|---|---|---|---|
| `ExecutionGasLimit` | 250,000 | 10,000 | Gas budget seeded into `__gas_remaining` for `Execute` calls |
| `OnReceiveGasLimit` | 25,000 | 1,000 | Gas budget for the `on_receive` hook |
| `MaxDescendantBlocks` | 16 | 1 | Maximum descendant send blocks per execution |
| `MaxEventsPerExecute` | 256 | 1 | Maximum events a single `Execute` can emit |
| `MaxEventDataPerEvent` | 4,096 | 256 | Maximum bytes of data in a single event |
| `MaxEventBytesPerExecute` | 65,536 | 4,096 | Maximum total event data bytes per `Execute` |
| `MaxViewReturnSize` | 65,536 | 1,024 | Maximum bytes returned from a `callView` |
| `QSRPerByteOfBytecode` | 500 | 100 | QSR burned per byte of deployed bytecode |
| `MinBytecodeCost` | 100,000,000 | 1,000,000 | Floor cost for bytecode deployment (1 QSR at 8 decimals) |
| `QSRPerByteOfState` | 1,000 | 100 | QSR burned per byte of state growth |
| `MaxWasmBytecodeSize` | 14,336 | 1,024 | Maximum bytecode size accepted by the validator |
| `MaxChunkCount` | 18 | 1 | Maximum chunks in a chunked deployment |
| `ChunkTTLMomentums` | 1,440 | 100 | Momentums before an incomplete chunked deploy expires |

Because the struct is a full replacement, a governance call that omits a field zeroes it — the caller must supply all 13 values. Bounds-checking rejects any field below its minimum, protecting the network from accidentally bricking the runtime with a dangerously low gas limit or event cap.

---

## 6. Code Examples

### 6.1 A minimal counter contract (Rust)

A contract is any WASM module that exports `execute (i32,i32) -> i32` and its memory, and imports only permitted host functions. In Rust:

```rust
// Host functions provided by the runtime under import module "env".
#[link(wasm_import_module = "env")]
extern "C" {
    fn state_read(key_ptr: u32, key_len: u32, out_ptr: u32, out_max: u32) -> u32;
    fn state_write(key_ptr: u32, key_len: u32, val_ptr: u32, val_len: u32) -> u32;
    fn emit_event(topic_ptr: u32, data_ptr: u32, data_len: u32, indexed: u32) -> u32;
}

const KEY: &[u8] = b"count";
const ABSENT: u32 = 0xFFFF_FFFF;

// The runtime calls execute(args_ptr, args_len) and treats a 0 return as success.
#[no_mangle]
pub extern "C" fn execute(_args_ptr: u32, _args_len: u32) -> u32 {
    let mut buf = [0u8; 8];

    // Read the current counter (8-byte little-endian), defaulting to 0 if absent.
    let n = unsafe {
        let read = state_read(KEY.as_ptr() as u32, KEY.len() as u32,
                              buf.as_mut_ptr() as u32, buf.len() as u32);
        if read == ABSENT { 0u64 } else { u64::from_le_bytes(buf) }
    };

    // Increment and persist.
    let next = n + 1;
    let next_bytes = next.to_le_bytes();
    unsafe {
        state_write(KEY.as_ptr() as u32, KEY.len() as u32,
                    next_bytes.as_ptr() as u32, next_bytes.len() as u32);
    }

    // Emit an event carrying the new value.
    let topic = [0u8; 32]; // 32-byte topic of the contract's choosing
    unsafe {
        emit_event(topic.as_ptr() as u32,
                   next_bytes.as_ptr() as u32, next_bytes.len() as u32,
                   0 /* not indexed */);
    }

    0 // success
}
```

The same module can offer a read-only query. A `view` export is preferred by the runtime over `execute` for view calls, and returns a length-prefixed buffer:

```rust
// Returns a pointer to [u32-LE length][bytes] in linear memory.
#[no_mangle]
pub extern "C" fn view(_args_ptr: u32, _args_len: u32) -> u32 {
    let mut buf = [0u8; 8];
    let n = unsafe {
        let read = state_read(KEY.as_ptr() as u32, KEY.len() as u32,
                              buf.as_mut_ptr() as u32, buf.len() as u32);
        if read == ABSENT { 0u64 } else { u64::from_le_bytes(buf) }
    };

    // Lay out [len=8][8 bytes] in a static buffer and return its pointer.
    static mut OUT: [u8; 12] = [0; 12];
    unsafe {
        OUT[0..4].copy_from_slice(&8u32.to_le_bytes());
        OUT[4..12].copy_from_slice(&n.to_le_bytes());
        OUT.as_ptr() as u32
    }
}
```

In a view context, calling `state_write` or `emit_event` would trap — views can read but not mutate.

### 6.2 Deploying, executing, and querying

Deployment and execution are account blocks to the embedded `WasmContract`, using its ABI methods (`Deploy`, `Activate`, `DiscardChunks`, `Revoke`, `Halt`/`Unhalt`, `Pause`/`Unpause`, `ChangeAdministrator`, `SetWasmVariables`, `Execute`). All deployments follow a two-call sequence: one or more `Deploy` calls to upload bytecode (carrying QSR on the first call), followed by a single `Activate` to finalize (carrying the ZNN fee). Read paths are exposed over RPC under the `embedded.wasm` namespace:

```
embedded.wasm.getContract(contractAddress)
   → { deployer, version, upgradeable, activated, bytecodeHash, bytecodeCost, halted }

embedded.wasm.getHaltStatus(contractAddress)            → { halted }

embedded.wasm.getEvents(contractAddress, topic,
                     fromHeight, toHeight,
                     pageIndex, pageSize)             → paged event list

embedded.wasm.callView(contractAddress, function, args) → decoded view result

embedded.wasm.getStats()                                → aggregate runtime stats
```

`callView` runs the contract read-only against current state, bounded by the same 250,000-gas budget, the watchdog, and the 64 KiB return cap — and it works even on a halted contract, since reading state harms no one.

---

## 7. Testing

The runtime is covered by two layers of tests: focused unit tests against each subsystem in `vm/wasm`, and end-to-end integration tests that drive the factory contract through a live chain in `vm/embedded/tests`. Hand-built WASM test modules in `vm/wasm/testmodules` give the unit tests precise control over bytecode shapes (a counter, an event emitter, a runaway loop, a view module, an echo module), and each is itself compile-checked under wazero.

### 7.1 Unit coverage (`vm/wasm`)

| Area | Representative tests |
|---|---|
| **Gas injection** | metering executes and is *deterministic* across runs; bounds an infinite loop; rejects too-small input; per-opcode costs verified for arithmetic, basic, call, `call_indirect`, control, division, memory load/store |
| **LEB128 encoding** | canonical signed and unsigned encoders |
| **Validation** | float types and float-const rejected; SIMD rejected; start section and `_start` rejected; `get_origin` rejected (renamed to `get_caller`); import allow-list enforced; out-of-bounds global/function indices rejected; integer conversions allowed; invalid magic/version/too-small rejected; store memarg parsed |
| **Host context & burns** | `balance_get` returns the contract's spendable balance; `state_write` burns the QSR growth delta and fails when spendable QSR (net of pending transfers and the aggregated burn) cannot cover it; `transfer` is reserved against the same spendable balance so it cannot double-spend; non-QSR balances are left untouched |
| **Events** | collector emits; enforces max events, max data per event, and max total bytes; `EventsHash` is deterministic and empty-hashes to zero |
| **View calls** | `view` export preferred; `execute` fallback; empty return; oversized return rejected; read-only traps on `state_write` and `emit_event` |
| **Module cache** | disk put/get, delete, delete-missing, get-missing; a corrupt disk entry falls through to recompile from the original |

### 7.2 Integration coverage (`vm/embedded/tests`)

These tests deploy, fund, execute, upgrade, halt, and query real contracts against a running chain, asserting balances, QSR burns, block structure, and consensus fields:

| Area | Representative tests |
|---|---|
| **Activation & dispatch** | pre-spork rejects `0x02`; `0x02` rejects undeployed; address is correctly prefixed; `WasmContract` is embedded and present in the embedded set |
| **Single deploy** | full lifecycle; zero-QSR deploy persists (fund later); insufficient QSR rejected; invalid bytecode rejected; address collision rejected; endowment forwards to the new contract |
| **Chunked deploy** | full lifecycle conserves the QSR pool; zero-QSR first chunk rejected; stray QSR on later chunks rejected; TTL expiry forfeits the collected QSR; `DiscardChunks` refunds before TTL and is rejected with no chunks |
| **Execute** | lifecycle; gas exhaustion; receive-block structure; undeployed target rejected |
| **Upgrade & admin** | upgrade by deployer; non-deployer rejected; non-upgradeable rejected; halt/unhalt by non-admin rejected |
| **Revoke** | deployer revokes; non-deployer rejected; already-revoked rejected; non-QSR balance check |
| **Pause/Unpause** | deployer pauses; non-deployer rejected; token send blocked; deployer unpause; non-deployer unpause rejected; not-paused rejected; already-paused rejected; revoke on paused succeeds; admin halt with paused contract |
| **on_receive** | contract without hook → silent receive; contract with hook → hook runs, balance credited; hook trap → balance still credited |
| **Events** | event version stamping on the receive block |
| **RPC** | `getContract` + `getHaltStatus`, `getEvents`, `getStats`, and `callView` end-to-end |
| **Governance-set variables** | `SetWasmVariables`: admin sets values and they persist; non-admin rejected; bounds check rejects below-minimum values; defaults returned when storage empty; multiple sequential updates work (full replacement semantics) |

Together these exercise every method on the factory, the QSR burn and chunk-refund accounting, the spork gate, the determinism guarantees of the gas and validation paths, and the consensus event hashing.

---

## 8. Backwards Compatibility

WASM-CONTRACT is purely additive and spork-gated.

- **Before `WasmRuntimeSpork` is enforced**, the runtime is inert: factory methods and all `0x02` execution behave as if the feature does not exist, so historical replay is unchanged.
- **The new account-block version (3)** that folds in `EventsHash` applies only to post-spork contract-receive blocks. User sends and embedded-contract descendants remain version 1, so existing block hashing is untouched.
- **Address predicates** are extended carefully: `IsEmbeddedAddress` is *not* widened to `0x02`. Privileged-builtin checks keep their old meaning; only the call-sites that should accept any contract were migrated to `IsContractAddress`.
- **Activation order is fixed:** `WasmRuntimeSpork` requires `DynamicPlasmaSpork` already enforced.

No existing block, contract, or hash changes meaning as a result of this proposal until the spork activates, and even then only WASM-specific blocks are affected.

---

## 9. Security Considerations

The threat model is "untrusted bytecode submitted by anyone," and the defenses are layered so that no single control is load-bearing:

- **Determinism** (interpreter-only, no floats/SIMD/WASI/clock/RNG, pinned engine) prevents consensus divergence — the most dangerous failure for a blockchain.
- **The validator** rejects non-deterministic and unbounded constructs *before* deployment via an explicit deny-list, bounds-checks every index, and is backstopped by wazero's own structural validation.
- **Deterministic gas** plus the **wall-clock watchdog** bound every execution identically on every node, defeating denial-of-service by computation.
- **The storage model** prices bytecode and state growth in burned QSR, defeating storage griefing without requiring per-user accounting.
- **Isolation** gives each contract its own address, balance, and state; there is no shared mutable global, no ambient authority, and no `get_caller` to build an undefined call-stack assumption on.
- **The spork gate** lets the network choose when to activate, and the **administrator halt** lets a misbehaving contract be paused — preserving its state and balance — with no fork.
- **Privilege separation** ensures an untrusted `0x02` contract can never be mistaken for a privileged `0x01` built-in.

Residual risks are concentrated in two places that this proposal treats as consensus-critical and change-controlled: the pinned wazero version, and the exact constants and hashing rules in `WASM_RUNTIME.md`. Both are intentionally conservative in this first release and can only be widened through a further spork-gated change validated against real network data.

---

## 10. Reference Implementation

The implementation lives in the go-zenon tree:

| Path | Responsibility |
|---|---|
| `common/types/address.go` | `0x02` address class, `WasmAddress` derivation, predicates |
| `vm/constants/` | All WASM gas, size, QSR-cost, and plasma constants |
| `vm/wasm/validator.go` | Deny-list determinism validation + index bounds checks |
| `vm/wasm/gas_injection.go` | Static per-opcode gas instrumentation |
| `vm/wasm/host_functions.go` | The 18 host functions and read-only trap |
| `vm/wasm/context.go` | Execution context, QSR burns, transfers, events |
| `vm/wasm/runtime.go` | Engine, Execute/CallView, module cache |
| `vm/embedded/definition/wasm.go` | Factory ABI, storage records |
| `vm/embedded/implementation/wasm.go` | Factory method handlers |
| `chain/nom/account_block.go` | `AccountBlockEvent`, `EventsHash`, block versioning |
| `rpc/api/embedded/wasm.go` | `embedded.wasm` RPC surface |

Full normative behaviour is specified in [`WASM_RUNTIME.md`](WASM_RUNTIME.md). A developer-oriented introduction is in [`WASM_FOR_DEVELOPERS.md`](WASM_FOR_DEVELOPERS.md).

---

## 11. Copyright

This document is placed in the public domain.
