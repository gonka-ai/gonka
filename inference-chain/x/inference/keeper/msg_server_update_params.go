package keeper

import (
	"context"
	"fmt"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) UpdateParams(goCtx context.Context, req *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	if err := k.CheckPermission(goCtx, req, GovernancePermission); err != nil {
		return nil, err
	}

	if err := req.Params.Validate(); err != nil {
		return nil, errorsmod.Wrap(err, "invalid params")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	current, err := k.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateApprovedVersionProgression(
		current.DevshardEscrowParams,
		req.Params.DevshardEscrowParams,
	); err != nil {
		return nil, errorsmod.Wrap(err, "invalid params")
	}
	if err := k.SetParams(ctx, req.Params); err != nil {
		return nil, err
	}

	err = k.PrecomputeSPRTValues(ctx)
	if err != nil {
		k.LogError("Failed to precompute SPRT values", types.Validation, "error", err)
		return nil, err
	}

	return &types.MsgUpdateParamsResponse{}, nil
}

func validateApprovedVersionProgression(current, proposed *types.DevshardEscrowParams) error {
	if proposed == nil {
		return fmt.Errorf("devshard escrow params cannot be removed")
	}
	if current == nil {
		return nil
	}
	proposedNames := make(map[string]struct{}, len(proposed.ApprovedVersions))
	for _, version := range proposed.ApprovedVersions {
		// proposed has already passed Params.Validate; keep this helper robust
		// against corrupt legacy state rather than panic while comparing it.
		if version == nil {
			return fmt.Errorf("proposed approved devshard version cannot be null")
		}
		proposedNames[version.Name] = struct{}{}
	}
	for _, version := range current.ApprovedVersions {
		if version == nil {
			return fmt.Errorf("current approved devshard version cannot be null")
		}
		if _, ok := proposedNames[version.Name]; !ok {
			return fmt.Errorf(
				"approved devshard version %q cannot be removed; version names are append-only",
				version.Name,
			)
		}
	}
	return nil
}
