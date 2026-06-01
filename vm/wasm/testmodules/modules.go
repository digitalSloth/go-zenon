// Package testdata provides inline WASM modules for integration testing.
// Each module is a minimal hand-crafted binary that exercises specific host
// functions. They are NOT consensus-critical — they exist only for tests.
package testmodules

// CounterModule returns a WASM module that imports state_read and state_write
// from "env" and implements a simple counter. On each execute call it:
//  1. Stores key "c" (1 byte) in linear memory at address 0
//  2. Calls state_read to load a 4-byte i32 counter from address 100
//  3. Increments the counter
//  4. Stores the new value at address 200
//  5. Calls state_write to persist it
//  6. Returns the new value
func CounterModule() []byte {
	return []byte{
		// Header
		0x00, 0x61, 0x73, 0x6D, // magic
		0x01, 0x00, 0x00, 0x00, // version 1

		// Type section (id=1, size=15)
		// 2 types:
		//   type 0: (i32, i32, i32, i32) -> i32  (state_read / state_write)
		//   type 1: (i32, i32) -> i32            (execute)
		0x01, 0x0F,
		0x02,
		0x60, 0x04, 0x7F, 0x7F, 0x7F, 0x7F, 0x01, 0x7F,
		0x60, 0x02, 0x7F, 0x7F, 0x01, 0x7F,

		// Import section (id=2, size=36)
		// 2 imports: env.state_read (func 0, type 0), env.state_write (func 1, type 0)
		0x02, 0x24,
		0x02,
		0x03, 0x65, 0x6E, 0x76, // "env"
		0x0A, 0x73, 0x74, 0x61, 0x74, 0x65, 0x5F, 0x72, 0x65, 0x61, 0x64, // "state_read"
		0x00, 0x00,
		0x03, 0x65, 0x6E, 0x76, // "env"
		0x0B, 0x73, 0x74, 0x61, 0x74, 0x65, 0x5F, 0x77, 0x72, 0x69, 0x74, 0x65, // "state_write"
		0x00, 0x00,

		// Function section (id=3, size=2)
		0x03, 0x02, 0x01, 0x01,

		// Memory section (id=5, size=3)
		0x05, 0x03, 0x01, 0x00, 0x01,

		// Export section (id=7, size=20)
		0x07, 0x14,
		0x02,
		0x07, 0x65, 0x78, 0x65, 0x63, 0x75, 0x74, 0x65, 0x00, 0x02, // "execute" func 2
		0x06, 0x6D, 0x65, 0x6D, 0x6F, 0x72, 0x79, 0x02, 0x00, // "memory" mem 0

		// Code section (id=10, size=57)
		0x0A, 0x39,
		0x01, // 1 function body
		0x37, // body size = 55
		0x00, // 0 local declarations

		// Store key "c" (0x63) at memory[0]. 99 is encoded as 2-byte signed-LEB128
		// (0xE3,0x00): a single-byte i32.const operand >= 0x40 sets the SLEB128 sign
		// bit and would decode as negative. i32.store8 keeps only the low 8 bits, so
		// the byte written is 0x63 ('c'), but encoding it as one byte (0x63) would
		// store 0xE3 instead.
		0x41, 0x00, // i32.const 0
		0x41, 0xE3, 0x00, // i32.const 99 ('c')
		0x3A, 0x00, 0x00, // i32.store8 align=0 offset=0

		// Call state_read(0, 1, 100, 4). 100 is encoded as 2-byte signed-LEB128
		// (0xE4,0x00) for the same reason — a bare 0x64 would decode as -28 and the
		// host write into guest memory at that negative offset would trap.
		0x41, 0x00, // i32.const 0 (key_ptr)
		0x41, 0x01, // i32.const 1 (key_len)
		0x41, 0xE4, 0x00, // i32.const 100 (result_ptr)
		0x41, 0x04, // i32.const 4 (result_max_len)
		0x10, 0x00, // call 0 (state_read)
		0x1A, // drop (discard value_len)

		// Load counter from memory[100], add 1, store at memory[200]
		0x41, 0xC8, 0x01, // i32.const 200
		0x41, 0xE4, 0x00, // i32.const 100
		0x28, 0x02, 0x00, // i32.load align=2 offset=0
		0x41, 0x01, // i32.const 1
		0x6A,             // i32.add
		0x36, 0x02, 0x00, // i32.store align=2 offset=0

		// Call state_write(0, 1, 200, 4)
		0x41, 0x00, // i32.const 0 (key_ptr)
		0x41, 0x01, // i32.const 1 (key_len)
		0x41, 0xC8, 0x01, // i32.const 200 (value_ptr)
		0x41, 0x04, // i32.const 4 (value_len)
		0x10, 0x01, // call 1 (state_write)
		0x1A, // drop

		// Return new value from memory[200]
		0x41, 0xC8, 0x01, // i32.const 200
		0x28, 0x02, 0x00, // i32.load align=2 offset=0

		0x0B, // end
	}
}

