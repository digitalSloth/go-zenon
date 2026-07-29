package nom

import (
	"testing"

	"github.com/ethereum/go-ethereum/rlp"

	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/types"
)

func sampleMomentum(version uint64) *Momentum {
	return &Momentum{
		Version:         version,
		ChainIdentifier: 1,
		PreviousHash:    types.NewHash([]byte("prev")),
		Height:          10,
		TimestampUnix:   1700000000,
		Data:            nil,
		Content:         NewMomentumContent(nil),
		ChangesHash:     types.NewHash([]byte("changes")),
		NextFusionPrice: 100,
		NextWorkPrice:   200,
	}
}

// TestComputeHashV2BackwardCompat is the §11.6 golden gate: a v2 momentum's hash preimage is
// exactly the pre-state-root preimage and is independent of StateRoot.
func TestComputeHashV2BackwardCompat(t *testing.T) {
	m := sampleMomentum(2)

	// Independent reconstruction of the v2 preimage (no StateRoot term).
	expected := types.NewHash(common.JoinBytes(
		common.Uint64ToBytes(m.Version),
		common.Uint64ToBytes(m.ChainIdentifier),
		m.PreviousHash.Bytes(),
		common.Uint64ToBytes(m.Height),
		common.Uint64ToBytes(m.TimestampUnix),
		types.NewHash(m.Data).Bytes(),
		m.Content.Hash().Bytes(),
		m.ChangesHash.Bytes(),
		common.Uint64ToBytes(m.NextFusionPrice),
		common.Uint64ToBytes(m.NextWorkPrice),
	))
	if got := m.ComputeHash(); got != expected {
		t.Fatalf("v2 ComputeHash = %v, want %v", got, expected)
	}

	// Setting StateRoot must NOT change a v2 momentum's hash.
	before := m.ComputeHash()
	m.StateRoot = types.NewHash([]byte("ignored-on-v2"))
	if after := m.ComputeHash(); after != before {
		t.Fatalf("v2 hash changed when StateRoot was set: %v != %v", after, before)
	}
}

// TestComputeHashV3IncludesStateRoot confirms v3 binds StateRoot, appended raw after the v2
// price bytes.
func TestComputeHashV3IncludesStateRoot(t *testing.T) {
	m := sampleMomentum(3)
	m.StateRoot = types.NewHash([]byte("root"))

	expected := types.NewHash(common.JoinBytes(
		common.Uint64ToBytes(m.Version),
		common.Uint64ToBytes(m.ChainIdentifier),
		m.PreviousHash.Bytes(),
		common.Uint64ToBytes(m.Height),
		common.Uint64ToBytes(m.TimestampUnix),
		types.NewHash(m.Data).Bytes(),
		m.Content.Hash().Bytes(),
		m.ChangesHash.Bytes(),
		common.Uint64ToBytes(m.NextFusionPrice),
		common.Uint64ToBytes(m.NextWorkPrice),
		m.StateRoot.Bytes(),
	))
	if got := m.ComputeHash(); got != expected {
		t.Fatalf("v3 ComputeHash = %v, want %v", got, expected)
	}

	// The root must actually move the hash.
	zero := sampleMomentum(3)
	if zero.ComputeHash() == m.ComputeHash() {
		t.Fatalf("v3 hash did not change with StateRoot")
	}
}

func TestProtoRoundTripV3(t *testing.T) {
	m := sampleMomentum(3)
	m.StateRoot = types.NewHash([]byte("root"))
	m.Hash = m.ComputeHash()

	data, err := m.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DeserializeMomentum(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.StateRoot != m.StateRoot {
		t.Fatalf("proto round-trip StateRoot = %v, want %v", got.StateRoot, m.StateRoot)
	}
	if got.ComputeHash() != m.Hash {
		t.Fatalf("proto round-trip changed the hash")
	}
}

func TestProtoV2HasZeroStateRoot(t *testing.T) {
	m := sampleMomentum(2)
	data, err := m.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DeserializeMomentum(data)
	if err != nil {
		t.Fatal(err)
	}
	if !got.StateRoot.IsZero() {
		t.Fatalf("v2 momentum decoded with non-zero StateRoot: %v", got.StateRoot)
	}
	// A zero StateRoot must not be emitted on the wire (keeps v2 serialization unchanged).
	if len(m.Proto().StateRoot) != 0 {
		t.Fatalf("v2 Proto emitted a StateRoot field")
	}
}

// TestRLPRoundTrip covers the wire path: a v3 momentum preserves StateRoot, and a v2 momentum
// (zero, trailing optional) decodes back to zero — the mixed-binary rollout guarantee.
func TestRLPRoundTrip(t *testing.T) {
	v3 := sampleMomentum(3)
	v3.StateRoot = types.NewHash([]byte("root"))
	v3.Hash = v3.ComputeHash()

	encoded, err := rlp.EncodeToBytes(v3)
	if err != nil {
		t.Fatalf("rlp encode v3: %v", err)
	}
	var decoded Momentum
	if err := rlp.DecodeBytes(encoded, &decoded); err != nil {
		t.Fatalf("rlp decode v3: %v", err)
	}
	if decoded.StateRoot != v3.StateRoot {
		t.Fatalf("rlp v3 StateRoot = %v, want %v", decoded.StateRoot, v3.StateRoot)
	}

	v2 := sampleMomentum(2)
	encoded2, err := rlp.EncodeToBytes(v2)
	if err != nil {
		t.Fatalf("rlp encode v2: %v", err)
	}
	var decoded2 Momentum
	if err := rlp.DecodeBytes(encoded2, &decoded2); err != nil {
		t.Fatalf("rlp decode v2: %v", err)
	}
	if !decoded2.StateRoot.IsZero() {
		t.Fatalf("rlp v2 StateRoot should be zero, got %v", decoded2.StateRoot)
	}
}
