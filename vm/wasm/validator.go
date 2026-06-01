package wasm

import (
	"encoding/binary"
	"errors"
)

var (
	ErrInvalidMagicBytes    = errors.New("invalid WASM magic bytes")
	ErrInvalidVersion       = errors.New("invalid WASM version")
	ErrFloatTypesForbidden  = errors.New("float types are forbidden")
	ErrForbiddenOpcode      = errors.New("forbidden WASM opcode")
	ErrStartFunctionPresent = errors.New("start function is forbidden")
	ErrEntryPointMissing    = errors.New("execute export is missing")
	ErrEntryPointSignature  = errors.New("execute export must have signature (i32, i32) -> i32")
	ErrMemoryNotExported    = errors.New("memory must be exported")
	ErrMemoryTooLarge       = errors.New("memory exceeds 256 pages")
	ErrResourceCapExceeded  = errors.New("resource cap exceeded")
	ErrComplexityBudget     = errors.New("complexity budget exceeded")
	ErrImportNotWhitelisted = errors.New("import not in whitelist")
	ErrUnderscoreStart      = errors.New("_start export is forbidden")
	ErrBytecodeTooSmall     = errors.New("bytecode too small")
	// ErrIndexOutOfBounds rejects an instruction whose immediate references an
	// index past the end of its index space (e.g. a global.set one past the
	// declared globals). Left unchecked this is a consensus-critical hole: gas
	// injection appends the mutable __gas_remaining global at exactly that index,
	// so an out-of-bounds reference in the original module resolves onto the gas
	// counter in the instrumented module and lets a contract refill its own gas.
	ErrIndexOutOfBounds = errors.New("instruction references out-of-bounds index")
)

// Allowed host function imports.
var allowedImports = map[string]bool{
	"state_read":        true,
	"state_write":       true,
	"state_delete":      true,
	"state_has":         true,
	"balance_get":       true,
	"transfer":          true,
	"get_height":        true,
	"get_timestamp":     true,
	"get_prev_hash":     true,
	"get_caller":        true,
	"get_call_token":    true,
	"get_call_amount":   true,
	"get_address":       true,
	"get_block_hash":    true,
	"get_remaining_gas": true,
	"emit_event":        true,
	"abort":             true,
	"log_msg":           true,
}

// WASM value types
const (
	valTypeI32 = 0x7F
	valTypeI64 = 0x7E
	valTypeF32 = 0x7D
	valTypeF64 = 0x7C
)

// WASM section IDs
const (
	sectionCustom   = 0
	sectionType     = 1
	sectionImport   = 2
	sectionFunction = 3
	sectionTable    = 4
	sectionMemory   = 5
	sectionGlobal   = 6
	sectionExport   = 7
	sectionStart    = 8
	sectionElement  = 9
	sectionCode     = 10
	sectionData     = 11
)

// Export kinds
const (
	exportFunc   = 0x00
	exportTable  = 0x01
	exportMem    = 0x02
	exportGlobal = 0x03
)

// Import kinds
const (
	importFunc   = 0x00
	importTable  = 0x01
	importMem    = 0x02
	importGlobal = 0x03
)

