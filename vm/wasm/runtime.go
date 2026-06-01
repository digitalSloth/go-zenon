package wasm

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"

	lru "github.com/hashicorp/golang-lru"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/constants"
)

// WasmRuntime is the singleton WASM execution engine.
type WasmRuntime struct {
	cache    *ModuleCache
	runtime  wazero.Runtime
	metering *HostGasListenerFactory
}

var (
	globalRuntime *WasmRuntime
	runtimeOnce   sync.Once
	runtimeMu     sync.Mutex
)

// GetWasmRuntime returns the singleton WASM runtime, creating it on first call.
func GetWasmRuntime(dataDir string) *WasmRuntime {
	runtimeOnce.Do(func() {
		runtimeMu.Lock()
		defer runtimeMu.Unlock()

		cache, err := NewModuleCache(dataDir)
		if err != nil {
			panic("failed to create module cache: " + err.Error())
		}

		// Interpreter mode only — deterministic across all platforms.
		// wazero version is pinned in go.mod — bumping requires a
		// determinism cross-check (consensus-critical).
		//
		// WithCloseOnContextDone arms the wall-clock watchdog: without it the
		// interpreter ignores the context deadline entirely and the runaway guard
		// is dead ceremony. A trip is wall-clock-dependent (non-deterministic), so
		// callers must treat ErrWallClockExceeded as a node-local fault, never a
		// committed block (see classifyExecErr and vm.applyBlock).
		config := wazero.NewRuntimeConfigInterpreter().WithCloseOnContextDone(true)
		rt := wazero.NewRuntimeWithConfig(context.Background(), config)

		metering := NewHostGasListenerFactory()

		// Build and instantiate the host module.
		builder := rt.NewHostModuleBuilder("env")
		registerHostFunctions(builder, metering)
		_, err = builder.Instantiate(context.Background())
		if err != nil {
			panic("failed to instantiate host module: " + err.Error())
		}

		globalRuntime = &WasmRuntime{
			cache:    cache,
			runtime:  rt,
			metering: metering,
		}
	})
	return globalRuntime
}

// Close shuts down the runtime and releases resources.
func (wr *WasmRuntime) Close() error {
	return wr.runtime.Close(context.Background())
}

// Evict removes a compiled module from the cache by its original bytecode.
func (wr *WasmRuntime) Evict(bytecode []byte) {
	wr.cache.Evict(bytecode)
}

// CompileModule validates, instruments, and compiles a WASM bytecode.
// The result is cached in both LRU and disk.
func (wr *WasmRuntime) CompileModule(bytecode []byte) (wazero.CompiledModule, error) {
	return wr.cache.GetModule(wr.runtime, bytecode)
}

// Execute runs a WASM contract. Returns the result code and any error.
func (wr *WasmRuntime) Execute(wc *WasmContext, bytecode []byte, args []byte) (int32, error) {
	compiled, err := wr.CompileModule(bytecode)
	if err != nil {
		return 0, err
	}

	// Create a context with WasmContext and gas listener factory.
	ctx := context.Background()
	ctx = contextWithWasmCtx(ctx, wc)
	ctx = WithFunctionListenerFactory(ctx, wr.metering)

	// Wall-clock watchdog (non-consensus, catches infinite loops).
	ctx, cancel := context.WithTimeout(ctx, constants.WasmWallClockLimit)
	defer cancel()

	// Instantiate the contract module with no WASI, no filesystem, no random.
	modConfig := wazero.NewModuleConfig().
		WithName("").
		WithStdout(nil).
		WithStderr(nil)

	mod, err := wr.runtime.InstantiateModule(ctx, compiled, modConfig)
	if err != nil {
		return 0, err
	}
	defer mod.Close(ctx)

	// Seed the gas budget. The injected $__gas_remaining global starts at 0; the
	// runtime owns the limit, which keeps the cached/instrumented module
	// independent of the limit's value. A missing global means the bytecode was
	// not instrumented, which must never reach execution.
	gasGlobal, ok := mod.ExportedGlobal(GasGlobalName).(api.MutableGlobal)
	if !ok {
		return 0, ErrGasGlobalMissing
	}
	gasGlobal.Set(wc.vars.ExecutionGasLimit)

	// Write args into WASM memory.
	// The execute function signature is (args_ptr, args_len) -> i32.
	memory := mod.Memory()
	argsPtr := uint32(0)
	if len(args) > 0 {
		// Allocate space in linear memory for args.
		// Args are placed at the end of linear memory.
		argsPtr = uint32(memory.Size()) - uint32(len(args))
		if !memory.Write(argsPtr, args) {
			return 0, ErrMemoryOOB
		}
	}

	// Call execute(args_ptr, args_len).
	execute := mod.ExportedFunction("execute")
	if execute == nil {
		return 0, ErrEntryPointMissing
	}

	results, err := execute.Call(ctx, uint64(argsPtr), uint64(len(args)))

	// Record remaining gas regardless of outcome so callers can compute gas
	// used as (limit - remaining).
	remaining := gasGlobal.Get()
	wc.gasRemaining = remaining

	if err != nil {
		// $__consume_gas zeroes the counter before trapping, so a trap with
		// remaining==0 is the out-of-gas signal. (A guest trap that coincides
		// with exactly-zero remaining is reported the same way; it is equally a
		// failed execution that consumed the full budget.) A non-exhausted call
		// whose deadline fired is the wall-clock watchdog (node-local fault).
		return 0, classifyExecErr(ctx, err, remaining)
	}

	return int32(results[0]), nil
}

