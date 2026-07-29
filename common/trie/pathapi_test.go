package trie

import (
	"bytes"
	"math/rand"
	"os"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
)

func TestNoKeyHashingOnPathAPI(t *testing.T) {
	src, err := os.ReadFile("pathapi.go")
	if err != nil {
		t.Fatalf("read pathapi.go: %v", err)
	}
	if bytes.Contains(src, []byte("types.NewHash")) {
		t.Fatalf("pathapi.go references types.NewHash — the path API must not hash keys")
	}
}

func TestRootOfLeavesMatchesCore(t *testing.T) {
	var p0, p1, p2 types.Hash
	p0[0] = 0x10
	p1[0] = 0x80
	p2[0] = 0xF0

	paths := []Path{Path(p0), Path(p1), Path(p2)}
	values := [][]byte{[]byte("a"), []byte("b"), []byte("c")}

	// Build equivalent internal leaf slice.
	leaves := []leaf{
		{path: p0, value: []byte("a")},
		{path: p1, value: []byte("b")},
		{path: p2, value: []byte("c")},
	}

	got, err := RootOfLeaves(paths, values)
	if err != nil {
		t.Fatalf("RootOfLeaves: unexpected error %v", err)
	}
	want := rootOfLeaves(leaves)
	if got != want {
		t.Fatalf("RootOfLeaves mismatch: got %x want %x", got, want)
	}

	// Present-empty leaf.
	paths2 := []Path{Path(p0)}
	values2 := [][]byte{[]byte{}}
	leaves2 := []leaf{{path: p0, value: []byte{}}}
	got2, err := RootOfLeaves(paths2, values2)
	if err != nil {
		t.Fatalf("RootOfLeaves present-empty: unexpected error %v", err)
	}
	want2 := rootOfLeaves(leaves2)
	if got2 != want2 {
		t.Fatalf("RootOfLeaves present-empty mismatch: got %x want %x", got2, want2)
	}

	// Empty set -> zero hash.
	gotEmpty, err := RootOfLeaves(nil, nil)
	if err != nil {
		t.Fatalf("RootOfLeaves(nil,nil): unexpected error %v", err)
	}
	if gotEmpty != emptyHash {
		t.Fatalf("RootOfLeaves(nil,nil) != emptyHash")
	}
}

func TestRootOfLeavesDuplicatePath(t *testing.T) {
	var p0 types.Hash
	p0[0] = 0x10

	paths := []Path{Path(p0), Path(p0)}
	values := [][]byte{[]byte("a"), []byte("b")}

	_, err := RootOfLeaves(paths, values)
	if err != ErrDuplicatePath {
		t.Fatalf("RootOfLeaves with duplicate paths: got err %v, want ErrDuplicatePath", err)
	}
}

func TestProveByPathMatchesCore(t *testing.T) {
	var p0, p1, absent types.Hash
	p0[0] = 0x10
	p1[0] = 0x80
	absent[0] = 0xF0

	paths := []Path{Path(p0), Path(p1)}
	values := [][]byte{[]byte("hello"), []byte("world")}

	leaves := []leaf{
		{path: p0, value: []byte("hello")},
		{path: p1, value: []byte("world")},
	}
	sortLeaves(leaves)

	// Present path.
	gotPresent, gotVal, gotProof, err := ProveByPath(paths, values, Path(p0))
	if err != nil {
		t.Fatalf("ProveByPath present: unexpected error %v", err)
	}
	wantPresent, wantVal, wantSib := proveLeaves(leaves, p0)
	wantProof := encodeProof(wantPresent, wantVal, p0, wantSib)

	if gotPresent != wantPresent {
		t.Fatalf("present: got %v want %v", gotPresent, wantPresent)
	}
	if !bytes.Equal(gotVal, wantVal) {
		t.Fatalf("value: got %x want %x", gotVal, wantVal)
	}
	if !bytes.Equal(gotProof, wantProof) {
		t.Fatalf("proof bytes differ for present path")
	}

	// Absent path.
	gotPresent2, gotVal2, gotProof2, err2 := ProveByPath(paths, values, Path(absent))
	if err2 != nil {
		t.Fatalf("ProveByPath absent: unexpected error %v", err2)
	}
	wantPresent2, wantVal2, wantSib2 := proveLeaves(leaves, absent)
	wantProof2 := encodeProof(wantPresent2, wantVal2, absent, wantSib2)

	if gotPresent2 != wantPresent2 {
		t.Fatalf("absent present flag: got %v want %v", gotPresent2, wantPresent2)
	}
	if !bytes.Equal(gotVal2, wantVal2) {
		t.Fatalf("absent value: got %x want %x", gotVal2, wantVal2)
	}
	if !bytes.Equal(gotProof2, wantProof2) {
		t.Fatalf("proof bytes differ for absent path")
	}
}

func TestProveByPathDuplicatePath(t *testing.T) {
	var p0 types.Hash
	p0[0] = 0x10

	paths := []Path{Path(p0), Path(p0)}
	values := [][]byte{[]byte("a"), []byte("b")}

	_, _, _, err := ProveByPath(paths, values, Path(p0))
	if err != ErrDuplicatePath {
		t.Fatalf("ProveByPath with duplicate paths: got err %v, want ErrDuplicatePath", err)
	}
}

