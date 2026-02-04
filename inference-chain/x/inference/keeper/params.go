package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

const defaultGenesisGuardianNetworkMaturityThreshold int64 = 2_000_000 // 2M
const defaultGenesisGuardianNetworkMaturityMinHeight int64 = 0

const defaultDeveloperAccessUntilBlockHeight int64 = 0
const defaultNewParticipantRegistrationStartHeight int64 = 0

// GetParams get all parameters as types.Params
func (k Keeper) GetParams(ctx context.Context) (params types.Params, err error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz := store.Get(types.ParamsKey)
	if bz == nil {
		return params, nil
	}

	err = k.cdc.Unmarshal(bz, &params)
	if err != nil {
		return types.Params{}, err
	}
	return params, nil
}

// SetParams set the params
func (k Keeper) SetParams(ctx context.Context, params types.Params) error {
	oldParams, _ := k.GetParams(ctx)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz, err := k.cdc.Marshal(&params)
	if err != nil {
		return err
	}
	store.Set(types.ParamsKey, bz)

	// Auto-set grace epoch when poc_v2_enabled transitions false -> true
	if params.PocParams != nil && params.PocParams.PocV2Enabled {
		wasV2Disabled := oldParams.PocParams == nil || !oldParams.PocParams.PocV2Enabled
		if wasV2Disabled {
			if _, exists := k.GetPocV2EnabledEpoch(ctx); !exists {
				if epoch, found := k.GetEffectiveEpochIndex(ctx); found {
					_ = k.SetPocV2EnabledEpoch(ctx, epoch)
				}
			}
		}
	}

	return nil
}

func (k Keeper) GetV1Params(ctx context.Context) (params types.ParamsV1, err error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz := store.Get(types.ParamsKey)
	if bz == nil {
		return params, nil
	}

	err = k.cdc.Unmarshal(bz, &params)
	if err != nil {
		return types.ParamsV1{}, err
	}
	return params, nil
}

// GetBandwidthLimitsParams returns bandwidth limits parameters
func (k Keeper) GetBandwidthLimitsParams(ctx context.Context) (*types.BandwidthLimitsParams, error) {
	params, err := k.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	if params.BandwidthLimitsParams == nil {
		// Return default values if not set
		return &types.BandwidthLimitsParams{
			EstimatedLimitsPerBlockKb: 1024, // Default 1MB per block
			KbPerInputToken: &types.Decimal{
				Value:    23, // 0.0023 = 23 × 10^(-4)
				Exponent: -4,
			},
			KbPerOutputToken: &types.Decimal{
				Value:    64, // 0.64 = 64 × 10^(-2)
				Exponent: -2,
			},
			MaxInferencesPerBlock: 1000, // Default 1000 inferences per block chain-wide
		}, nil
	}
	return params.BandwidthLimitsParams, nil
}

// GetGenesisGuardianAddresses returns the governance-controlled genesis guardian operator addresses.
func (k Keeper) GetGenesisGuardianAddresses(ctx context.Context) []string {
	p, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("Unable to get Params in GetGenesisGuardianAddresses", types.System, "error", err)
		return []string{}
	}
	if p.GenesisGuardianParams == nil {
		return []string{}
	}
	return p.GenesisGuardianParams.GuardianAddresses
}

// GetGenesisGuardianNetworkMaturityThreshold returns the governance-controlled maturity threshold.
// If unset (0), it falls back to a safe default.
func (k Keeper) GetGenesisGuardianNetworkMaturityThreshold(ctx context.Context) int64 {
	p, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("Unable to get Params in GetGenesisGuardianNetworkMaturityThreshold", types.System, "error", err)
		return defaultGenesisGuardianNetworkMaturityThreshold
	}
	if p.GenesisGuardianParams == nil {
		return defaultGenesisGuardianNetworkMaturityThreshold
	}
	threshold := p.GenesisGuardianParams.NetworkMaturityThreshold
	if threshold == 0 {
		return defaultGenesisGuardianNetworkMaturityThreshold
	}
	return threshold
}

// GetGenesisGuardianNetworkMaturityMinHeight returns the governance-controlled minimum height for maturity.
// If unset, defaults to 0 (no height gating).
func (k Keeper) GetGenesisGuardianNetworkMaturityMinHeight(ctx context.Context) int64 {
	p, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("Unable to get Params in GetGenesisGuardianNetworkMaturityMinHeight", types.System, "error", err)
		return defaultGenesisGuardianNetworkMaturityMinHeight
	}
	if p.GenesisGuardianParams == nil {
		return defaultGenesisGuardianNetworkMaturityMinHeight
	}
	minHeight := p.GenesisGuardianParams.NetworkMaturityMinHeight
	if minHeight == 0 {
		return defaultGenesisGuardianNetworkMaturityMinHeight
	}
	return minHeight
}

