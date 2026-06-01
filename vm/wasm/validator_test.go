package wasm

import (
	"testing"
)

func TestValidate_InvalidMagicBytes(t *testing.T) {
	err := ValidateBytecode([]byte{0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00})
	if err != ErrInvalidMagicBytes {
		t.Fatalf("expected ErrInvalidMagicBytes, got %v", err)
	}
}

// Regression for the gas-metering bypass (C1): a module that declares zero
// globals but does `global.set 0` is out of bounds in the original module. Gas
// injection appends the exported mutable __gas_remaining global at exactly that
// index, so before bounds-checking this aliased the gas counter post-injection
// and let the contract refill its own gas. The validator must reject it.
func TestValidate_OutOfBoundsGlobalRejected(t *testing.T) {
	body := []byte{0x00} // no locals
	body = append(body, 0x42)
	body = append(body, encodeSLEB128(1_000_000)...) // i64.const 1_000_000
	body = append(body, 0x24)
	body = append(body, encodeULEB128(0)...) // global.set 0 (module declares 0 globals)
	body = append(body, 0x41, 0x00, 0x0B)    // i32.const 0; end
	if err := ValidateBytecode(buildExecuteModule(body)); err != ErrIndexOutOfBounds {
		t.Fatalf("expected ErrIndexOutOfBounds, got %v", err)
	}
}

// A call to a function index past the function space must also be rejected
// (the $__consume_gas helper is appended at totalFuncs by gas injection).
func TestValidate_OutOfBoundsCallRejected(t *testing.T) {
	body := []byte{0x00} // no locals
	body = append(body, 0x10)
	body = append(body, encodeULEB128(5)...) // call 5 (only func 0 exists)
	body = append(body, 0x41, 0x00, 0x0B)    // i32.const 0; end
	if err := ValidateBytecode(buildExecuteModule(body)); err != ErrIndexOutOfBounds {
		t.Fatalf("expected ErrIndexOutOfBounds, got %v", err)
	}
}

func TestValidate_TooSmall(t *testing.T) {
	err := ValidateBytecode([]byte{0x00, 0x61, 0x73})
	if err != ErrBytecodeTooSmall {
		t.Fatalf("expected ErrBytecodeTooSmall, got %v", err)
	}
}

func TestValidate_InvalidVersion(t *testing.T) {
	// Valid magic, version 2
	err := ValidateBytecode([]byte{0x00, 0x61, 0x73, 0x6D, 0x02, 0x00, 0x00, 0x00})
	if err != ErrInvalidVersion {
		t.Fatalf("expected ErrInvalidVersion, got %v", err)
	}
}

func TestValidate_EmptyModule(t *testing.T) {
	// Valid magic + version, no sections — should fail (no execute export)
	err := ValidateBytecode([]byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00})
	if err != ErrEntryPointMissing {
		t.Fatalf("expected ErrEntryPointMissing, got %v", err)
	}
}

func TestValidate_ImportWhitelist(t *testing.T) {
	// Build a minimal WASM module with an invalid import.
	module := buildMinimalModule(funcType{
		params:  []byte{valTypeI32, valTypeI32},
		results: []byte{valTypeI32},
	}, importEntry{
		module: "env",
		name:   "evil_function",
		kind:   importFunc,
	})

	err := ValidateBytecode(module)
	if err != ErrImportNotWhitelisted {
		t.Fatalf("expected ErrImportNotWhitelisted, got %v", err)
	}
}

func TestValidate_GetOriginRejected(t *testing.T) {
	// get_origin was removed from the allowlist; importing it must fail.
	module := buildMinimalModule(funcType{
		params:  []byte{valTypeI32},
		results: []byte{},
	}, importEntry{
		module: "env",
		name:   "get_origin",
		kind:   importFunc,
	})

	err := ValidateBytecode(module)
	if err != ErrImportNotWhitelisted {
		t.Fatalf("expected ErrImportNotWhitelisted, got %v", err)
	}
}

func TestValidate_GetCallerAllowed(t *testing.T) {
	// get_caller is now a valid host function.
	module := buildMinimalModule(funcType{
		params:  []byte{valTypeI32, valTypeI32},
		results: []byte{valTypeI32},
	}, importEntry{
		module: "env",
		name:   "get_caller",
		kind:   importFunc,
	})

	err := ValidateBytecode(module)
	if err != nil {
		t.Fatalf("get_caller should be allowed, got %v", err)
	}
}

