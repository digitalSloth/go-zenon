# WASM-CONTRACT Runtime Specification

> **Component:** WebAssembly smart-contract runtime for go-zenon
> **Activation:** `WasmRuntimeSpork` — requires `DynamicPlasmaSpork` already enforced
> **Factory contract:** `z1qxemdeddedxwasmxxxxxxxxxxxxxxxxxr38qaq` (embedded, `0x01` prefix). Referred to throughout as `WasmContract`.
> **Deployed contracts:** `0x02` prefix — one chain account per contract, with its own balance, state, and block history.

This document is the normative specification of the WASM-CONTRACT runtime as built. It defines the address model, activation rules, the factory contract's method surface and lifecycle, the execution model, the host-function ABI, the storage and QSR-burn model, the determinism and validation rules, the event model and its consensus hashing, view calls, and the constants that govern all of the above. Conforming implementations must reproduce every value and behaviour described here byte-for-byte, because all of it participates in consensus.

---

## 1. Overview

Zenon ships a set of **embedded contracts** compiled into the node binary. WASM-CONTRACT adds a general-purpose WebAssembly execution layer so that arbitrary smart contracts can be deployed by any account. Each deployed contract is a first-class chain account: it owns its address, its token balances, its slice of state, and its own account-block chain.

The runtime is built on three load-bearing decisions:

1. **Determinism is absolute.** Execution uses the wazero interpreter only — no JIT, no SIMD, no floating point, no WASI, no host clock or RNG. The same bytecode applied to the same inputs produces a byte-identical state transition on every node, so the resulting `ChangesHash` is identical network-wide.

2. **Gas is deterministic and self-contained.** A static instrumentation pass injects per-basic-block gas accounting into the bytecode before it ever runs, and host-call costs are charged through a metering hook. The wall-clock bound is a runaway safety net, never a budget — honest contracts always exhaust the gas budget (or return) long before it fires.

3. **Costs fall on the party that imposes them.** Storage is paid for in QSR burned from the contract's own balance. A contract that merely receives a plain token transfer imposes no execution cost on the sender. Users always pay plasma (via POW or fused QSR) to submit their transactions, which is the network's base cost model and anti-spam layer.

The runtime is gated behind a spork and an administrator-controlled emergency halt, so it can be introduced and, if necessary, paused without a hard fork.

---

## 2. Address Model

### 2.1 Prefix bytes

Addresses are 20 bytes: one prefix byte plus 19 hash bytes (`AddressSize = 20`, `AddressCoreSize = 19`).

```
0x00  User addresses
0x01  Embedded contracts (including WasmContract, the factory)
0x02  Deployed WASM contracts (one address per contract)
```

The relevant constants are `ContractAddrByte = 0x01` and `WasmContractAddrByte = 0x02`.

### 2.2 Address predicates

Three predicates classify an address by its prefix:

| Predicate | True when prefix is | Meaning |
|---|---|---|
| `IsEmbeddedAddress` | `0x01` | A built-in embedded contract |
| `IsWasmContractAddress` | `0x02` | A deployed WASM contract |
| `IsContractAddress` | `0x01` or `0x02` | Any contract (embedded or WASM) |

`IsEmbeddedAddress` is **not** widened to include `0x02`. Logic that must apply only to privileged built-ins keeps checking `IsEmbeddedAddress`; logic that should apply to any contract uses `IsContractAddress`. This distinction is deliberate and security-relevant: WASM contracts are untrusted and must never be mistaken for privileged built-ins.

### 2.3 Deployed-contract address derivation

A deployed contract's address is derived deterministically from its deployer and a 32-byte salt:

```go
func WasmAddress(deployer Address, salt [32]byte) Address {
    input := append(deployer.Bytes(), salt[:]...)
    hash := sha3.Sum256(input)
    var addr Address
    addr[0] = WasmContractAddrByte            // 0x02
    copy(addr[1:], hash[0:AddressCoreSize])   // hash[0:19]
    return addr
}
```

The address is `0x02 || SHA3-256(deployer || salt)[0:19]`. Because the salt is bound into the address, a deployer chooses where their contract lands by choosing the salt, and two distinct `(deployer, salt)` pairs cannot be made to collide except by breaking SHA3-256. The salt is also bound into the deployment so a contract cannot be finalized at an address other than the one its salt derives.

### 2.4 Dispatch

When a node applies an account block destined for a contract address, the prefix selects the execution path: `0x01` routes to the embedded-contract dispatcher; `0x02` routes to the WASM runtime. User-to-user transfers (`0x00`) are unaffected.

---

## 3. Activation and Spork Gating

### 3.1 Spork dependency

The runtime is gated by `WasmRuntimeSpork`. Its enforcement **requires `DynamicPlasmaSpork` to be enforced first** — the activation order is fixed and checked. Until `WasmRuntimeSpork` is enforced, all factory methods and all `0x02` execution are inert, exactly as if the feature did not exist.

### 3.2 Send validation under the spork

Once the spork is active, the consensus rules that admit account blocks treat `0x02` addresses as valid contract destinations. A send to a `0x02` address is admitted only if a contract actually exists there (its bytecode has been finalized), with one carve-out.

#### 3.2.1 Deploy-endowment carve-out

A deployment finalizes bytecode at the new `0x02` address and, in the same operation, may forward an initial token endowment from the factory to that new address. At the moment this endowment send is created, the destination's bytecode has just been written but the send is produced **by the `WasmContract` factory itself**. Sends whose sender is `WasmContract` are therefore exempt from the "bytecode must already exist" precondition. Without this exemption the endowment send would be rejected and the deployment would roll back. The exemption is narrow: it applies only when the sender is the factory address, so ordinary accounts cannot use it to send to not-yet-deployed addresses.

---

## 4. The Factory Contract

`WasmContract` is an embedded contract (prefix `0x01`) that owns the lifecycle of every deployed WASM contract: deployment, upgrade, revocation, the emergency halt switch, and administrator rotation. It also is the relay through which `Execute` calls reach deployed contracts.

### 4.1 Method surface