// StateWriteModule is CounterModule with its return value forced to 0. It exists
// because CounterModule returns the incremented counter (a non-zero value), and
// ExecuteMethod treats any non-zero execute result as ErrWasmExecutionFailed and
// reverts the whole receive — including the state-write burn. By returning 0 the
// execute SUCCEEDS, so the aggregated state-write burn actually settles, which is
// what lets a test assert real execute-path supply reduction.
//
// On each execute it stores key "c" (1 byte) and a 4-byte counter value via
// state_write, so a fresh write burns (1+4)*QSRPerByteOfState QSR — provided the
// contract holds enough spendable QSR (otherwise StateWrite returns its error
// before the Put, the guest ignores the status code, and execute still returns 0
// with no burn). Identical to CounterModule except the trailing
// `i32.load memory[200]` return is replaced with `i32.const 0` (and the code
// section / body sizes shrink by 4 bytes accordingly).
func StateWriteModule() []byte {
	return []byte{
		// Header
		0x00, 0x61, 0x73, 0x6D, // magic
		0x01, 0x00, 0x00, 0x00, // version 1

		// Type section (id=1, size=15) — identical to CounterModule
		0x01, 0x0F,
		0x02,
		0x60, 0x04, 0x7F, 0x7F, 0x7F, 0x7F, 0x01, 0x7F,
		0x60, 0x02, 0x7F, 0x7F, 0x01, 0x7F,

		// Import section (id=2, size=36) — env.state_read (func 0), env.state_write (func 1)
		0x02, 0x24,
		0x02,
		0x03, 0x65, 0x6E, 0x76, // "env"
		0x0A, 0x73, 0x74, 0x61, 0x74, 0x65, 0x5F, 0x72, 0x65, 0x61, 0x64, // "state_read"
		0x00, 0x00,
		0x03, 0x65, 0x6E, 0x76, // "env"
		0x0B, 0x73, 0x74, 0x61, 0x74, 0x65, 0x5F, 0x77, 0x72, 0x69, 0x74, 0x65, // "state_write"
		0x00, 0x00,

		// Function section (id=3, size=2)
		0x03, 0x02, 0x01, 0x01,

		// Memory section (id=5, size=3)
		0x05, 0x03, 0x01, 0x00, 0x01,

		// Export section (id=7, size=20) — "execute" func 2, "memory" mem 0
		0x07, 0x14,
		0x02,
		0x07, 0x65, 0x78, 0x65, 0x63, 0x75, 0x74, 0x65, 0x00, 0x02,
		0x06, 0x6D, 0x65, 0x6D, 0x6F, 0x72, 0x79, 0x02, 0x00,

		// Code section (id=10, size=53). Body size 51 (4 bytes shorter than
		// CounterModule: the i32.load return is replaced with i32.const 0).
		0x0A, 0x35,
		0x01, // 1 function body
		0x33, // body size = 51
		0x00, // 0 local declarations

		// Store key "c" (0x63) at memory[0]
		0x41, 0x00, // i32.const 0
		0x41, 0xE3, 0x00, // i32.const 99 ('c')
		0x3A, 0x00, 0x00, // i32.store8

		// Call state_read(0, 1, 100, 4); drop value_len
		0x41, 0x00, // i32.const 0 (key_ptr)
		0x41, 0x01, // i32.const 1 (key_len)
		0x41, 0xE4, 0x00, // i32.const 100 (result_ptr)
		0x41, 0x04, // i32.const 4 (result_max_len)
		0x10, 0x00, // call 0 (state_read)
		0x1A, // drop

		// memory[200] = memory[100] + 1
		0x41, 0xC8, 0x01, // i32.const 200
		0x41, 0xE4, 0x00, // i32.const 100
		0x28, 0x02, 0x00, // i32.load
		0x41, 0x01, // i32.const 1
		0x6A,             // i32.add
		0x36, 0x02, 0x00, // i32.store

		// Call state_write(0, 1, 200, 4); drop status
		0x41, 0x00, // i32.const 0 (key_ptr)
		0x41, 0x01, // i32.const 1 (key_len)
		0x41, 0xC8, 0x01, // i32.const 200 (value_ptr)
		0x41, 0x04, // i32.const 4 (value_len)
		0x10, 0x01, // call 1 (state_write)
		0x1A, // drop

		// Return 0 (success) — the only divergence from CounterModule
		0x41, 0x00, // i32.const 0
		0x0B, // end
	}
}

