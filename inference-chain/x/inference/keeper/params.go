package keeper

import (
	"context"
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const defaultGenesisGuardianNetworkMaturityThreshold int64 = 2_000_000 // 2M
const defaultGenesisGuardianNetworkMaturityMinHeight int64 = 0

const defaultDeveloperAccessUntilBlockHeight int64 = 0
const defaultNewParticipantRegistrationStartHeight int64 = 0

type paramsKey struct{}

func (k Keeper) getParamsFromStore(ctx context.Context) (params types.Params, err error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz := store.Get(types.ParamsKey)
	if bz == nil {
		return params, nil
	}
	if err := k.cdc.Unmarshal(bz, &params); err != nil {
		return types.Params{}, err
	}
	return restoredParams(params), nil
}

// GetParams get all parameters as types.Params
func (k Keeper) GetParams(ctx context.Context) (params types.Params, err error) {
	if cached, ok := ctx.Value(paramsKey{}).(*types.Params); ok && cached != nil {
		return *cached, nil
	}
	if c := txCacheFrom(ctx); c != nil {
		return k.getParamsTxCached(ctx, c)
	}
	return k.getParamsFromStore(ctx)
}

// txParamsCache keeps the params bytes (and the SPRT values derived from them) and
// the effective epoch index for one tx, so repeated reads pay the store once.
// Turned off for the rest of the tx by SetParams or PrecomputeSPRTValues;
// SetEffectiveEpochIndex stops caching only the epoch index.
type txParamsCache struct {
	bz           []byte
	sprt         []byte
	epoch        uint64
	epochSet     bool
	epochWritten bool // the write may sit in a discarded CacheContext
	off          bool
	egd          egdTxCache // not turned off with the params part
}

func txCacheFrom(ctx context.Context) *txParamsCache {
	if c, ok := ctx.Value(txParamsCacheKey{}).(*txParamsCache); ok && c != nil && !c.off {
		return c
	}
	return nil
}

func turnOffTxCache(ctx context.Context) {
	if c, ok := ctx.Value(txParamsCacheKey{}).(*txParamsCache); ok && c != nil {
		c.bz, c.sprt, c.epochSet, c.off = nil, nil, false, true
	}
}

// forgetTxEpochIndex stops caching the effective epoch index for the rest of the tx.
func forgetTxEpochIndex(ctx context.Context) {
	if c, ok := ctx.Value(txParamsCacheKey{}).(*txParamsCache); ok && c != nil {
		c.epochSet, c.epochWritten = false, true
	}
}

type txParamsCacheKey struct{}

// WithTxParamsCache installs an empty per-tx params cache (set in the ante handler).
func WithTxParamsCache(ctx sdk.Context) sdk.Context {
	return ctx.WithValue(txParamsCacheKey{}, &txParamsCache{})
}

func (k Keeper) getParamsTxCached(ctx context.Context, c *txParamsCache) (params types.Params, err error) {
	if c.bz == nil {
		store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
		bz := store.Get(types.ParamsKey)
		if bz == nil {
			return params, nil
		}
		c.bz = append([]byte(nil), bz...)
	}
	if err := k.cdc.Unmarshal(c.bz, &params); err != nil {
		return types.Params{}, err
	}
	return restoredParams(params), nil
}

// InjectParamsIntoContext returns a new context with the params cached.
func (k Keeper) InjectParamsIntoContext(ctx sdk.Context) (sdk.Context, error) {
	params, err := k.getParamsFromStore(ctx)
	if err != nil {
		return ctx, err
	}
	return ctx.WithValue(paramsKey{}, &params), nil
}

// SetParams set the params
func (k Keeper) SetParams(ctx context.Context, params types.Params) error {
	oldParams, _ := k.getParamsFromStore(ctx)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	stored, err := storedParams(params)
	if err != nil {
		return err
	}
	bz, err := k.cdc.Marshal(&stored)
	if err != nil {
		return err
	}
	store.Set(types.ParamsKey, bz)
	// A write may be discarded with a CacheContext, so stop trusting the cache.
	turnOffTxCache(ctx)

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

// storedParams keeps the creator, developer, transfer agent and guardian
// allowlists (mainnet: 46 bech32 strings, ~60% of the params bytes) as address
// bytes; restoredParams undoes it. A list with an entry that does not
// round-trip stays as strings. The caller's params are not modified.
func storedParams(p types.Params) (types.Params, error) {
	if e := p.DevshardEscrowParams; e != nil {
		if len(e.AllowedCreatorAddrs) > 0 {
			return p, fmt.Errorf("params: allowed_creator_addrs is storage-only")
		}
		if b, ok := rawAddresses(e.AllowedCreatorAddresses, rawAddress); ok {
			c := *e
			c.AllowedCreatorAddrs, c.AllowedCreatorAddresses = b, nil
			p.DevshardEscrowParams = &c
		}
	}
	if d := p.DeveloperAccessParams; d != nil {
		if len(d.AllowedDeveloperAddrs) > 0 {
			return p, fmt.Errorf("params: allowed_developer_addrs is storage-only")
		}
		if b, ok := rawAddresses(d.AllowedDeveloperAddresses, rawAddress); ok {
			c := *d
			c.AllowedDeveloperAddrs, c.AllowedDeveloperAddresses = b, nil
			p.DeveloperAccessParams = &c
		}
	}
	if t := p.TransferAgentAccessParams; t != nil {
		if len(t.AllowedTransferAddrs) > 0 {
			return p, fmt.Errorf("params: allowed_transfer_addrs is storage-only")
		}
		if b, ok := rawAddresses(t.AllowedTransferAddresses, rawAddress); ok {
			c := *t
			c.AllowedTransferAddrs, c.AllowedTransferAddresses = b, nil
			p.TransferAgentAccessParams = &c
		}
	}
	if g := p.GenesisGuardianParams; g != nil {
		if len(g.GuardianAddrs) > 0 {
			return p, fmt.Errorf("params: guardian_addrs is storage-only")
		}
		if b, ok := rawAddresses(g.GuardianAddresses, rawValAddress); ok {
			c := *g
			c.GuardianAddrs, c.GuardianAddresses = b, nil
			p.GenesisGuardianParams = &c
		}
	}
	return p, nil
}

func restoredParams(p types.Params) types.Params {
	if e := p.DevshardEscrowParams; e != nil && len(e.AllowedCreatorAddrs) > 0 {
		e.AllowedCreatorAddresses, e.AllowedCreatorAddrs = accAddressStrings(e.AllowedCreatorAddrs), nil
	}
	if d := p.DeveloperAccessParams; d != nil && len(d.AllowedDeveloperAddrs) > 0 {
		d.AllowedDeveloperAddresses, d.AllowedDeveloperAddrs = accAddressStrings(d.AllowedDeveloperAddrs), nil
	}
	if t := p.TransferAgentAccessParams; t != nil && len(t.AllowedTransferAddrs) > 0 {
		t.AllowedTransferAddresses, t.AllowedTransferAddrs = accAddressStrings(t.AllowedTransferAddrs), nil
	}
	if g := p.GenesisGuardianParams; g != nil && len(g.GuardianAddrs) > 0 {
		addrs := make([]string, len(g.GuardianAddrs))
		for i, b := range g.GuardianAddrs {
			addrs[i] = sdk.ValAddress(b).String()
		}
		g.GuardianAddresses, g.GuardianAddrs = addrs, nil
	}
	return p
}

// rawAddresses returns the bytes of every address, or false if any of them does not round-trip.
func rawAddresses(addrs []string, raw func(string) ([]byte, bool)) ([][]byte, bool) {
	if len(addrs) == 0 {
		return nil, false
	}
	out := make([][]byte, len(addrs))
	for i, a := range addrs {
		b, ok := raw(a)
		if !ok {
			return nil, false
		}
		out[i] = b
	}
	return out, true
}

// rawValAddress returns the bytes of a validator bech32 address that encodes back to the same string.
func rawValAddress(addr string) ([]byte, bool) {
	b, err := sdk.ValAddressFromBech32(addr)
	if err != nil || len(b) == 0 || b.String() != addr {
		return nil, false
	}
	return b, true
}

func accAddressStrings(raw [][]byte) []string {
	addrs := make([]string, len(raw))
	for i, b := range raw {
		addrs[i] = sdk.AccAddress(b).String()
	}
	return addrs
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

// GetDevshardEscrowParams returns devshard escrow params, falling back to defaults if unset.
func (k Keeper) GetDevshardEscrowParams(ctx context.Context) *types.DevshardEscrowParams {
	p, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("Unable to get Params in GetDevshardEscrowParams", types.System, "error", err)
		return types.DefaultDevshardEscrowParams()
	}
	if p.DevshardEscrowParams == nil {
		return types.DefaultDevshardEscrowParams()
	}
	return p.DevshardEscrowParams
}

// IsAllowedEscrowCreator returns true if the address is allowed to create/settle devshard escrows.
// An empty allowlist means everyone is allowed.
func (k Keeper) IsAllowedEscrowCreator(ctx context.Context, address string) bool {
	ep := k.GetDevshardEscrowParams(ctx)
	if len(ep.AllowedCreatorAddresses) == 0 {
		return true
	}
	for _, a := range ep.AllowedCreatorAddresses {
		if a == address {
			return true
		}
	}
	return false
}

// GetMaintenanceParams returns maintenance params, falling back to defaults if unset.
func (k Keeper) GetMaintenanceParams(ctx context.Context) *types.MaintenanceParams {
	p, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("Unable to get Params in GetMaintenanceParams", types.System, "error", err)
		return types.DefaultMaintenanceParams()
	}
	if p.MaintenanceParams == nil {
		return types.DefaultMaintenanceParams()
	}
	return p.MaintenanceParams
}
