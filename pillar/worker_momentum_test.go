package pillar

import "testing"

// TestMomentumVersionTriState mirrors verifier.rawMomentumVerifier.version's tri-state switch:
// state root wins over dynamic plasma, which wins over the legacy version.
func TestMomentumVersionTriState(t *testing.T) {
	cases := []struct {
		name    string
		dp, sr  bool
		version uint64
	}{
		{"neither active", false, false, 1},
		{"dynamic plasma only", true, false, 2},
		{"both active", true, true, 3},
		{"state root only", false, true, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := momentumVersion(c.dp, c.sr); got != c.version {
				t.Fatalf("momentumVersion(dp=%v,sr=%v) = %d, want %d", c.dp, c.sr, got, c.version)
			}
		})
	}
}

// TestStateRootProducible pins the not-ready producer guard: a not-ready state tree suppresses
// v3 stamping/enforcement exactly as if the spork were inactive, regardless of the spork state.
func TestStateRootProducible(t *testing.T) {
	cases := []struct {
		name              string
		isStateRootActive bool
		stateTreeReady    bool
		want              bool
	}{
		{"spork inactive, tree ready", false, true, false},
		{"spork active, tree not ready", true, false, false},
		{"spork active, tree ready", true, true, true},
		{"spork inactive, tree not ready", false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stateRootProducible(c.isStateRootActive, c.stateTreeReady); got != c.want {
				t.Fatalf("stateRootProducible(active=%v,ready=%v) = %v, want %v", c.isStateRootActive, c.stateTreeReady, got, c.want)
			}
		})
	}
}