// ExecuteOnReceive runs the on_receive hook if the contract exports it.
// Returns (0, nil) if the contract has no on_receive export (silent no-op).
// Uses a lower gas limit (WasmOnReceiveGasLimit) than Execute.
func (wr *WasmRuntime) ExecuteOnReceive(wc *WasmContext, bytecode []byte, args []byte) (int32, error) {
	compiled, err := wr.CompileModule(bytecode)
	if err != nil {
		return 0, err
	}

	// Check if on_receive export exists — silent no-op if absent.
	if _, ok := compiled.ExportedFunctions()["on_receive"]; !ok {
		return 0, nil
	}

	ctx := context.Background()
	ctx = contextWithWasmCtx(ctx, wc)
	ctx = WithFunctionListenerFactory(ctx, wr.metering)
	ctx, cancel := context.WithTimeout(ctx, constants.WasmWallClockLimit)
	defer cancel()

	modConfig := wazero.NewModuleConfig().
		WithName("").
		WithStdout(nil).
		WithStderr(nil)

	mod, err := wr.runtime.InstantiateModule(ctx, compiled, modConfig)
	if err != nil {
		return 0, err
	}
	defer mod.Close(ctx)

	gasGlobal, ok := mod.ExportedGlobal(GasGlobalName).(api.MutableGlobal)
	if !ok {
		return 0, ErrGasGlobalMissing
	}
	gasGlobal.Set(wc.vars.OnReceiveGasLimit)

	memory := mod.Memory()
	argsPtr := uint32(0)
	if len(args) > 0 {
		argsPtr = uint32(memory.Size()) - uint32(len(args))
		if !memory.Write(argsPtr, args) {
			return 0, ErrMemoryOOB
		}
	}

	fn := mod.ExportedFunction("on_receive")
	if fn == nil {
		return 0, nil
	}

	results, err := fn.Call(ctx, uint64(argsPtr), uint64(len(args)))

	remaining := gasGlobal.Get()
	wc.gasRemaining = remaining

	if err != nil {
		return 0, classifyExecErr(ctx, err, remaining)
	}

	return int32(results[0]), nil
}

