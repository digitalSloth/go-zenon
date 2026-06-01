package vm

import (
	"math/big"

	"github.com/pkg/errors"

	"github.com/zenon-network/go-zenon/chain"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/db"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/dp"
	"github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/vm/vm_context"
	"github.com/zenon-network/go-zenon/vm/wasm"
)

var (
	log = common.VmLogger
)

const (
	resultInvalid uint64 = iota
	resultSuccess
	resultFail
)

func errToStatus(err error) uint64 {
	switch err {
	case nil:
		return resultSuccess
	default:
		return resultFail
	}
}

type VM struct {
	context vm_context.AccountVmContext
}

func NewVM(context vm_context.AccountVmContext) *VM {
	return &VM{
		context: context,
	}
}

func enoughPlasma(context vm_context.AccountVmContext, block *nom.AccountBlock) error {
	// embedded address have unlimited plasma
	if types.IsContractAddress(block.Address) {
		return nil
	}

	var available uint64
	var err error
	if context.IsDynamicPlasmaSporkEnforced() {
		available, err = AvailablePlasmaV2(context.MomentumStore(), context)
	} else {
		available, err = AvailablePlasma(context.MomentumStore(), context)
	}
	common.DealWithErr(err)
	if available < block.FusedPlasma {
		return constants.ErrNotEnoughPlasma
	}

	var powPlasma uint64
	if context.IsDynamicPlasmaSporkEnforced() {
		powPlasma = dp.DifficultyToPlasma(block.Difficulty)
	} else {
		powPlasma = DifficultyToPlasma(block.Difficulty)
	}
	block.TotalPlasma = powPlasma + block.FusedPlasma
	if block.TotalPlasma > constants.MaxPlasmaForAccountBlock {
		return constants.ErrBlockPlasmaLimitReached
	}

	block.BasePlasma, err = GetBasePlasmaForAccountBlock(context, block)
	common.DealWithErr(err)

	if block.TotalPlasma < block.BasePlasma {
		return constants.ErrNotEnoughTotalPlasma
	}

	return context.AddChainPlasma(block.FusedPlasma)
}
func enoughFunds(context vm_context.AccountVmContext, block *nom.AccountBlock) bool {
	if block.TokenStandard == types.ZeroTokenStandard {
		return true
	}

	balance, err := context.GetBalance(block.TokenStandard)
	common.DealWithErr(err)
	if balance.Cmp(block.Amount) == -1 {
		return false
	}

	return true
}

// applyBlock is used to apply the block on top of the vm.context
// After calling applyBlock vm.context.Changes() has all the changes necessary to create a nom.AccountBlockTransaction
func (vm *VM) applyBlock(block *nom.AccountBlock) error {
	if err := enoughPlasma(vm.context, block); err != nil {
		return err
	}

	// In case vm will update some fields of block, make a copy of block.
	switch block.BlockType {
	case nom.BlockTypeUserSend, nom.BlockTypeContractSend:
		return vm.applySend(block)
	case nom.BlockTypeUserReceive:
		return vm.applyReceive(block)
	case nom.BlockTypeContractReceive:
		generated, _, err := vm.generateEmbeddedReceive(block.FromBlockHash)
		if err != nil {
			return err
		}
		if generated.ChangesHash != block.ChangesHash {
			return errors.Errorf("auto-received block has different changes-hash expected %v but got %v", generated.ChangesHash, block.ChangesHash)
		}
		computed := generated.ComputeHash()
		if computed != block.Hash {
			return errors.Errorf("auto-received block has different hash expected %v but got %v", computed, generated)
		}
		return nil
	default:
		panic("unknown block type")
	}
}
func (vm *VM) applySend(block *nom.AccountBlock) error {
	// Pre-spork 0x02 rejection
	if types.IsWasmContractAddress(block.ToAddress) && !vm.context.IsWasmRuntimeSporkEnforced() {
		return constants.ErrWasmNotActivated
	}
	// Bytecode-exists check (post-spork). WasmContract (0x01) is exempt — it is
	// the deploy authority and only sends to 0x02 addresses it is deploying in
	// the same receive, before the bytecode is in the committed snapshot.
	if types.IsWasmContractAddress(block.ToAddress) && block.Address != types.WasmContract {
		// Pause check: if the contract is paused, only the deployer may send.
		// The pause key stores the deployer address, so no metadata lookup needed.
		wasmStore := vm.context.MomentumStore().GetAccountStore(types.WasmContract)
		deployer, err := definition.GetWasmPausedDeployer(wasmStore.Storage(), block.ToAddress)
		if err != nil {
			return err
		}
		if deployer != nil && block.Address != *deployer {
			return constants.ErrWasmContractPaused
		}

		if !vm.context.WasmContractHasBytecode(block.ToAddress) {
			return constants.ErrWasmContractNotDeployed
		}
	}

	// Plain token transfers to a 0x02 contract (empty Data) skip embedded-method
	// dispatch — GetEmbeddedMethod would reject them on ABI resolution.
	plainWasmTransfer := types.IsWasmContractAddress(block.ToAddress) && len(block.Data) == 0

	// check can make transaction
	if !plainWasmTransfer {
		if method, err := embedded.GetEmbeddedMethod(vm.context, block.ToAddress, block.Data); err != constants.ErrNotContractAddress {
			if err != nil {
				return err
			}

			// validate block
			err = method.ValidateSendBlock(block)
			if err != nil {
				return err
			}
		}
	}

	// affect balance
	if !enoughFunds(vm.context, block) {
		return constants.ErrInsufficientBalance
	}

	vm.context.SubBalance(&block.TokenStandard, block.Amount)

	return nil
}
func (vm *VM) applyReceive(block *nom.AccountBlock) error {
	fromBlock, err := vm.context.MomentumStore().GetAccountBlockByHash(block.FromBlockHash)
	if err != nil {
		return err
	}

	err = vm.context.MarkAsReceived(block.FromBlockHash)
	if err != nil {
		return err
	}

	vm.context.AddBalance(&fromBlock.TokenStandard, fromBlock.Amount)
	return nil
}

