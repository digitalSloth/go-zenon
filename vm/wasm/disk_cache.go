package wasm

import (
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/zenon-network/go-zenon/common/types"
)

// DiskCache stores compiled wazero module artifacts on disk.
// Files are stored at <baseDir>/<hex(hash)>.
type DiskCache struct {
	baseDir string
}

// NewDiskCache creates a new disk cache at the given directory.
func NewDiskCache(baseDir string) (*DiskCache, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, err
	}
	return &DiskCache{baseDir: baseDir}, nil
}

func (dc *DiskCache) path(hash types.Hash) string {
	return filepath.Join(dc.baseDir, hex.EncodeToString(hash[:]))
}

// Get reads a compiled module artifact from disk.
// Returns nil if the file does not exist.
func (dc *DiskCache) Get(hash types.Hash) ([]byte, error) {
	data, err := os.ReadFile(dc.path(hash))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Put writes a compiled module artifact to disk atomically.
func (dc *DiskCache) Put(hash types.Hash, data []byte) error {
	path := dc.path(hash)
	tmpPath := path + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// Delete removes a compiled module artifact from disk.
func (dc *DiskCache) Delete(hash types.Hash) error {
	err := os.Remove(dc.path(hash))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Has checks if a compiled module artifact exists on disk.
func (dc *DiskCache) Has(hash types.Hash) bool {
	_, err := os.Stat(dc.path(hash))
	return err == nil
}
