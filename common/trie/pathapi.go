package trie

import (
	"errors"

	"github.com/zenon-network/go-zenon/common/types"
)

// Path is the 32-byte SMT position, a distinct type from types.Hash requiring an explicit
// conversion at the L1/L2 boundary. The L2 derived key is a Path directly; callers of this API
// supply the path and the core never hashes it. (SPEC §33.1)
type Path types.Hash

// ErrDuplicatePath is returned when a leaf set built from parallel paths/values contains the
// same path more than once.
var ErrDuplicatePath = errors.New("trie: duplicate path in leaf set")

// leavesFromPaths zips parallel paths/values into the internal leaf form. Caller guarantees
// len(paths) == len(values); panics otherwise (programmer error, not a runtime condition).
// Returns ErrDuplicatePath if paths contains the same path more than once.
func leavesFromPaths(paths []Path, values [][]byte) ([]leaf, error) {
	if len(paths) != len(values) {
		panic("trie: paths and values length mismatch")
	}
	seen := make(map[Path]struct{}, len(paths))
	ls := make([]leaf, len(paths))
	for i := range paths {
		if _, dup := seen[paths[i]]; dup {
			return nil, ErrDuplicatePath
		}
		seen[paths[i]] = struct{}{}
		ls[i] = leaf{path: types.Hash(paths[i]), value: values[i]}
	}
	return ls, nil
}

// RootOfLeaves computes the full-depth-256 SMT root over a leaf set given as parallel slices
// (paths[i] holds values[i]). Empty set -> zero hash. No key hashing, no DB. (SPEC §33.1)
func RootOfLeaves(paths []Path, values [][]byte) (types.Hash, error) {
	ls, err := leavesFromPaths(paths, values)
	if err != nil {
		return types.Hash{}, err
	}
	return rootOfLeaves(ls), nil
}

// ProveByPath returns presence, the value if present, and the canonical proof bytes
// (proof-format.md) for targetPath over the leaf set. No key hashing, no DB. (SPEC §33.1)
func ProveByPath(paths []Path, values [][]byte, targetPath Path) (present bool, value []byte, proof []byte, err error) {
	ls, err := leavesFromPaths(paths, values)
	if err != nil {
		return false, nil, nil, err
	}
	sortLeaves(ls)
	present, value, sib := proveLeaves(ls, types.Hash(targetPath))
	return present, value, encodeProof(present, value, types.Hash(targetPath), sib), nil
}
