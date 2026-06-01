package wasmtest

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
)

func AssertResult(t *testing.T, r Result) {
	t.Helper()
	if r.Err != nil {
		t.Fatalf("execution error: %v", r.Err)
	}
	if r.Code != 0 {
		t.Fatalf("expected success (code 0), got %d", r.Code)
	}
}

func AssertTrap(t *testing.T, r Result) {
	t.Helper()
	if r.Err == nil && r.Code == 0 {
		t.Fatal("expected trap or non-zero code, got success")
	}
}

func AssertEvent(t *testing.T, r Result, topic types.Hash, data []byte) {
	t.Helper()
	for _, ev := range r.Events {
		if ev.Topic == topic && bytes.Equal(ev.Data, data) {
			return
		}
	}
	t.Fatalf("expected event with topic %s and data %s, not found in %d events",
		topic.String(), hex.EncodeToString(data), len(r.Events))
}

func AssertEventCount(t *testing.T, r Result, expected int) {
	t.Helper()
	if len(r.Events) != expected {
		t.Fatalf("expected %d events, got %d", expected, len(r.Events))
	}
}

func AssertTransfer(t *testing.T, r Result, to types.Address, zts types.ZenonTokenStandard, amount int64) {
	t.Helper()
	for _, tx := range r.Transfers {
		if tx.ToAddress == to && tx.TokenStandard == zts && tx.Amount.Int64() == amount {
			return
		}
	}
	t.Fatalf("expected transfer to %s of %d %s, not found in %d transfers",
		to.String(), amount, zts.String(), len(r.Transfers))
}

func AssertGasAtMost(t *testing.T, r Result, maxGas uint64) {
	t.Helper()
	if r.GasUsed > maxGas {
		t.Fatalf("expected gas used <= %d, got %d", maxGas, r.GasUsed)
	}
}

func AssertViewEquals(t *testing.T, data []byte, expected uint64) {
	t.Helper()
	val, ok := DecodeViewU64(data)
	if !ok {
		t.Fatal("failed to decode view return as u64")
	}
	if val != expected {
		t.Fatalf("expected view value %d, got %d", expected, val)
	}
}

func AssertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func AssertError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func AssertEqual(t *testing.T, got, want interface{}) {
	t.Helper()
	if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}