// EventModule returns a WASM module that imports emit_event from "env" and
// emits a single event with a 32-byte zero topic and 4-byte data {1,2,3,4}
// on each execute call. Returns 0 on success.
func EventModule() []byte {
	return []byte{
		// Header
		0x00, 0x61, 0x73, 0x6D, // magic
		0x01, 0x00, 0x00, 0x00, // version 1

		// Type section (id=1, size=15)
		0x01, 0x0F,
		0x02,
		0x60, 0x04, 0x7F, 0x7F, 0x7F, 0x7F, 0x01, 0x7F, // type 0: (i32,i32,i32,i32)->i32
		0x60, 0x02, 0x7F, 0x7F, 0x01, 0x7F, // type 1: (i32,i32)->i32

		// Import section (id=2, size=18)
		0x02, 0x12,
		0x01,
		0x03, 0x65, 0x6E, 0x76, // "env"
		0x0A, 0x65, 0x6D, 0x69, 0x74, 0x5F, 0x65, 0x76, 0x65, 0x6E, 0x74, // "emit_event"
		0x00, 0x00,

		// Function section (id=3, size=2)
		0x03, 0x02, 0x01, 0x01,

		// Memory section (id=5, size=3)
		0x05, 0x03, 0x01, 0x00, 0x01,

		// Export section (id=7, size=20)
		0x07, 0x14,
		0x02,
		0x07, 0x65, 0x78, 0x65, 0x63, 0x75, 0x74, 0x65, 0x00, 0x01, // "execute" func 1
		0x06, 0x6D, 0x65, 0x6D, 0x6F, 0x72, 0x79, 0x02, 0x00, // "memory" mem 0

		// Code section (id=10, size=45)
		0x0A, 0x2D,
		0x01, // 1 function body
		0x2B, // body size = 43
		0x00, // 0 local declarations

		// Store data bytes {1,2,3,4} at memory[32..35]. Addresses must stay < 64 so
		// each fits in a single-byte signed-LEB128 i32.const: a byte >= 0x40 has the
		// SLEB128 sign bit set and would decode as a NEGATIVE offset, trapping with
		// an out-of-bounds memory access before emit_event is ever reached. The
		// 32-byte zero topic lives at [0,31]; data at [32,35] does not overlap it.
		0x41, 0x20, // i32.const 32
		0x41, 0x01, // i32.const 1
		0x3A, 0x00, 0x00, // i32.store8
		0x41, 0x21, // i32.const 33
		0x41, 0x02, // i32.const 2
		0x3A, 0x00, 0x00, // i32.store8
		0x41, 0x22, // i32.const 34
		0x41, 0x03, // i32.const 3
		0x3A, 0x00, 0x00, // i32.store8
		0x41, 0x23, // i32.const 35
		0x41, 0x04, // i32.const 4
		0x3A, 0x00, 0x00, // i32.store8

		// Call emit_event(topic_ptr=0, data_ptr=32, data_len=4, indexed=1)
		0x41, 0x00, // i32.const 0 (topic_ptr — 32 zero bytes at memory[0])
		0x41, 0x20, // i32.const 32 (data_ptr)
		0x41, 0x04, // i32.const 4 (data_len)
		0x41, 0x01, // i32.const 1 (indexed=true)
		0x10, 0x00, // call 0 (emit_event)
		0x1A, // drop

		// Return 0 (success)
		0x41, 0x00, // i32.const 0

		0x0B, // end
	}
}

