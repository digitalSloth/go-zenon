package trie

import (
	"encoding/binary"
	"errors"
	"math/bits"

	"github.com/zenon-network/go-zenon/common/types"
)

// Proof format (proof-format.md §4), deepest-first compressed-sparse with a bitmap:
//
//	flags        : u8       // bit0 = present (1) / absent (0); bits1..7 MUST be 0
//	key          : 32 bytes // the path (sha3(key))
//	leaf_hash    : 32 bytes // SHA3-256(path||value) if present; 32 zero bytes if absent
//	sibling_bitmap : 32 bytes  // 256 bits, deepest-first, MSB-first within each byte
//	sib_count    : u16      // number of non-zero siblings == popcount(sibling_bitmap)
//	siblings     : sib_count * 32 bytes  // non-zero siblings, deepest-first (ascending i)
//	// present-only tail:
//	value_len    : u32      // present iff flags.bit0 == 1
//	value        : value_len bytes  // present iff flags.bit0 == 1
//
// Deepest-first index i runs 0..255 where i ↔ depth (255 - i).
// Bit i is set iff sib[255-i] != zero32.
// Bit i is stored MSB-first within the 32-byte bitmap: byte i/8, bit 7-(i mod 8).
//
// The verifier is pure: it depends only on common/crypto + common/types and never touches a
// database, so it can run in a light client or a future on-chain dispute referee (§6.1).
const (
	flagInclusion = 0x01

	bitmapBytes   = treeDepth / 8                                         // 32
	proofMinBytes = 1 + types.HashSize + types.HashSize + bitmapBytes + 2 // 99
)

var (
	ErrProofMalformed    = errors.New("trie: malformed proof")
	ErrProofPathMismatch = errors.New("trie: proof path does not match key")
)

func bitmapGet(bm []byte, i int) bool {
	return bm[i>>3]&(1<<(7-uint(i&7))) != 0
}

func bitmapSet(bm []byte, i int) {
	bm[i>>3] |= 1 << (7 - uint(i&7))
}

// encodeProof serializes a proof produced by proveLeaves for `targetPath`.
// Siblings are emitted deepest-first: index i=0..255 corresponds to depth=255-i.
// Bit i of the bitmap is set iff sib[255-i] != emptyHash.
func encodeProof(present bool, value []byte, targetPath types.Hash, sib [treeDepth]types.Hash) []byte {
	// Build bitmap and collect non-zero siblings deepest-first (i=0..255, depth=255-i).
	var bitmap [bitmapBytes]byte
	siblings := make([]types.Hash, 0, treeDepth)
	for i := 0; i < treeDepth; i++ {
		depth := treeDepth - 1 - i
		if sib[depth] != emptyHash {
			bitmapSet(bitmap[:], i)
			siblings = append(siblings, sib[depth])
		}
	}

	// Compute leaf_hash.
	var leafHash types.Hash
	if present {
		leafHash = LeafHash(targetPath, value)
	}
	// absent: leafHash stays zero32

	// Size: flags(1) + key(32) + leaf_hash(32) + bitmap(32) + sib_count(2) + siblings + [value_len(4) + value]
	size := proofMinBytes + len(siblings)*types.HashSize
	if present {
		size += 4 + len(value)
	}
	out := make([]byte, 0, size)

	// flags
	if present {
		out = append(out, flagInclusion)
	} else {
		out = append(out, 0x00)
	}
	// key (path)
	out = append(out, targetPath[:]...)
	// leaf_hash
	out = append(out, leafHash[:]...)
	// sibling_bitmap
	out = append(out, bitmap[:]...)
	// sib_count
	out = binary.BigEndian.AppendUint16(out, uint16(len(siblings)))
	// siblings
	for i := range siblings {
		out = append(out, siblings[i][:]...)
	}
	// present-only tail
	if present {
		out = binary.BigEndian.AppendUint32(out, uint32(len(value)))
		out = append(out, value...)
	}
	return out
}

// decodedProof is the parsed, validated wire form of a proof.
type decodedProof struct {
	inclusion bool
	path      types.Hash
	leafHash  types.Hash         // inclusion: LeafHash(path, value); absent: zero
	value     []byte             // inclusion only; nil for absence
	sibByLvl  map[int]types.Hash // keyed by depth (0..255)
}

