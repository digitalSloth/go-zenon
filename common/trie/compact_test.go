package trie

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
)

// TestCompactEmpty checks that an empty CompactTree returns the default root.
func TestCompactEmpty(t *testing.T) {
	ct := NewCompactTree()
	got := ct.Root()
	want := emptyHash
	if got != want {
		t.Fatalf("empty root: got %v want %v", got, want)
	}
}

// TestCompactSingleLeaf checks that a single-leaf tree root equals padLeaf(leaf, 0).
func TestCompactSingleLeaf(t *testing.T) {
	ct := NewCompactTree()
	path := types.NewHash([]byte("hello"))
	value := []byte("world")
	ct.Insert(Path(path), value)

	l := leaf{path: path, value: value}
	want := padLeaf(l, 0)
	got := ct.Root()
	if got != want {
		t.Fatalf("single-leaf root: got %v want %v", got, want)
	}

	// Also verify against oracle.
	m := map[types.Hash][]byte{path: value}
	if got != oracleRoot(m) {
		t.Fatalf("single-leaf root vs oracle: got %v want %v", got, oracleRoot(m))
	}
}

// TestCompactDeleteDownToEmpty inserts a leaf, then deletes it, expects empty root.
func TestCompactDeleteDownToEmpty(t *testing.T) {
	ct := NewCompactTree()
	path := types.NewHash([]byte("key"))
	ct.Insert(Path(path), []byte("value"))
	ct.Delete(Path(path))

	got := ct.Root()
	want := emptyHash
	if got != want {
		t.Fatalf("delete-to-empty root: got %v want %v", got, want)
	}
}

// TestCompactWriteDeleteEqualsNeverWrite checks that inserting then deleting a key leaves
// the root identical to never having inserted it (history independence).
func TestCompactWriteDeleteEqualsNeverWrite(t *testing.T) {
	// Tree A: insert two keys, delete one.
	pathA := types.NewHash([]byte("a"))
	pathB := types.NewHash([]byte("b"))
	val := []byte("v")

	ctA := NewCompactTree()
	ctA.Insert(Path(pathA), val)
	ctA.Insert(Path(pathB), val)
	ctA.Delete(Path(pathA))

	// Tree B: only insert pathB from the start.
	ctB := NewCompactTree()
	ctB.Insert(Path(pathB), val)

	if ctA.Root() != ctB.Root() {
		t.Fatalf("write-then-delete != never-write: A=%v B=%v", ctA.Root(), ctB.Root())
	}
}

// TestCompactTopBitDivergence exercises two leaves that diverge at bit 0 (the MSB).
func TestCompactTopBitDivergence(t *testing.T) {
	// Construct paths differing at bit 0: one with MSB=0, one with MSB=1.
	var pathL, pathR types.Hash
	pathL[0] = 0x00 // MSB 0
	pathR[0] = 0x80 // MSB 1

	ct := NewCompactTree()
	valL := []byte("left")
	valR := []byte("right")
	ct.Insert(Path(pathL), valL)
	ct.Insert(Path(pathR), valR)

	m := map[types.Hash][]byte{pathL: valL, pathR: valR}
	got := ct.Root()
	want := oracleRoot(m)
	if got != want {
		t.Fatalf("top-bit divergence: got %v want %v", got, want)
	}
}

// TestCompactDeepDivergence exercises two leaves sharing a long prefix (deep branch).
func TestCompactDeepDivergence(t *testing.T) {
	// Paths identical in bytes 0..30, differ only in the last byte.
	var pathA, pathB types.Hash
	for i := range pathA {
		pathA[i] = 0xAB
		pathB[i] = 0xAB
	}
	pathA[31] = 0xAB // last byte: bit pattern 10101011
	pathB[31] = 0xAA // last byte: bit pattern 10101010 — differ at bit 255

	ct := NewCompactTree()
	valA := []byte("deep-a")
	valB := []byte("deep-b")
	ct.Insert(Path(pathA), valA)
	ct.Insert(Path(pathB), valB)

	m := map[types.Hash][]byte{pathA: valA, pathB: valB}
	got := ct.Root()
	want := oracleRoot(m)
	if got != want {
		t.Fatalf("deep-divergence: got %v want %v", got, want)
	}
}