// ViewModule returns a WASM module that exports BOTH `execute` and `view`,
// used to test callView's view-export preference (§11.3). `execute` (func 0)
// returns i32.const 0; `view` (func 1) returns local 0 (the args pointer). When
// callView writes a length-prefixed buffer into args memory and invokes `view`,
// it gets that buffer back — whereas if `execute` were (wrongly) chosen it would
// read offset 0 (zeroed) and yield an empty buffer. Both bodies are trivial so
// the module also exercises gas instrumentation over a 2-function module.
func ViewModule() []byte {
	return []byte{
		// Header
		0x00, 0x61, 0x73, 0x6D, // magic
		0x01, 0x00, 0x00, 0x00, // version 1

		// Type section (id=1, size=7): 1 type (i32,i32)->i32
		0x01, 0x07,
		0x01,
		0x60, 0x02, 0x7F, 0x7F, 0x01, 0x7F,

		// Function section (id=3, size=3): 2 funcs, both type 0
		0x03, 0x03, 0x02, 0x00, 0x00,

		// Memory section (id=5, size=3): 1 memory, min 1 page
		0x05, 0x03, 0x01, 0x00, 0x01,

		// Export section (id=7, size=27): execute(func0), view(func1), memory
		0x07, 0x1B,
		0x03,
		0x07, 0x65, 0x78, 0x65, 0x63, 0x75, 0x74, 0x65, 0x00, 0x00, // "execute" func 0
		0x04, 0x76, 0x69, 0x65, 0x77, 0x00, 0x01, // "view" func 1
		0x06, 0x6D, 0x65, 0x6D, 0x6F, 0x72, 0x79, 0x02, 0x00, // "memory" mem 0

		// Code section (id=10, size=11): 2 bodies
		0x0A, 0x0B,
		0x02,
		0x04, 0x00, 0x41, 0x00, 0x0B, // func0 execute: i32.const 0; end
		0x04, 0x00, 0x20, 0x00, 0x0B, // func1 view: local.get 0; end
	}
}

// EchoExecuteModule returns a WASM module that exports only `execute` (func 0),
// whose body returns local 0 (the args pointer). callView, finding no `view`
// export, falls back to `execute` and reads the length-prefixed buffer at that
// pointer — so a length-prefixed buffer passed as args is echoed back. It is
// also used to drive the return-too-large guard by passing an oversized length
// prefix.
func EchoExecuteModule() []byte {
	return []byte{
		// Header
		0x00, 0x61, 0x73, 0x6D, // magic
		0x01, 0x00, 0x00, 0x00, // version 1

		// Type section (id=1, size=7): 1 type (i32,i32)->i32
		0x01, 0x07,
		0x01,
		0x60, 0x02, 0x7F, 0x7F, 0x01, 0x7F,

		// Function section (id=3, size=2): 1 func, type 0
		0x03, 0x02, 0x01, 0x00,

		// Memory section (id=5, size=3): 1 memory, min 1 page
		0x05, 0x03, 0x01, 0x00, 0x01,

		// Export section (id=7, size=20): execute(func0), memory
		0x07, 0x14,
		0x02,
		0x07, 0x65, 0x78, 0x65, 0x63, 0x75, 0x74, 0x65, 0x00, 0x00, // "execute" func 0
		0x06, 0x6D, 0x65, 0x6D, 0x6F, 0x72, 0x79, 0x02, 0x00, // "memory" mem 0

		// Code section (id=10, size=6): 1 body, returns local 0
		0x0A, 0x06,
		0x01,
		0x04, 0x00, 0x20, 0x00, 0x0B, // local.get 0; end
	}
}

// RunawayModule returns a WASM module with an infinite loop in execute.
// Used to test wall-clock trap and gas exhaustion.
func RunawayModule() []byte {
	return []byte{
		// Header
		0x00, 0x61, 0x73, 0x6D, // magic
		0x01, 0x00, 0x00, 0x00, // version 1

		// Type section (id=1, size=7)
		0x01, 0x07,
		0x01,
		0x60, 0x02, 0x7F, 0x7F, 0x01, 0x7F,

		// Function section (id=3, size=2)
		0x03, 0x02, 0x01, 0x00,

		// Memory section (id=5, size=3)
		0x05, 0x03, 0x01, 0x00, 0x01,

		// Export section (id=7, size=20)
		0x07, 0x14,
		0x02,
		0x07, 0x65, 0x78, 0x65, 0x63, 0x75, 0x74, 0x65, 0x00, 0x00,
		0x06, 0x6D, 0x65, 0x6D, 0x6F, 0x72, 0x79, 0x02, 0x00,

		// Code section (id=10, size=10)
		0x0A, 0x0A,
		0x01,       // 1 function body
		0x08,       // body size = 8
		0x00,       // 0 local declarations
		0x03, 0x40, // loop (void block type)
		0x0C, 0x00, // br 0 (branch back to loop)
		0x0B, // end loop
		0x00, // unreachable (satisfies i32 return type)
		0x0B, // end function
	}
}