func TestValidate_GetCallTokenAndAmountAllowed(t *testing.T) {
	// get_call_token and get_call_amount are valid host functions.
	module := buildMinimalModule(funcType{
		params:  []byte{valTypeI32, valTypeI32},
		results: []byte{valTypeI32},
	}, importEntry{
		module: "env",
		name:   "get_call_token",
		kind:   importFunc,
	}, importEntry{
		module: "env",
		name:   "get_call_amount",
		kind:   importFunc,
	})

	err := ValidateBytecode(module)
	if err != nil {
		t.Fatalf("get_call_token/get_call_amount should be allowed, got %v", err)
	}
}

func TestValidate_FloatTypesForbidden(t *testing.T) {
	// Build a module where the import uses a float type (f32).
	module := buildModuleWithFloatImport()
	err := ValidateBytecode(module)
	if err != ErrFloatTypesForbidden {
		t.Fatalf("expected ErrFloatTypesForbidden, got %v", err)
	}
}

func buildModuleWithFloatImport() []byte {
	var module []byte

	module = append(module, 0x00, 0x61, 0x73, 0x6D) // magic
	module = append(module, 0x01, 0x00, 0x00, 0x00) // version 1

	// Type section: 2 types
	var typeSection []byte
	typeSection = append(typeSection, 0x02) // 2 types
	// Type 0: import uses float (f32) -> i32
	typeSection = append(typeSection, 0x60)
	typeSection = append(typeSection, 0x01, valTypeF32) // 1 param: f32
	typeSection = append(typeSection, 0x01, valTypeI32) // 1 result: i32
	// Type 1: execute (i32, i32) -> i32
	typeSection = append(typeSection, 0x60)
	typeSection = append(typeSection, 0x02, valTypeI32, valTypeI32)
	typeSection = append(typeSection, 0x01, valTypeI32)
	module = appendSection(module, sectionType, typeSection)

	// Import section: 1 import using type 0
	var importSection []byte
	importSection = appendULEB128(importSection, 1) // 1 import
	importSection = appendULEB128(importSection, 3) // module len
	importSection = append(importSection, []byte("env")...)
	importSection = appendULEB128(importSection, 10) // name len
	importSection = append(importSection, []byte("state_read")...)
	importSection = append(importSection, importFunc)
	importSection = appendULEB128(importSection, 0) // type index 0
	module = appendSection(module, sectionImport, importSection)

	// Function section: 1 function using type 1
	var funcSection []byte
	funcSection = appendULEB128(funcSection, 1)
	funcSection = appendULEB128(funcSection, 1) // type 1
	module = appendSection(module, sectionFunction, funcSection)

	// Memory section
	var memSection []byte
	memSection = appendULEB128(memSection, 1)
	memSection = append(memSection, 0x00)
	memSection = appendULEB128(memSection, 1)
	module = appendSection(module, sectionMemory, memSection)

	// Export section: execute + memory
	var exportSection []byte
	exportSection = appendULEB128(exportSection, 2)
	exportSection = appendULEB128(exportSection, 7)
	exportSection = append(exportSection, []byte("execute")...)
	exportSection = append(exportSection, exportFunc)
	exportSection = appendULEB128(exportSection, 1) // func index 1 (after import)
	exportSection = appendULEB128(exportSection, 6)
	exportSection = append(exportSection, []byte("memory")...)
	exportSection = append(exportSection, exportMem)
	exportSection = appendULEB128(exportSection, 0)
	module = appendSection(module, sectionExport, exportSection)

	// Code section
	var codeSection []byte
	codeSection = appendULEB128(codeSection, 1)
	body := []byte{0x00, 0x20, 0x00, 0x41, 0x01, 0x6A, 0x0B}
	codeSection = appendULEB128(codeSection, uint64(len(body)))
	codeSection = append(codeSection, body...)
	module = appendSection(module, sectionCode, codeSection)

	return module
}

func TestValidate_UnderscoreStartForbidden(t *testing.T) {
	module := buildMinimalModule(funcType{
		params:  []byte{valTypeI32, valTypeI32},
		results: []byte{valTypeI32},
	}, importEntry{
		module: "env",
		name:   "state_read",
		kind:   importFunc,
	})

	// Add _start export
	module = addExport(module, "_start", exportFunc, 0)

	err := ValidateBytecode(module)
	if err != ErrUnderscoreStart {
		t.Fatalf("expected ErrUnderscoreStart, got %v", err)
	}
}

func TestValidate_StartSectionForbidden(t *testing.T) {
	module := buildMinimalModule(funcType{
		params:  []byte{valTypeI32, valTypeI32},
		results: []byte{valTypeI32},
	}, importEntry{
		module: "env",
		name:   "state_read",
		kind:   importFunc,
	})

	// Add start section (section ID 8)
	startSection := []byte{sectionStart, 0x01, 0x00} // section 8, size 1, func index 0
	module = append(module, startSection...)

	err := ValidateBytecode(module)
	if err != ErrStartFunctionPresent {
		t.Fatalf("expected ErrStartFunctionPresent, got %v", err)
	}
}