| Method | Caller | Token | Purpose |
|---|---|---|---|
| `Deploy` | anyone (new) / deployer (upgrade) | QSR on chunk 0 | Upload one or more bytecode chunks — new deploy or upgrade |
| `Activate` | the uploader | ZNN | Assemble, validate, and activate — new deploy or upgrade |
| `DiscardChunks` | the uploader | none | Abandon an in-progress upload and refund unexpired QSR |
| `Revoke` | the original deployer | none | Permanently disable a contract — bytecode and state persist, address cannot be reused |
| `Halt` | administrator | none | Emergency-pause the entire WASM runtime |
| `Unhalt` | administrator | none | Resume the WASM runtime |
| `Pause` | deployer | none | Per-contract pause — blocks all sends to the contract except from the deployer |
| `Unpause` | deployer | none | Remove a per-contract pause |
| `ChangeAdministrator` | administrator | none | Two-phase, time-locked administrator rotation |
| `Execute` | any account | any | Invoke a deployed contract's `execute` entry point |
| `SetWasmVariables` | administrator | none | Update governance-tunable runtime parameters (gas limits, event caps, QSR rates, size limits, chunk TTL) |

All deployments — single-shot or chunked, new or upgrade — follow the same two-call sequence: one or more `Deploy` calls to upload bytecode (carrying QSR on the first call), followed by a single `Activate` call to finalize (carrying the ZNN fee). Because an account block can carry only one token standard, this split is how the ZNN fee and QSR bytecode cost are both collected without requiring a multi-token transaction.

### 4.2 Storage costs

Persisting bytecode and state on-chain is paid for in QSR, which is **burned** (permanently destroyed) at the point of the storage write. There is no deposit ledger, no locked balance, and no refund on deletion.

**Bytecode cost.** A contract of `size` bytes requires

```
bytecodeCost = max(size × QSRPerByteOfBytecode, MinBytecodeCost)
             = max(size × 500, 100_000_000)
```

i.e. 500 QSR-units per bytecode byte, with a floor of 1 QSR (`100_000_000` units). This QSR is burned at `Activate` time and is a one-way cost.

**State cost.** State writes are charged per byte of `key + value`:

```
stateCost(key, value) = (len(key) + len(value)) × QSRPerByteOfState
                      = (len(key) + len(value)) × 1000
```

Writes charge only the **growth delta** relative to the prior occupant of the key — overwriting a key with the same-sized or smaller value costs nothing additional. The delta QSR is burned from the contract's own balance at the time of the write. If the contract has insufficient spendable QSR for a write, `state_write` returns an error code (see §7.1); it does not trap. Deleting a key frees the storage bytes but recovers no QSR, since it was already burned.

**Developer responsibility.** All state write costs are borne by the contract's own QSR balance. It is the deployer's responsibility to pre-fund the contract with sufficient QSR (via plain token transfers to the `0x02` address, see §5) and to monitor that balance so writes do not fail unexpectedly. Developers may build application-level fee mechanisms into their contracts to recover costs from users — for example by charging ZNN or a custom token via the `transfer` host function. Users do not pay QSR for storage directly; they always pay plasma (POW or fused QSR) to submit their transactions, which is the network's standard anti-spam layer.

### 4.3 Unified upload: `Deploy`

`Deploy` is the single upload method for all bytecode — new deployments and upgrades, single-shot and chunked. Its behaviour adapts based on whether bytecode already exists at `wasmAddr`.

```
Deploy(wasmAddr, salt, chunkIndex, totalChunks, chunkData)
```

**New deploy path** (no bytecode exists at `wasmAddr`):

- Any account may call.
- The target address is `WasmAddress(caller, salt)`; if `wasmAddr` does not match this derivation the call is rejected. This binds the salt to the upload and prevents a caller from squatting an address they did not derive.
- Chunk 0 must carry the **full bytecode cost** in QSR for the declared `totalChunks`. The cost is computed as `bytecodeCost(totalChunks × MaxWasmBytecodeSize)` — the worst-case size — to ensure the factory holds enough QSR before any chunk is stored. Subsequent chunks must carry no QSR.
- For a single-shot deploy, `totalChunks = 1` and `chunkIndex = 0`. If the bytecode is complete in chunk 0, it is validated immediately; invalid bytecode is rejected before QSR is committed.
- For multi-chunk deploys, validation is deferred to `Activate`. If `Activate` fails validation, the QSR is refunded and chunk state is cleaned up.

**Upgrade path** (bytecode already exists at `wasmAddr`):

- Only the **original deployer** may call.
- The contract must be **upgradeable**.
- Chunk 0 must carry the **delta QSR cost**: `max(newBytecodeCost − oldBytecodeCost, 0)`. If the new bytecode is the same size or smaller, no QSR is required on chunk 0.
- Single-shot and multi-chunk uploads work identically to the new deploy path.
- Validation on single-chunk uploads is performed immediately; multi-chunk validation is deferred to `Activate`.

**TTL.** A partially uploaded sequence expires `ChunkTTLMomentums` (1440) momentums after its first chunk. Chunks belonging to an expired upload are treated as stale, and the QSR from an expired upload is **burned** — it does not remain in the factory pool and cannot be recovered even via `DiscardChunks`. This prevents indefinite slot squatting.

A contract may span at most `MaxChunkCount` (18) chunks.

### 4.4 Unified finalization: `Activate`

`Activate` is the single finalization method for all uploads — new deployments and upgrades.

```
Activate(wasmAddr, salt, upgradeable)  + ZNN (ZNNDeployFee, burned)
```

`Activate` must be called by the same account that submitted chunk 0. It:

1. Rejects expired uploads (`ErrWasmChunksExpired`).
2. For multi-chunk uploads, concatenates all chunks in order and validates the assembled bytecode (§9). On validation failure, refunds the held QSR to the uploader and deletes all chunk state — the upload can be retried.
3. Burns the ZNN fee (`ZNNDeployFee`). This fee is burned regardless of whether it is a new deploy or an upgrade, consistent with the token-creation and Accelerator-Z patterns.
4. Settles the chunk-0 QSR: burns the actual bytecode cost (for an upgrade, the delta over the previous cost) and **forwards any remaining QSR to the new contract's `0x02` balance as an initial prefund**. A single-shot deploy collects exactly the cost, so there is no excess; a multi-chunk deploy pays the worst-case overcharge of §4.3 on chunk 0, and the difference between that and the actual assembled cost is returned to the contract this way rather than burned. The forward is emitted with the factory as sender, so it is admitted under the §3.2.1 endowment carve-out. Because it is a plain transfer to the now-activated contract, this prefund also fires the contract's `on_receive` hook (§5.1) once, if one is exported — a deployer whose hook reacts to receives should account for this initial credit.
5. Writes the bytecode to factory storage.

**New deploy path** (no existing metadata):

