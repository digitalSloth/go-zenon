package nom

import (
	"bytes"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeEvent builds a test event with deterministic values.
func makeEvent(contractAddr types.Address, topic types.Hash, indexed bool, data []byte) AccountBlockEvent {
	return AccountBlockEvent{
		ContractAddress: contractAddr,
		Topic:           topic,
		Indexed:         indexed,
		Data:            data,
	}
}

// makeV3Block builds a minimal v3 contract-receive block with the given events.
func makeV3Block(events []AccountBlockEvent) *AccountBlock {
	return &AccountBlock{
		Version:              WasmAccountBlockVersion,
		ChainIdentifier:      69,
		BlockType:            BlockTypeContractReceive,
		Hash:                 types.Hash{},
		PreviousHash:         types.Hash{1},
		Height:               2,
		MomentumAcknowledged: types.HashHeight{Hash: types.Hash{2}, Height: 10},
		Address:              types.Address{2, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		ToAddress:            types.Address{},
		Amount:               big.NewInt(0),
		TokenStandard:        types.ZnnTokenStandard,
		FromBlockHash:        types.Hash{3},
		Data:                 []byte{},
		FusedPlasma:          1000,
		Difficulty:           0,
		Nonce:                Nonce{Data: [8]byte{}},
		Events:               events,
	}
}

// makeV1Block builds a minimal v1 user-send block (no events).
func makeV1Block() *AccountBlock {
	return &AccountBlock{
		Version:              1,
		ChainIdentifier:      69,
		BlockType:            BlockTypeUserSend,
		Hash:                 types.Hash{},
		PreviousHash:         types.Hash{1},
		Height:               2,
		MomentumAcknowledged: types.HashHeight{Hash: types.Hash{2}, Height: 10},
		Address:              types.Address{0, 1, 1},
		ToAddress:            types.Address{0, 1, 2},
		Amount:               big.NewInt(1000),
		TokenStandard:        types.ZnnTokenStandard,
		FromBlockHash:        types.Hash{},
		Data:                 []byte{},
		FusedPlasma:          1000,
		Difficulty:           0,
		Nonce:                Nonce{Data: [8]byte{}},
	}
}

// ---------------------------------------------------------------------------
// EventsHash tests
// ---------------------------------------------------------------------------

func TestEventsHash_Deterministic(t *testing.T) {
	addr := types.Address{2, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	topic := types.Hash{1, 2, 3}
	events := []AccountBlockEvent{
		makeEvent(addr, topic, false, []byte("hello")),
		makeEvent(addr, topic, true, []byte("world")),
	}

	ab1 := &AccountBlock{Events: events}
	ab2 := &AccountBlock{Events: events}

	h1 := ab1.EventsHash()
	h2 := ab2.EventsHash()
	if h1 != h2 {
		t.Fatal("EventsHash not deterministic for identical event lists")
	}
}

func TestEventsHash_Empty(t *testing.T) {
	ab := &AccountBlock{}
	h := ab.EventsHash()
	if h != (types.Hash{}) {
		t.Fatal("empty EventsHash should be zero hash")
	}
}

func TestEventsHash_OrderDependent(t *testing.T) {
	addr := types.Address{2, 1}
	topicA := types.Hash{1}
	topicB := types.Hash{2}

	events1 := []AccountBlockEvent{
		makeEvent(addr, topicA, false, []byte("a")),
		makeEvent(addr, topicB, false, []byte("b")),
	}
	events2 := []AccountBlockEvent{
		makeEvent(addr, topicB, false, []byte("b")),
		makeEvent(addr, topicA, false, []byte("a")),
	}

	ab1 := &AccountBlock{Events: events1}
	ab2 := &AccountBlock{Events: events2}

	if ab1.EventsHash() == ab2.EventsHash() {
		t.Fatal("EventsHash should differ when event order differs")
	}
}

func TestEventsHash_DistinguishesContractAddress(t *testing.T) {
	addr1 := types.Address{2, 1}
	addr2 := types.Address{2, 2}
	topic := types.Hash{1}

	events1 := []AccountBlockEvent{makeEvent(addr1, topic, false, []byte("x"))}
	events2 := []AccountBlockEvent{makeEvent(addr2, topic, false, []byte("x"))}

	ab1 := &AccountBlock{Events: events1}
	ab2 := &AccountBlock{Events: events2}

	if ab1.EventsHash() == ab2.EventsHash() {
		t.Fatal("EventsHash should differ when ContractAddress differs")
	}
}

func TestEventsHash_DistinguishesIndexed(t *testing.T) {
	addr := types.Address{2, 1}
	topic := types.Hash{1}

	events1 := []AccountBlockEvent{makeEvent(addr, topic, false, []byte("x"))}
	events2 := []AccountBlockEvent{makeEvent(addr, topic, true, []byte("x"))}

	ab1 := &AccountBlock{Events: events1}
	ab2 := &AccountBlock{Events: events2}

	if ab1.EventsHash() == ab2.EventsHash() {
		t.Fatal("EventsHash should differ when Indexed differs")
	}
}

func TestEventsHash_DistinguishesTopic(t *testing.T) {
	addr := types.Address{2, 1}

	events1 := []AccountBlockEvent{makeEvent(addr, types.Hash{1}, false, []byte("x"))}
	events2 := []AccountBlockEvent{makeEvent(addr, types.Hash{2}, false, []byte("x"))}

	ab1 := &AccountBlock{Events: events1}
	ab2 := &AccountBlock{Events: events2}

	if ab1.EventsHash() == ab2.EventsHash() {
		t.Fatal("EventsHash should differ when Topic differs")
	}
}

func TestEventsHash_DistinguishesData(t *testing.T) {
	addr := types.Address{2, 1}
	topic := types.Hash{1}

	events1 := []AccountBlockEvent{makeEvent(addr, topic, false, []byte("aaa"))}
	events2 := []AccountBlockEvent{makeEvent(addr, topic, false, []byte("bbb"))}

	ab1 := &AccountBlock{Events: events1}
	ab2 := &AccountBlock{Events: events2}

	if ab1.EventsHash() == ab2.EventsHash() {
		t.Fatal("EventsHash should differ when Data differs")
	}
}

// ---------------------------------------------------------------------------
// ComputeHash gate tests
// ---------------------------------------------------------------------------

func TestComputeHash_V1IgnoresEvents(t *testing.T) {
	// A v1 block with events should produce the same hash as v1 without events,
	// because the events branch only triggers for Version >= WasmAccountBlockVersion.
	blk := makeV1Block()
	hashNoEvents := blk.ComputeHash()

	blk.Events = []AccountBlockEvent{
		makeEvent(types.Address{2, 1}, types.Hash{1}, false, []byte("data")),
	}
	hashWithEvents := blk.ComputeHash()

	if hashNoEvents != hashWithEvents {
		t.Fatal("v1 ComputeHash should ignore events")
	}
}

func TestComputeHash_V3AppendsEventsHash(t *testing.T) {
	events := []AccountBlockEvent{
		makeEvent(types.Address{2, 1}, types.Hash{1}, false, []byte("data")),
	}
	blk := makeV3Block(events)
	hashWith := blk.ComputeHash()

	// Same block without events: should produce a different hash because v3
	// appends EventsHash to the hash input.
	blkNoEvents := makeV3Block(nil)
	hashWithout := blkNoEvents.ComputeHash()

	if hashWith == hashWithout {
		t.Fatal("v3 ComputeHash should differ when events are present vs absent")
	}
}

func TestComputeHash_V3EmptyEventsAppendsZeroHash(t *testing.T) {
	// v3 with empty events: EventsHash() == zero hash, which is still appended.
	blkEmpty := makeV3Block([]AccountBlockEvent{})
	hashEmpty := blkEmpty.ComputeHash()

	// v3 with nil events: same result (EventsHash() == zero hash).
	blkNil := makeV3Block(nil)
	hashNil := blkNil.ComputeHash()

	if hashEmpty != hashNil {
		t.Fatal("v3 ComputeHash with empty vs nil events should be identical")
	}

	// Verify the zero-hash is actually appended by comparing against a manually
	// constructed hash without the events branch. We can do this by temporarily
	// setting Version to 1 (which skips the events branch).
	saved := blkEmpty.Version
	blkEmpty.Version = 1
	hashV1 := blkEmpty.ComputeHash()
	blkEmpty.Version = saved

	if hashEmpty == hashV1 {
		t.Fatal("v3 ComputeHash with empty events should differ from v1 (zero hash appended)")
	}
}

func TestComputeHash_V3EventsOrderMatters(t *testing.T) {
	events1 := []AccountBlockEvent{
		makeEvent(types.Address{2, 1}, types.Hash{1}, false, []byte("a")),
		makeEvent(types.Address{2, 1}, types.Hash{2}, false, []byte("b")),
	}
	events2 := []AccountBlockEvent{
		makeEvent(types.Address{2, 1}, types.Hash{2}, false, []byte("b")),
		makeEvent(types.Address{2, 1}, types.Hash{1}, false, []byte("a")),
	}

	blk1 := makeV3Block(events1)
	blk2 := makeV3Block(events2)

	if blk1.ComputeHash() == blk2.ComputeHash() {
		t.Fatal("v3 ComputeHash should be order-dependent for events")
	}
}

// ---------------------------------------------------------------------------
// Proto round-trip tests
// ---------------------------------------------------------------------------

func TestProtoRoundTrip_WithEvents(t *testing.T) {
	events := []AccountBlockEvent{
		makeEvent(types.Address{2, 1, 3}, types.Hash{1, 2, 3}, false, []byte("hello")),
		makeEvent(types.Address{2, 2, 4}, types.Hash{4, 5, 6}, true, []byte("world")),
	}
	orig := makeV3Block(events)
	orig.Hash = orig.ComputeHash()

	data, err := orig.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	decoded, err := DeserializeAccountBlock(data)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}

	if decoded.Hash != orig.Hash {
		t.Fatal("hash mismatch after proto round-trip")
	}
	if len(decoded.Events) != len(orig.Events) {
		t.Fatalf("event count mismatch: got %d, want %d", len(decoded.Events), len(orig.Events))
	}
	for i := range orig.Events {
		if decoded.Events[i].ContractAddress != orig.Events[i].ContractAddress {
			t.Fatalf("event %d ContractAddress mismatch", i)
		}
		if decoded.Events[i].Topic != orig.Events[i].Topic {
			t.Fatalf("event %d Topic mismatch", i)
		}
		if decoded.Events[i].Indexed != orig.Events[i].Indexed {
			t.Fatalf("event %d Indexed mismatch", i)
		}
		if !bytes.Equal(decoded.Events[i].Data, orig.Events[i].Data) {
			t.Fatalf("event %d Data mismatch", i)
		}
	}
}

func TestProtoRoundTrip_EmptyEvents(t *testing.T) {
	orig := makeV3Block(nil)
	orig.Hash = orig.ComputeHash()

	data, err := orig.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	decoded, err := DeserializeAccountBlock(data)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}

	if decoded.Hash != orig.Hash {
		t.Fatal("hash mismatch after proto round-trip with empty events")
	}
	if len(decoded.Events) != 0 {
		t.Fatalf("expected 0 events after round-trip, got %d", len(decoded.Events))
	}
}

func TestProtoRoundTrip_V1NoEvents(t *testing.T) {
	orig := makeV1Block()
	orig.Hash = orig.ComputeHash()

	data, err := orig.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	decoded, err := DeserializeAccountBlock(data)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}

	if decoded.Hash != orig.Hash {
		t.Fatal("hash mismatch after proto round-trip for v1 block")
	}
}