// ValidateBytecode performs exhaustive structural validation of a WASM binary.
func ValidateBytecode(bytecode []byte) error {
	if len(bytecode) < 8 {
		return ErrBytecodeTooSmall
	}

	// Magic bytes
	if bytecode[0] != 0x00 || bytecode[1] != 0x61 || bytecode[2] != 0x73 || bytecode[3] != 0x6D {
		return ErrInvalidMagicBytes
	}

	// Version
	version := binary.LittleEndian.Uint32(bytecode[4:8])
	if version != 1 {
		return ErrInvalidVersion
	}

	// Parse sections
	r := &wasmReader{data: bytecode[8:], pos: 0}

	var typeSection []funcType
	var importCount uint32
	var importedFuncs uint32
	var importedGlobals uint32
	var funcTypeIndices []uint32
	var hasMemory bool
	var memoryPages uint32
	var exportCount uint32
	var hasExecuteExport bool
	var executeTypeIdx uint32
	var hasMemoryExport bool
	var hasStartSection bool
	var hasUnderscoreStart bool
	var functionCount uint32
	var globalCount uint32
	var tableCount uint32
	var tableEntries uint32
	var elementEntries uint32
	var dataBytes uint64
	var customBytes uint64
	var codeFunctions []codeFunc
	var complexitySum uint64

	for r.hasMore() {
		sectionID, err := r.readByte()
		if err != nil {
			return err
		}
		sectionSize, err := r.readULEB128()
		if err != nil {
			return err
		}
		if sectionSize > uint64(len(r.data)-r.pos) {
			return ErrResourceCapExceeded
		}
		sectionPayload := r.data[r.pos : r.pos+int(sectionSize)]
		sectionEnd := r.pos + int(sectionSize)

		switch sectionID {
		case sectionType:
			types, err := parseTypeSection(sectionPayload)
			if err != nil {
				return err
			}
			typeSection = types

		case sectionImport:
			count, impFuncs, impGlobals, names, err := parseImportSection(sectionPayload)
			if err != nil {
				return err
			}
			importCount = count
			importedFuncs = impFuncs
			importedGlobals = impGlobals
			for _, name := range names {
				if !allowedImports[name] {
					return ErrImportNotWhitelisted
				}
			}
			if importCount > 256 {
				return ErrResourceCapExceeded
			}

		case sectionFunction:
			indices, err := parseFunctionSection(sectionPayload)
			if err != nil {
				return err
			}
			funcTypeIndices = indices
			functionCount = uint32(len(indices)) + importedFuncs
			if functionCount > 10000 {
				return ErrResourceCapExceeded
			}

		case sectionTable:
			count, entries, err := parseTableSection(sectionPayload)
			if err != nil {
				return err
			}
			tableCount = count
			tableEntries = entries
			if tableCount > 1 || tableEntries > 10000 {
				return ErrResourceCapExceeded
			}

		case sectionMemory:
			pages, err := parseMemorySection(sectionPayload)
			if err != nil {
				return err
			}
			hasMemory = true
			memoryPages = pages
			if memoryPages > 256 {
				return ErrMemoryTooLarge
			}

		case sectionGlobal:
			count, hasFloats, err := parseGlobalSection(sectionPayload)
			if err != nil {
				return err
			}
			if hasFloats {
				return ErrFloatTypesForbidden
			}
			globalCount = count
			if globalCount > 1000 {
				return ErrResourceCapExceeded
			}

		case sectionExport:
			count, execIdx, hasExec, memExp, underscoreStart, err := parseExportSection(sectionPayload, typeSection, funcTypeIndices, importCount)
			if err != nil {
				return err
			}
			exportCount = count
			if exportCount > 256 {
				return ErrResourceCapExceeded
			}
			if hasExec {
				hasExecuteExport = true
				executeTypeIdx = execIdx
			}
			hasMemoryExport = memExp
			hasUnderscoreStart = underscoreStart

		case sectionStart:
			hasStartSection = true

		case sectionElement:
			entries, err := parseElementSection(sectionPayload)
			if err != nil {
				return err
			}
			elementEntries = entries
			if elementEntries > 10000 {
				return ErrResourceCapExceeded
			}

		case sectionCode:
			// Index spaces are complete by the code section (imports < globals <
			// code in WASM section order), so the code parser can bounds-check
			// global and function references against them.
			totalGlobals := importedGlobals + globalCount
			totalFuncs := importedFuncs + uint32(len(funcTypeIndices))
			funcs, complexity, err := parseCodeSection(sectionPayload, totalGlobals, totalFuncs)
			if err != nil {
				return err
			}
			codeFunctions = funcs
			complexitySum = complexity

		case sectionData:
			bytes, err := parseDataSection(sectionPayload)
			if err != nil {
				return err
			}
			dataBytes += bytes
			if dataBytes > 16384 {
				return ErrResourceCapExceeded
			}

		case sectionCustom:
			customBytes += uint64(len(sectionPayload))
			if customBytes > 16384 {
				return ErrResourceCapExceeded
			}
		}

		r.pos = sectionEnd
	}

	// Post-parse checks
	if hasStartSection {
		return ErrStartFunctionPresent
	}
	if hasUnderscoreStart {
		return ErrUnderscoreStart
	}
	if !hasExecuteExport {
		return ErrEntryPointMissing
	}
	if !hasMemoryExport {
		return ErrMemoryNotExported
	}
	if !hasMemory {
		return ErrMemoryNotExported
	}

	// Validate execute signature: (i32, i32) -> i32
	if int(executeTypeIdx) >= len(typeSection) {
		return ErrEntryPointSignature
	}
	ft := typeSection[executeTypeIdx]
	if len(ft.params) != 2 || ft.params[0] != valTypeI32 || ft.params[1] != valTypeI32 {
		return ErrEntryPointSignature
	}
	if len(ft.results) != 1 || ft.results[0] != valTypeI32 {
		return ErrEntryPointSignature
	}

	// Validate no float types in type section
	for _, ft := range typeSection {
		for _, p := range ft.params {
			if p == valTypeF32 || p == valTypeF64 {
				return ErrFloatTypesForbidden
			}
		}
		for _, r := range ft.results {
			if r == valTypeF32 || r == valTypeF64 {
				return ErrFloatTypesForbidden
			}
		}
	}

	// Validate code section: no float locals, no forbidden opcodes
	for _, cf := range codeFunctions {
		if cf.localCount > 50000 {
			return ErrResourceCapExceeded
		}
		if cf.instructionCount > 100000 {
			return ErrResourceCapExceeded
		}
		for _, localType := range cf.localTypes {
			if localType == valTypeF32 || localType == valTypeF64 {
				return ErrFloatTypesForbidden
			}
		}
	}

	// Complexity budget
	if complexitySum > 2000000 {
		return ErrComplexityBudget
	}

	return nil
}

