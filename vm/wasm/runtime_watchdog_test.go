package wasm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
)

// classifyExecErr must give the DETERMINISTIC out-of-gas signal priority over the
// non-deterministic wall-clock one: a call that exhausted its gas is reported as
// out-of-gas even if the deadline also elapsed, so it is never reclassified into
// the node-local wall-clock fault (which would let slow nodes diverge from fast
// ones on an otherwise-deterministic result).
func TestClassifyExecErr_Priority(t *testing.T) {
	live := context.Background()
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()
	if expired.Err() != context.DeadlineExceeded {
		t.Fatalf("setup: expected DeadlineExceeded, got %v", expired.Err())
	}
	guestErr := errors.New("guest trap")

	cases := []struct {
		name      string
		ctx       context.Context
		remaining uint64
		want      error
	}{
		{"out-of-gas, live ctx", live, 0, ErrOutOfGas},
		{"out-of-gas wins even when deadline elapsed", expired, 0, ErrOutOfGas},
		{"wall-clock only when gas remains", expired, 100, ErrWallClockExceeded},
		{"plain guest error otherwise", live, 100, guestErr},
	}
	for _, tc := range cases {
		if got := classifyExecErr(tc.ctx, guestErr, tc.remaining); got != tc.want {
			t.Errorf("%s: want %v, got %v", tc.name, tc.want, got)
		}
	}
}

// End-to-end: the singleton runtime must actually be armed with the wall-clock
// watchdog (WithCloseOnContextDone). With gas effectively disabled, an infinite
// loop has to be stopped by the 1s wall-clock limit and surface as the
// node-local ErrWallClockExceeded — not run forever, and not out-of-gas.
func TestRuntime_WatchdogArmedAndReports(t *testing.T) {
	if testing.Short() {
		t.Skip("waits ~1s for the wall-clock limit")
	}
	fake := newFakeBalCtx()
	vars := definition.DefaultWasmVariables()
	vars.ExecutionGasLimit = 1 << 60 // remove gas as the bound; only the watchdog can stop it
	wc := NewViewContext(fake, types.Address{}, vars)

	start := time.Now()
	_, err := GetWasmRuntime("").CallView(wc, infiniteLoopModule(), nil)
	elapsed := time.Since(start)

	if err != ErrWallClockExceeded {
		t.Fatalf("want ErrWallClockExceeded, got %v (after %v)", err, elapsed)
	}
	if elapsed < 500*time.Millisecond {
		t.Fatalf("stopped after only %v — gas bound it, not the wall-clock watchdog", elapsed)
	}
}
