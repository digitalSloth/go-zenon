package wasm

import (
	"testing"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
)

func TestEventCollector_Emit(t *testing.T) {
	ec := NewEventCollector(types.Address{2, 1, 2, 3}, definition.DefaultWasmVariables())

	topic := types.Hash{1, 2, 3}
	data := []byte("hello")

	if err := ec.Emit(topic, data, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events := ec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Topic != topic {
		t.Fatal("topic mismatch")
	}
	if string(events[0].Data) != "hello" {
		t.Fatal("data mismatch")
	}
}

func TestEventCollector_MaxEvents(t *testing.T) {
	vars := definition.DefaultWasmVariables()
	ec := NewEventCollector(types.Address{2, 1}, vars)
	topic := types.Hash{1}

	for i := 0; i < int(vars.MaxEventsPerExecute); i++ {
		if err := ec.Emit(topic, []byte{1}, false); err != nil {
			t.Fatalf("event %d: unexpected error: %v", i, err)
		}
	}

	// 257th should fail.
	if err := ec.Emit(topic, []byte{1}, false); err != ErrMaxEvents {
		t.Fatalf("expected ErrMaxEvents, got %v", err)
	}
}

func TestEventCollector_MaxTotalBytes(t *testing.T) {
	vars := definition.DefaultWasmVariables()
	ec := NewEventCollector(types.Address{2, 1}, vars)
	topic := types.Hash{1}

	// Fill up to the limit with 1KB events.
	chunkSize := 1024
	maxEvents := int(vars.MaxEventBytesPerExecute) / chunkSize
	for i := 0; i < maxEvents; i++ {
		data := make([]byte, chunkSize)
		if err := ec.Emit(topic, data, false); err != nil {
			t.Fatalf("event %d: unexpected error: %v", i, err)
		}
	}

	// Next small event should fail.
	if err := ec.Emit(topic, []byte{1}, false); err != ErrMaxEventBytes {
		t.Fatalf("expected ErrMaxEventBytes, got %v", err)
	}
}

func TestEventCollector_MaxDataPerEvent(t *testing.T) {
	vars := definition.DefaultWasmVariables()
	ec := NewEventCollector(types.Address{2, 1}, vars)
	topic := types.Hash{1}

	data := make([]byte, int(vars.MaxEventDataPerEvent)+1)
	if err := ec.Emit(topic, data, false); err != ErrMaxEventData {
		t.Fatalf("expected ErrMaxEventData, got %v", err)
	}
}

func TestAccountBlock_EventsHash_Deterministic(t *testing.T) {
	topic := types.Hash{1, 2, 3}
	addr := types.Address{2, 1}
	data := []byte("test")

	events := []nom.AccountBlockEvent{
		{ContractAddress: addr, Topic: topic, Indexed: false, Data: data},
	}

	ab1 := &nom.AccountBlock{Events: events}
	ab2 := &nom.AccountBlock{Events: events}

	h1 := ab1.EventsHash()
	h2 := ab2.EventsHash()

	if h1 != h2 {
		t.Fatal("EventsHash not deterministic")
	}
}

func TestAccountBlock_EventsHash_Empty(t *testing.T) {
	ab := &nom.AccountBlock{}
	h := ab.EventsHash()
	if h != (types.Hash{}) {
		t.Fatal("empty EventsHash should be zero")
	}
}
