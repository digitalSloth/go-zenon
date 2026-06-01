package verifier

import (
	"fmt"

	"github.com/pkg/errors"

	"github.com/zenon-network/go-zenon/chain"
	"github.com/zenon-network/go-zenon/chain/nom"
	"github.com/zenon-network/go-zenon/chain/store"
	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/consensus"
	"github.com/zenon-network/go-zenon/pow"
	vm_constants "github.com/zenon-network/go-zenon/vm/constants"
	"github.com/zenon-network/go-zenon/vm/embedded/definition"
	"github.com/zenon-network/go-zenon/wallet"
)

var (
	ReceiverMismatchEnforcementHeight uint64 = 10109240 // Targeting 2025-04-16 12:00:00 UTC
)

func isBatched(block *nom.AccountBlock) bool {
	return block.IsSendBlock() && types.IsContractAddress(block.Address)
}
func isContractReceive(block *nom.AccountBlock) bool {
	return block.IsReceiveBlock() && types.IsContractAddress(block.Address)
}

type AccountBlockVerifier interface {
	AccountBlock(block *nom.AccountBlock) error
	AccountBlockTransaction(transaction *nom.AccountBlockTransaction) error
}

type accountVerifier struct {
	chain     chain.Chain
	consensus consensus.Consensus
}

func (av *accountVerifier) getContext(block *nom.AccountBlock) (store.Account, store.Momentum, error) {
	if block.Height == 0 {
		return nil, nil, ErrABMHeightMissing
	}
	if block.Height == 1 && !block.PreviousHash.IsZero() {
		return nil, nil, ErrABPrevHashMustBeZero
	}
	if block.Height != 1 && block.PreviousHash.IsZero() {
		return nil, nil, ErrABPrevHashMissing
	}

	if block.MomentumAcknowledged.IsZero() {
		return nil, nil, ErrABMAMustNotBeZero
	}
	momentumStore := av.chain.GetMomentumStore(block.MomentumAcknowledged)
	if momentumStore == nil {
		return nil, nil, ErrABMAMissing
	}

	accountStore := av.chain.GetAccountStore(block.Address, block.Previous())

	if accountStore == nil {
		// try to give a better error in case we are not able to give a better error
		globalStore := av.chain.GetFrontierMomentumStore().GetAccountStore(block.Address)
		globalFrontier, err := globalStore.Frontier()
		if err != nil {
			return nil, nil, InternalError(err)
		}

		if globalFrontier.Height > block.Height-1 {
			block, err := globalStore.ByHash(block.PreviousHash)
			if err != nil {
				return nil, nil, InternalError(err)
			}
			if block != nil {
				return nil, nil, ErrABPrevHasCementedOnTop
			}
			return nil, nil, ErrABPrevHeightExists
		} else {
			return nil, nil, ErrABPreviousMissing
		}
	}

	return accountStore, momentumStore, nil
}
func (av *accountVerifier) AccountBlock(block *nom.AccountBlock) error {
	if block.BlockType == nom.BlockTypeContractSend {
		return ErrABTypeInvalidExternal
	}

	accountStore, momentumStore, err := av.getContext(block)
	if err != nil {
		return err
	}

	return (&accountBlockVerifier{
		block:         block,
		accountStore:  accountStore,
		momentumStore: momentumStore,
		frontierStore: av.chain.GetFrontierMomentumStore(),
	}).all()
}
func (av *accountVerifier) AccountBlockTransaction(transaction *nom.AccountBlockTransaction) error {
	if transaction.Block.BlockType == nom.BlockTypeContractSend {
		return ErrABTypeInvalidExternal
	}

	accountStore, momentumStore, err := av.getContext(transaction.Block)
	if err != nil {
		return err
	}

	return (&accountBlockTransactionVerifier{
		transaction:   transaction,
		accountStore:  accountStore,
		momentumStore: momentumStore,
	}).all()
}

func NewAccountBlockVerifier(chain chain.Chain, consensus consensus.Consensus) AccountBlockVerifier {
	return &accountVerifier{
		chain:     chain,
		consensus: consensus,
	}
}

type accountBlockVerifier struct {
	block         *nom.AccountBlock
	accountStore  store.Account
	momentumStore store.Momentum
	frontierStore store.Momentum
}