// generateEmbeddedReceive is used to generate the embedded receive nom.AccountBlock from an fromBlockHash
// Since the receive-block is auto-generated, we don't actually need the whole block (just the fromBlockHash)
// After calling applyBlock vm.context.Changes() has all the changes necessary to create a nom.AccountBlockTransaction
func (vm *VM) generateEmbeddedReceive(fromBlockHash types.Hash) (*nom.AccountBlock, error, error) {
	// mark block as received (only for contracts, using sequencer)
	vm.context.SequencerPopFront()

	sendBlock, err := vm.context.MomentumStore().GetAccountBlockByHash(fromBlockHash)
	if err != nil {
		return nil, nil, err
	}

	// Plain token receive to a 0x02 contract.
	if types.IsWasmContractAddress(sendBlock.ToAddress) && len(sendBlock.Data) == 0 {
		vm.context.Save()
		vm.context.AddBalance(&sendBlock.TokenStandard, sendBlock.Amount)

		// Try on_receive hook — silent no-op if the contract doesn't export it.
		var descendantBlocks []*nom.AccountBlock
		if vm.context.IsWasmRuntimeSporkEnforced() {
			var hookErr error
			descendantBlocks, hookErr = vm.tryOnReceive(sendBlock)
			if hookErr == wasm.ErrWallClockExceeded {
				// Non-deterministic watchdog trip: abort the whole receive as a
				// node-local fault rather than committing a divergent block. (The
				// usual keep-balance-on-hook-failure rule below is for DETERMINISTIC
				// hook failures only.)
				return nil, nil, hookErr
			}
			if hookErr != nil {
				// Hook trapped — roll back hook state, but keep the balance credit.
				vm.context.Reset()
				vm.context.Save()
				vm.context.AddBalance(&sendBlock.TokenStandard, sendBlock.Amount)
				descendantBlocks = nil
			}
		}

		// Process any descendant transfers queued by the hook.
		for _, dblock := range descendantBlocks {
			if err := vm.applySend(dblock); err != nil {
				return vm.rollbackEmbedded(fromBlockHash, err)
			}
		}

		vm.context.Done()
		return vm.finalizeEmbedded(fromBlockHash, descendantBlocks, vm.context.Events(), nil)
	}

	method, err := embedded.GetEmbeddedMethod(vm.context, sendBlock.ToAddress, sendBlock.Data)

	// can happen when a method is deleted in a spork (height 100) and someone calls it before the spork (height 95)
	// and the autoReceive uses momentum height 105 for various reasons
	if err == constants.ErrContractMethodNotFound {
		return vm.rollbackEmbedded(fromBlockHash, err)
	}

	vm.context.Save()
	// balance
	vm.context.AddBalance(&sendBlock.TokenStandard, sendBlock.Amount)
	// call code
	descendantBlocks, err := method.ReceiveBlock(vm.context, sendBlock)
	if err != nil {
		// The wall-clock watchdog is non-deterministic across nodes, so a trip
		// must never be folded into a committed (failed) block — that would fork
		// the chain. Surface it as a fatal, node-local error that aborts this
		// receive entirely (see classifyExecErr / supervisor / applyBlock).
		if err == wasm.ErrWallClockExceeded {
			return nil, nil, err
		}
		return vm.rollbackEmbedded(fromBlockHash, err)
	}
	// apply send-descendant-blocks
	for _, dblock := range descendantBlocks {
		err := vm.applySend(dblock)
		if err != nil {
			return vm.rollbackEmbedded(fromBlockHash, err)
		}
	}

	// everything went right, no rollback required
	vm.context.Done()
	return vm.finalizeEmbedded(fromBlockHash, descendantBlocks, vm.context.Events(), nil)
}
// tryOnReceive runs the on_receive hook on a 0x02 contract. Returns nil if the
// contract has no on_receive export, is halted, is revoked, or has no bytecode.
// On success, events from the hook are set on vm.context, and any queued
// transfers are returned as descendant send blocks.
func (vm *VM) tryOnReceive(sendBlock *nom.AccountBlock) ([]*nom.AccountBlock, error) {
	wasmStore := vm.context.MomentumStore().GetAccountStore(types.WasmContract)

	bytecode, err := definition.GetWasmBytecode(wasmStore.Storage(), sendBlock.ToAddress)
	if err != nil || len(bytecode) == 0 {
		return nil, nil
	}

	info, err := definition.GetWasmContractInfo(wasmStore.Storage())
	if err != nil {
		return nil, nil
	}
	if info.Halted {
		return nil, nil
	}

	revoked, err := definition.IsWasmRevoked(wasmStore.Storage(), sendBlock.ToAddress)
	if err != nil || revoked {
		return nil, nil
	}

	meta, err := definition.GetWasmContractMetadata(wasmStore.Storage(), sendBlock.ToAddress)
	if err != nil || meta == nil || !meta.Activated {
		return nil, nil
	}

	vars, err := definition.GetWasmVariables(wasmStore.Storage())
	if err != nil {
		return nil, nil
	}

	wc := wasm.NewWasmContext(vm.context, sendBlock, sendBlock.ToAddress, vars)

	args := serializeOnReceiveArgs(sendBlock)
	resultCode, execErr := wasm.GetWasmRuntime("").ExecuteOnReceive(wc, bytecode, args)
	if execErr != nil {
		return nil, execErr
	}
	if resultCode != 0 {
		return nil, constants.ErrWasmExecutionFailed
	}

	// Convert WASM events to nom-level events and store on the context.
	events := make([]nom.AccountBlockEvent, 0, len(wc.Events()))
	for _, e := range wc.Events() {
		events = append(events, nom.AccountBlockEvent{
			ContractAddress: e.ContractAddress,
			Topic:           e.Topic,
			Indexed:         e.Indexed,
			Data:            e.Data,
		})
	}
	vm.context.SetEvents(events)

	// Return queued transfers as descendant send blocks, plus the aggregated QSR
	// burn for any state growth the hook performed. StateWrite only accumulates
	// burnAmount (vm/wasm/context.go); the burn is realized here as a descendant
	// Burn block, exactly as ExecuteMethod does. Omitting it would let on_receive
	// grow persistent state without paying the QSR storage cost — a free-storage
	// griefing vector, since on_receive fires on any plain transfer.
	descendants := wc.Transfers()
	if wc.BurnAmount().Sign() > 0 {
		descendants = append(descendants, &nom.AccountBlock{
			ToAddress:     types.TokenContract,
			Data:          definition.ABIToken.PackMethodPanic(definition.BurnMethodName),
			TokenStandard: types.QsrTokenStandard,
			Amount:        new(big.Int).Set(wc.BurnAmount()),
		})
	}
	return descendants, nil
}