// --- Parsing helpers ---

type funcType struct {
	params  []byte
	results []byte
}

type codeFunc struct {
	localCount       uint32
	localTypes       []byte
	instructionCount uint32
}

type wasmReader struct {
	data []byte
	pos  int
}

func (r *wasmReader) hasMore() bool {
	return r.pos < len(r.data)
}

func (r *wasmReader) readByte() (byte, error) {
	if r.pos >= len(r.data) {
		return 0, ErrBytecodeTooSmall
	}
	b := r.data[r.pos]
	r.pos++
	return b, nil
}

func (r *wasmReader) readULEB128() (uint64, error) {
	var result uint64
	var shift uint
	for {
		b, err := r.readByte()
		if err != nil {
			return 0, err
		}
		result |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return result, nil
		}
		shift += 7
		if shift > 63 {
			return 0, ErrResourceCapExceeded
		}
	}
}

func (r *wasmReader) readBytes(n int) ([]byte, error) {
	if r.pos+n > len(r.data) {
		return nil, ErrBytecodeTooSmall
	}
	result := r.data[r.pos : r.pos+n]
	r.pos += n
	return result, nil
}

func parseTypeSection(data []byte) ([]funcType, error) {
	r := &wasmReader{data: data, pos: 0}
	count, err := r.readULEB128()
	if err != nil {
		return nil, err
	}
	if count > 10000 {
		return nil, ErrResourceCapExceeded
	}
	types := make([]funcType, count)
	for i := uint64(0); i < count; i++ {
		tag, err := r.readByte()
		if err != nil {
			return nil, err
		}
		if tag != 0x60 {
			return nil, errors.New("invalid function type tag")
		}
		paramCount, err := r.readULEB128()
		if err != nil {
			return nil, err
		}
		params := make([]byte, paramCount)
		for j := uint64(0); j < paramCount; j++ {
			params[j], err = r.readByte()
			if err != nil {
				return nil, err
			}
		}
		resultCount, err := r.readULEB128()
		if err != nil {
			return nil, err
		}
		results := make([]byte, resultCount)
		for j := uint64(0); j < resultCount; j++ {
			results[j], err = r.readByte()
			if err != nil {
				return nil, err
			}
		}
		types[i] = funcType{params: params, results: results}
	}
	return types, nil
}

