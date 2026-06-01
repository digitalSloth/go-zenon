package testmodules

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// TestModulesCompile verifies that each test module compiles under wazero.
func TestModulesCompile(t *testing.T) {
	tests := []struct {
		name   string
		module func() []byte
	}{
		{"CounterModule", CounterModule},
		{"EventModule", EventModule},
		{"RunawayModule", RunawayModule},
		{"ViewModule", ViewModule},
		{"EchoExecuteModule", EchoExecuteModule},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod := tt.module()
			if len(mod) == 0 {
				t.Fatal("module is empty")
			}

			config := wazero.NewRuntimeConfigInterpreter()
			rt := wazero.NewRuntimeWithConfig(context.Background(), config)
			defer rt.Close(context.Background())

			// Instantiate a no-op host module so imports resolve.
			builder := rt.NewHostModuleBuilder("env")
			builder.NewFunctionBuilder().
				WithFunc(func(ctx context.Context, m api.Module, a, b, c, d uint32) uint32 { return 0 }).
				Export("state_read")
			builder.NewFunctionBuilder().
				WithFunc(func(ctx context.Context, m api.Module, a, b, c, d uint32) uint32 { return 0 }).
				Export("state_write")
			builder.NewFunctionBuilder().
				WithFunc(func(ctx context.Context, m api.Module, a, b, c, d uint32) uint32 { return 0 }).
				Export("emit_event")
			_, err := builder.Instantiate(context.Background())
			if err != nil {
				t.Fatalf("failed to instantiate host module: %v", err)
			}

			compiled, err := rt.CompileModule(context.Background(), mod)
			if err != nil {
				t.Fatalf("failed to compile module: %v", err)
			}
			defer compiled.Close(context.Background())

			_, err = rt.InstantiateModule(context.Background(), compiled, wazero.NewModuleConfig())
			if err != nil {
				t.Fatalf("failed to instantiate module: %v", err)
			}
		})
	}
}