// ---------------------------------------------------------------------------
// JSON round-trip tests
// ---------------------------------------------------------------------------

func TestJSONRoundTrip_WithEvents(t *testing.T) {
	events := []AccountBlockEvent{
		makeEvent(types.Address{2, 1, 3}, types.Hash{1, 2, 3}, false, []byte("hello")),
		makeEvent(types.Address{2, 2, 4}, types.Hash{4, 5, 6}, true, []byte("world")),
	}
	orig := makeV3Block(events)
	orig.Hash = orig.ComputeHash()

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	decoded := &AccountBlock{}
	if err := json.Unmarshal(data, decoded); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	if decoded.Hash != orig.Hash {
		t.Fatal("hash mismatch after JSON round-trip")
	}
	if len(decoded.Events) != len(orig.Events) {
		t.Fatalf("event count mismatch: got %d, want %d", len(decoded.Events), len(orig.Events))
	}
	for i := range orig.Events {
		if decoded.Events[i].ContractAddress != orig.Events[i].ContractAddress {
			t.Fatalf("event %d ContractAddress mismatch", i)
		}
		if decoded.Events[i].Topic != orig.Events[i].Topic {
			t.Fatalf("event %d Topic mismatch", i)
		}
		if decoded.Events[i].Indexed != orig.Events[i].Indexed {
			t.Fatalf("event %d Indexed mismatch", i)
		}
		if !bytes.Equal(decoded.Events[i].Data, orig.Events[i].Data) {
			t.Fatalf("event %d Data mismatch", i)
		}
	}
}

