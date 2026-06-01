package wasm

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/tetratelabs/wazero"

	"github.com/zenon-network/go-zenon/common/types"
)

func TestDiskCache_PutGet(t *testing.T) {
	dir, err := os.MkdirTemp("", "wasm-cache-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	dc, err := NewDiskCache(dir)
	if err != nil {
		t.Fatal(err)
	}

	hash := types.Hash{1, 2, 3}
	data := []byte("compiled module data")

	if err := dc.Put(hash, data); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := dc.Get(hash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("data mismatch: got %q, want %q", got, data)
	}
}

func TestDiskCache_GetMissing(t *testing.T) {
	dir, err := os.MkdirTemp("", "wasm-cache-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	dc, err := NewDiskCache(dir)
	if err != nil {
		t.Fatal(err)
	}

	hash := types.Hash{9, 9, 9}
	got, err := dc.Get(hash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %q", got)
	}
}

func TestDiskCache_Delete(t *testing.T) {
	dir, err := os.MkdirTemp("", "wasm-cache-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	dc, err := NewDiskCache(dir)
	if err != nil {
		t.Fatal(err)
	}

	hash := types.Hash{1, 2, 3}
	dc.Put(hash, []byte("data"))

	if !dc.Has(hash) {
		t.Fatal("expected Has=true before delete")
	}

	dc.Delete(hash)

	if dc.Has(hash) {
		t.Fatal("expected Has=false after delete")
	}
}

func TestDiskCache_DeleteMissing(t *testing.T) {
	dir, err := os.MkdirTemp("", "wasm-cache-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	dc, err := NewDiskCache(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Deleting non-existent key should not error.
	if err := dc.Delete(types.Hash{9, 9, 9}); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
}

// H3 regression: a corrupt on-disk entry must not fail compilation. The disk
// tier is a non-consensus optimization, so a cached blob that no longer
// compiles has to fall through to a recompile from the original bytecode —
// otherwise one node would reject an Execute every other node accepts.
func TestModuleCache_CorruptDiskEntryRecompiles(t *testing.T) {
	dir := t.TempDir()
	mc, err := NewModuleCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	rt := wazero.NewRuntimeWithConfig(context.Background(), wazero.NewRuntimeConfigInterpreter())
	defer rt.Close(context.Background())

	bytecode := trivialModule()

	// Warm the cache: compiles, stores instrumented bytes on disk.
	if _, err := mc.GetModule(rt, bytecode); err != nil {
		t.Fatalf("initial GetModule: %v", err)
	}

	// Corrupt the on-disk entry and drop the LRU so the next call must read disk.
	h := bytecodeHash(bytecode)
	fpath := filepath.Join(dir, "wasm-modules", hex.EncodeToString(h[:]))
	if err := os.WriteFile(fpath, []byte("not a wasm module"), 0644); err != nil {
		t.Fatal(err)
	}
	mc.lruCache.Remove(h)

	// Must still return a compiled module (recompiled from the original).
	compiled, err := mc.GetModule(rt, bytecode)
	if err != nil {
		t.Fatalf("GetModule after corruption should recompile, got error: %v", err)
	}
	if compiled == nil {
		t.Fatal("expected a compiled module")
	}
}