func decodeProof(proof []byte) (*decodedProof, error) {
	if len(proof) < proofMinBytes {
		return nil, ErrProofMalformed
	}
	off := 0

	// flags: bits 1..7 MUST be zero.
	flags := proof[off]
	off++
	if flags & ^byte(flagInclusion) != 0 {
		return nil, ErrProofMalformed
	}
	inclusion := flags&flagInclusion != 0

	d := &decodedProof{inclusion: inclusion, sibByLvl: map[int]types.Hash{}}

	// key (path)
	copy(d.path[:], proof[off:off+types.HashSize])
	off += types.HashSize

	// leaf_hash
	copy(d.leafHash[:], proof[off:off+types.HashSize])
	off += types.HashSize

	// sibling_bitmap
	bitmap := proof[off : off+bitmapBytes]
	off += bitmapBytes

	// sib_count
	sibCount := int(binary.BigEndian.Uint16(proof[off : off+2]))
	off += 2

	// sib_count MUST equal popcount(bitmap).
	popcount := 0
	for _, b := range bitmap {
		popcount += bits.OnesCount8(b)
	}
	if popcount != sibCount {
		return nil, ErrProofMalformed
	}

	// For an absent proof, leaf_hash MUST be zero32.
	if !inclusion && d.leafHash != emptyHash {
		return nil, ErrProofMalformed
	}

	// Validate total length now that we know sibCount.
	// Absent: exactly proofMinBytes + sibCount*32.
	// Present: exactly proofMinBytes + sibCount*32 + 4 + value_len (checked after reading value_len).
	sibBytes := sibCount * types.HashSize
	if !inclusion {
		if len(proof) != proofMinBytes+sibBytes {
			return nil, ErrProofMalformed
		}
	} else {
		// Present: need at least proofMinBytes + sibBytes + 4 for value_len field.
		if len(proof) < proofMinBytes+sibBytes+4 {
			return nil, ErrProofMalformed
		}
	}

	// Read siblings: walk bitmap deepest-first (i=0..255, depth=255-i).
	// The n-th set bit corresponds to the n-th stored sibling (ascending i order).
	sibIdx := 0
	for i := 0; i < treeDepth; i++ {
		if bitmapGet(bitmap, i) {
			depth := treeDepth - 1 - i
			var h types.Hash
			base := off + sibIdx*types.HashSize
			copy(h[:], proof[base:base+types.HashSize])
			d.sibByLvl[depth] = h
			sibIdx++
		}
	}
	off += sibBytes

	// Present-only tail.
	if inclusion {
		valueLen := int(binary.BigEndian.Uint32(proof[off : off+4]))
		off += 4
		// Exact total length check (no trailing bytes).
		if len(proof) != off+valueLen {
			return nil, ErrProofMalformed
		}
		d.value = make([]byte, valueLen)
		copy(d.value, proof[off:off+valueLen])
		off += valueLen

		// Verify leaf_hash == LeafHash(path, value) — proof-format.md §4 encoding rule.
		if LeafHash(d.path, d.value) != d.leafHash {
			return nil, ErrProofMalformed
		}
	}

	// Satisfies proof-format.md §5 step 5: sibIdx == sibCount guarantees all stored siblings
	// were consumed (bitmap popcount == sib_count == siblings read). No unused siblings possible.
	_ = off
	return d, nil
}

// reconstructRoot folds `start` (the leaf hash for inclusion, or emptyHash for absence) up
// the path using the proven sibling at each depth or emptyHash where absent.
// Walks deepest-first (depth 255 down to 0), consuming stored siblings in ascending i order
// as required by proof-format.md §5.
func (d *decodedProof) reconstructRoot(start types.Hash) types.Hash {
	cur := start
	for l := treeDepth - 1; l >= 0; l-- {
		s, ok := d.sibByLvl[l]
		if !ok {
			s = emptyHash
		}
		if pathBit(d.path, l) == 0 {
			cur = InternalHash(cur, s)
		} else {
			cur = InternalHash(s, cur)
		}
	}
	return cur
}

// VerifyProofByPath reconstructs the root from (path, value, proof) and reports whether it
// matches `root`. `path` is the 32-byte SMT position directly (no key hashing). Used by the
// L2 executor and the Phase 2 dispute referee, whose keys are already SHA3 outputs. Pure; no DB.
func VerifyProofByPath(root types.Hash, path Path, value, proof []byte) (bool, error) {
	d, err := decodeProof(proof)
	if err != nil {
		return false, err
	}
	if !d.inclusion {
		return false, ErrProofMalformed
	}
	if types.Hash(path) != d.path {
		return false, ErrProofPathMismatch
	}
	if LeafHash(d.path, value) != d.leafHash {
		return false, nil
	}
	return d.reconstructRoot(d.leafHash) == root, nil
}

// VerifyAbsenceByPath reconstructs the root assuming `path` maps to an empty leaf and reports
// whether it matches `root`. `path` is the 32-byte SMT position directly (no key hashing). Pure.
func VerifyAbsenceByPath(root types.Hash, path Path, proof []byte) (bool, error) {
	d, err := decodeProof(proof)
	if err != nil {
		return false, err
	}
	if d.inclusion {
		return false, ErrProofMalformed
	}
	if types.Hash(path) != d.path {
		return false, ErrProofPathMismatch
	}
	return d.reconstructRoot(emptyHash) == root, nil
}

// VerifyProof verifies a proof for a raw application key (sha3 is applied internally to derive
// the path). Behaviour unchanged; thin wrapper over VerifyProofByPath.
func VerifyProof(root types.Hash, key, value, proof []byte) (bool, error) {
	return VerifyProofByPath(root, Path(types.NewHash(key)), value, proof)
}

// VerifyAbsence verifies an absence proof for a raw application key (sha3 applied internally).
// Behaviour unchanged; thin wrapper over VerifyAbsenceByPath.
func VerifyAbsence(root types.Hash, key, proof []byte) (bool, error) {
	return VerifyAbsenceByPath(root, Path(types.NewHash(key)), proof)
}