// parseImportSection returns the total import count, the number of imported
// functions and globals (which occupy the low indices of their respective index
// spaces, so index bounds-checks in the code section must account for them), and
// the imported names for whitelist validation.
func parseImportSection(data []byte) (total, importedFuncs, importedGlobals uint32, names []string, err error) {
	r := &wasmReader{data: data, pos: 0}
	count, err := r.readULEB128()
	if err != nil {
		return 0, 0, 0, nil, err
	}
	for i := uint64(0); i < count; i++ {
		// module name
		modLen, err := r.readULEB128()
		if err != nil {
			return 0, 0, 0, nil, err
		}
		r.pos += int(modLen) // skip module name
		// import name
		nameLen, err := r.readULEB128()
		if err != nil {
			return 0, 0, 0, nil, err
		}
		nameBytes, err := r.readBytes(int(nameLen))
		if err != nil {
			return 0, 0, 0, nil, err
		}
		names = append(names, string(nameBytes))
		// import kind
		kind, err := r.readByte()
		if err != nil {
			return 0, 0, 0, nil, err
		}
		switch kind {
		case importFunc:
			importedFuncs++
			_, err = r.readULEB128() // type index
		case importTable:
			r.readByte()             // element type
			_, err = r.readULEB128() // limits flag
			if err == nil {
				_, err = r.readULEB128() // min
			}
			// max is optional based on flag
		case importMem:
			_, err = r.readULEB128() // limits flag
			if err == nil {
				_, err = r.readULEB128() // min pages
			}
		case importGlobal:
			importedGlobals++
			r.readByte() // type
			r.readByte() // mutability
		}
		if err != nil {
			return 0, 0, 0, nil, err
		}
	}
	return uint32(count), importedFuncs, importedGlobals, names, nil
}

func parseFunctionSection(data []byte) ([]uint32, error) {
	r := &wasmReader{data: data, pos: 0}
	count, err := r.readULEB128()
	if err != nil {
		return nil, err
	}
	indices := make([]uint32, count)
	for i := uint64(0); i < count; i++ {
		idx, err := r.readULEB128()
		if err != nil {
			return nil, err
		}
		indices[i] = uint32(idx)
	}
	return indices, nil
}

func parseTableSection(data []byte) (uint32, uint32, error) {
	r := &wasmReader{data: data, pos: 0}
	count, err := r.readULEB128()
	if err != nil {
		return 0, 0, err
	}
	var entries uint32
	for i := uint64(0); i < count; i++ {
		r.readByte() // element type (0x70 = funcref)
		flag, err := r.readULEB128()
		if err != nil {
			return 0, 0, err
		}
		min, err := r.readULEB128()
		if err != nil {
			return 0, 0, err
		}
		entries = uint32(min)
		if flag == 1 {
			max, err := r.readULEB128()
			if err != nil {
				return 0, 0, err
			}
			entries = uint32(max)
		}
	}
	return uint32(count), entries, nil
}

func parseMemorySection(data []byte) (uint32, error) {
	r := &wasmReader{data: data, pos: 0}
	count, err := r.readULEB128()
	if err != nil {
		return 0, err
	}
	if count != 1 {
		return 0, ErrResourceCapExceeded
	}
	flag, err := r.readULEB128()
	if err != nil {
		return 0, err
	}
	min, err := r.readULEB128()
	if err != nil {
		return 0, err
	}
	_ = flag
	return uint32(min), nil
}