func TestJSONRoundTrip_EmptyEvents(t *testing.T) {
	orig := makeV3Block(nil)
	orig.Hash = orig.ComputeHash()

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	decoded := &AccountBlock{}
	if err := json.Unmarshal(data, decoded); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	if decoded.Hash != orig.Hash {
		t.Fatal("hash mismatch after JSON round-trip with empty events")
	}
}

// ---------------------------------------------------------------------------
// Copy() deep-copy tests
// ---------------------------------------------------------------------------

func TestCopy_DeepCopiesEvents(t *testing.T) {
	events := []AccountBlockEvent{
		makeEvent(types.Address{2, 1}, types.Hash{1}, false, []byte("mutable")),
		makeEvent(types.Address{2, 2}, types.Hash{2}, true, []byte{0, 1, 2, 3}),
	}
	orig := makeV3Block(events)
	orig.Hash = orig.ComputeHash()

	copied := orig.Copy()

	// Hash should be identical.
	if copied.Hash != orig.Hash {
		t.Fatal("Copy hash mismatch")
	}

	// Mutate the original's events — the copy should be unaffected.
	orig.Events[0].Data[0] = 0xFF
	mutatedAddr := types.Address{9, 9, 9}
	orig.Events[0].ContractAddress = mutatedAddr
	orig.Events[1].Indexed = false

	if copied.Events[0].Data[0] == 0xFF {
		t.Fatal("Copy did not deep-copy event Data")
	}
	if copied.Events[0].ContractAddress == mutatedAddr {
		t.Fatal("Copy did not deep-copy event ContractAddress")
	}
	if copied.Events[1].Indexed != true {
		t.Fatal("Copy did not deep-copy event Indexed")
	}

	// Verify the copy's hash is still correct.
	if copied.ComputeHash() != copied.Hash {
		t.Fatal("copied block hash invalidated after mutation")
	}
}