func TestVerifyByPathRoundTrip(t *testing.T) {
	var p0, p1, absent types.Hash
	p0[0] = 0x10
	p1[0] = 0x80
	absent[0] = 0xF0

	paths := []Path{Path(p0), Path(p1)}
	values := [][]byte{[]byte("hello"), []byte("world")}

	root, err := RootOfLeaves(paths, values)
	if err != nil {
		t.Fatalf("RootOfLeaves: unexpected error %v", err)
	}

	// Present: inclusion proof.
	_, val, proof, err := ProveByPath(paths, values, Path(p0))
	if err != nil {
		t.Fatalf("ProveByPath present: unexpected error %v", err)
	}
	ok, verr := VerifyProofByPath(root, Path(p0), val, proof)
	if verr != nil || !ok {
		t.Fatalf("VerifyProofByPath present: ok=%v err=%v", ok, verr)
	}

	// Absent: absence proof.
	_, _, proofAbsent, err := ProveByPath(paths, values, Path(absent))
	if err != nil {
		t.Fatalf("ProveByPath absent: unexpected error %v", err)
	}
	ok2, err2 := VerifyAbsenceByPath(root, Path(absent), proofAbsent)
	if err2 != nil || !ok2 {
		t.Fatalf("VerifyAbsenceByPath absent: ok=%v err=%v", ok2, err2)
	}
}

// TestLeavesFromPathsPanicOnMismatch verifies the documented length-guard panic when
// len(paths) != len(values) for both RootOfLeaves and ProveByPath.
func TestLeavesFromPathsPanicOnMismatch(t *testing.T) {
	var p types.Hash
	p[0] = 0xAA

	// RootOfLeaves with mismatched slices must panic.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("RootOfLeaves(1 path, 0 values): expected panic, got none")
			}
		}()
		RootOfLeaves([]Path{Path(p)}, [][]byte{})
	}()

	// ProveByPath with mismatched slices must panic.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("ProveByPath(1 path, 0 values, ...): expected panic, got none")
			}
		}()
		ProveByPath([]Path{Path(p)}, [][]byte{}, Path(p))
	}()
}

// TestPathAPIPropertyCrossCheck is a randomised cross-check: for N random leaf sets,
// RootOfLeaves must equal rootOfLeaves and ProveByPath must produce byte-identical proofs
// to proveLeaves+encodeProof. This proves the public path API is a thin wrapper over the core
// for arbitrary inputs, not just the hand-crafted example cases.
func TestPathAPIPropertyCrossCheck(t *testing.T) {
	rng := rand.New(rand.NewSource(42))

	for trial := 0; trial < 50; trial++ {
		n := rng.Intn(8) // 0..7 leaves
		paths := make([]Path, n)
		values := make([][]byte, n)
		leaves := make([]leaf, n)

		seen := map[types.Hash]bool{}
		for i := 0; i < n; i++ {
			var p types.Hash
			for {
				rng.Read(p[:])
				if !seen[p] {
					break
				}
			}
			seen[p] = true
			vLen := rng.Intn(16) // 0 = present-empty
			v := make([]byte, vLen)
			rng.Read(v)
			paths[i] = Path(p)
			values[i] = v
			leaves[i] = leaf{path: p, value: v}
		}

		// Root cross-check.
		gotRoot, err := RootOfLeaves(paths, values)
		if err != nil {
			t.Fatalf("trial %d: RootOfLeaves unexpected error %v", trial, err)
		}
		wantRoot := rootOfLeaves(leaves)
		if gotRoot != wantRoot {
			t.Fatalf("trial %d: RootOfLeaves mismatch", trial)
		}

		// Proof cross-check for each path (present) and one absent path.
		sortedLeaves := make([]leaf, n)
		copy(sortedLeaves, leaves)
		sortLeaves(sortedLeaves)

		for i := 0; i < n; i++ {
			targetPath := paths[i]
			gotPresent, gotVal, gotProof, perr := ProveByPath(paths, values, targetPath)
			if perr != nil {
				t.Fatalf("trial %d path %d: ProveByPath unexpected error %v", trial, i, perr)
			}

			wantPresent, wantVal, wantSib := proveLeaves(sortedLeaves, types.Hash(targetPath))
			wantProof := encodeProof(wantPresent, wantVal, types.Hash(targetPath), wantSib)

			if gotPresent != wantPresent {
				t.Fatalf("trial %d path %d: present flag mismatch", trial, i)
			}
			if !bytes.Equal(gotVal, wantVal) {
				t.Fatalf("trial %d path %d: value mismatch", trial, i)
			}
			if !bytes.Equal(gotProof, wantProof) {
				t.Fatalf("trial %d path %d: proof bytes mismatch", trial, i)
			}
		}

		// Absent path cross-check.
		var absentPath types.Hash
		rng.Read(absentPath[:])
		// Ensure it's not in the set (unlikely collision but guard it).
		if !seen[absentPath] {
			gotPresent, _, gotProof, perr := ProveByPath(paths, values, Path(absentPath))
			if perr != nil {
				t.Fatalf("trial %d absent: ProveByPath unexpected error %v", trial, perr)
			}
			wantPresent, wantVal, wantSib := proveLeaves(sortedLeaves, absentPath)
			wantProof := encodeProof(wantPresent, wantVal, absentPath, wantSib)
			if gotPresent != wantPresent {
				t.Fatalf("trial %d absent: present flag mismatch", trial)
			}
			if !bytes.Equal(gotProof, wantProof) {
				t.Fatalf("trial %d absent: proof bytes mismatch", trial)
			}
		}
	}
}