// TestCompactFuzzAgainstOracle runs a long random insert/update/delete sequence with the root
// checked against oracleRoot after every op.
func TestCompactFuzzAgainstOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	ct := NewCompactTree()
	oracle := map[types.Hash][]byte{}

	// keys holds the set of keys inserted so far so we can pick them for updates/deletes.
	var keys []types.Hash

	compactRandPath := func() types.Hash {
		k := make([]byte, 9)
		k[0] = byte(3 + rng.Intn(7))
		binary.BigEndian.PutUint64(k[1:], rng.Uint64())
		return types.NewHash(k)
	}

	const ops = 2000
	for i := 0; i < ops; i++ {
		switch rng.Intn(3) {
		case 0, 1: // insert or update
			var path types.Hash
			if len(keys) > 0 && rng.Intn(2) == 0 {
				path = keys[rng.Intn(len(keys))]
			} else {
				path = compactRandPath()
				keys = append(keys, path)
			}
			value := make([]byte, 1+rng.Intn(40))
			rng.Read(value)
			ct.Insert(Path(path), value)
			oracle[path] = value

		case 2: // delete
			if len(keys) == 0 {
				continue
			}
			idx := rng.Intn(len(keys))
			path := keys[idx]
			ct.Delete(Path(path))
			delete(oracle, path)
			// Remove from keys slice (swap with last).
			keys[idx] = keys[len(keys)-1]
			keys = keys[:len(keys)-1]
		}

		got := ct.Root()
		want := oracleRoot(oracle)
		if got != want {
			t.Fatalf("op %d: root mismatch got=%v want=%v", i, got, want)
		}
	}

	// Delete all remaining keys and verify empty root.
	for _, path := range keys {
		ct.Delete(Path(path))
		delete(oracle, path)
	}
	if ct.Root() != emptyHash {
		t.Fatalf("after delete-all, root is not default: %v", ct.Root())
	}
	if ct.Root() != oracleRoot(oracle) {
		t.Fatalf("after delete-all, root != oracle empty root")
	}
}

// TestCompactDeleteNonExistent verifies that deleting a key not in the tree is a no-op.
func TestCompactDeleteNonExistent(t *testing.T) {
	ct := NewCompactTree()
	path := types.NewHash([]byte("present"))
	value := []byte("val")
	ct.Insert(Path(path), value)
	before := ct.Root()

	absent := types.NewHash([]byte("absent"))
	ct.Delete(Path(absent))
	after := ct.Root()

	if before != after {
		t.Fatalf("delete-nonexistent changed root: before=%v after=%v", before, after)
	}
}

// TestCompactUpdateValue verifies that re-inserting a key with a new value changes the root
// to match the oracle.
func TestCompactUpdateValue(t *testing.T) {
	ct := NewCompactTree()
	path := types.NewHash([]byte("key"))
	ct.Insert(Path(path), []byte("v1"))
	ct.Insert(Path(path), []byte("v2"))

	m := map[types.Hash][]byte{path: []byte("v2")}
	got := ct.Root()
	want := oracleRoot(m)
	if got != want {
		t.Fatalf("update value: got %v want %v", got, want)
	}
}

// TestCompactThreeLeaves exercises a small tree where the branch structure is non-trivial.
func TestCompactThreeLeaves(t *testing.T) {
	// Use the same randKey shape as tree_test.go for consistency.
	rng := rand.New(rand.NewSource(99))
	ct := NewCompactTree()
	oracle := map[types.Hash][]byte{}
	for i := 0; i < 3; i++ {
		k := make([]byte, 9)
		k[0] = byte(3 + rng.Intn(7))
		binary.BigEndian.PutUint64(k[1:], rng.Uint64())
		path := types.NewHash(k)
		val := make([]byte, 8)
		rng.Read(val)
		ct.Insert(Path(path), val)
		oracle[path] = val
	}
	got := ct.Root()
	want := oracleRoot(oracle)
	if got != want {
		t.Fatalf("three-leaf root: got %v want %v", got, want)
	}
}

// flipBit returns p with the bit at level flipped (MSB-first, as in pathBit).
func flipBit(p Path, level int) Path {
	p[level/8] ^= 0x80 >> (level % 8)
	return p
}

