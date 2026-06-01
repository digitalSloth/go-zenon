package momentum

import (
	"bytes"
	"sort"

	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/types"
)

func getWasmPendingKey(addr types.Address) []byte {
	return common.JoinBytes(wasmPendingPrefix, addr.Bytes())
}

func (ms *momentumStore) markWasmPending(addr types.Address) {
	common.DealWithErr(ms.DB.Put(getWasmPendingKey(addr), []byte{1}))
}

func (ms *momentumStore) GetWasmPendingAddresses() ([]types.Address, error) {
	iterator := ms.DB.NewIterator(wasmPendingPrefix)
	defer iterator.Release()

	var addresses []types.Address
	for {
		if !iterator.Next() {
			if iterator.Error() != nil {
				return nil, iterator.Error()
			}
			break
		}
		// Skip deleted entries (empty values from enableDeleteDB)
		if len(iterator.Value()) == 0 {
			continue
		}
		key := iterator.Key()
		// key is wasmPendingPrefix(1) + addr(20)
		if len(key) != 1+types.AddressSize {
			continue
		}
		var addr types.Address
		copy(addr[:], key[1:])
		addresses = append(addresses, addr)
	}

	// Sort by address bytes for deterministic iteration order
	sort.Slice(addresses, func(i, j int) bool {
		return bytes.Compare(addresses[i][:], addresses[j][:]) < 0
	})
	return addresses, nil
}

// clearWasmPendingIfDrained removes addr from the wasm-pending index when its
// sequencer holds no more unreceived sends. Must be called from the committed
// apply path (AddAccountBlockTransaction) — not from a frontier store, whose
// writes would be silently discarded.
func (ms *momentumStore) clearWasmPendingIfDrained(addr types.Address) error {
	accStore := ms.GetAccountStore(addr)
	mailbox := ms.getAccountMailbox(addr)
	if accStore.SequencerFront(mailbox) == nil {
		return ms.DB.Delete(getWasmPendingKey(addr))
	}
	return nil
}