// --- Helpers for building minimal WASM modules ---

type importEntry struct {
	module string
	name   string
	kind   byte
}

func buildMinimalModule(executeType funcType, imports ...importEntry) []byte {
	var module []byte

	// Header: magic + version
	module = append(module, 0x00, 0x61, 0x73, 0x6D) // magic
	module = append(module, 0x01, 0x00, 0x00, 0x00) // version 1

	// Type section (1)
	var typeSection []byte
	typeSection = append(typeSection, 0x02) // 2 types
	// Type 0: for imports (i32) -> i32 (simplified)
	typeSection = append(typeSection, 0x60)             // func type
	typeSection = append(typeSection, 0x01, valTypeI32) // 1 param: i32
	typeSection = append(typeSection, 0x01, valTypeI32) // 1 result: i32
	// Type 1: execute signature (i32, i32) -> i32
	typeSection = append(typeSection, 0x60) // func type
	typeSection = append(typeSection, byte(len(executeType.params)))
	typeSection = append(typeSection, executeType.params...)
	typeSection = append(typeSection, byte(len(executeType.results)))
	typeSection = append(typeSection, executeType.results...)
	module = appendSection(module, sectionType, typeSection)

	// Import section (2)
	if len(imports) > 0 {
		var importSection []byte
		importSection = appendULEB128(importSection, uint64(len(imports)))
		for _, imp := range imports {
			importSection = appendULEB128(importSection, uint64(len(imp.module)))
			importSection = append(importSection, []byte(imp.module)...)
			importSection = appendULEB128(importSection, uint64(len(imp.name)))
			importSection = append(importSection, []byte(imp.name)...)
			importSection = append(importSection, imp.kind)
			if imp.kind == importFunc {
				importSection = appendULEB128(importSection, 0) // type index 0
			}
		}
		module = appendSection(module, sectionImport, importSection)
	}

	// Function section (3): 1 function using type 1
	var funcSection []byte
	funcSection = appendULEB128(funcSection, 1) // 1 function
	funcSection = appendULEB128(funcSection, 1) // type index 1
	module = appendSection(module, sectionFunction, funcSection)

	// Memory section (5): 1 page
	var memSection []byte
	memSection = appendULEB128(memSection, 1) // 1 memory
	memSection = append(memSection, 0x00)     // no max
	memSection = appendULEB128(memSection, 1) // min 1 page
	module = appendSection(module, sectionMemory, memSection)

	// Export section (7): execute + memory
	var exportSection []byte
	exportSection = appendULEB128(exportSection, 2) // 2 exports
	// Export "execute"
	exportSection = appendULEB128(exportSection, uint64(len("execute")))
	exportSection = append(exportSection, []byte("execute")...)
	exportSection = append(exportSection, exportFunc)
	exportSection = appendULEB128(exportSection, uint64(len(imports))) // func index
	// Export "memory"
	exportSection = appendULEB128(exportSection, uint64(len("memory")))
	exportSection = append(exportSection, []byte("memory")...)
	exportSection = append(exportSection, exportMem)
	exportSection = appendULEB128(exportSection, 0) // memory index 0
	module = appendSection(module, sectionExport, exportSection)

	// Code section (10): 1 function body
	var codeSection []byte
	codeSection = appendULEB128(codeSection, 1) // 1 function body
	body := []byte{
		0x00,       // 0 local declarations
		0x20, 0x00, // local.get 0
		0x41, 0x01, // i32.const 1
		0x6A, // i32.add
		0x0B, // end
	}
	codeSection = appendULEB128(codeSection, uint64(len(body)))
	codeSection = append(codeSection, body...)
	module = appendSection(module, sectionCode, codeSection)

	return module
}

func appendSection(module []byte, id byte, payload []byte) []byte {
	module = append(module, id)
	module = appendULEB128(module, uint64(len(payload)))
	module = append(module, payload...)
	return module
}

func appendULEB128(b []byte, v uint64) []byte {
	if v == 0 {
		return append(b, 0)
	}
	for v > 0 {
		byte_ := byte(v & 0x7F)
		v >>= 7
		if v > 0 {
			byte_ |= 0x80
		}
		b = append(b, byte_)
	}
	return b
}

func addExport(module []byte, name string, kind byte, index uint64) []byte {
	// This is a simplified helper — in practice we'd rebuild the export section.
	// For testing, we append a second export section.
	var exportSection []byte
	exportSection = appendULEB128(exportSection, 1) // 1 export
	exportSection = appendULEB128(exportSection, uint64(len(name)))
	exportSection = append(exportSection, []byte(name)...)
	exportSection = append(exportSection, kind)
	exportSection = appendULEB128(exportSection, index)
	return appendSection(module, sectionExport, exportSection)
}

