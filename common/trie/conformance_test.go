package trie

// Conformance test against smt-v1-test-vectors.json (SMT-001 … SMT-014).
//
// KEY DISTINCTION: vector "key" fields are already the 32-byte PATH (tree position),
// NOT raw application keys. The public VerifyProof/VerifyAbsence functions internally
// compute sha3(rawKey) to derive the path, which would double-hash the vector keys.
// Therefore proof verification here goes through the PURE layer: decodeProof +
// reconstructRoot, bypassing the sha3-of-key step. This is correct per spec §7.
// The VerifyProof/VerifyAbsence sha3-of-key path is tested separately in proof_test.go.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/zenon-network/go-zenon/common/crypto"
	"github.com/zenon-network/go-zenon/common/types"
)

// ---------- JSON schema ----------

type smtVectorFile struct {
	SentinelConstants struct {
		SHA3EmptyHex string `json:"sha3_256_empty_input_hex"`
	} `json:"sentinel_constants"`
	Vectors []smtVector `json:"vectors"`
}

type smtVector struct {
	ID             string            `json:"id"`
	ResultingState map[string]string `json:"resulting_state"` // hex path -> hex value ("" = empty-present)
	ExpectedRoot   string            `json:"expected_root"`
	ExpLeafHashes  map[string]string `json:"expected_leaf_hashes"`
	InclusionProof *smtProofVector   `json:"expected_inclusion_proof"`
	NonIncProof    *smtProofVector   `json:"expected_non_inclusion_proof"`
	AssertDistinct bool              `json:"assert_distinct"`
	RootAbsent     string            `json:"root_absent"`
	RootPresentEmp string            `json:"root_present_empty"`
	Steps          []smtStep         `json:"steps"`
	Operations     []smtOp           `json:"operations"`
	DiffSteps      []cvApplyStep     `json:"diff_steps"` // CV-APPLY-2
}

// cvApplyStep is one step of a CV-APPLY-2-style vector: a StateDiff (possibly several
// entries applied together) plus the state and root expected after it.
type cvApplyStep struct {
	Diff            []cvApplyDiffEntry `json:"diff"`
	ResultingState  map[string]string  `json:"resulting_state"`
	IncrementalRoot string             `json:"incremental_root"`
}

// cvApplyDiffEntry mirrors the wire-level StateDiff entry (SPEC §27.2): NewValue "EMPTY" is
// the 0xFFFFFFFF delete sentinel; any other value (including "") is a present write.
type cvApplyDiffEntry struct {
	Key      string `json:"key"`
	NewValue string `json:"new_value"`
}

type smtProofVector struct {
	Key        string  `json:"key"`
	Present    bool    `json:"present"`
	Value      *string `json:"value"` // nil => absent; "" => present-empty
	Serialized string  `json:"serialized"`
}

type smtStep struct {
	AfterKey        string `json:"after_insert_key"`
	IncrementalRoot string `json:"incremental_root"`
}

type smtOp struct {
	Op    string `json:"op"`
	Key   string `json:"key"`
	Value string `json:"value"` // empty string for absent in delete ops
}

// ---------- helpers ----------

func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func mustHash(t *testing.T, hexStr string) types.Hash {
	t.Helper()
	b := mustDecodeHex(t, hexStr)
	if len(b) != types.HashSize {
		t.Fatalf("expected 32-byte hash, got %d bytes from %q", len(b), hexStr)
	}
	var h types.Hash
	copy(h[:], b)
	return h
}

// stateToLeaves converts a resulting_state map (hex path -> hex value) into a []leaf.
// An empty hex value string means present-with-empty-value ([]byte{}).
func stateToLeaves(t *testing.T, state map[string]string) []leaf {
	t.Helper()
	leaves := make([]leaf, 0, len(state))
	for pathHex, valHex := range state {
		path := mustHash(t, pathHex)
		var value []byte
		if valHex == "" {
			value = []byte{} // present-empty
		} else {
			value = mustDecodeHex(t, valHex)
		}
		leaves = append(leaves, leaf{path: path, value: value})
	}
	return leaves
}

// buildProofForPath computes a proof for targetPath over the given leaf set.
func buildProofForPath(leaves []leaf, targetPath types.Hash) (present bool, value []byte, encoded []byte) {
	sortedLeaves := make([]leaf, len(leaves))
	copy(sortedLeaves, leaves)
	sortLeaves(sortedLeaves)
	present, value, sib := proveLeaves(sortedLeaves, targetPath)
	encoded = encodeProof(present, value, targetPath, sib)
	return present, value, encoded
}

