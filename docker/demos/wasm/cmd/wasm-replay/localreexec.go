package main

import (
	"fmt"
)

// localReexec re-derives a committed execution's changesHash locally
// and compares it to the committed value.
func localReexec() error {
	fmt.Println("Local re-execution mode")
	fmt.Println("This mode requires a node datadir or the in-process harness.")
	fmt.Println("For v1, cross-pillar mode is the primary determinism proof.")
	return fmt.Errorf("local re-exec mode not yet implemented — use --mode cross-pillar")
}