// serializeOnReceiveArgs packs the received token info into the args buffer
// for on_receive: zts (10 bytes) + amount (32 bytes big-endian) + from (20 bytes).
func serializeOnReceiveArgs(sendBlock *nom.AccountBlock) []byte {
	args := make([]byte, 0, 62)
	args = append(args, sendBlock.TokenStandard[:]...)
	args = append(args, common.BigIntToBytes(sendBlock.Amount)...)
	args = append(args, sendBlock.Address.Bytes()...)
	return args
}

func (vm *VM) rollbackEmbedded(fromBlockHash types.Hash, methodErr error) (*nom.AccountBlock, error, error) {
	sendBlock, err := vm.context.MomentumStore().GetAccountBlockByHash(fromBlockHash)
	common.DealWithErr(err) // impossible to not find send-block at rollback

	vm.context.Reset()
	// If sendBlock contains amount, add current amount to embedded to be able to refund it
	// This operation was rollbacked with vm.context.Reset()
	vm.context.AddBalance(&sendBlock.TokenStandard, sendBlock.Amount)
	descendantBlocks := make([]*nom.AccountBlock, 0, 1)

	// If sendBlock contained tokens, refund them
	if sendBlock.Amount.Sign() > 0 {
		dBlock := &nom.AccountBlock{
			BlockType:     nom.BlockTypeContractSend,
			Address:       sendBlock.ToAddress,
			ToAddress:     sendBlock.Address,
			Amount:        new(big.Int).Set(sendBlock.Amount),
			TokenStandard: sendBlock.TokenStandard,
		}

		err := vm.applySend(dBlock)
		if err != nil {
			log.Error("Unable to apply descendant blocks for refund", "reason", err, "send-block-hash", sendBlock.Hash)
			return nil, nil, err
		}

		descendantBlocks = append(descendantBlocks, dBlock)
	}

	return vm.finalizeEmbedded(fromBlockHash, descendantBlocks, nil, methodErr)
}
func (vm *VM) finalizeEmbedded(fromBlockHash types.Hash, descendantBlocks []*nom.AccountBlock, events []nom.AccountBlockEvent, executionError error) (*nom.AccountBlock, error, error) {
	var err error

	prevFrontier, err := vm.context.Frontier()
	common.DealWithErr(err)
	prevHash := types.ZeroHash
	height := uint64(1)
	if prevFrontier != nil {
		prevHash = prevFrontier.Hash
		height = prevFrontier.Height + 1
	}

	momentum, err := vm.context.MomentumStore().GetFrontierMomentum()
	common.DealWithErr(err)

	for _, dblock := range descendantBlocks {
		dblock.Version = 1
		dblock.ChainIdentifier = vm.context.MomentumStore().ChainIdentifier()
		dblock.BlockType = nom.BlockTypeContractSend
		dblock.Address = *vm.context.Address()
		dblock.MomentumAcknowledged = momentum.Identifier()
		dblock.PreviousHash = prevHash
		dblock.Height = height
		dblock.ChangesHash = types.ZeroHash
		dblock.Hash = dblock.ComputeHash()
		prevHash = dblock.Hash
		height = height + 1
	}

	changes, err := vm.context.Changes()
	common.DealWithErr(err)

	// Version 3 for contract-receive blocks under WasmRuntimeSpork; 1 otherwise.
	// Descendant sends stay version 1 (set above).
	blockVersion := uint64(1)
	if vm.context.IsWasmRuntimeSporkEnforced() {
		blockVersion = nom.WasmAccountBlockVersion
	}

	block := &nom.AccountBlock{
		Version:              blockVersion,
		ChainIdentifier:      vm.context.MomentumStore().ChainIdentifier(),
		BlockType:            nom.BlockTypeContractReceive,
		Address:              *vm.context.Address(),
		FromBlockHash:        fromBlockHash,
		MomentumAcknowledged: momentum.Identifier(),
		PreviousHash:         prevHash,
		Height:               height,
		Data:                 common.Uint64ToBytes(errToStatus(executionError)),
		DescendantBlocks:     descendantBlocks,
		Events:               events,
		ChangesHash:          db.PatchHash(changes),
	}

	block.Hash = block.ComputeHash()
	return block, executionError, nil
}

type MomentumVM struct {
	context vm_context.MomentumVMContext
}

func NewMomentumVM(context vm_context.MomentumVMContext) *MomentumVM {
	return &MomentumVM{
		context: context,
	}
}

func (vm *MomentumVM) applyMomentum(pool chain.AccountPool, momentum *nom.Momentum) error {
	momentumStore := vm.context

	for _, header := range momentum.Content {
		if err := momentumStore.AddAccountBlockTransaction(*header, pool.GetPatch(header.Address, header.Identifier())); err != nil {
			return err
		}
	}

	return nil
}