// refSlices flattens ref into parallel slices for the package-level API.
func refSlices(ref map[Path][]byte) ([]Path, [][]byte) {
	paths := make([]Path, 0, len(ref))
	values := make([][]byte, 0, len(ref))
	for p, v := range ref {
		paths = append(paths, p)
		values = append(values, v)
	}
	return paths, values
}

// assertMatchesRef checks ct's root and each target's proof against the package-level API over ref.
func assertMatchesRef(t *testing.T, ct *CompactTree, ref map[Path][]byte, targets []Path) {
	t.Helper()
	paths, values := refSlices(ref)
	wantRoot, err := RootOfLeaves(paths, values)
	if err != nil {
		t.Fatalf("RootOfLeaves: %v", err)
	}
	if got := ct.Root(); got != wantRoot {
		t.Fatalf("root mismatch: got %x want %x", got, wantRoot)
	}
	for _, target := range targets {
		wantPresent, wantValue, wantProof, err := ProveByPath(paths, values, target)
		if err != nil {
			t.Fatalf("ProveByPath: %v", err)
		}
		present, value, proof := ct.ProveByPath(target)
		if present != wantPresent {
			t.Fatalf("target %x: present = %v, want %v", target, present, wantPresent)
		}
		if !bytes.Equal(value, wantValue) {
			t.Fatalf("target %x: value = %x, want %x", target, value, wantValue)
		}
		if !bytes.Equal(proof, wantProof) {
			t.Fatalf("target %x: proof mismatch:\n  got  %x\n  want %x", target, proof, wantProof)
		}
	}
}

// TestCompactProveByPathShapes covers each tree shape ProveByPath can diverge in.
func TestCompactProveByPathShapes(t *testing.T) {
	var a Path
	a[0], a[1] = 0x5C, 0xA7
	b := flipBit(a, 200)
	c := flipBit(a, 10)

	build := func(leaves ...Path) (*CompactTree, map[Path][]byte) {
		ct := NewCompactTree()
		ref := map[Path][]byte{}
		for i, l := range leaves {
			v := []byte{byte('a' + i)}
			ct.Insert(l, v)
			ref[l] = v
		}
		return ct, ref
	}

	t.Run("empty", func(t *testing.T) {
		ct, ref := build()
		assertMatchesRef(t, ct, ref, []Path{a})
	})
	t.Run("single-leaf", func(t *testing.T) {
		ct, ref := build(a)
		assertMatchesRef(t, ct, ref, []Path{a, flipBit(a, 0), flipBit(a, 255)})
	})
	t.Run("root-chain", func(t *testing.T) {
		ct, ref := build(a, b)
		assertMatchesRef(t, ct, ref, []Path{a, b, flipBit(a, 5), flipBit(a, 250), flipBit(b, 230)})
	})
	t.Run("chain-below-branch", func(t *testing.T) {
		ct, ref := build(a, b, c)
		assertMatchesRef(t, ct, ref, []Path{a, b, c, flipBit(a, 100)})
	})
	t.Run("present-empty", func(t *testing.T) {
		p, q, r := a, flipBit(a, 20), flipBit(a, 40)
		ct := NewCompactTree()
		ref := map[Path][]byte{p: {}, q: nil, r: []byte("x")}
		for k, v := range ref {
			ct.Insert(k, v)
		}
		assertMatchesRef(t, ct, ref, []Path{p, q, r})
		for _, k := range []Path{p, q} {
			present, value, _ := ct.ProveByPath(k)
			if !present || len(value) != 0 {
				t.Fatalf("target %x: present=%v len(value)=%d, want present-empty", k, present, len(value))
			}
		}
	})
	t.Run("no-op-delete", func(t *testing.T) {
		ct, ref := build(a, b, c)
		targets := []Path{a, b, c, flipBit(a, 100)}
		before := ct.Root()
		beforeProofs := make([][]byte, len(targets))
		for i, tg := range targets {
			_, _, beforeProofs[i] = ct.ProveByPath(tg)
		}
		ct.Delete(flipBit(a, 77))
		if ct.Root() != before {
			t.Fatalf("no-op delete changed the root")
		}
		for i, tg := range targets {
			if _, _, p := ct.ProveByPath(tg); !bytes.Equal(p, beforeProofs[i]) {
				t.Fatalf("no-op delete changed the proof for %x", tg)
			}
		}
		assertMatchesRef(t, ct, ref, targets)
	})
	t.Run("delete-to-empty", func(t *testing.T) {
		ct, _ := build(a, b, c)
		ct.Delete(a)
		ct.Delete(b)
		ct.Delete(c)
		if ct.Root() != emptyHash {
			t.Fatalf("root after deleting everything = %x, want emptyHash", ct.Root())
		}
		assertMatchesRef(t, ct, map[Path][]byte{}, []Path{a})
	})
}

