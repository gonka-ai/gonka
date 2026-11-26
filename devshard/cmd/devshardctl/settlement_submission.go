package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"common/chain"
	chaintx "common/chain/tx"
	"devshard/state"
	"devshard/user"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

const settlementRebuildLimit = 3

// currentSettlementGrants queries the chain's current marker grants. The
// query evaluates expiry at the chain's block time and bypasses warm log
// caches and the dAPI feed used by historical authorization.
func currentSettlementGrants(client chain.InferenceClient) user.SettlementGrantQuery {
	return func(ctx context.Context, cold string) (map[string]struct{}, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		resp, err := client.GranteesByMessageType(ctx, &inferencetypes.QueryGranteesByMessageTypeRequest{
			GranterAddress: cold, MessageTypeUrl: inferencetypes.WarmKeyGrantMarkerTypeURL,
		})
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return nil, fmt.Errorf("nil chain grant response for %s", cold)
		}
		keys := make(map[string]struct{}, len(resp.GetGrantees()))
		for _, grantee := range resp.GetGrantees() {
			if grantee != nil && grantee.Address != "" {
				keys[grantee.Address] = struct{}{}
			}
		}
		return keys, nil
	}
}

func (g *Gateway) submitV7Settlement(ctx context.Context, rt *devshardRuntime, txMgr *chaintx.Manager, signer chaintx.UnorderedSigner) (*chaintx.SettleDevshardEscrowResult, error) {
	q, err := rt.session.NewSettlementQuorum()
	if err != nil {
		return nil, err
	}
	nonce := rt.session.Nonce()
	snapshot := rt.proxy.sm.SnapshotState()
	query := currentSettlementGrants(g.chainClient.InferenceQueryClient())
	var attempts []map[uint32][]byte
	var previousErr error
	for range settlementRebuildLimit + 1 {
		signatures, err := q.Build(ctx, query)
		if err != nil {
			return nil, errors.Join(previousErr, err)
		}
		for _, tried := range attempts {
			if maps.EqualFunc(tried, signatures, bytes.Equal) {
				return nil, fmt.Errorf("settlement quorum is unchanged after rejection: %w", previousErr)
			}
		}
		payload, err := state.BuildSettlement(rt.proxy.escrowID, snapshot, signatures, nonce)
		if err != nil {
			return nil, err
		}
		encoded, err := buildSettlementJSON(payload)
		if err != nil {
			return nil, err
		}
		params, err := settleParamsFromJSON(encoded)
		if err != nil {
			return nil, err
		}
		result, err := txMgr.SettleDevshardEscrowWithPreflight(ctx, signer, params, func(ctx context.Context) error {
			return q.Validate(ctx, query, params.Nonce, params.StateRoot, signatures)
		})
		if err == nil {
			return result, nil
		}
		previousErr = err
		if errors.Is(err, user.ErrSettlementSignaturesChanged) {
			continue
		}
		if !errors.Is(err, chaintx.ErrTxRejected) {
			return nil, err
		}
		attempts = append(attempts, signatures)
		// A definitive failure may race a revoke after preflight. Re-read
		// authority and rebuild only if one of the submitted signers was lost.
		if q.Validate(ctx, query, params.Nonce, params.StateRoot, signatures) == nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("settlement quorum changed through all rebuilds: %w", previousErr)
}
