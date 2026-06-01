package wasm

import (
	"bytes"
	"errors"
)

// GasGlobalName is the exported name of the injected gas-counter global.
// The runtime seeds it with the execution budget after instantiation; the
// injected per-opcode metering and the host-call listener both debit it, so it
// is the single authoritative gas counter during execution (spec §6.3).
const GasGlobalName = "__gas_remaining"

// ErrGasInjection is returned when the bytecode cannot be re-encoded after
// passing ValidateBytecode — indicates a parser error or unhandled edge case.
var ErrGasInjection = errors.New("gas injection: malformed wasm")

// Gas cost categories for WASM instructions.
const (
	gasBasic        = 1  // local.get/set, global.get/set, drop, select, nop
	gasConstant     = 1  // i32.const, i64.const
	gasArithmetic   = 3  // i32.add/sub/mul, i64.*, and/or/xor
	gasDivision     = 10 // i32.div/rem, i64.div/rem
	gasComparison   = 2  // eq, lt, gt, etc.
	gasLoad         = 3  // memory load
	gasStore        = 5  // memory store
	gasControl      = 2  // block, loop, if, br, br_if, return
	gasCall         = 5  // direct call
	gasCallIndirect = 20 // indirect call
)

// opcodeGas returns the gas cost for a given WASM opcode.
func opcodeGas(op byte) uint64 {
	switch op {
	// Constants
	case 0x41, 0x42: // i32.const, i64.const
		return gasConstant

	// Local/Global operations
	case 0x20, 0x21, 0x22, 0x23, 0x24: // local.get/set/tee, global.get/set
		return gasBasic

	// Control flow
	case 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x0C, 0x0D, 0x0E, 0x0F: // unreachable, nop, block, loop, if, else, br, br_if, br_table, return
		return gasControl

	// Calls
	case 0x10: // call
		return gasCall
	case 0x11: // call_indirect
		return gasCallIndirect

	// Drop, Select
	case 0x1A, 0x1B, 0x1C: // drop, select, select t
		return gasBasic

	// Memory operations
	case 0x28, 0x29, 0x2A, 0x2B, 0x2C, 0x2D, 0x2E, 0x2F, 0x30, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37:
		return gasLoad // load ops
	case 0x38, 0x39, 0x3A, 0x3B, 0x3C, 0x3D, 0x3E:
		return gasStore // store ops

	case 0x3F, 0x40: // memory.size, memory.grow
		return gasBasic

	// i32 arithmetic
	case 0x6A, 0x6B, 0x6C: // i32.add, i32.sub, i32.mul
		return gasArithmetic
	case 0x6D, 0x6E, 0x6F: // i32.div_s, i32.div_u, i32.rem_s
		return gasDivision
	case 0x70: // i32.rem_u
		return gasDivision
	case 0x71, 0x72, 0x73, 0x74, 0x75: // i32.and, i32.or, i32.xor, i32.shl, i32.shr_s
		return gasArithmetic
	case 0x76, 0x77, 0x78: // i32.rotl, i32.rotr
		return gasArithmetic

	// i32 comparison
	case 0x45, 0x46, 0x47, 0x48, 0x49, 0x4A, 0x4B, 0x4C, 0x4D, 0x4E: // i32.eqz, i32.eq, i32.ne, i32.lt_s, i32.lt_u, i32.gt_s, i32.gt_u, i32.le_s, i32.le_u, i32.ge_s
		return gasComparison
	case 0x4F: // i32.ge_u
		return gasComparison

	// i64 arithmetic
	case 0x7C, 0x7D, 0x7E: // i64.add, i64.sub, i64.mul
		return gasArithmetic
	case 0x7F, 0x80, 0x81: // i64.div_s, i64.div_u, i64.rem_s
		return gasDivision
	case 0x82: // i64.rem_u
		return gasDivision
	case 0x83, 0x84, 0x85, 0x86, 0x87: // i64.and, i64.or, i64.xor, i64.shl, i64.shr_s
		return gasArithmetic
	case 0x88, 0x89, 0x8A: // i64.shr_u, i64.rotl, i64.rotr
		return gasArithmetic

	// i64 comparison
	case 0x50, 0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59, 0x5A:
		return gasComparison

	// Type conversions (integer only)
	case 0xA7, 0xA8, 0xA9, 0xAA, 0xAB, 0xAC, 0xAD, 0xAE, 0xAF, 0xB0, 0xB1, 0xB2, 0xB3, 0xB4, 0xB5, 0xB6, 0xB7, 0xB8, 0xB9, 0xBA, 0xBB:
		return gasArithmetic

	default:
		return gasBasic
	}
}

