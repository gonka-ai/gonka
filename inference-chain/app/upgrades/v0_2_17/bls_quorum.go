package v0_2_17

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	blstypes "github.com/productscience/inference/x/bls/types"
)

// dkgQuorumUpgradeInfo is the quorum portion of the software-upgrade proposal's
// info JSON. Cosmovisor keys in the same object are ignored. Omitting both
// fields stores 1/2. Setting one without the other is rejected.
type dkgQuorumUpgradeInfo struct {
	DKGQuorumNumerator   *uint32 `json:"dkg_quorum_numerator"`
	DKGQuorumDenominator *uint32 `json:"dkg_quorum_denominator"`
}

type blsParamsKeeper interface {
	GetParams(ctx context.Context) (blstypes.Params, error)
	SetParams(ctx context.Context, params blstypes.Params) error
}

// MigrateDKGQuorum writes the DKG slot quorum and per-participant dealer
// attempt cap introduced in v0.2.17. The upgrade block is the first time these
// fields exist, so the handler stores 1/2 and 3 directly instead of leaving a
// zero that later code would have to interpret. Upgrade info may set another
// valid fraction, such as 2/3.
func MigrateDKGQuorum(ctx context.Context, k blsParamsKeeper, infoJSON string) error {
	numerator, denominator, err := dkgQuorumFromUpgradeInfo(infoJSON)
	if err != nil {
		return err
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	params.DkgQuorumNumerator = numerator
	params.DkgQuorumDenominator = denominator
	params.MaxDealerAttemptsPerEpoch = blstypes.DefaultMaxDealerAttemptsPerEpoch
	if err := params.Validate(); err != nil {
		return err
	}
	return k.SetParams(ctx, params)
}

func dkgQuorumFromUpgradeInfo(infoJSON string) (uint32, uint32, error) {
	if strings.TrimSpace(infoJSON) == "" {
		return blstypes.DefaultDKGQuorumNumerator, blstypes.DefaultDKGQuorumDenominator, nil
	}
	var info dkgQuorumUpgradeInfo
	if err := json.Unmarshal([]byte(infoJSON), &info); err != nil {
		return 0, 0, fmt.Errorf("unmarshal v0.2.17 upgrade info: %w", err)
	}
	if info.DKGQuorumNumerator == nil && info.DKGQuorumDenominator == nil {
		return blstypes.DefaultDKGQuorumNumerator, blstypes.DefaultDKGQuorumDenominator, nil
	}
	if info.DKGQuorumNumerator == nil || info.DKGQuorumDenominator == nil {
		return 0, 0, fmt.Errorf("dkg_quorum_numerator and dkg_quorum_denominator must be set together")
	}
	return *info.DKGQuorumNumerator, *info.DKGQuorumDenominator, nil
}