func parseGlobalSection(data []byte) (uint32, bool, error) {
	r := &wasmReader{data: data, pos: 0}
	count, err := r.readULEB128()
	if err != nil {
		return 0, false, err
	}
	hasFloats := false
	for i := uint64(0); i < count; i++ {
		valType, err := r.readByte()
		if err != nil {
			return 0, false, err
		}
		if valType == valTypeF32 || valType == valTypeF64 {
			hasFloats = true
		}
		r.readByte() // mutability
		// Skip init expression (terminated by 0x0B = end)
		for {
			op, err := r.readByte()
			if err != nil {
				return 0, false, err
			}
			if op == 0x0B { // end
				break
			}
			// Skip immediate operands
			switch op {
			case 0x41: // i32.const
				r.readULEB128()
			case 0x42: // i64.const
				r.readULEB128()
			case 0x43: // f32.const
				r.readBytes(4)
			case 0x44: // f64.const
				r.readBytes(8)
			case 0x23: // global.get
				r.readULEB128()
			}
		}
	}
	return uint32(count), hasFloats, nil
}

func parseExportSection(data []byte, typeSection []funcType, funcTypeIndices []uint32, importCount uint32) (uint32, uint32, bool, bool, bool, error) {
	r := &wasmReader{data: data, pos: 0}
	count, err := r.readULEB128()
	if err != nil {
		return 0, 0, false, false, false, err
	}

	var executeTypeIdx uint32
	var hasExecute bool
	var hasMemoryExport bool
	var hasUnderscoreStart bool

	for i := uint64(0); i < count; i++ {
		nameLen, err := r.readULEB128()
		if err != nil {
			return 0, 0, false, false, false, err
		}
		nameBytes, err := r.readBytes(int(nameLen))
		if err != nil {
			return 0, 0, false, false, false, err
		}
		name := string(nameBytes)
		kind, err := r.readByte()
		if err != nil {
			return 0, 0, false, false, false, err
		}
		idx, err := r.readULEB128()
		if err != nil {
			return 0, 0, false, false, false, err
		}

		if name == "_start" {
			hasUnderscoreStart = true
		}

		if kind == exportFunc && name == "execute" {
			// Resolve the function's type index
			funcIdx := uint32(idx) - importCount
			if int(funcIdx) < len(funcTypeIndices) && funcIdx < uint32(len(funcTypeIndices)) {
				executeTypeIdx = funcTypeIndices[funcIdx]
				hasExecute = true
			}
		}

		if kind == exportMem {
			hasMemoryExport = true
		}
	}

	return uint32(count), executeTypeIdx, hasExecute, hasMemoryExport, hasUnderscoreStart, nil
}

func parseElementSection(data []byte) (uint32, error) {
	r := &wasmReader{data: data, pos: 0}
	count, err := r.readULEB128()
	if err != nil {
		return 0, err
	}
	var totalEntries uint32
	for i := uint64(0); i < count; i++ {
		tableIdx, err := r.readULEB128()
		if err != nil {
			return 0, err
		}
		_ = tableIdx
		// Offset expression
		for {
			op, err := r.readByte()
			if err != nil {
				return 0, err
			}
			if op == 0x0B { // end
				break
			}
			switch op {
			case 0x41: // i32.const
				r.readULEB128()
			case 0x42: // i64.const
				r.readULEB128()
			}
		}
		numElem, err := r.readULEB128()
		if err != nil {
			return 0, err
		}
		totalEntries += uint32(numElem)
		for j := uint64(0); j < numElem; j++ {
			r.readULEB128() // function index
		}
	}
	return totalEntries, nil
}