// InjectGasMetering rewrites a validated WASM binary so that every straight-line
// basic block charges its summed per-opcode gas against an injected global
// counter before executing (spec §6.3). The input MUST have already passed
// ValidateBytecode.
//
// The transform is purely additive and deterministic: identical input bytes
// always yield identical output bytes on every node, which is required because
// the instrumented module is what executes on the consensus path.
//
//  1. A mutable, exported i64 global ($__gas_remaining, init 0) is appended. Its
//     index is higher than every existing global, so existing global.get/set
//     immediates are unaffected.
//  2. A defined helper $__consume_gas(i64) is appended at the END of the function
//     index space. Appending (never inserting) keeps every existing call /
//     export / element function index valid.
//  3. At each basic-block head — function entry, and immediately after every
//     block/loop/if/else/end — a stack-neutral `i64.const N; call $__consume_gas`
//     is inserted, where N is the summed opcodeGas of that block's straight-line
//     run. Charging at the loop header means every back-edge re-pays, which is
//     what bounds total work (and thus wall-clock) by the gas budget.
//
// The helper traps (unreachable) once the global would underflow, after zeroing
// it so the host can distinguish out-of-gas from an ordinary trap.
func InjectGasMetering(bytecode []byte) ([]byte, error) {
	if len(bytecode) < 8 {
		return nil, ErrGasInjection
	}

	secs, err := splitSections(bytecode[8:])
	if err != nil {
		return nil, err
	}

	// Index spaces: imports occupy the low indices of both the function and
	// global spaces, so appended entries must skip past them.
	importedFuncs, importedGlobals := 0, 0
	if s := findSection(secs, sectionImport); s != nil {
		importedFuncs, importedGlobals, err = countImports(s.payload)
		if err != nil {
			return nil, err
		}
	}

	typeSec := findSection(secs, sectionType)
	funcSec := findSection(secs, sectionFunction)
	exportSec := findSection(secs, sectionExport)
	codeSec := findSection(secs, sectionCode)
	if typeSec == nil || funcSec == nil || exportSec == nil || codeSec == nil {
		// A module that passed ValidateBytecode always has these.
		return nil, ErrGasInjection
	}

	typeCount, err := vecCount(typeSec.payload)
	if err != nil {
		return nil, err
	}
	definedFuncs, err := vecCount(funcSec.payload)
	if err != nil {
		return nil, err
	}
	definedGlobals := 0
	if s := findSection(secs, sectionGlobal); s != nil {
		if definedGlobals, err = vecCount(s.payload); err != nil {
			return nil, err
		}
	}

	helperTypeIdx := uint32(typeCount)
	helperFuncIdx := uint32(importedFuncs + definedFuncs)
	gasGlobalIdx := uint32(importedGlobals + definedGlobals)

	var out bytes.Buffer
	out.Write(bytecode[:8]) // magic + version (already validated)

	emit := func(id byte, payload []byte) {
		out.WriteByte(id)
		out.Write(encodeULEB128(uint64(len(payload))))
		out.Write(payload)
	}

	// Re-emit known sections in canonical id order, transforming the four we
	// extend. Anything else (import, table, memory, element, data) passes
	// through byte-for-byte.
	for id := byte(sectionType); id <= byte(sectionData); id++ {
		switch id {
		case sectionType:
			p, err := appendVecEntry(typeSec.payload, helperFuncType())
			if err != nil {
				return nil, err
			}
			emit(sectionType, p)

		case sectionGlobal:
			if s := findSection(secs, sectionGlobal); s != nil {
				p, err := appendVecEntry(s.payload, gasGlobalEntry())
				if err != nil {
					return nil, err
				}
				emit(sectionGlobal, p)
			} else {
				// No global section in the original module: synthesize one.
				p := append(encodeULEB128(1), gasGlobalEntry()...)
				emit(sectionGlobal, p)
			}

		case sectionFunction:
			p, err := appendVecEntry(funcSec.payload, encodeULEB128(uint64(helperTypeIdx)))
			if err != nil {
				return nil, err
			}
			emit(sectionFunction, p)

		case sectionExport:
			p, err := appendVecEntry(exportSec.payload, gasGlobalExport(gasGlobalIdx))
			if err != nil {
				return nil, err
			}
			emit(sectionExport, p)

		case sectionCode:
			p, err := rewriteCodeSection(codeSec.payload, helperFuncIdx, gasGlobalIdx)
			if err != nil {
				return nil, err
			}
			emit(sectionCode, p)

		case sectionStart:
			// Forbidden by the validator; never present.

		default:
			if s := findSection(secs, id); s != nil {
				emit(id, s.payload)
			}
		}
	}

	// Preserve custom sections (names, etc.) after the known sections; they
	// carry no execution semantics and may legally appear here.
	for i := range secs {
		if secs[i].id == sectionCustom {
			emit(sectionCustom, secs[i].payload)
		}
	}

	return out.Bytes(), nil
}