// ---------------------------------------------------------------------------
// Codec drop detection
// ---------------------------------------------------------------------------

func TestEventsHash_StableAcrossCodecs(t *testing.T) {
	events := []AccountBlockEvent{
		makeEvent(types.Address{2, 1, 3}, types.Hash{1, 2, 3}, false, []byte("hello")),
		makeEvent(types.Address{2, 2, 4}, types.Hash{4, 5, 6}, true, []byte("world")),
	}
	orig := makeV3Block(events)
	orig.Hash = orig.ComputeHash()

	// Proto round-trip.
	protoData, err := orig.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	protoDecoded, err := DeserializeAccountBlock(protoData)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}

	// JSON round-trip.
	jsonData, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	jsonDecoded := &AccountBlock{}
	if err := json.Unmarshal(jsonData, jsonDecoded); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	// Copy.
	copyDecoded := orig.Copy()

	// All three codecs must produce blocks with the same ComputeHash.
	origHash := orig.ComputeHash()
	protoHash := protoDecoded.ComputeHash()
	jsonHash := jsonDecoded.ComputeHash()
	copyHash := copyDecoded.ComputeHash()

	if origHash != protoHash {
		t.Fatalf("proto codec changed ComputeHash: orig=%x proto=%x", origHash, protoHash)
	}
	if origHash != jsonHash {
		t.Fatalf("JSON codec changed ComputeHash: orig=%x json=%x", origHash, jsonHash)
	}
	if origHash != copyHash {
		t.Fatalf("Copy changed ComputeHash: orig=%x copy=%x", origHash, copyHash)
	}
}

// ---------------------------------------------------------------------------
// Sync compatibility (sync compat)
// ---------------------------------------------------------------------------

func TestSyncCompat_OldBinaryIgnoresEvents(t *testing.T) {
	// A post-spork v3 block with events, when decoded by an old binary that
	// doesn't know about field 24, will have empty Events and compute the
	// pre-spork hash. This is the expected sync-compat behavior: the old binary
	// sees a hash mismatch and rejects the block (no panic on unknown field).
	events := []AccountBlockEvent{
		makeEvent(types.Address{2, 1}, types.Hash{1}, false, []byte("data")),
	}
	blk := makeV3Block(events)
	blk.Hash = blk.ComputeHash()

	// Serialize via proto.
	data, err := blk.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	// Decode. In reality, protobuf silently ignores unknown fields, so the
	// decoded block will have Events populated (field 24 is known). But we
	// simulate the old-binary behavior by clearing events after decode.
	decoded, err := DeserializeAccountBlock(data)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}

	// Verify events survived the round-trip (proto knows field 24).
	if len(decoded.Events) != 1 {
		t.Fatalf("expected 1 event after round-trip, got %d", len(decoded.Events))
	}

	// Simulate old binary: clear events and compute v1-style hash.
	decoded.Events = nil
	decoded.Version = 1
	oldBinaryHashV1 := decoded.ComputeHash()

	// The v1 hash without events should differ from the v3 hash with events.
	if blk.Hash == oldBinaryHashV1 {
		t.Fatal("old-binary v1 hash should differ from v3 hash with events (sync-compat)")
	}

	// The v3 hash without events should also differ (zero hash appended).
	decoded.Version = WasmAccountBlockVersion
	oldBinaryHashNoEvents := decoded.ComputeHash()
	if blk.Hash == oldBinaryHashNoEvents {
		t.Fatal("v3 hash with events should differ from v3 hash without events")
	}
}