// TestCompactProveByPathDifferential drives random writes and checks every root and proof
// against the package-level API over the same leaf set.
func TestCompactProveByPathDifferential(t *testing.T) {
	for _, n := range []int{0, 1, 2, 64, 512} {
		n := n
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(n) + 7))
			ct := NewCompactTree()
			ref := map[Path][]byte{}
			var live, dead []Path
			written := map[Path]bool{}

			fresh := func() Path {
				for {
					var p Path
					rng.Read(p[:])
					if !written[p] {
						return p
					}
				}
			}
			randValue := func() []byte {
				v := make([]byte, rng.Intn(41))
				rng.Read(v)
				return v
			}
			isLive := func(p Path) bool { _, ok := ref[p]; return ok }
			put := func(p Path, v []byte) {
				if !isLive(p) {
					live = append(live, p)
				}
				written[p] = true
				ct.Insert(p, v)
				ref[p] = v
			}
			del := func(p Path) {
				for i, l := range live {
					if l == p {
						live = append(live[:i], live[i+1:]...)
						dead = append(dead, p)
						break
					}
				}
				ct.Delete(p)
				delete(ref, p)
			}
			pickLive := func() (Path, bool) {
				if len(live) == 0 {
					return Path{}, false
				}
				return live[rng.Intn(len(live))], true
			}

			for i := 0; i < n; i++ {
				put(fresh(), randValue())
			}
			assertMatchesRef(t, ct, ref, []Path{fresh()})

			const ops = 48
			for op := 0; op < ops; op++ {
				var opPath Path
				switch rng.Intn(6) {
				case 0:
					opPath = fresh()
					put(opPath, randValue())
				case 1:
					if l, ok := pickLive(); ok {
						opPath = flipBit(l, 200+rng.Intn(56))
						if isLive(opPath) {
							continue
						}
					} else {
						opPath = fresh()
					}
					put(opPath, randValue())
				case 2:
					l, ok := pickLive()
					if !ok {
						continue
					}
					opPath = l
					if rng.Intn(4) == 0 {
						put(l, append([]byte(nil), ref[l]...))
					} else {
						put(l, randValue())
					}
				case 3:
					if l, ok := pickLive(); ok {
						opPath = l
					} else {
						opPath = fresh()
					}
					if op%2 == 0 {
						put(opPath, []byte{})
					} else {
						put(opPath, nil)
					}
				case 4:
					l, ok := pickLive()
					if !ok {
						continue
					}
					opPath = l
					del(l)
				case 5:
					if len(dead) > 0 && rng.Intn(2) == 0 {
						opPath = dead[rng.Intn(len(dead))]
						if isLive(opPath) {
							continue
						}
					} else {
						opPath = fresh()
					}
					before := ct.Root()
					ct.Delete(opPath)
					if ct.Root() != before {
						t.Fatalf("op %d: deleting an absent path changed the root", op)
					}
				}

				if n <= 64 || op%8 == 7 || op == ops-1 {
					targets := []Path{opPath, fresh()}
					if l, ok := pickLive(); ok {
						targets = append(targets, l)
						if f := flipBit(l, 200+rng.Intn(56)); !isLive(f) {
							targets = append(targets, f)
						}
					}
					assertMatchesRef(t, ct, ref, targets)
				}
			}

			for len(live) > 0 {
				del(live[0])
			}
			if ct.Root() != emptyHash {
				t.Fatalf("root after deleting every leaf = %x, want emptyHash", ct.Root())
			}
			assertMatchesRef(t, ct, ref, []Path{fresh()})
		})
	}
}