// --- section splitting / lookup ---

type rawSection struct {
	id      byte
	payload []byte
}

// splitSections parses the section sequence (everything after magic+version)
// into ordered raw sections without interpreting their contents.
func splitSections(data []byte) ([]rawSection, error) {
	r := &wasmReader{data: data, pos: 0}
	var secs []rawSection
	for r.hasMore() {
		id, err := r.readByte()
		if err != nil {
			return nil, err
		}
		size, err := r.readULEB128()
		if err != nil {
			return nil, err
		}
		payload, err := r.readBytes(int(size))
		if err != nil {
			return nil, err
		}
		secs = append(secs, rawSection{id: id, payload: payload})
	}
	return secs, nil
}

// findSection returns the first section with the given id, or nil. Known
// (non-custom) sections appear at most once in a valid module.
func findSection(secs []rawSection, id byte) *rawSection {
	for i := range secs {
		if secs[i].id == id {
			return &secs[i]
		}
	}
	return nil
}

// vecCount reads the leading LEB128 element count of a section payload.
func vecCount(payload []byte) (int, error) {
	r := &wasmReader{data: payload, pos: 0}
	n, err := r.readULEB128()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// appendVecEntry returns a copy of a vec-prefixed section payload with one extra
// entry appended and the leading count incremented by one.
func appendVecEntry(payload, entry []byte) ([]byte, error) {
	r := &wasmReader{data: payload, pos: 0}
	count, err := r.readULEB128()
	if err != nil {
		return nil, err
	}
	rest := payload[r.pos:]
	var b bytes.Buffer
	b.Write(encodeULEB128(count + 1))
	b.Write(rest)
	b.Write(entry)
	return b.Bytes(), nil
}

// countImports counts the imported functions and globals, which precede defined
// entries in their respective index spaces.
func countImports(payload []byte) (funcs int, globals int, err error) {
	r := &wasmReader{data: payload, pos: 0}
	count, err := r.readULEB128()
	if err != nil {
		return 0, 0, err
	}
	for i := uint64(0); i < count; i++ {
		modLen, err := r.readULEB128()
		if err != nil {
			return 0, 0, err
		}
		if _, err := r.readBytes(int(modLen)); err != nil {
			return 0, 0, err
		}
		nameLen, err := r.readULEB128()
		if err != nil {
			return 0, 0, err
		}
		if _, err := r.readBytes(int(nameLen)); err != nil {
			return 0, 0, err
		}
		kind, err := r.readByte()
		if err != nil {
			return 0, 0, err
		}
		switch kind {
		case importFunc:
			funcs++
			if _, err := r.readULEB128(); err != nil { // type index
				return 0, 0, err
			}
		case importTable:
			if _, err := r.readByte(); err != nil { // element type
				return 0, 0, err
			}
			flag, err := r.readULEB128()
			if err != nil {
				return 0, 0, err
			}
			if _, err := r.readULEB128(); err != nil { // min
				return 0, 0, err
			}
			if flag == 1 {
				if _, err := r.readULEB128(); err != nil { // max
					return 0, 0, err
				}
			}
		case importMem:
			flag, err := r.readULEB128()
			if err != nil {
				return 0, 0, err
			}
			if _, err := r.readULEB128(); err != nil { // min
				return 0, 0, err
			}
			if flag == 1 {
				if _, err := r.readULEB128(); err != nil { // max
					return 0, 0, err
				}
			}
		case importGlobal:
			globals++
			if _, err := r.readByte(); err != nil { // value type
				return 0, 0, err
			}
			if _, err := r.readByte(); err != nil { // mutability
				return 0, 0, err
			}
		default:
			return 0, 0, ErrGasInjection
		}
	}
	return funcs, globals, nil
}

// --- code section rewriting ---

// rewriteCodeSection instruments every existing function body and appends the
// $__consume_gas helper body.
func rewriteCodeSection(payload []byte, helperFuncIdx, gasGlobalIdx uint32) ([]byte, error) {
	r := &wasmReader{data: payload, pos: 0}
	count, err := r.readULEB128()
	if err != nil {
		return nil, err
	}

	bodies := make([][]byte, 0, count+1)
	for i := uint64(0); i < count; i++ {
		size, err := r.readULEB128()
		if err != nil {
			return nil, err
		}
		body, err := r.readBytes(int(size))
		if err != nil {
			return nil, err
		}
		newBody, err := rewriteFuncBody(body, helperFuncIdx)
		if err != nil {
			return nil, err
		}
		bodies = append(bodies, newBody)
	}
	bodies = append(bodies, helperBody(gasGlobalIdx))

	var b bytes.Buffer
	b.Write(encodeULEB128(count + 1))
	for _, body := range bodies {
		b.Write(encodeULEB128(uint64(len(body))))
		b.Write(body)
	}
	return b.Bytes(), nil
}

// rewriteFuncBody splits a function body into its locals declaration (kept
// verbatim) and its instruction stream (instrumented).
func rewriteFuncBody(body []byte, helperFuncIdx uint32) ([]byte, error) {
	r := &wasmReader{data: body, pos: 0}
	localDeclCount, err := r.readULEB128()
	if err != nil {
		return nil, err
	}
	for j := uint64(0); j < localDeclCount; j++ {
		if _, err := r.readULEB128(); err != nil { // run length
			return nil, err
		}
		if _, err := r.readByte(); err != nil { // value type
			return nil, err
		}
	}
	localsEnd := r.pos
	newInstrs, err := injectIntoInstrs(body[localsEnd:], helperFuncIdx)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, localsEnd+len(newInstrs))
	out = append(out, body[:localsEnd]...)
	out = append(out, newInstrs...)
	return out, nil
}