func (abv *accountBlockVerifier) all() error {
	if err := abv.version(); err != nil {
		return err
	}
	if err := abv.chainIdentifier(); err != nil {
		return err
	}
	if err := abv.blockType(); err != nil {
		return err
	}
	if err := abv.amounts(); err != nil {
		return err
	}
	if err := abv.pow(); err != nil {
		return err
	}
	if err := abv.previous(); err != nil {
		return err
	}
	if err := abv.momentumAcknowledged(); err != nil {
		return err
	}
	if err := abv.fromHash(); err != nil {
		return err
	}
	if err := abv.wasmContractSend(); err != nil {
		return err
	}
	if err := abv.sequencer(); err != nil {
		return err
	}
	return nil
}
func (abv *accountBlockVerifier) version() error {
	if abv.block.Version == 0 {
		return ErrABVersionMissing
	}
	if abv.block.Version == 1 {
		// v1 blocks must not carry events — they are not folded into the hash.
		if len(abv.block.Events) != 0 {
			return ErrABVersionInvalid
		}
		return nil
	}
	// Post-spork: only BlockTypeContractReceive blocks may use version 3.
	if abv.block.Version == nom.WasmAccountBlockVersion {
		// Must use the enforced helper (WasmRuntime → DynamicPlasma dependency)
		// so the verifier agrees with the producer in vm/vm.go.
		isWasmEnforced, err := abv.momentumStore.IsWasmRuntimeSporkEnforced()
		if err != nil {
			return err
		}
		if !isWasmEnforced {
			return ErrABVersionInvalid
		}
		if abv.block.BlockType != nom.BlockTypeContractReceive {
			return ErrABVersionInvalid
		}
		return nil
	}
	return ErrABVersionInvalid
}
func (abv *accountBlockVerifier) chainIdentifier() error {
	if abv.block.ChainIdentifier == 0 {
		return ErrMChainIdentifierMissing
	}
	if abv.block.ChainIdentifier != abv.momentumStore.ChainIdentifier() {
		return fmt.Errorf("%w - expected %v but received %v", ErrMChainIdentifierMismatch, abv.momentumStore.ChainIdentifier(), abv.block.ChainIdentifier)
	}
	return nil
}
func (abv *accountBlockVerifier) blockType() error {
	if abv.block.BlockType == 0 {
		return ErrABTypeMissing
	}
	if abv.block.BlockType == nom.BlockTypeGenesisReceive {
		return ErrABTypeMustNotBeGenesis
	}
	if abv.block.IsSendBlock() || abv.block.IsReceiveBlock() {
	} else {
		return ErrABTypeUnsupported
	}

	if types.IsContractAddress(abv.block.Address) {
		if abv.block.BlockType == nom.BlockTypeContractReceive || abv.block.BlockType == nom.BlockTypeContractSend {
		} else {
			return ErrABTypeMustBeContract
		}
	} else {
		if abv.block.BlockType == nom.BlockTypeUserReceive || abv.block.BlockType == nom.BlockTypeUserSend {
		} else {
			return ErrABTypeMustBeUser
		}
	}
	return nil
}
func (abv *accountBlockVerifier) amounts() error {
	if abv.block.IsSendBlock() {
		if abv.block.Amount.Sign() == -1 {
			return ErrABAmountNegative
		}
		if abv.block.Amount.BitLen() > 255 {
			return ErrABAmountTooBig
		}
		if abv.block.Amount.Sign() == +1 && abv.block.TokenStandard == types.ZeroTokenStandard {
			return ErrABZtsMissing
		}
		// ToAddress can be null

		if !abv.block.FromBlockHash.IsZero() {
			return ErrABFromBlockHashMustBeZero
		}
	} else {
		if abv.block.Amount != nil && abv.block.Amount.Sign() != 0 {
			return ErrABAmountMustBeZero
		}
		if abv.block.TokenStandard != types.ZeroTokenStandard {
			return ErrABZtsMustBeZero
		}
		if abv.block.ToAddress != types.ZeroAddress {
			return ErrABToAddressMustBeZero
		}

		if abv.block.FromBlockHash.IsZero() {
			return ErrABFromBlockHashMissing
		}
	}
	return nil
}
func (abv *accountBlockVerifier) pow() error {
	if abv.block.Difficulty != 0 {
		if types.IsContractAddress(abv.block.Address) {
			return ErrABPoWInvalid
		}
		if !pow.CheckPoWNonce(abv.block) {
			return ErrABPoWInvalid
		}
	}
	return nil
}
func (abv *accountBlockVerifier) previous() error {
	// for consistency, check again
	if abv.block.Height == 0 {
		return ErrABMHeightMissing
	}
	if abv.block.Height == 1 && !abv.block.PreviousHash.IsZero() {
		return ErrABPrevHashMustBeZero
	}
	if abv.block.Height != 1 && abv.block.PreviousHash.IsZero() {
		return ErrABPrevHashMissing
	}

	// start blocks don't expect previous
	if abv.block.Height == 1 {
		return nil
	}

	// don't check previous on contract
	if types.IsContractAddress(abv.block.Address) {
		return nil
	}

	block, err := abv.accountStore.Frontier()
	if err != nil {
		return InternalError(err)
	}
	if block == nil {
		return InternalError(errors.Errorf("empty frontier account-block"))
	}
	if block.Identifier() != abv.block.Previous() {
		return ErrABPreviousMissing
	}
	return nil
}
func (abv *accountBlockVerifier) momentumAcknowledged() error {
	momentum, err := abv.momentumStore.GetFrontierMomentum()
	if err != nil {
		return InternalError(err)
	}
	if momentum.Identifier() != abv.block.MomentumAcknowledged {
		return InternalError(errors.Errorf("impossible scenario. verifier momentum-store exists but frontier is different. Expected MomentumAcknowledged %v but got %v from MomentumStore", abv.block.MomentumAcknowledged, momentum.Identifier()))
	}

	// all checks are done by the parent
	if isBatched(abv.block) {
		return nil
	}

	// MomentumAcknowledged is the same as all the ones in dBlocks
	if isContractReceive(abv.block) {
		for _, dBlock := range abv.block.DescendantBlocks {
			if dBlock.MomentumAcknowledged != abv.block.MomentumAcknowledged {
				return ErrABMAMustBeTheSame
			}
		}

		height, err := abv.momentumStore.GetBlockConfirmationHeight(abv.block.FromBlockHash)
		if err != nil {
			return InternalError(err)
		}
		if height != abv.block.MomentumAcknowledged.Height {
			return ErrABMAInvalidForAutoGenerated
		}
		return nil
	}

	// current MomentumAcknowledged is bigger than previous
	if previous := abv.block.Previous(); previous != types.ZeroHashHeight {
		previousBlock, err := abv.accountStore.ByHeight(previous.Height)
		if err != nil {
			return InternalError(err)
		}
		if previousBlock.MomentumAcknowledged.Height > abv.block.MomentumAcknowledged.Height {
			return ErrABMAGap
		}
	}

	return nil
}
func (abv *accountBlockVerifier) fromHash() error {
	if abv.block.IsSendBlock() {
		return nil
	}

	// check that from-hash is a valid hash
	sendBlock, err := abv.momentumStore.GetAccountBlockByHash(abv.block.FromBlockHash)
	if err != nil {
		return InternalError(err)
	} else if sendBlock == nil {
		return ErrABFromBlockMissing
	}

	if abv.block.Address != sendBlock.ToAddress {
		// Use the momentum ledger's true frontier height when comparing
		if abv.frontierStore.Identifier().Height >= ReceiverMismatchEnforcementHeight {
			return ErrABFromBlockReceiverMismatch
		}
	}

	// check if abv.block was already received
	status := abv.accountStore.IsReceived(abv.block.FromBlockHash)
	if status {
		return ErrABFromBlockAlreadyReceived
	}

	return nil
}
func (abv *accountBlockVerifier) wasmContractSend() error {
	if !abv.block.IsSendBlock() || !types.IsWasmContractAddress(abv.block.ToAddress) {
		return nil
	}
	// Pre-spork rejection — same rule as vm.go applySend, including the
	// WasmRuntime → DynamicPlasma dependency (§2.3, §3).
	enforced, err := abv.momentumStore.IsWasmRuntimeSporkEnforced()
	if err != nil {
		return InternalError(err)
	}
	if !enforced {
		return vm_constants.ErrWasmNotActivated
	}
	// Exempt deploy/finalize endowments emitted BY the WasmContract (0x01) management
	// contract. WasmContract is the deploy authority: it only sends to a 0x02 address
	// it is itself deploying, and the destination bytecode is written in the same
	// atomic receive that emits this send — so it is not yet in this committed
	// snapshot (§4.3, §4.4, §8.5). Must mirror vm.go applySend exactly so send-
	// validation and verification agree. Sends from a 0x02 contract (Execute
	// transfers) carry that contract's address, not WasmContract's, and stay checked.
	if abv.block.Address == types.WasmContract {
		return nil
	}
	// Pause check: if the contract is paused, only the deployer may send.
	// Must mirror vm.go applySend exactly.
	wasmContractStore := abv.momentumStore.GetAccountStore(types.WasmContract)
	deployer, err := definition.GetWasmPausedDeployer(wasmContractStore.Storage(), abv.block.ToAddress)
	if err != nil {
		return InternalError(err)
	}
	if deployer != nil && abv.block.Address != *deployer {
		return vm_constants.ErrWasmContractPaused
	}
	// Bytecode-exists check
	has, err := wasmContractStore.Storage().Has(common.JoinBytes(vm_constants.WasmBytecodeKeyPrefix, abv.block.ToAddress.Bytes()))
	if err != nil {
		return InternalError(err)
	}
	if !has {
		return vm_constants.ErrWasmContractNotDeployed
	}
	return nil
}
func (abv *accountBlockVerifier) sequencer() error {
	if types.IsContractAddress(abv.block.Address) && abv.block.IsReceiveBlock() {
	} else {
		return nil
	}

	nextInLine := abv.accountStore.SequencerFront(abv.momentumStore.GetAccountMailbox(abv.block.Address))
	if nextInLine == nil {
		return ErrABSequencerNothing
	}

	sendBlock, err := abv.momentumStore.GetAccountBlockByHash(abv.block.FromBlockHash)
	if err != nil {
		return InternalError(err)
	}
	if sendBlock.Header() != *nextInLine {
		return ErrABSequencerNotNext
	}

	return nil
}

