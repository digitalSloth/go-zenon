package wasm

import (
	"errors"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
)

var (
	ErrMaxEvents     = errors.New("max events per execute exceeded")
	ErrMaxEventBytes = errors.New("max event bytes per execute exceeded")
	ErrMaxEventData  = errors.New("max data per event exceeded")
)

type WasmEvent struct {
	ContractAddress types.Address
	Topic           types.Hash
	Indexed         bool
	Data            []byte
}

type EventCollector struct {
	events       []WasmEvent
	totalBytes   int
	contractAddr types.Address
	vars         *definition.WasmVariables
}

func NewEventCollector(contractAddr types.Address, vars *definition.WasmVariables) *EventCollector {
	return &EventCollector{
		events:       make([]WasmEvent, 0, 16),
		contractAddr: contractAddr,
		vars:         vars,
	}
}

func (ec *EventCollector) Emit(topic types.Hash, data []byte, indexed bool) error {
	if len(ec.events) >= int(ec.vars.MaxEventsPerExecute) {
		return ErrMaxEvents
	}
	if len(data) > int(ec.vars.MaxEventDataPerEvent) {
		return ErrMaxEventData
	}
	if ec.totalBytes+len(data) > int(ec.vars.MaxEventBytesPerExecute) {
		return ErrMaxEventBytes
	}

	ec.events = append(ec.events, WasmEvent{
		ContractAddress: ec.contractAddr,
		Topic:           topic,
		Indexed:         indexed,
		Data:            data,
	})
	ec.totalBytes += len(data)
	return nil
}

func (ec *EventCollector) Events() []WasmEvent {
	return ec.events
}