// buildModuleWithCodeBody builds a minimal valid module (no imports) whose
// single exported `execute` function has the given code body. The body must
// include its local-declaration count prefix and trailing 0x0B (end).
func buildModuleWithCodeBody(body []byte) []byte {
	var module []byte

	module = append(module, 0x00, 0x61, 0x73, 0x6D) // magic
	module = append(module, 0x01, 0x00, 0x00, 0x00) // version 1

	// Type section: 1 type, type 0 = execute (i32, i32) -> i32
	var typeSection []byte
	typeSection = append(typeSection, 0x01) // 1 type
	typeSection = append(typeSection, 0x60) // func type
	typeSection = append(typeSection, 0x02, valTypeI32, valTypeI32)
	typeSection = append(typeSection, 0x01, valTypeI32)
	module = appendSection(module, sectionType, typeSection)

	// Function section: 1 function using type 0
	var funcSection []byte
	funcSection = appendULEB128(funcSection, 1)
	funcSection = appendULEB128(funcSection, 0)
	module = appendSection(module, sectionFunction, funcSection)

	// Memory section: 1 page
	var memSection []byte
	memSection = appendULEB128(memSection, 1)
	memSection = append(memSection, 0x00)
	memSection = appendULEB128(memSection, 1)
	module = appendSection(module, sectionMemory, memSection)

	// Export section: execute (func 0) + memory (mem 0)
	var exportSection []byte
	exportSection = appendULEB128(exportSection, 2)
	exportSection = appendULEB128(exportSection, uint64(len("execute")))
	exportSection = append(exportSection, []byte("execute")...)
	exportSection = append(exportSection, exportFunc)
	exportSection = appendULEB128(exportSection, 0)
	exportSection = appendULEB128(exportSection, uint64(len("memory")))
	exportSection = append(exportSection, []byte("memory")...)
	exportSection = append(exportSection, exportMem)
	exportSection = appendULEB128(exportSection, 0)
	module = appendSection(module, sectionExport, exportSection)

	// Code section: 1 function body
	var codeSection []byte
	codeSection = appendULEB128(codeSection, 1)
	codeSection = appendULEB128(codeSection, uint64(len(body)))
	codeSection = append(codeSection, body...)
	module = appendSection(module, sectionCode, codeSection)

	return module
}

// TestValidate_IntegerConversionsAllowed ensures integer conversion opcodes
// (here i64.extend_i32_u, 0xAD) are NOT rejected as floats.
func TestValidate_IntegerConversionsAllowed(t *testing.T) {
	body := []byte{
		0x00,       // 0 locals
		0x20, 0x00, // local.get 0
		0xAD, // i64.extend_i32_u
		0xA7, // i32.wrap_i64
		0x0B, // end
	}
	module := buildModuleWithCodeBody(body)
	if err := ValidateBytecode(module); err != nil {
		t.Fatalf("expected integer conversions to validate, got %v", err)
	}
}

// TestValidate_FloatConstForbidden ensures f64.const (0x44) is rejected.
func TestValidate_FloatConstForbidden(t *testing.T) {
	body := []byte{
		0x00,                                                 // 0 locals
		0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // f64.const 0
		0x1A, // drop
		0x20, 0x00,
		0x0B, // end
	}
	module := buildModuleWithCodeBody(body)
	if err := ValidateBytecode(module); err != ErrForbiddenOpcode {
		t.Fatalf("expected ErrForbiddenOpcode, got %v", err)
	}
}

// TestValidate_SimdForbidden ensures the SIMD prefix (0xFD) is rejected.
func TestValidate_SimdForbidden(t *testing.T) {
	body := []byte{
		0x00,       // 0 locals
		0x20, 0x00, // local.get 0
		0xFD, 0x0C, // v128.const prefix (forbidden)
		0x0B, // end
	}
	module := buildModuleWithCodeBody(body)
	if err := ValidateBytecode(module); err != ErrForbiddenOpcode {
		t.Fatalf("expected ErrForbiddenOpcode, got %v", err)
	}
}

// TestValidate_StoreMemargParsed ensures store opcodes (i32.store, 0x36) have
// their memarg (align+offset) consumed so the parser stays in sync.
func TestValidate_StoreMemargParsed(t *testing.T) {
	body := []byte{
		0x00,       // 0 locals
		0x20, 0x00, // local.get 0 (address)
		0x20, 0x01, // local.get 1 (value)
		0x36, 0x02, 0x00, // i32.store align=2 offset=0
		0x20, 0x00, // local.get 0
		0x0B, // end
	}
	module := buildModuleWithCodeBody(body)
	if err := ValidateBytecode(module); err != nil {
		t.Fatalf("expected i32.store with memarg to validate, got %v", err)
	}
}