// CallView runs a read-only view call (§11.3) and returns the contract's
// length-prefixed return buffer. It mirrors Execute's setup (compile, gas seed,
// metering, wall-clock watchdog) but differs in three ways: (1) the WasmContext
// must be read-only (NewViewContext) so the mutating host functions trap; (2) it
// prefers the optional `view` export, falling back to `execute`; (3) it copies
// the [u32 little-endian length][bytes] return buffer out of guest memory BEFORE
// the deferred Close invalidates it.
//
// CallView is deliberately separate from Execute and is never reached on the
// consensus path: view results are off-consensus and explicitly determinism-
// exempt (§11.3), so this must not be folded into the consensus hot path.
func (wr *WasmRuntime) CallView(wc *WasmContext, bytecode []byte, args []byte) ([]byte, error) {
	compiled, err := wr.CompileModule(bytecode)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	ctx = contextWithWasmCtx(ctx, wc)
	ctx = WithFunctionListenerFactory(ctx, wr.metering)

	// Wall-clock watchdog (non-consensus, catches infinite loops).
	ctx, cancel := context.WithTimeout(ctx, constants.WasmWallClockLimit)
	defer cancel()

	modConfig := wazero.NewModuleConfig().
		WithName("").
		WithStdout(nil).
		WithStderr(nil)

	mod, err := wr.runtime.InstantiateModule(ctx, compiled, modConfig)
	if err != nil {
		return nil, err
	}
	defer mod.Close(ctx)

	gasGlobal, ok := mod.ExportedGlobal(GasGlobalName).(api.MutableGlobal)
	if !ok {
		return nil, ErrGasGlobalMissing
	}
	gasGlobal.Set(wc.vars.ExecutionGasLimit)

	memory := mod.Memory()
	argsPtr := uint32(0)
	if len(args) > 0 {
		argsPtr = uint32(memory.Size()) - uint32(len(args))
		if !memory.Write(argsPtr, args) {
			return nil, ErrMemoryOOB
		}
	}

	// Prefer the optional `view` export; fall back to `execute` in read-only
	// mode (spec §11.3). A contract exporting neither cannot be queried. Both
	// are invoked as (args_ptr, args_len) -> i32 and their i32 result is treated
	// as a pointer to the length-prefixed return buffer.
	entry := mod.ExportedFunction("view")
	if entry == nil {
		entry = mod.ExportedFunction("execute")
	}
	if entry == nil {
		return nil, ErrViewEntryPointMissing
	}

	results, err := entry.Call(ctx, uint64(argsPtr), uint64(len(args)))

	// Record remaining gas so callers can compute gas used as (limit - remaining).
	remaining := gasGlobal.Get()
	wc.gasRemaining = remaining

	if err != nil {
		return nil, classifyExecErr(ctx, err, remaining)
	}
	if len(results) == 0 {
		return nil, ErrViewNoResult
	}

	// results[0] is an i32 pointer to [u32 LE length][bytes]. Read and copy the
	// buffer out before the deferred Close invalidates the memory view.
	ptr := uint32(results[0])
	lenBytes, ok := memory.Read(ptr, 4)
	if !ok {
		return nil, ErrMemoryOOB
	}
	n := binary.LittleEndian.Uint32(lenBytes)
	if n > uint32(wc.vars.MaxViewReturnSize) {
		return nil, ErrViewReturnTooLarge
	}
	if n == 0 {
		return []byte{}, nil
	}
	data, ok := memory.Read(ptr+4, n)
	if !ok {
		return nil, ErrMemoryOOB
	}
	out := make([]byte, n)
	copy(out, data)
	return out, nil
}

// ModuleCache provides LRU + disk caching for compiled WASM modules.
type ModuleCache struct {
	mu       sync.Mutex
	lruCache *lru.Cache
	disk     *DiskCache
}

// NewModuleCache creates a new module cache.
func NewModuleCache(dataDir string) (*ModuleCache, error) {
	l, err := lru.New(constants.WasmModuleCacheMaxSize)
	if err != nil {
		return nil, err
	}

	var disk *DiskCache
	if dataDir != "" {
		disk, err = NewDiskCache(dataDir + "/wasm-modules")
		if err != nil {
			return nil, err
		}
	}

	return &ModuleCache{
		lruCache: l,
		disk:     disk,
	}, nil
}

// bytecodeHash computes SHA256 of the bytecode for cache keying.
func bytecodeHash(bytecode []byte) [32]byte {
	return sha256.Sum256(bytecode)
}

// hashToTypesHash converts a [32]byte to types.Hash.
func hashToTypesHash(h [32]byte) types.Hash {
	var result types.Hash
	copy(result[:], h[:])
	return result
}