// injectIntoInstrs walks an instruction stream and inserts a gas charge at the
// head of each straight-line basic block. A block runs up to and including the
// next block-boundary opcode (block/loop/if/else/end); the charge for that
// block is emitted before its first instruction, so control can never execute
// an opcode it has not paid for, and every loop back-edge re-pays.
func injectIntoInstrs(instrs []byte, helperFuncIdx uint32) ([]byte, error) {
	var out bytes.Buffer
	var seg bytes.Buffer
	var segGas uint64

	flush := func() {
		if seg.Len() == 0 {
			return
		}
		if segGas > 0 {
			out.WriteByte(0x42) // i64.const
			out.Write(encodeSLEB128(int64(segGas)))
			out.WriteByte(0x10) // call
			out.Write(encodeULEB128(uint64(helperFuncIdx)))
		}
		out.Write(seg.Bytes())
		seg.Reset()
		segGas = 0
	}

	r := &wasmReader{data: instrs, pos: 0}
	for r.hasMore() {
		start := r.pos
		op, err := r.readByte()
		if err != nil {
			return nil, err
		}
		if err := skipImmediates(r, op); err != nil {
			return nil, err
		}
		seg.Write(instrs[start:r.pos])
		segGas += opcodeGas(op)
		if isBlockBoundary(op) {
			flush()
		}
	}
	flush()
	return out.Bytes(), nil
}

