// momentum_fold.go — L1 MOMENTUM ADAPTER (not for L2).
//
// The fold rule and account-store whitelist are L1-momentum maintenance conventions, not part
// of the shared SMT core. The L1 momentum tree deliberately folds an empty value to a deletion
// and commits only to the account-store balance and contract-storage keys, which gives
// history-independence because go-zenon embedded-contract storage has no present-empty values.
// This is an L1 application choice and MUST NOT be reused by L2 — the L2 executor keeps
// present-empty leaves distinct from absent slots and drives the shared core (pathapi.go)
// directly.
package trie

import (
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
)

const (
	// accountStorePrefixByte is the momentum-store top-level prefix for the per-account store.
	// Every key beneath it has the shape {3} || address(20) || subPrefix || … .
	accountStorePrefixByte = 0x03
	// balanceSubPrefixByte and storageSubPrefixByte are the two account-store sub-prefixes the
	// state-root fold commits to: token balances and embedded-contract storage.
	balanceSubPrefixByte = 0x03
	storageSubPrefixByte = 0x04
)

// accountSubPrefixIndex is the offset of the sub-prefix byte inside an account-store key:
// one top-level prefix byte followed by a 20-byte address.
const accountSubPrefixIndex = 1 + types.AddressSize

// foldKeep reports whether key is included in the state-root fold. The fold commits to exactly
// two account-store categories — token balances ({3}||addr||{3}||…) and embedded-contract
// storage ({3}||addr||{4}||…) — and nothing else. This whitelist is the single source of truth
// for the fold, shared by FoldFilter and the in-place tree appliers so they can never diverge;
// any key category not named here is excluded from the commitment by construction.
func foldKeep(key []byte) bool {
	if len(key) <= accountSubPrefixIndex {
		return false
	}
	if key[0] != accountStorePrefixByte {
		return false
	}
	sub := key[accountSubPrefixIndex]
	return sub == balanceSubPrefixByte || sub == storageSubPrefixByte
}

// FoldFilter returns a copy of `changes` with everything outside the state-root fold's
// whitelist removed — the canonical, consensus-visible fold rule. The tree folds this filtered
// write-set so its root commits to exactly the account-store token-balance and
// contract-storage keys that ChangesHash covers; the same rule is applied identically by the
// producer, verifier, live maintainer, and Init build (the fold-equivalence property).
func FoldFilter(changes db.Patch) db.Patch {
	out := db.NewPatch()
	f := &foldFilter{out: out}
	if err := changes.Replay(f); err != nil {
		// db.Patch.Replay over an in-memory leveldb batch does not error in practice;
		// surface it loudly rather than silently dropping writes.
		panic(err)
	}
	return out
}

type foldFilter struct {
	out db.Patch
}

func (f *foldFilter) Put(key, value []byte) {
	if !foldKeep(key) {
		return
	}
	f.out.Put(key, value)
}

func (f *foldFilter) Delete(key []byte) {
	if !foldKeep(key) {
		return
	}
	f.out.Delete(key)
}

// stagedApplier turns a write-set into staged ops under the fold rule: keys outside the
// whitelist are skipped; a Put with an empty value or a Delete removes the leaf (deleted ==
// empty).
type stagedApplier struct {
	staged map[types.Hash]stagedOp
}

func (a *stagedApplier) Put(key, value []byte) {
	if !foldKeep(key) {
		return
	}
	path := types.NewHash(key)
	if len(value) == 0 {
		a.staged[path] = stagedOp{del: true}
		return
	}
	v := make([]byte, len(value))
	copy(v, value)
	a.staged[path] = stagedOp{value: v}
}

func (a *stagedApplier) Delete(key []byte) {
	if !foldKeep(key) {
		return
	}
	a.staged[types.NewHash(key)] = stagedOp{del: true}
}

// stagedApplierMap applies a write-set directly to a leaf map under the fold whitelist.
type stagedApplierMap struct {
	m map[types.Hash][]byte
}

func (a *stagedApplierMap) Put(key, value []byte) {
	if !foldKeep(key) {
		return
	}
	path := types.NewHash(key)
	if len(value) == 0 {
		delete(a.m, path)
		return
	}
	v := make([]byte, len(value))
	copy(v, value)
	a.m[path] = v
}

func (a *stagedApplierMap) Delete(key []byte) {
	if !foldKeep(key) {
		return
	}
	delete(a.m, types.NewHash(key))
}