// InNetworkMature returns true iff the network has enough total power and has reached the minimum height.
func (k Keeper) InNetworkMature(ctx context.Context, height int64, totalNetworkPower int64) bool {
	threshold := k.GetGenesisGuardianNetworkMaturityThreshold(ctx)
	minHeight := k.GetGenesisGuardianNetworkMaturityMinHeight(ctx)
	return totalNetworkPower >= threshold && height >= minHeight
}

func (k Keeper) GetDeveloperAccessParams(ctx context.Context) *types.DeveloperAccessParams {
	p, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("Unable to get Params in GetDeveloperAccessParams", types.System, "error", err)
		return nil
	}
	return p.DeveloperAccessParams
}

// IsDeveloperAccessRestricted returns true iff the chain is still in the restricted mode where only
// allowed developers may request inferences.
func (k Keeper) IsDeveloperAccessRestricted(ctx context.Context, height int64) bool {
	p := k.GetDeveloperAccessParams(ctx)
	if p == nil {
		return false
	}
	until := p.UntilBlockHeight
	if until == 0 {
		until = defaultDeveloperAccessUntilBlockHeight
	}
	return height < until
}

func (k Keeper) IsAllowedDeveloper(ctx context.Context, developerAddress string) bool {
	p := k.GetDeveloperAccessParams(ctx)
	if p == nil {
		return true // no restriction configured
	}
	allowed := p.AllowedDeveloperAddresses
	if len(allowed) == 0 {
		return false
	}
	for _, a := range allowed {
		if a == developerAddress {
			return true
		}
	}
	return false
}

func (k Keeper) GetParticipantAccessParams(ctx context.Context) *types.ParticipantAccessParams {
	p, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("Unable to get Params in GetParticipantAccessParams", types.System, "error", err)
		return nil
	}
	return p.ParticipantAccessParams
}

// IsNewParticipantRegistrationClosed returns true iff NEW participant registration is closed at this height.
// Semantics: registration is blocked while current height < endHeight (i.e. opens at endHeight).
// Existing participants may still update their keys/URL.
func (k Keeper) IsNewParticipantRegistrationClosed(ctx context.Context, height int64) bool {
	p := k.GetParticipantAccessParams(ctx)
	if p == nil {
		return false
	}
	start := p.NewParticipantRegistrationStartHeight
	if start == 0 {
		start = defaultNewParticipantRegistrationStartHeight
	}
	if start == 0 {
		return false
	}
	return height < start
}

// IsPoCParticipantBlocked returns true if the address is blocked from participating in PoC.
// Uses a map for O(1) membership checks (map build is O(n) per call).
func (k Keeper) IsPoCParticipantBlocked(ctx context.Context, address string) bool {
	p := k.GetParticipantAccessParams(ctx)
	if p == nil {
		return false
	}
	list := p.BlockedParticipantAddresses
	if len(list) == 0 {
		return false
	}
	blocked := make(map[string]struct{}, len(list))
	for _, a := range list {
		blocked[a] = struct{}{}
	}
	_, ok := blocked[address]
	return ok
}

func (k Keeper) GetTransferAgentAccessParams(ctx context.Context) *types.TransferAgentAccessParams {
	p, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("Unable to get Params in GetTransferAgentAccessParams", types.System, "error", err)
		return nil
	}
	return p.TransferAgentAccessParams
}

// IsTransferAgentRestricted returns true if TA whitelist is active (non-empty list).
func (k Keeper) IsTransferAgentRestricted(ctx context.Context) bool {
	p := k.GetTransferAgentAccessParams(ctx)
	if p == nil {
		return false
	}
	return len(p.AllowedTransferAddresses) > 0
}

// IsAllowedTransferAgent returns true if the address is in the TA whitelist (or whitelist is empty).
func (k Keeper) IsAllowedTransferAgent(ctx context.Context, taAddress string) bool {
	p := k.GetTransferAgentAccessParams(ctx)
	if p == nil || len(p.AllowedTransferAddresses) == 0 {
		return true // no restriction
	}
	for _, a := range p.AllowedTransferAddresses {
		if a == taAddress {
			return true
		}
	}
	return false
}
