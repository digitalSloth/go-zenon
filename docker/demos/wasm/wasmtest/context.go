package wasmtest

import (
	"math/big"

	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/vm_context"
)

// Context is a minimal mock of AccountVmContext for in-process contract testing.
// It embeds the interface as nil — any method NOT overridden will panic if
// touched, which tells the author exactly which host dependency a given
// contract exercises.
type Context struct {
	vm_context.AccountVmContext

	storage  db.DB
	balances map[types.ZenonTokenStandard]*big.Int
	momentum *nom.Momentum
}

// NewContext creates a test context with sane defaults.
func NewContext() *Context {
	return &Context{
		storage:  db.NewMemDB(),
		balances: make(map[types.ZenonTokenStandard]*big.Int),
		momentum: &nom.Momentum{
			Height:         100,
			TimestampUnix:  1700000000,
		},
	}
}

func (c *Context) WithBalance(token types.ZenonTokenStandard, amount *big.Int) *Context {
	c.balances[token] = new(big.Int).Set(amount)
	return c
}

func (c *Context) WithMomentum(height uint64, timestamp uint64) *Context {
	c.momentum = &nom.Momentum{
		Height:        height,
		TimestampUnix: timestamp,
	}
	return c
}

// --- Methods the WASM runtime actually calls ---

func (c *Context) Storage() db.DB { return c.storage }

// GetBalance returns the full balance.
func (c *Context) GetBalance(zts types.ZenonTokenStandard) (*big.Int, error) {
	return new(big.Int).Set(c.bal(zts)), nil
}

func (c *Context) SubBalance(zts *types.ZenonTokenStandard, amount *big.Int) {
	c.balances[*zts] = new(big.Int).Sub(c.bal(*zts), amount)
}

func (c *Context) GetFrontierMomentum() (*nom.Momentum, error) {
	return c.momentum, nil
}

func (c *Context) IsDynamicPlasmaSporkEnforced() bool  { return true }
func (c *Context) IsWasmRuntimeSporkEnforced() bool    { return true }

// --- helpers ---

func (c *Context) bal(zts types.ZenonTokenStandard) *big.Int {
	if v, ok := c.balances[zts]; ok {
		return v
	}
	return big.NewInt(0)
}