func parseCodeSection(data []byte, totalGlobals, totalFuncs uint32) ([]codeFunc, uint64, error) {
	r := &wasmReader{data: data, pos: 0}
	count, err := r.readULEB128()
	if err != nil {
		return nil, 0, err
	}
	funcs := make([]codeFunc, count)
	var complexitySum uint64

	for i := uint64(0); i < count; i++ {
		bodySize, err := r.readULEB128()
		if err != nil {
			return nil, 0, err
		}
		bodyEnd := r.pos + int(bodySize)

		// Parse locals
		localDeclCount, err := r.readULEB128()
		if err != nil {
			return nil, 0, err
		}
		var localCount uint32
		var localTypes []byte
		for j := uint64(0); j < localDeclCount; j++ {
			n, err := r.readULEB128()
			if err != nil {
				return nil, 0, err
			}
			t, err := r.readByte()
			if err != nil {
				return nil, 0, err
			}
			localCount += uint32(n)
			if t == valTypeF32 || t == valTypeF64 {
				localTypes = append(localTypes, t)
			}
		}

		// Count instructions (simplified: count non-END opcodes in the body)
		var instructionCount uint32
		var callDepth uint32
		var maxCallDepth uint32
		for r.pos < bodyEnd {
			op, err := r.readByte()
			if err != nil {
				return nil, 0, err
			}
			instructionCount++

			// Check for forbidden opcodes
			if isForbiddenOpcode(op) {
				return nil, 0, ErrForbiddenOpcode
			}

			// Track a deterministic call-density heuristic (NOT true call-graph
			// depth): the running count of un-returned call/call_indirect, whose
			// max weights this body's complexity figure. Defense-in-depth on top
			// of the instruction-count and function-count caps.
			switch op {
			case 0x10: // call
				callDepth++
				if callDepth > maxCallDepth {
					maxCallDepth = callDepth
				}
				// Bounds-check the callee index. An index past the function space
				// is out of bounds in the original module; gas injection appends
				// the $__consume_gas helper at totalFuncs, so an unchecked call
				// could be made to reference injected code.
				fnIdx, err := r.readULEB128()
				if err != nil {
					return nil, 0, err
				}
				if uint32(fnIdx) >= totalFuncs {
					return nil, 0, ErrIndexOutOfBounds
				}
			case 0x11: // call_indirect
				callDepth++
				if callDepth > maxCallDepth {
					maxCallDepth = callDepth
				}
				r.readULEB128() // type index
				r.readByte()    // table index
			case 0x0F: // return
				if callDepth > 0 {
					callDepth--
				}
			case 0x41: // i32.const
				r.readSLEB128()
			case 0x42: // i64.const
				r.readSLEB128()
			case 0x43: // f32.const
				r.readBytes(4)
			case 0x44: // f64.const
				r.readBytes(8)
			case 0x20, 0x21, 0x22: // local.get/set/tee
				// Local index bounds are enforced by the wazero compile backstop
				// (it needs per-function param+local counts to check precisely).
				r.readULEB128()
			case 0x23, 0x24: // global.get/set
				// Bounds-check the global index. This is the consensus-critical
				// check: gas injection appends the mutable __gas_remaining global
				// at totalGlobals, so an out-of-bounds reference here would alias
				// the gas counter post-injection and defeat metering (ErrIndexOutOfBounds).
				gIdx, err := r.readULEB128()
				if err != nil {
					return nil, 0, err
				}
				if uint32(gIdx) >= totalGlobals {
					return nil, 0, ErrIndexOutOfBounds
				}
			case 0x28, 0x29, 0x2A, 0x2B, 0x2C, 0x2D, 0x2E, 0x2F,
				0x30, 0x31, 0x32, 0x33, 0x34, 0x35,
				0x36, 0x37, 0x38, 0x39, 0x3A, 0x3B, 0x3C, 0x3D, 0x3E:
				// memory load/store — all carry a memarg (align + offset). The
				// float variants (0x2A/0x2B/0x38/0x39) are already rejected by
				// isForbiddenOpcode above; they are listed here only so the
				// parser stays byte-aligned if that check is ever relaxed.
				r.readULEB128() // align
				r.readULEB128() // offset
			case 0x3F, 0x40: // memory.size/grow
				r.readByte()
			case 0x02, 0x03, 0x04: // block, loop, if
				r.readByte() // block type
			case 0x0C: // br
				r.readULEB128()
			case 0x0D: // br_if
				r.readULEB128()
			case 0x0E: // br_table
				targetCount, _ := r.readULEB128()
				for t := uint64(0); t < targetCount; t++ {
					r.readULEB128()
				}
				r.readULEB128() // default
			case 0xFC: // multi-byte prefix: only memory.copy/fill are allowed
				subOp, _ := r.readULEB128()
				switch subOp {
				case 10: // memory.copy — allowed (bounded, priced per-byte §9.2)
					r.readByte()
					r.readByte()
				case 11: // memory.fill — allowed
					r.readByte()
				default:
					// Forbidden: 0-7 saturating float→int truncation (float, §9.2),
					// 8 memory.init, 9 data.drop, 12 table.init, 13 elem.drop,
					// 14 table.copy, 15 table.grow, 16 table.size, 17 table.fill.
					return nil, 0, ErrForbiddenOpcode
				}
			}
		}

		funcs[i] = codeFunc{
			localCount:       localCount,
			localTypes:       localTypes,
			instructionCount: instructionCount,
		}
		complexitySum += uint64(instructionCount) * uint64(maxCallDepth+1)
		r.pos = bodyEnd
	}

	return funcs, complexitySum, nil
}