// isBlockBoundary reports whether an opcode ends a straight-line basic block.
// block/loop/if open a new scope; else/end transition between scopes. The
// instruction following any of these is a fresh basic-block head.
func isBlockBoundary(op byte) bool {
	switch op {
	case 0x02, 0x03, 0x04, 0x05, 0x0B: // block, loop, if, else, end
		return true
	}
	return false
}

// skipImmediates advances the reader past the immediate operands of op. It must
// match the WASM binary encoding exactly for every opcode the validator admits;
// a wrong length silently corrupts the re-encoded body.
func skipImmediates(r *wasmReader, op byte) error {
	switch op {
	case 0x02, 0x03, 0x04: // block, loop, if — blocktype is an s33 (may be multi-byte)
		_, err := r.readSLEB128()
		return err
	case 0x0C, 0x0D: // br, br_if — label index
		_, err := r.readULEB128()
		return err
	case 0x0E: // br_table — vec(label) + default label
		n, err := r.readULEB128()
		if err != nil {
			return err
		}
		for i := uint64(0); i <= n; i++ {
			if _, err := r.readULEB128(); err != nil {
				return err
			}
		}
		return nil
	case 0x10: // call — function index
		_, err := r.readULEB128()
		return err
	case 0x11: // call_indirect — type index + table index
		if _, err := r.readULEB128(); err != nil {
			return err
		}
		_, err := r.readByte()
		return err
	case 0x20, 0x21, 0x22, 0x23, 0x24: // local.get/set/tee, global.get/set
		_, err := r.readULEB128()
		return err
	case 0x1C: // typed select — vec(valtype)
		n, err := r.readULEB128()
		if err != nil {
			return err
		}
		for i := uint64(0); i < n; i++ {
			if _, err := r.readByte(); err != nil {
				return err
			}
		}
		return nil
	case 0x28, 0x29, 0x2A, 0x2B, 0x2C, 0x2D, 0x2E, 0x2F,
		0x30, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37,
		0x38, 0x39, 0x3A, 0x3B, 0x3C, 0x3D, 0x3E: // memory load/store — memarg
		if _, err := r.readULEB128(); err != nil { // align
			return err
		}
		_, err := r.readULEB128() // offset
		return err
	case 0x3F, 0x40: // memory.size, memory.grow — reserved byte
		_, err := r.readByte()
		return err
	case 0x41, 0x42: // i32.const, i64.const
		_, err := r.readSLEB128()
		return err
	case 0x43: // f32.const
		_, err := r.readBytes(4)
		return err
	case 0x44: // f64.const
		_, err := r.readBytes(8)
		return err
	case 0xFC: // misc prefix — only memory.copy/fill admitted by the validator
		sub, err := r.readULEB128()
		if err != nil {
			return err
		}
		switch sub {
		case 10: // memory.copy — two reserved bytes
			if _, err := r.readByte(); err != nil {
				return err
			}
			_, err := r.readByte()
			return err
		case 11: // memory.fill — one reserved byte
			_, err := r.readByte()
			return err
		default:
			return ErrGasInjection
		}
	default:
		return nil // opcode has no immediates
	}
}