- Verifies `WasmAddress(caller, salt) == wasmAddr`.
- Records metadata: `{ deployer: caller, version: 1, upgradeable: upgradeable, bytecodeHash, bytecodeCost }`.
- The contract is now live and `Execute` calls are accepted.

**Upgrade path** (existing metadata):

- Verifies caller == original deployer and contract is upgradeable.
- The `upgradeable` parameter is ignored — the flag was fixed at initial deployment and cannot be changed on upgrade.
- Increments `version`, updates `bytecodeHash` and `bytecodeCost`.
- Evicts the old bytecode from the compiled-module cache so the new code takes effect immediately.
- Non-upgradeable contracts are immutable for the life of the chain; `Activate` on a non-upgradeable contract with existing metadata is rejected.

### 4.5 Abandon: `DiscardChunks`

`DiscardChunks` abandons an in-progress upload (new deploy or upgrade) before `Activate` is called. It may only be called by the account that submitted chunk 0. It deletes all stored chunk data and chunk metadata.

- If the upload has **not** expired, the QSR from chunk 0 is refunded to the caller.
- If the upload **has** expired, the QSR has already been burned; `DiscardChunks` still cleans up the chunk storage so the address slot can be reused, but no refund is issued.

### 4.6 Revoke

`Revoke` permanently disables a contract by setting a one-way `Revoked` flag. It is permitted only when the caller is the **original deployer** recorded in the contract metadata. The contract's bytecode and state stay on-chain as a permanent record; the address is not freed for re-deployment.