// verifyProofPure decodes and reconstructs the root without re-hashing the path.
// Returns the reconstructed root.
func verifyProofPure(t *testing.T, proofBytes []byte, expectedRoot types.Hash, id string) {
	t.Helper()
	d, err := decodeProof(proofBytes)
	if err != nil {
		t.Errorf("%s: decodeProof failed: %v", id, err)
		return
	}
	var start types.Hash
	if d.inclusion {
		start = d.leafHash
	}
	// start is emptyHash (zero) for absence — reconstructRoot uses emptyHash for absence
	reconstructed := d.reconstructRoot(start)
	if reconstructed != expectedRoot {
		t.Errorf("%s: reconstructRoot = %x, want %x", id, reconstructed, expectedRoot)
	}
}

// ---------- main conformance test ----------

func TestConformance(t *testing.T) {
	vectorPath := "testdata/smt-v1-test-vectors.json"
	data, err := os.ReadFile(vectorPath)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var vf smtVectorFile
	if err := json.Unmarshal(data, &vf); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}

	// Sanity: SHA3-256("") must match the pinned constant in the vectors file.
	t.Run("SHA3Sanity", func(t *testing.T) {
		got := hex.EncodeToString(crypto.Hash())
		want := vf.SentinelConstants.SHA3EmptyHex
		if got != want {
			t.Fatalf("SHA3-256(\"\") = %s, want %s (Keccak vs SHA3 mismatch?)", got, want)
		}
	})

	for _, vec := range vf.Vectors {
		vec := vec // capture
		if vec.ExpectedRoot == "" {
			// CV-PATH-2 and CV-APPLY-2 carry no independent leaf data of their own; they are
			// covered by their own dedicated subtests below instead of this generic body.
			continue
		}
		t.Run(vec.ID, func(t *testing.T) {
			leaves := stateToLeaves(t, vec.ResultingState)
			expectedRoot := mustHash(t, vec.ExpectedRoot)

			// 1. Root computation.
			gotRoot := rootOfLeaves(leaves)
			if gotRoot != expectedRoot {
				t.Errorf("rootOfLeaves = %x\n  want       %x", gotRoot, expectedRoot)
			}

			// 2. Leaf hashes.
			for pathHex, lhHex := range vec.ExpLeafHashes {
				path := mustHash(t, pathHex)
				wantLH := mustHash(t, lhHex)
				// Find the leaf value from resulting_state.
				valHex, ok := vec.ResultingState[pathHex]
				if !ok {
					t.Errorf("leaf hash vector key %s not in resulting_state", pathHex)
					continue
				}
				var value []byte
				if valHex != "" {
					value = mustDecodeHex(t, valHex)
				}
				gotLH := LeafHash(path, value)
				if gotLH != wantLH {
					t.Errorf("LeafHash(%s) = %x\n  want       %x", pathHex, gotLH, wantLH)
				}
			}

			// 3. Inclusion proof (byte-for-byte + pure verify).
			if p := vec.InclusionProof; p != nil {
				targetPath := mustHash(t, p.Key)
				_, _, encoded := buildProofForPath(leaves, targetPath)

				wantBytes := mustDecodeHex(t, p.Serialized)
				if string(encoded) != string(wantBytes) {
					t.Errorf("inclusion proof bytes mismatch for %s/%s:\n  got  %x\n  want %x",
						vec.ID, p.Key, encoded, wantBytes)
				} else {
					// Only verify if bytes are correct (avoid double-reporting).
					verifyProofPure(t, encoded, expectedRoot, fmt.Sprintf("%s/inclusion", vec.ID))
				}
			}

			// CV-PATH-1: the path-native verifier accepts the vector key as a path; the
			// key-hashing verifier rejects the identical bytes used as a raw key.
			if vec.ID == "CV-PATH-1" && vec.InclusionProof != nil {
				p := vec.InclusionProof
				targetPath := mustHash(t, p.Key)
				var value []byte
				if p.Value != nil && *p.Value != "" {
					value = mustDecodeHex(t, *p.Value)
				}
				_, _, encoded := buildProofForPath(leaves, targetPath)

				okPath, errPath := VerifyProofByPath(expectedRoot, Path(targetPath), value, encoded)
				if errPath != nil || !okPath {
					t.Errorf("CV-PATH-1: VerifyProofByPath(path) ok=%v err=%v, want accepted", okPath, errPath)
				}

				okKey, errKey := VerifyProof(expectedRoot, targetPath[:], value, encoded)
				if errKey == nil && okKey {
					t.Errorf("CV-PATH-1: VerifyProof(path-as-key) accepted, want rejected (double-hash)")
				}
			}

			// 4. Non-inclusion proof (byte-for-byte + pure verify).
			if p := vec.NonIncProof; p != nil {
				targetPath := mustHash(t, p.Key)
				_, _, encoded := buildProofForPath(leaves, targetPath)

				wantBytes := mustDecodeHex(t, p.Serialized)
				if string(encoded) != string(wantBytes) {
					t.Errorf("non-inclusion proof bytes mismatch for %s/%s:\n  got  %x\n  want %x",
						vec.ID, p.Key, encoded, wantBytes)
				} else {
					verifyProofPure(t, encoded, expectedRoot, fmt.Sprintf("%s/non-inclusion", vec.ID))
				}
			}

			// 5. SMT-010: present-empty root must differ from absent root.
			if vec.AssertDistinct {
				rootAbsent := mustHash(t, vec.RootAbsent)
				rootPresEmp := mustHash(t, vec.RootPresentEmp)
				if rootAbsent == rootPresEmp {
					t.Errorf("SMT-010: present-empty root == absent root, expected distinct")
				}
				// The computed root should equal root_present_empty.
				if gotRoot != rootPresEmp {
					t.Errorf("SMT-010: computed root = %x, want root_present_empty = %x",
						gotRoot, rootPresEmp)
				}
			}

			// 6. SMT-014: incremental == full recompute at each step.
			if len(vec.Steps) > 0 {
				acc := map[string]string{}
				for i, step := range vec.Steps {
					// Find the operation for this step by index.
					op := vec.Operations[i]
					acc[op.Key] = op.Value

					// Full recompute from current accumulated state.
					accumulated := stateToLeaves(t, acc)
					fullRoot := rootOfLeaves(accumulated)

					wantRoot := mustHash(t, step.IncrementalRoot)
					if fullRoot != wantRoot {
						t.Errorf("SMT-014 step %d (after %s): full recompute = %x, want %x",
							i+1, step.AfterKey, fullRoot, wantRoot)
					}
				}
			}
		})
	}

	// CV-PATH-2: the exported path-native API reproduces every SMT-* expected_root, not just
	// the internal rootOfLeaves already exercised above.
	t.Run("CV-PATH-2", func(t *testing.T) {
		for _, vec := range vf.Vectors {
			if !strings.HasPrefix(vec.ID, "SMT-") {
				continue
			}
			paths := make([]Path, 0, len(vec.ResultingState))
			values := make([][]byte, 0, len(vec.ResultingState))
			for pathHex, valHex := range vec.ResultingState {
				paths = append(paths, Path(mustHash(t, pathHex)))
				var value []byte
				if valHex != "" {
					value = mustDecodeHex(t, valHex)
				}
				values = append(values, value)
			}
			gotRoot, err := RootOfLeaves(paths, values)
			if err != nil {
				t.Errorf("%s: RootOfLeaves: %v", vec.ID, err)
				continue
			}
			wantRoot := mustHash(t, vec.ExpectedRoot)
			if gotRoot != wantRoot {
				t.Errorf("%s: RootOfLeaves = %x, want %x", vec.ID, gotRoot, wantRoot)
			}
		}
	})

	// CV-APPLY-2: incremental application equals full recompute at every step, for a
	// sequence that (unlike SMT-014) batches multiple entries per StateDiff and mixes an
	// EMPTY delete with a present-empty write within one diff. This is an independent,
	// L1-side mini-applier gating the same vectors the executor's own applier is tested
	// against.
	for _, vec := range vf.Vectors {
		if vec.ID != "CV-APPLY-2" {
			continue
		}
		vec := vec
		t.Run(vec.ID, func(t *testing.T) {
			acc := map[string]string{}
			for i, step := range vec.DiffSteps {
				for _, entry := range step.Diff {
					if entry.NewValue == "EMPTY" {
						delete(acc, entry.Key)
						continue
					}
					acc[entry.Key] = entry.NewValue
				}

				gotRoot := rootOfLeaves(stateToLeaves(t, acc))
				wantRoot := mustHash(t, step.IncrementalRoot)
				if gotRoot != wantRoot {
					t.Errorf("step %d: incremental root = %x, want %x", i, gotRoot, wantRoot)
				}

				// Cross-check the vector's own declared per-step state against the same
				// root, catching a mistyped fixture rather than just a wrong applier.
				wantStateRoot := rootOfLeaves(stateToLeaves(t, step.ResultingState))
				if wantStateRoot != wantRoot {
					t.Errorf("step %d: vector's declared resulting_state root = %x, want incremental_root %x",
						i, wantStateRoot, wantRoot)
				}
			}
		})
	}
}