// --- injected entity encoders ---

// helperFuncType is the (i64) -> () function type for $__consume_gas.
func helperFuncType() []byte {
	return []byte{0x60, 0x01, valTypeI64, 0x00}
}

// gasGlobalEntry is a mutable i64 global initialized to 0 (i64.const 0; end).
func gasGlobalEntry() []byte {
	return []byte{valTypeI64, 0x01, 0x42, 0x00, 0x0B}
}

// gasGlobalExport exports the gas global under GasGlobalName.
func gasGlobalExport(globalIdx uint32) []byte {
	name := []byte(GasGlobalName)
	var b bytes.Buffer
	b.Write(encodeULEB128(uint64(len(name))))
	b.Write(name)
	b.WriteByte(exportGlobal)
	b.Write(encodeULEB128(uint64(globalIdx)))
	return b.Bytes()
}

// helperBody builds the $__consume_gas(cost i64) function body:
//
//	if remaining < cost { remaining = 0; unreachable }
//	remaining -= cost
//
// Zeroing before the trap lets the host read remaining==0 as the out-of-gas
// signal, distinct from an ordinary guest trap.
func helperBody(gasGlobalIdx uint32) []byte {
	g := encodeULEB128(uint64(gasGlobalIdx))
	var b bytes.Buffer
	b.WriteByte(0x00) // no local declarations

	b.WriteByte(0x23) // global.get gas
	b.Write(g)
	b.WriteByte(0x20) // local.get 0 (cost)
	b.WriteByte(0x00)
	b.WriteByte(0x54) // i64.lt_u
	b.WriteByte(0x04) // if
	b.WriteByte(0x40) // (empty block type)
	b.WriteByte(0x42) // i64.const 0
	b.WriteByte(0x00)
	b.WriteByte(0x24) // global.set gas
	b.Write(g)
	b.WriteByte(0x00) // unreachable
	b.WriteByte(0x0B) // end (if)

	b.WriteByte(0x23) // global.get gas
	b.Write(g)
	b.WriteByte(0x20) // local.get 0 (cost)
	b.WriteByte(0x00)
	b.WriteByte(0x7D) // i64.sub
	b.WriteByte(0x24) // global.set gas
	b.Write(g)

	b.WriteByte(0x0B) // end (function)
	return b.Bytes()
}

// --- LEB128 encoders ---

// encodeULEB128 encodes a uint64 as unsigned LEB128 (canonical/minimal).
func encodeULEB128(value uint64) []byte {
	if value == 0 {
		return []byte{0}
	}
	var result []byte
	for value > 0 {
		b := byte(value & 0x7F)
		value >>= 7
		if value > 0 {
			b |= 0x80
		}
		result = append(result, b)
	}
	return result
}

// encodeSLEB128 encodes an int64 as signed LEB128 (canonical/minimal). Used for
// i64.const immediates; gas charges are non-negative but the encoding must still
// emit the sign-disambiguating trailing byte when bit 6 of the final group is
// set (e.g. 64 -> 0xC0 0x00, not the negative 0x40).
func encodeSLEB128(value int64) []byte {
	var result []byte
	for {
		b := byte(value & 0x7F)
		value >>= 7 // arithmetic shift preserves sign
		signBitSet := b&0x40 != 0
		if (value == 0 && !signBitSet) || (value == -1 && signBitSet) {
			result = append(result, b)
			return result
		}
		result = append(result, b|0x80)
	}
}