type accountBlockTransactionVerifier struct {
	transaction   *nom.AccountBlockTransaction
	accountStore  store.Account
	momentumStore store.Momentum
}

func (abvt *accountBlockTransactionVerifier) all() error {
	if err := abvt.hash(); err != nil {
		return err
	}
	if err := abvt.signature(); err != nil {
		return err
	}
	if err := abvt.producer(); err != nil {
		return err
	}
	if err := abvt.descendantBlocks(); err != nil {
		return err
	}

	return nil
}
func (abvt *accountBlockTransactionVerifier) signature() error {
	block := abvt.transaction.Block
	if types.IsContractAddress(block.Address) {
		if len(block.PublicKey) != 0 {
			return ErrABPublicKeyMustBeZero
		}
		if len(block.Signature) != 0 {
			return ErrABSignatureMustBeZero
		}
		return nil
	}

	if len(block.Signature) == 0 {
		return ErrABSignatureMissing
	}
	if len(block.PublicKey) == 0 {
		return ErrABPublicKeyMissing
	}
	isVerified, err := wallet.VerifySignature(block.PublicKey, block.Hash.Bytes(), block.Signature)
	if err != nil {
		return ErrABSignatureInvalid
	}
	if !isVerified {
		return ErrABSignatureInvalid
	}
	return nil
}
func (abvt *accountBlockTransactionVerifier) hash() error {
	block := abvt.transaction.Block

	// check expected hash matches
	computedHash := block.ComputeHash()
	if block.Hash.IsZero() {
		return ErrABHashMissing
	}
	if computedHash != block.Hash {
		return ErrABHashInvalid
	}
	return nil
}
func (abvt *accountBlockTransactionVerifier) producer() error {
	block := abvt.transaction.Block

	if types.IsContractAddress(block.Address) {
		return nil
	}
	if types.PubKeyToAddress(block.PublicKey) != block.Address {
		return ErrABPublicKeyWrongAddress
	}

	return nil
}
func (abvt *accountBlockTransactionVerifier) descendantBlocks() error {
	block := abvt.transaction.Block
	if !isContractReceive(block) && len(block.DescendantBlocks) > 0 {
		return ErrABDescendantMustBeZero
	}
	for _, dBlock := range block.DescendantBlocks {
		if err := (&accountBlockVerifier{
			block:         dBlock,
			accountStore:  abvt.accountStore,
			momentumStore: abvt.momentumStore,
		}).all(); err != nil {
			return DescendantVerifyError(err)
		}
	}
	return nil
}
