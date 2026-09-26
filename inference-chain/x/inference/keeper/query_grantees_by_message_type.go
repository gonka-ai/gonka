package keeper

import (
	"context"
	"encoding/base64"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// maxGrantsScanned bounds how many authz grants this query examines. The grant
// set is caller-controllable and runs in EndBlock (infinite gas), so counting
// grants scanned (not just matches) keeps an unbounded scan from stalling a
// validator; only the granter's own grantees can be truncated.
const maxGrantsScanned = 10000

// grantsPageSize bounds each GranterGrants page. It must be nonzero (see the
// call site) and divides maxGrantsScanned so paging stops exactly at the cap.
const grantsPageSize = 100

func (k Keeper) GranteesByMessageType(ctx context.Context, req *types.QueryGranteesByMessageTypeRequest) (*types.QueryGranteesByMessageTypeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if req.GranterAddress == "" {
		return nil, status.Error(codes.InvalidArgument, "granter address cannot be empty")
	}

	if req.MessageTypeUrl == "" {
		return nil, status.Error(codes.InvalidArgument, "message type URL cannot be empty")
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockTime := sdkCtx.BlockTime()
	// devshard and devshardctl ship on their own upgrade cycle, so binaries in
	// the field still probe warm keys by the pre-v0.2.15 marker. Participants
	// who joined after v0.2.15 have no legacy grant at all, so without this
	// alias those binaries would silently see every new host as not-warm. Keep
	// it until no deployed devshard queries the legacy marker; the chain cannot
	// observe that, so removal needs a deliberate rollout check.
	messageTypeURL := req.MessageTypeUrl
	if strings.TrimPrefix(messageTypeURL, "/") == strings.TrimPrefix(types.LegacyMsgStartInferenceTypeURL, "/") {
		messageTypeURL = types.WarmKeyGrantMarkerTypeURL
	}

	authzKeeper := k.AuthzKeeper
	grantees := []*types.Grantee{}
	nextKey := []byte(nil)
	scanned := 0
	capped := false
	for {
		authReq := &authztypes.QueryGranterGrantsRequest{
			Granter: req.GranterAddress,
			Pagination: &query.PageRequest{
				Key: nextKey,
				// A nonzero limit is required: the SDK treats Limit==0 as
				// CountTotal=true and then scans the granter's whole prefix to
				// count it, so a page read would be O(all grants) regardless of
				// the scan cap below.
				Limit: grantsPageSize,
			},
		}
		grants, err := authzKeeper.GranterGrants(ctx, authReq)
		if err != nil {
			return nil, status.Error(codes.Internal, "failed to get grants")
		}

		for _, grant := range grants.Grants {
			if scanned >= maxGrantsScanned {
				capped = true
				break
			}
			scanned++
			if grant.Expiration != nil && grant.Expiration.Before(blockTime) {
				continue
			}

			authorization := grant.Authorization.GetCachedValue()

			if genericAuth, ok := authorization.(*authztypes.GenericAuthorization); ok {
				if strings.TrimPrefix(genericAuth.Msg, "/") == strings.TrimPrefix(messageTypeURL, "/") {
					granteeAddr, err := sdk.AccAddressFromBech32(grant.Grantee)
					if err != nil {
						k.LogError("invalid grantee address", types.Participants, "address", grant.Grantee, "error", err)
						continue
					}

					account := k.AccountKeeper.GetAccount(sdkCtx, granteeAddr)
					if account == nil {
						k.LogError("account not found", types.Participants, "address", grant.Grantee)
						continue
					}

					pubKey := account.GetPubKey()
					pubKeyStr := ""
					if pubKey != nil {
						pubKeyStr = base64.StdEncoding.EncodeToString(pubKey.Bytes())
					}

					grantees = append(grantees, &types.Grantee{
						Address: grant.Grantee,
						PubKey:  pubKeyStr,
					})
				}
			}
		}

		if capped || grants.Pagination == nil || len(grants.Pagination.NextKey) == 0 {
			break
		}
		if scanned >= maxGrantsScanned {
			capped = true
			break
		}
		nextKey = grants.Pagination.NextKey
	}

	if capped {
		k.LogWarn("GranteesByMessageType hit the grant scan cap; result may be truncated", types.Participants,
			"granter", req.GranterAddress,
			"messageType", req.MessageTypeUrl,
			"cap", maxGrantsScanned)
	}

	k.LogInfo("GranteesByMessageType query called", types.Participants,
		"granter", req.GranterAddress,
		"messageType", req.MessageTypeUrl,
		"grantee_count", len(grantees))

	return &types.QueryGranteesByMessageTypeResponse{
		Grantees: grantees,
	}, nil
}