Before calling `Revoke`, the deployer must withdraw all non-QSR token balances from the contract (by invoking the contract's own logic or by ensuring the contract has no other token holdings). `Revoke` is rejected if the contract holds any non-QSR token balance at the time of the call.

On success:

1. The `Revoked` flag is set in factory storage (prefix `0x09`).
2. `Execute` calls to the contract are permanently rejected (`ErrWasmContractRevoked`).
3. The contract's bytecode, metadata, and state remain intact on-chain.

`Revoke` is available on both halted and unhalted contracts, giving the deployer a disable path even after an emergency halt. No ZNN fee is charged. QSR remaining in the contract's balance is not moved — it remains stranded on the `0x02` address.

> **Note on address reuse.** A revoked address cannot be re-deployed. The bytecode and metadata records persist, so `WasmHasBytecode` remains true and any subsequent `Deploy` to the same address is rejected. Downstream integrations should treat a revoked contract as permanently disabled.

### 4.7 Emergency halt

`Halt` and `Unhalt` are administrator-only. A halted contract rejects `Execute` (its bytecode will not run) while leaving its stored state and balance intact. This is the emergency brake for a contract discovered to be misbehaving, modeled on the existing bridge halt pattern, and it requires no spork to toggle. The halt flag lives in the contract's `WasmContractInfo`.

> **Note on halted-contract funds.** Token balances held by a permanently halted contract that is never unhalted are effectively frozen — they cannot be moved because `Execute` is blocked and there is no automatic recovery path. The deployer should call `Revoke` (§4.6) to clean up a permanently halted contract, or use `Unhalt` first if they intend to restore it.

### 4.8 Per-contract pause

`Pause` and `Unpause` are deployer-only, per-contract controls. Unlike `Halt`/`Unhalt` (which are administrator-level and affect the entire runtime), `Pause` targets a single deployed contract. A paused contract blocks all incoming sends — `Execute` calls and plain token transfers — except from the deployer. The deployer can still execute, transfer tokens in, revoke, or unpause the contract.

The paused state is stored under prefix `0x0A` in factory storage, with the deployer's address as the value. This lets the VM check sender exemption with a single `Get` — no metadata lookup needed. The check is performed at the VM level in `applySend` and mirrored in the verifier, so it is consensus-critical.

`Pause` is rejected if the contract is already paused; `Unpause` is rejected if the contract is not paused. Both send to the factory (`0x01`), not the target contract, so they work regardless of the contract's pause state.

### 4.9 Administrator rotation

`ChangeAdministrator` rotates the administrator through a **two-phase, time-locked** challenge. A change is proposed, then becomes effective only after `WasmAdministratorDelay` (1440) momentums, during which it can be observed and, if illegitimate, contested. The initial administrator is `InitialWasmAdministrator`, which is `types.GovernanceAddress`. The pending challenge is stored under its own storage prefix.

### 4.10 Execute

`Execute` is the relay that drives a deployed contract's `execute` entry point. Processing an `Execute`:

1. Reads the target contract's halt flag, bytecode, and metadata cross-account from `WasmContract` storage (`MomentumStore().GetAccountStore(types.WasmContract)`). A halted contract, one with no bytecode, one that has not been activated, or one that has been revoked, is rejected.
2. Constructs a `WasmContext` bound to the target, the incoming send block, and the immediate caller, then runs the bytecode under the full execution model of §6.
3. A non-zero `execute` return value is a contract-level failure (`ErrWasmExecutionFailed`) and aborts the state transition.
4. On success, emitted events are converted to `nom.AccountBlockEvent` records and attached to the receive block, and the contract's queued transfers are returned as descendant send blocks.

The `function` string field carried in the `Execute` ABI call is accepted and parsed but does not affect dispatch in Phase 1 — the runtime always invokes the `execute` export regardless of its value. It is reserved for future ABI Outputs decoding, where the function name can be used to deserialise the args and return data against a known ABI schema.

A contract is not callable until `Activate` has completed successfully. Attempting to `Execute` a contract whose bytecode upload is in progress (chunks uploaded but not yet activated) is rejected identically to calling a non-existent contract.

The `args` blob carried in an `Execute` call is capped at `MaxArgsBytes` (15,800); a larger payload is rejected at send admission with `ErrWasmArgsTooLarge`, rather than failing late as an out-of-bounds memory write when the runtime copies it into guest memory.

### 4.11 Governance-tunable variables (`SetWasmVariables`)

All runtime parameters that control gas limits, event caps, QSR burn rates, bytecode size limits, and chunk TTL are governance-tunable. They live in a `WasmVariables` struct stored as a singleton in `WasmContract` storage (prefix `{11}`). The administrator updates them via `SetWasmVariables`, which performs a **full replacement** of all 13 fields in a single call. Changes take effect at the next momentum — no caching, every consumer reads from storage per-call.

When storage is empty (no `SetWasmVariables` call has ever been made), `GetWasmVariables` returns the hardcoded defaults matching the constants in §14. This means no migration is needed — existing chains work unchanged until governance elects to tune.

Each field has a minimum bound to prevent bricking the runtime. Attempting to set any field below its minimum returns `ErrForbiddenParam`.

| Field | Default | Min | Purpose |
|---|---|---|---|
| `executionGasLimit` | 250,000 | 10,000 | Gas budget per `Execute` / view call |
| `onReceiveGasLimit` | 25,000 | 1,000 | Gas budget per `on_receive` hook |
| `maxDescendantBlocks` | 16 | 1 | Max transfers per `Execute` |
| `maxEventsPerExecute` | 256 | 1 | Max events per `Execute` |
| `maxEventDataPerEvent` | 4,096 | 256 | Max bytes per event payload |
| `maxEventBytesPerExecute` | 65,536 | 4,096 | Total event bytes per `Execute` |
| `maxViewReturnSize` | 65,536 | 1,024 | Max return buffer for view calls |
| `qsrPerByteOfBytecode` | 500 | 100 | QSR cost per bytecode byte |
| `minBytecodeCost` | 100,000,000 | 1,000,000 | Floor for bytecode cost |
| `qsrPerByteOfState` | 1,000 | 100 | QSR cost per state-write byte |
| `maxWasmBytecodeSize` | 14,336 | 1,024 | Max assembled bytecode size |
| `maxChunkCount` | 18 | 1 | Max chunks per deploy |
| `chunkTTLMomentums` | 1,440 | 100 | Chunk upload expiry window |

**What is NOT tunable.** Four constants are deliberately excluded from governance control:

- `WasmWallClockLimit` (1 second) — the runaway safety net. Making this tunable would let governance weaken the last-resort termination guarantee, so it stays hardcoded.
- `WasmModuleCacheMaxSize` (1,000) — an operational knob that affects startup and memory, not consensus. Changing it at runtime would require cache invalidation logic for no security benefit.
- `WasmAdministratorDelay` (1,440 momentums) — the time-lock on administrator rotation. Making this tunable would let a compromised administrator shorten the challenge window and lock out legitimate challengers, which defeats its entire purpose.
- All `WasmGas*` host-function costs — the per-function gas prices (`state_read` at 200, `transfer` at 9,000, `emit_event` at 375 + 8/byte, etc.). These are read once at startup into an immutable map. Making them tunable would require re-instrumenting every cached module (the static opcode metering interacts with host-call costs), which is architecturally impractical for a runtime parameter change.

---

## 5. Plain Token Receives and the `on_receive` Hook

A `0x02` contract can receive a plain token transfer — a send with empty `Data` — without requiring the sender to call `Execute`. Such a receive credits the contract's balance and imposes no execution gas on the sender. This is the primary mechanism for developers to fund a contract's QSR balance for storage writes, and for any other token endowments.

### 5.1 The `on_receive` hook

A contract may export an `on_receive` function to react to incoming plain token transfers. If the contract exports `on_receive`, the runtime calls it after crediting the balance. If the contract does not export `on_receive`, the receive completes silently (the existing behavior).

**Signature:** `on_receive(args_ptr, args_len) -> i32` — identical to `execute`. The runtime writes a 62-byte args buffer into guest memory containing:

```
offset 0:  token standard (10 bytes)
offset 10: amount (32 bytes, big-endian)
offset 42: sender address (20 bytes)
```

**Gas:** `WasmOnReceiveGasLimit` (25,000) — roughly 10× cheaper than `Execute`. The hook is meant for light bookkeeping, not heavy logic.

**Transfers and events:** `on_receive` may call `transfer` and `emit_event` using the same host functions as `execute`, subject to the same per-call caps. Transfers queued from `on_receive` become descendant send blocks processed after the hook completes.

**Failure semantics:** this is the load-bearing contract: **if `on_receive` traps or runs out of gas, the receive still succeeds and the tokens are still credited.** The trap rolls back the hook's state side effects (state writes, events, queued transfers) but does not roll back the balance credit. A contract therefore cannot reject or fail an incoming transfer — receiving value remains unconditional.

**Halt, revoke, and pause:** A halted or revoked contract's `on_receive` is skipped (balance credited, no hook runs). A paused contract blocks the send entirely at the VM level (§4.8), so `on_receive` never fires for non-deployer senders.

---

## 6. Execution Model

### 6.1 Engine

Execution uses **wazero in interpreter mode**, pinned to a specific version in `go.mod`. The interpreter is chosen over any compiling backend because it is deterministic across architectures and operating systems. The pinned version is consensus-critical: bumping it requires a cross-check that the new version preserves identical observable behaviour, because a behavioural change would fork the chain. The module is instantiated with an empty name, no stdout, and no stderr; no WASI, filesystem, clock, or randomness is exposed.

### 6.2 The Execute path

For each `Execute`, the runtime:

1. Obtains the compiled module from the cache (§6.6), compiling and instrumenting on a miss.
2. Instantiates a fresh module instance with the host functions of §7 bound under the import module name `env`.
3. Seeds the gas counter `__gas_remaining` to `WasmExecutionGasLimit` (250,000).
4. Writes the call arguments into guest memory at `memory.Size() − len(args)` (the high end of linear memory) and passes the pointer/length to `execute`.
5. Invokes the exported `execute` function with signature `(i32, i32) -> i32`.
6. Arms a wall-clock watchdog of `WasmWallClockLimit` (1 second).
7. Interprets the result: a return value of `0` is success; a non-zero value is a contract failure. A trap with `__gas_remaining == 0` is reported as out-of-gas.

### 6.3 Gas: one counter, two feeds

All gas is accounted in a single authoritative counter — the exported mutable `i64` global `__gas_remaining` (`GasGlobalName = "__gas_remaining"`), which is instrumented to start at 0 and is seeded by the host at the start of each call. Two mechanisms decrement it:

**(a) Static per-opcode metering.** Before a module is ever executed, an instrumentation pass (`InjectGasMetering`) rewrites the code section so that the head of every basic block subtracts that block's cost from `__gas_remaining` and calls an injected `__consume_gas` helper. If the subtraction would underflow, `__consume_gas` zeroes the counter and traps via `unreachable`. Because the cost of a block is fixed at instrumentation time and the same for every node, gas accounting is fully deterministic. The helper and the metering arithmetic are emitted with canonical LEB128 encodings so the instrumented bytecode is itself reproducible.

**(b) Host-call costs.** Each host function charges a fixed cost (plus, for events, a per-byte cost) through a wazero function-listener hook before the host logic runs. These costs come out of the same `__gas_remaining` counter, so a contract that spends its budget in host calls is stopped exactly as one that spends it in compute.

Out-of-gas is therefore observed uniformly: execution traps and `__gas_remaining` is `0`.

### 6.4 Opcode gas schedule

Static metering assigns each instruction a cost by category:

| Category | Cost | Examples |
|---|---|---|
| Basic | 1 | local/global get/set, nops |
| Constant | 1 | `i32.const`, `i64.const` |
| Comparison | 2 | `eq`, `lt_u`, `ge_s`, … |
| Control | 2 | `block`, `loop`, `br`, `if` |
| Arithmetic | 3 | `add`, `sub`, `mul`, `and`, shifts |
| Load | 3 | `i32.load`, `i64.load`, … |
| Store | 5 | `i32.store`, `i64.store`, … |
| Call | 5 | `call` |
| Division | 10 | `div_s`, `div_u`, `rem_s`, `rem_u` |
| Indirect call | 20 | `call_indirect` |

A basic block's cost is the sum of its instructions' costs, charged once at the block head.

### 6.5 Host-call gas schedule

| Host function(s) | Cost |
|---|---|
| `state_read`, `state_has`, `balance_get` | 200 |
| `state_delete` | 500 |
| `state_write` | 5000 |
| `transfer` | 9000 |
| `get_height`, `get_timestamp`, `get_prev_hash`, `get_caller`, `get_address`, `get_block_hash` | 50 |
| `get_call_token`, `get_call_amount` | 50 |
| `get_remaining_gas` | 10 |
| `emit_event` | 375 base + 8 per data byte |

### 6.6 Descendant blocks

A single `Execute` may produce token transfers, each of which becomes a descendant send block from the contract. The **total** number of descendant sends produced by one execution is capped at `MaxDescendantBlocksPerExecute` (16). Because any state growth is settled as a single aggregated QSR **burn** descendant emitted after the transfers, the `transfer` host function caps transfers at `MaxDescendantBlocksPerExecute − 1`, reserving one slot for that burn; the total therefore stays at or below the cap whether or not a burn is ultimately emitted (so a transfer-only execution is bounded at 15 transfers). An attempt to exceed the transfer cap fails the call rather than emitting an unbounded fan-out.

### 6.7 Module cache

Compilation and instrumentation are amortized by a two-tier cache keyed by `SHA-256(original bytecode)`:

- An in-memory **LRU** of up to `WasmModuleCacheMaxSize` (1000) compiled modules.
- A **disk tier** that stores the **instrumented** bytecode (the interpreter backend cannot serialize a compiled module, so the instrumented bytes are persisted and recompiled on a hit).

The cache key is the original bytecode hash, so distinct source bytecodes never alias, and an upgrade evicts the superseded entry.

The disk tier is a **non-consensus optimization, and nothing it does may change the outcome of compilation**. A read error, a missing file, or cached bytes that no longer compile (corruption, format drift) all fall through to revalidate and recompile from the original bytecode, and the recompiled bytes overwrite the bad entry so it self-heals. A corrupt or unreadable cache file can therefore never make one node fail an execution that another node accepts — which would be a consensus split.

### 6.8 Watchdog

A 1-second wall-clock watchdog (`WasmWallClockLimit`) terminates a call that has not returned. It is armed via wazero's `WithCloseOnContextDone`, which interrupts the interpreter when the deadline elapses. It exists solely to bound pathological host stalls or interpreter loops that gas accounting has not yet caught; it is not a substitute for gas and is not part of the cost model a contract should rely on.

A watchdog trip is **wall-clock-dependent and therefore non-deterministic across nodes**, so it must never decide a committed block. It is surfaced as a distinct `ErrWallClockExceeded` (separate from out-of-gas) and treated as a **node-local fault**: the node aborts processing of that receive entirely rather than producing a "failed" block. A node that trips the watchdog on a receive other nodes execute within budget therefore halts on that block — a liveness event for that node — instead of committing a divergent state, which would be a safety violation. The deterministic out-of-gas signal always takes priority: a call that exhausts its gas is reported as out-of-gas even if the deadline also elapsed, so a deterministically gas-bounded result is never reclassified as a wall-clock fault. Deterministic execution always terminates by gas exhaustion or by returning, independently of wall-clock time.

---

## 7. Host-Function ABI

The runtime binds exactly **18** host functions under the import module name `env`. A module may import any subset of them; importing anything outside this set fails validation (§9). Pointers and lengths are `i32` offsets into the module's exported linear memory; scalar integers are little-endian; 256-bit token amounts and balances are 32-byte big-endian.

### 7.1 State

| Function | Signature | Behaviour |
|---|---|---|
| `state_read` | `(keyPtr, keyLen, resultPtr, resultMaxLen) -> i32` | Copies the value for `key` into `[resultPtr, resultPtr+resultMaxLen)`; returns the value length, or `0xFFFFFFFF` if the key is absent. |
| `state_write` | `(keyPtr, keyLen, valPtr, valLen) -> i32` | Writes `value` at `key`, burning the state-cost growth delta from the contract's QSR balance. Returns `0` on success, `1` if the contract has insufficient QSR. Traps in read-only contexts. |
| `state_delete` | `(keyPtr, keyLen) -> i32` | Deletes `key`, freeing the storage bytes. Returns `1` if the key existed, `0` if not. No QSR is recovered. Traps in read-only contexts. |
| `state_has` | `(keyPtr, keyLen) -> i32` | Returns 1 if `key` exists, else 0. |

### 7.2 Balances and transfers

| Function | Signature | Behaviour |
|---|---|---|
| `balance_get` | `(tokenPtr, resultPtr)` | Writes the contract's 32-byte big-endian spendable balance for the token into `resultPtr`. No return value. |
| `transfer` | `(tokenPtr, toPtr, amountPtr) -> i32` | Queues a transfer of a big-endian amount of a token to an address. Subject to the descendant-block cap. Traps in read-only contexts. |

### 7.3 Context accessors

| Function | Signature | Returns |
|---|---|---|
| `get_height` | `() -> i64` | Current momentum height. |
| `get_timestamp` | `() -> i64` | Current momentum timestamp. |
| `get_prev_hash` | `(resultPtr) -> i32` | Previous momentum hash (32 bytes) into `resultPtr`. |
| `get_caller` | `(resultPtr) -> i32` | Immediate caller address (20 bytes) into `resultPtr`. |
| `get_address` | `(resultPtr) -> i32` | The executing contract's own address (20 bytes). |
| `get_block_hash` | `(resultPtr) -> i32` | The triggering send block's hash (32 bytes). |
| `get_remaining_gas` | `() -> i64` | The current value of `__gas_remaining`. |
| `get_call_token` | `(resultPtr) -> i32` | The ZTS of the token attached to the incoming `Execute` call (10 bytes) into `resultPtr`. Returns all-zero bytes if no token was attached. |
| `get_call_amount` | `(resultPtr) -> i32` | The amount of the token attached to the incoming `Execute` call (32-byte big-endian) into `resultPtr`. Returns all-zero bytes if no amount was attached. |

`get_height` and `get_timestamp` read the **momentum** (not wall-clock), keeping them deterministic. `get_caller` returns the address of the account that submitted the `Execute` send block — the immediate sender of the triggering transaction. In Phase 1 this is always a user address, since contract-to-contract `Execute` calls are not supported (see §16).

`get_call_token` and `get_call_amount` expose the token standard and amount from the send block that triggered this `Execute` call. These are the primary mechanism for a contract to inspect an incoming payment. In a view call context, both return zero values since there is no real send block.

### 7.4 Events, diagnostics, and abort

| Function | Signature | Behaviour |
|---|---|---|
| `emit_event` | `(topicPtr, dataPtr, dataLen, indexed) -> i32` | Records an event (32-byte topic, data blob, indexed flag). Subject to the event caps (§10). Traps in read-only contexts. |
| `abort` | `(msgPtr, msgLen, filePtr, fileLen)` | Aborts the call (panics → trap). Used by language runtimes' `abort`. `filePtr`/`fileLen` are accepted for AssemblyScript compatibility but unused. |
| `log_msg` | `(msgPtr, msgLen)` | Diagnostic logging hook; a no-op for consensus (produces no state change). |

### 7.5 Read-only enforcement

In a read-only (view) context, the four mutating host functions — `state_write`, `state_delete`, `transfer`, and `emit_event` — invoke a read-only trap that panics and surfaces as a wazero trap, aborting the call. All read accessors remain available. This is what makes view calls (§11) safe to run off-consensus against live state.

---

## 8. Storage Model

### 8.1 Deployed-contract state

A deployed contract's user state lives in its own account store under the key prefix `0xFE`. All user key/value pairs written by `state_write` are stored here. There is no deposit ledger prefix — QSR is burned at write time, not tracked per-key.

### 8.2 WasmContract (factory) storage

The factory keeps per-deployed-contract records under typed prefixes within `WasmContract`'s own account store:

| Prefix | Record |
|---|---|
| `0x01` | `WasmContractInfo` — `{ halted, administrator }` |
| `0x02` | Bytecode |
| `0x03` | `WasmContractMetadata` — `{ deployer, version, upgradeable, activated, bytecodeHash, bytecodeCost }` |
| `0x04` | Chunk data (chunked deploys) |
| `0x05` | `WasmChunkMetadata` — `{ totalChunks, firstChunkHeight, collectedQsr, uploader, isUpgrade }` |
| `0x08` | Administrator time-challenge (pending rotation) |
| `0x09` | Revoked flag (per-contract, set by `Revoke`) |
| `0x0A` | Paused flag (per-contract, value = deployer address, set by `Pause`) |
| `0x0B` | `WasmVariables` — singleton governance-tunable parameters (see §4.11) |

`Execute` reads the `0x01`, `0x02`, and `0x03` records of the target contract cross-account from this store.

### 8.3 ChangesHash

All of the above — user state, factory records — flows through the node's normal state-change accounting, so a deployment, an execution, or an upgrade produces a deterministic `ChangesHash`. Identical inputs yield an identical hash on every node; this is the consensus anchor for WASM state transitions.

---

## 9. Determinism and Validation

Every byte of submitted bytecode is validated **before** it can be deployed. Validation rejects anything non-deterministic, anything unbounded, and anything outside the supported feature set. Validation is performed at deploy and finalize time, and re-performed on upgrade.

### 9.1 Forbidden features

The following are rejected outright, by exact opcode where applicable:

- **All floating-point** types and instructions. Floats are non-deterministic across platforms and have no place in consensus code.
- **SIMD** (the `0xFD` prefix).
- **Atomics / threads** (the `0xFE` prefix).
- **Exception handling** (`0x06`–`0x08`).
- **Reference types** (`0xD0`–`0xD2`).
- **The start section** and any `_start` export — a module may not run code at instantiation time.
- Under the `0xFC` (miscellaneous) prefix, only `memory.copy` (10) and `memory.fill` (11) are permitted; the saturating-truncation and other `0xFC` operations are rejected.

Integer width conversions (`0xA7`, `0xAC`, `0xAD`, and `0xC0`–`0xC4`) are explicitly **allowed** — they are deterministic and are needed by ordinary integer code.

### 9.2 Resource caps

Validation enforces hard structural limits so that neither compilation nor execution can be driven to pathological cost:

| Limit | Cap |
|---|---|
| Imports | 256 |
| Functions | 10,000 |
| Tables | 1 (entries ≤ 10,000) |
| Memory | 256 pages |
| Globals | 1,000 |
| Exports | 256 |
| Element-segment entries | 10,000 |
| Data-segment bytes | 16,384 |
| Custom-section bytes | 16,384 |
| Locals per function | 50,000 |
| Instructions per function | 100,000 |
| Complexity: Σ (instructions × (maxCallDepth + 1)) | 2,000,000 |

The complexity figure weights each function's instruction count by a deterministic call-density heuristic — the maximum number of un-returned `call`/`call_indirect` instructions seen while scanning the body, which is **not** the true call-graph depth — and caps the sum across all functions. It is defense-in-depth on top of the per-function instruction limit and the function-count cap, not a precise measure of interpretive work.

### 9.3 Required exports

- The module must export an `execute` function with signature `(i32, i32) -> i32`.
- The module must export its linear **memory** (host functions read and write guest memory through it).

### 9.4 Imports

Imports are restricted to the 18 host functions of §7. Any other import fails validation.

### 9.5 Index bounds

Every instruction operand that references an index space — `global.get`/`global.set`, `call`, `local.*`, and `call_indirect` type indices — must fall within the space declared by the module. This is **consensus-critical, not hygiene**: gas instrumentation (§6.3) appends entities to the function and global index spaces — the exported mutable `__gas_remaining` global and the `__consume_gas` helper. An index that is out of bounds in the *original* module would, after instrumentation, resolve onto one of those injected entities; in particular an out-of-bounds `global.set` would alias `__gas_remaining`, letting a contract rewrite its own gas counter and defeat metering entirely. The validator therefore rejects any operand index outside its declared space.

### 9.6 Compiler backstop

After the determinism and structural checks pass, the runtime compiles the **original** (un-instrumented) module with wazero and rejects the bytecode if wazero's own validator rejects it. wazero's validator is mature and exhaustive — it independently re-checks structural correctness, type-checking, and every index bound — so it backstops the hand-written checks above and closes any gap in them before instrumented code is ever produced. Because wazero permits floating point (it is core WASM 1.0 and cannot be disabled), this backstop runs **after** the determinism deny-list of §9.1, never instead of it: the determinism rejections are unique to this runtime, while everything structural is also guaranteed by the engine that will execute the code.

---

## 10. Events

### 10.1 Structure

An event is recorded as `nom.AccountBlockEvent`:

```go
type AccountBlockEvent struct {
    ContractAddress types.Address
    Topic           types.Hash
    Indexed         bool
    Data            []byte
}
```

Events are carried on the contract's receive block (proto field 24, omitted when empty).

### 10.2 Caps

| Limit | Cap |
|---|---|
| Events per `Execute` | `MaxEventsPerExecute` = 256 |
| Data bytes per event | `MaxEventDataPerEvent` = 4096 |
| Total event data bytes per `Execute` | `MaxEventBytesPerExecute` = 65,536 |

`emit_event` additionally charges 375 gas plus 8 gas per data byte, so events are bounded by both the hard caps and the gas budget.

### 10.3 Consensus hashing

Events participate in consensus through an `EventsHash` folded into the block's `ComputeHash`. The hash is computed by SHA3-256 over each event's fields in order:

```
Topic || ContractAddress || Indexed(1 byte) || len(Data) as 4-byte little-endian || Data
```

An empty event set hashes to the zero hash. The hash is **order-sensitive**: emitting the same events in a different order yields a different `EventsHash`, so event order is part of the state transition.

### 10.4 Block versioning

The events hash is folded into `ComputeHash` only when the block's `Version >= WasmAccountBlockVersion` (3). `WasmAccountBlockVersion` is defined once, in `chain/nom`, the package that owns `ComputeHash`, and is intentionally not duplicated as a `vm/constants` value to avoid two drifting copies of a consensus number. Only post-spork contract-receive blocks carry version 3; user sends and embedded descendants remain version 1, so the change is confined to WASM execution.

---

## 11. View Calls

View calls expose read-only contract queries to RPC clients without touching consensus.

### 11.1 Semantics

A view call runs the contract against current state in a **read-only context**: the four mutating host functions trap (§7.5), so a view can read state, balances, and context but cannot write state, transfer, or emit events. Because nothing is mutated, view calls are **off-consensus and determinism-exempt** — they never produce a block and never alter `ChangesHash`.

### 11.2 Entry point

`CallView` prefers a `view` export if the module provides one and falls back to `execute` (run read-only) otherwise. This lets a contract offer a dedicated, possibly cheaper, read path while remaining queryable even if it does not.

### 11.3 Return convention

A view returns its result as a length-prefixed buffer in guest memory. The `i32` result is a pointer to `[u32 little-endian length][bytes]`. The runtime reads the 4-byte length, then copies that many bytes out. The length is capped at `WasmMaxViewReturnSize` (65,536); an oversized length header is rejected with `ErrViewReturnTooLarge` **before** any copy, so a malicious length cannot drive RPC memory use. A zero length yields an empty, non-error result.

### 11.4 Metering and limits

A view is metered exactly like `Execute` — the same 250,000 gas budget and the same 1-second watchdog — so a query cannot be used to make a node do unbounded work. View calls are **not halt-gated**: a halted contract can still be queried, because reading state cannot harm anyone even while execution is paused.

### 11.5 Errors

`CallView` surfaces a defined set of errors, including `ErrViewEntryPointMissing` (no `view` or `execute` export), `ErrViewNoResult`, `ErrViewReturnTooLarge`, `ErrMemoryOOB`, `ErrOutOfGas`, and `ErrGasGlobalMissing`.

---

## 12. Consensus and Momentum Integration

WASM execution slots into the existing block/momentum machinery rather than running beside it:

- An `Execute` is an account block to `WasmContract` that drives the target `0x02` contract; its state writes and events all pass through normal state-change accounting and into `ChangesHash`.
- Token transfers emitted by a contract become descendant send blocks, capped per execution (§6.6).
- Time and height seen by a contract come from the **momentum** (`get_height`, `get_timestamp`), never from wall-clock, so two nodes replaying the same momentum compute identical results.
- The wall-clock watchdog (§6.8) is local to each node and never enters consensus. A trip is surfaced as `ErrWallClockExceeded` and treated as a node-local fault that aborts the block — never a committed (and therefore divergent) failed block. Honest nodes reach a committed result deterministically via gas; only a node that cannot execute within the wall-clock bound halts, which is a liveness event, not a fork.

The net effect is that a WASM state transition is as deterministic and replayable as any embedded-contract transition.

---

## 13. Security Model

The runtime's safety rests on layered, independently sufficient controls:

1. **Spork gate.** Nothing activates until `WasmRuntimeSpork` is enforced, and only after `DynamicPlasmaSpork`. The feature can be introduced on the network's schedule.
2. **Emergency halt.** A misbehaving contract can be paused by the administrator with no fork, preserving its state and balance for later resolution.
3. **Validation.** Non-deterministic and unbounded constructs are rejected before deployment by an explicit deny-list (floats, SIMD, atomics, exceptions, reference types), and every index reference is bounds-checked (§9.5) so gas instrumentation cannot be subverted. The original module is then compiled by wazero as a backstop (§9.6), so every structural property the engine relies on is independently validated by the engine itself.
4. **Deterministic gas.** Static metering plus host-call costs bound every execution in a way that is identical on every node; the wall-clock watchdog backstops the pathological residue.
5. **Storage economics.** On-chain bytes — bytecode and state — must be paid for in QSR burned from the contract's balance. This prices storage growth and prevents storage griefing without requiring per-user accounting.
6. **Isolation.** Each contract has its own address, balance, and state subset. There is no shared mutable global state, no host clock, no RNG, no filesystem, and no network.
7. **Privilege separation.** `IsEmbeddedAddress` is never widened to `0x02`; untrusted WASM contracts can never be mistaken for privileged built-ins.

---

## 14. Constants Reference

The values below are the **default** hardcoded constants. The 13 constants marked **(tunable)** can be overridden at runtime via `SetWasmVariables` (§4.11). When a governance update has been applied, the effective value is read from `WasmContract` storage per-call; the defaults here apply only when no update has been stored.

| Constant | Value | Meaning |
|---|---|---|
| `WasmExecutionGasLimit` | 250,000 | Gas budget per Execute / view **(tunable via `SetWasmVariables`)** |
| `WasmOnReceiveGasLimit` | 25,000 | Gas budget per on_receive hook **(tunable)** |
| `WasmWallClockLimit` | 1 s | Runaway watchdog bound |
| `MaxDescendantBlocksPerExecute` | 16 | Max transfers (descendant sends) per Execute **(tunable)** |
| `MaxWasmBytecodeSize` | 14,336 (14 KiB) | Max assembled bytecode size **(tunable)** |
| `MaxChunkCount` | 18 | Max chunks per chunked deploy **(tunable)** |
| `MaxArgsBytes` | 15,800 | Max Execute argument bytes |
| `MaxEventsPerExecute` | 256 | Max events per Execute **(tunable)** |
| `MaxEventDataPerEvent` | 4,096 | Max data bytes in one event **(tunable)** |
| `MaxEventBytesPerExecute` | 65,536 | Max total event data per Execute **(tunable)** |
| `WasmMaxViewReturnSize` | 65,536 | Max view return buffer **(tunable)** |
| `ZNNDeployFee` | 1 ZNN | ZNN burned at `Activate` |
| `QSRPerByteOfBytecode` | 500 | Bytecode cost per byte (burned at Activate) **(tunable)** |
| `QSRPerByteOfState` | 1,000 | State write cost per (key+value) byte (burned at write) **(tunable)** |
| `MinBytecodeCost` | 100,000,000 | Bytecode-cost floor (1 QSR) **(tunable)** |
| `ChunkTTLMomentums` | 1,440 | Chunked-deploy expiry; QSR forfeited and burned on expiry **(tunable)** |
| `WasmAdministratorDelay` | 1,440 | Administrator-rotation time-lock |
| `WasmModuleCacheMaxSize` | 1,000 | In-memory compiled-module LRU size |
| `InitialWasmAdministrator` | `GovernanceAddress` | Initial factory administrator |
| `WasmAccountBlockVersion` | 3 | Block version that folds in `EventsHash` |
| **Plasma** | | |
| `EmbeddedWasmDeploy` | 210,000 | Plasma for Deploy and Activate (10× base) |
| `EmbeddedWasmExecute` | 105,000 | Plasma for Execute (5× base) |
| `EmbeddedSimplePlasma` | 52,500 | Plasma for DiscardChunks, Revoke, Halt, Unhalt, Pause, Unpause, ChangeAdministrator (2.5× base) |
| `AccountBlockBasePlasma` | 21,000 | Base account-block plasma |
| **Opcode gas** | | |
| basic / constant | 1 | |
| comparison / control | 2 | |
| arithmetic / load | 3 | |
| store / call | 5 | |
| division | 10 | |
| call_indirect | 20 | |
| **Host-call gas** | | |
| `state_read` / `state_has` / `balance_get` | 200 | |
| `state_delete` | 500 | |
| `state_write` | 5,000 | |
| `transfer` | 9,000 | |
| context accessors (height, timestamp, prev_hash, caller, address, block_hash, call_token, call_amount) | 50 | |
| `get_remaining_gas` | 10 | |
| `emit_event` | 375 + 8/byte | |

---

## 15. RPC API

The runtime exposes read-only queries under the `embedded.wasm` namespace. None of these methods alter chain state.

| Method | Parameters | Returns |
|---|---|---|
| `embedded.wasm.getContract` | `contractAddress` | Contract info + metadata (deployer, version, upgradeable, halt status, bytecode hash, bytecode cost) |
| `embedded.wasm.getHaltStatus` | `contractAddress` | Whether the contract is halted |
| `embedded.wasm.getEvents` | `contractAddress, topic, fromHeight, toHeight, pageIndex, pageSize` | Paged list of events matching a topic over a height range |
| `embedded.wasm.getStats` | — | Aggregate runtime statistics |
| `embedded.wasm.callView` | `contractAddress, function, args` | The view result (§11), as a hex-encoded byte buffer |

`callView` runs the contract read-only against current state and is subject to the gas budget, the watchdog, and the return-size cap; it works even on a halted contract. The `function` parameter is accepted and parsed but does not affect dispatch in Phase 1 (see §4.10); it is provided for forward compatibility with ABI Outputs decoding.

---

## 16. Phase 1 Boundaries

This specification describes the runtime as built for its first release. The following are explicit boundaries of the current scope rather than the runtime described above:

- **Few entry points.** Contracts expose one `execute` entry, an optional `view` (§11), and an optional `on_receive` hook invoked on plain token credits (§5); a richer multi-method dispatch is a contract-level convention layered over `execute`, not a runtime feature.
- **No contract-to-contract `Execute` call frame.** A contract influences others by transferring tokens (descendant sends), not by synchronously calling another contract's `execute`. In Phase 1, `get_caller` always returns a user address (`0x00` prefix). Future phases may introduce synchronous C2C calls, at which point `get_caller` and a distinct `get_origin` (the original signing account) would diverge; this naming is intentionally forward-compatible.
- **Conservative budgets.** The 250,000-gas budget, the size and chunk caps, and the event caps are deliberately conservative. Because the runtime is spork-gated, any future widening of these consensus parameters is itself a spork-gated change validated against real network data.
- **Interpreter only.** Execution is interpreter-only by design. A compiling backend is excluded as long as it cannot be proven byte-identical across the supported platforms.
- **No contract removal of non-QSR balances via Revoke.** `Revoke` (§4.6) requires the deployer to drain non-QSR token balances before calling. Future phases may add a swept-transfer mechanism to handle this atomically. QSR remaining on a revoked contract is stranded.

Everything in §§1–15 is in force as specified; this section only delimits what is intentionally left to later, spork-gated evolution.