func parseDataSection(data []byte) (uint64, error) {
	r := &wasmReader{data: data, pos: 0}
	count, err := r.readULEB128()
	if err != nil {
		return 0, err
	}
	var totalBytes uint64
	for i := uint64(0); i < count; i++ {
		_, err := r.readULEB128() // memory index
		if err != nil {
			return 0, err
		}
		// Offset expression
		for {
			op, err := r.readByte()
			if err != nil {
				return 0, err
			}
			if op == 0x0B { // end
				break
			}
			switch op {
			case 0x41:
				r.readSLEB128()
			case 0x42:
				r.readSLEB128()
			}
		}
		size, err := r.readULEB128()
		if err != nil {
			return 0, err
		}
		totalBytes += size
		r.pos += int(size)
	}
	return totalBytes, nil
}

func (r *wasmReader) readSLEB128() (int64, error) {
	var result int64
	var shift uint
	for {
		b, err := r.readByte()
		if err != nil {
			return 0, err
		}
		result |= int64(b&0x7F) << shift
		shift += 7
		if b&0x80 == 0 {
			if shift < 64 && b&0x40 != 0 {
				result |= -(1 << shift)
			}
			return result, nil
		}
	}
}

// isForbiddenOpcode rejects every opcode banned by spec §9.2 (all float ops,
// SIMD, atomics/threads, exception handling, reference types) while preserving
// the integer-only conversions that share the 0xA7-0xC4 range.
func isForbiddenOpcode(op byte) bool {
	switch {
	case op == 0x2A || op == 0x2B: // f32.load, f64.load
		return true
	case op == 0x38 || op == 0x39: // f32.store, f64.store
		return true
	case op == 0x43 || op == 0x44: // f32.const, f64.const
		return true
	case op >= 0x5B && op <= 0x66: // float comparisons (f32: 0x5B-0x60, f64: 0x61-0x66)
		return true
	case op >= 0x8B && op <= 0xA6: // float arithmetic (f32: 0x8B-0x98, f64: 0x99-0xA6)
		return true
	case op >= 0xA8 && op <= 0xAB: // i32.trunc_f32/f64_s/u
		return true
	case op >= 0xAE && op <= 0xBF: // i64.trunc_f*, f*.convert/demote/promote, *.reinterpret
		return true
	case op == 0x06 || op == 0x07 || op == 0x08: // exception handling proposal (try/catch/throw)
		return true
	case op >= 0xD0 && op <= 0xD2: // reference types (ref.null, ref.is_null, ref.func)
		return true
	case op == 0xFD || op == 0xFE: // SIMD prefix / atomics+threads prefix
		return true
	}
	// Integer-only conversions remain allowed: 0xA7 (i32.wrap_i64),
	// 0xAC/0xAD (i64.extend_i32_s/u), 0xC0-0xC4 (sign-extension ops).
	return false
}


