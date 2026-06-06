package simulation

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// Second-wave self-signed participant ops. Each is signed by an active sim
// participant (the Creator), so it clears ValidateBasic + the permission gate
// + signature verification and reaches real keeper state mutation. These are
// the real factories for three messages that previously had only NoOpMsg
// legacy stubs (unreachable under the simsx HasWeightedOperationsX path).

// MsgSubmitUnitOfComputePriceProposalFactory exercises the unit-of-compute
// price proposal path: CheckPermission(ActiveParticipantPermission) +
// GetEffectiveEpoch, then SetUnitOfComputePriceProposal.
func MsgSubmitUnitOfComputePriceProposalFactory(k keeper.Keeper) simsx.SimMsgFactoryFn[*types.MsgSubmitUnitOfComputePriceProposal] {
	return func(ctx context.Context, testData *simsx.ChainDataSource, reporter simsx.SimulationReporter) ([]simsx.SimAccount, *types.MsgSubmitUnitOfComputePriceProposal) {
		if err := EnsureSimActiveParticipantsSeeded(ctx, k); err != nil {
			reporter.Skipf("sim active-participants seeding failed: %v", err)
			return nil, nil
		}
		from, ok := PickRandomActiveSimAccount(ctx, k, testData, reporter)
		if !ok {
			return nil, nil
		}
		msg := &types.MsgSubmitUnitOfComputePriceProposal{
			Creator: from.AddressBech32,
			Price:   uint64(1 + testData.Rand().Intn(10000)),
		}
		return []simsx.SimAccount{from}, msg
	}
}

// MsgSubmitHardwareDiffFactory exercises the hardware-node diff path:
// CheckPermission(ParticipantPermission) + governance-model validation, then
// SetHardwareNodes. The node advertises a genesis-registered governance model
// (SimModelIDs) so IsValidGovernanceModel passes; the RNG-varied LocalId makes
// repeated calls produce add/modify diffs against the participant's node set.
func MsgSubmitHardwareDiffFactory(k keeper.Keeper) simsx.SimMsgFactoryFn[*types.MsgSubmitHardwareDiff] {
	return func(ctx context.Context, testData *simsx.ChainDataSource, reporter simsx.SimulationReporter) ([]simsx.SimAccount, *types.MsgSubmitHardwareDiff) {
		if err := EnsureModelsInEpochGroup(ctx, k); err != nil {
			reporter.Skipf("models epoch-group seeding failed: %v", err)
			return nil, nil
		}
		if err := EnsureSimActiveParticipantsSeeded(ctx, k); err != nil {
			reporter.Skipf("sim active-participants seeding failed: %v", err)
			return nil, nil
		}
		from, ok := PickRandomActiveSimAccount(ctx, k, testData, reporter)
		if !ok {
			return nil, nil
		}
		node := &types.HardwareNode{
			LocalId: fmt.Sprintf("sim-node-%d", testData.Rand().Intn(1<<20)),
			Models:  []string{SimModelIDs[testData.Rand().Intn(len(SimModelIDs))]},
			Host:    "sim-host",
			Port:    "5000",
		}
		msg := &types.MsgSubmitHardwareDiff{
			Creator:       from.AddressBech32,
			NewOrModified: []*types.HardwareNode{node},
		}
		return []simsx.SimAccount{from}, msg
	}
}

// MsgSubmitNewUnfundedParticipantFactory exercises the direct (unfunded)
// participant-registration path: CheckPermission(OpenRegistrationPermission),
// then account creation with a pubkey that must match the supplied address.
// The signer is an existing active participant (Creator); the new participant
// (Address) is a fresh secp256k1 identity derived deterministically from the
// sim RNG so the address matches the handler-rederived pubkey and the run
// stays reproducible.
func MsgSubmitNewUnfundedParticipantFactory(k keeper.Keeper) simsx.SimMsgFactoryFn[*types.MsgSubmitNewUnfundedParticipant] {
	return func(ctx context.Context, testData *simsx.ChainDataSource, reporter simsx.SimulationReporter) ([]simsx.SimAccount, *types.MsgSubmitNewUnfundedParticipant) {
		if err := EnsureSimActiveParticipantsSeeded(ctx, k); err != nil {
			reporter.Skipf("sim active-participants seeding failed: %v", err)
			return nil, nil
		}
		creator, ok := PickRandomActiveSimAccount(ctx, k, testData, reporter)
		if !ok {
			return nil, nil
		}
		secret := make([]byte, 32)
		testData.Rand().Read(secret)
		pub := secp256k1.GenPrivKeyFromSecret(secret).PubKey()
		newAddr := sdk.AccAddress(pub.Address()).String()
		msg := &types.MsgSubmitNewUnfundedParticipant{
			Creator:      creator.AddressBech32,
			Address:      newAddr,
			PubKey:       base64.StdEncoding.EncodeToString(pub.Bytes()),
			ValidatorKey: SimValidatorKey(newAddr),
		}
		return []simsx.SimAccount{creator}, msg
	}
}