// GetModule returns a compiled module, using cache or compiling fresh.
func (mc *ModuleCache) GetModule(rt wazero.Runtime, bytecode []byte) (wazero.CompiledModule, error) {
	hash := bytecodeHash(bytecode)

	// Check LRU.
	if cached, ok := mc.lruCache.Get(hash); ok {
		return cached.(wazero.CompiledModule), nil
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Double-check after acquiring lock.
	if cached, ok := mc.lruCache.Get(hash); ok {
		return cached.(wazero.CompiledModule), nil
	}

	// Check disk cache. The disk tier is a NON-consensus performance
	// optimization, so nothing it does may change the outcome of compilation: a
	// read error (I/O, permissions, a truncated file) or cached bytes that fail
	// to compile must never propagate, or one node would fail an Execute that
	// every other node accepts — a consensus split. On any such condition we fall
	// through and recompile from the original bytecode (always available); the
	// Put below then overwrites a bad entry, so it self-heals.
	if mc.disk != nil {
		th := hashToTypesHash(hash)
		if data, err := mc.disk.Get(th); err == nil && len(data) > 0 {
			if compiled, cerr := rt.CompileModule(context.Background(), data); cerr == nil {
				mc.lruCache.Add(hash, compiled)
				return compiled, nil
			}
			// Cached instrumented bytes failed to compile (corruption / format
			// drift): fall through to recompile from the original and overwrite.
		}
	}

	// Validate bytecode (determinism deny-list: floats, SIMD, atomics, …).
	if err := ValidateBytecode(bytecode); err != nil {
		return nil, err
	}

	// Backstop: compile the ORIGINAL (un-instrumented) module with wazero so its
	// mature validator catches every structural/index error the determinism pass
	// is not responsible for — out-of-bounds global/local/function indices, type
	// mismatches, malformed bodies. This is consensus-critical: gas injection
	// appends the mutable __gas_remaining global (and the $__consume_gas helper)
	// to the index space, so an out-of-bounds reference in the original would
	// resolve onto injected entities post-instrumentation. wazero rejects the
	// original here, before that can happen. (wazero permits floats, so this does
	// NOT replace ValidateBytecode — it runs after it.) The compiled artifact is
	// discarded; only the instrumented module is executed.
	if origCompiled, err := rt.CompileModule(context.Background(), bytecode); err != nil {
		return nil, err
	} else {
		_ = origCompiled.Close(context.Background())
	}

	// Inject gas metering instrumentation.
	instrumented, err := InjectGasMetering(bytecode)
	if err != nil {
		return nil, err
	}

	// Compile.
	compiled, err := rt.CompileModule(context.Background(), instrumented)
	if err != nil {
		return nil, err
	}

	// Store in LRU.
	mc.lruCache.Add(hash, compiled)

	// Store on disk.
	if mc.disk != nil {
		th := hashToTypesHash(hash)
		// wazero CompiledModule doesn't expose serialization in v1.8.x, so the
		// disk cache holds the *instrumented* bytecode and the hit path compiles
		// it as-is. Storing the original here instead would serve un-instrumented
		// modules on a warm cache while cold nodes ran instrumented ones — a
		// consensus fork. The key is still the original bytecode's hash.
		if err := mc.disk.Put(th, instrumented); err != nil {
			// Non-fatal: disk cache is a performance optimization.
			_ = err
		}
	}

	return compiled, nil
}

// Evict removes a module from the cache by bytecode hash.
func (mc *ModuleCache) Evict(bytecode []byte) {
	hash := bytecodeHash(bytecode)
	mc.lruCache.Remove(hash)
	if mc.disk != nil {
		th := hashToTypesHash(hash)
		mc.disk.Delete(th)
	}
}

// Size returns the number of entries in the LRU cache.
func (mc *ModuleCache) Size() int {
	return mc.lruCache.Len()
}

// ErrMemoryOOB is returned when a memory access is out of bounds.
var ErrMemoryOOB = errors.New("memory access out of bounds")

// ErrOutOfGas is returned when execution exhausts the gas budget (the injected
// metering traps once $__gas_remaining would underflow).
var ErrOutOfGas = errors.New("wasm execution ran out of gas")

// ErrGasGlobalMissing is returned when an instantiated module lacks the injected
// gas global — i.e. it bypassed InjectGasMetering, which must never happen on
// the execution path.
var ErrGasGlobalMissing = errors.New("instrumented module missing gas global")

// ErrViewEntryPointMissing is returned by CallView when a contract exports
// neither a `view` nor an `execute` entry point and so cannot be queried (§11.3).
var ErrViewEntryPointMissing = errors.New("wasm contract exports no view or execute entry point")

// ErrViewNoResult is returned by CallView when the view/execute entry point
// returns no value, so there is no return-buffer pointer to read.
var ErrViewNoResult = errors.New("wasm view call returned no result")

// ErrViewReturnTooLarge is returned by CallView when the length prefix of the
// return buffer exceeds WasmMaxViewReturnSize.
var ErrViewReturnTooLarge = errors.New("wasm view return buffer too large")

// ErrWallClockExceeded is returned when the wall-clock watchdog
// (WithCloseOnContextDone) interrupts a call because it exceeded
// WasmWallClockLimit. Unlike out-of-gas, this outcome depends on wall-clock time
// and so differs across nodes; the VM must treat it as a node-local fault and
// must never commit a block whose result was decided by it.
var ErrWallClockExceeded = errors.New("wasm execution exceeded wall-clock limit")

// classifyExecErr maps a failed wazero Call onto the runtime's sentinels. The
// deterministic out-of-gas signal (remaining==0, set when $__consume_gas zeroes
// the counter before trapping) takes priority: it is reported even if the
// deadline also elapsed, so a deterministically gas-exhausted call is never
// reclassified as the non-deterministic wall-clock fault. Only a call that did
// NOT exhaust its gas and whose context deadline fired is a watchdog trip.
func classifyExecErr(ctx context.Context, err error, remaining uint64) error {
	if remaining == 0 {
		return ErrOutOfGas
	}
	if ctx.Err() == context.DeadlineExceeded {
		return ErrWallClockExceeded
	}
	return err
}
