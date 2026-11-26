package user

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/lightproof"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
	"github.com/stretchr/testify/require"
)

type signingRetryOracle struct {
	blocks.BlockOracle
	lightproof.Source
	unavailable atomic.Bool
}

func (o *signingRetryOracle) Latest(ctx context.Context) (*blocks.Header, error) {
	if o.unavailable.Load() {
		return nil, errors.New("temporary mainnet tip lookup failure")
	}
	return o.BlockOracle.Latest(ctx)
}

func (o *signingRetryOracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	if o.unavailable.Load() {
		return nil, errors.New("temporary canonical header lookup failure")
	}
	return o.BlockOracle.At(ctx, height)
}

func TestCollectSignaturesRetriesCurrentCheckpointAfterOracleRecovers(t *testing.T) {
	for _, remainsFresh := range []bool{true, false} {
		name := "fresh"
		if !remainsFresh {
			name = "stale"
		}
		t.Run(name, func(t *testing.T) {
			validator := testutil.MustGenerateKey(t)
			address, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
			require.NoError(t, err)
			source, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "retry-signature", Validators: []mockoracle.MockValidator{{Signer: validator, Address: address, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
			require.NoError(t, err)
			anchor, err := source.AdvanceOne()
			require.NoError(t, err)
			cfg := testutil.DefaultConfig(1)
			cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, bytes.Clone(anchor.BlockHash)
			owner, signer := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
			group := testutil.MakeGroup([]*signing.Secp256k1Signer{signer})
			verifier := signing.NewSecp256k1Verifier()
			const escrow = "retry-signature"
			newSM := func(oracle state.MainnetHeightSource) *state.StateMachine {
				sm, err := state.NewStateMachine(escrow, cfg, group, 1_000_000, owner.Address(), verifier, testutil.MustMemoryStore(t, escrow, owner.Address(), cfg, group, 1_000_000), state.WithVersion("v7"), state.WithMainnetHeights(oracle))
				require.NoError(t, err)
				return sm
			}
			oracle := &signingRetryOracle{BlockOracle: source, Source: source}
			hostSM := newSM(oracle)
			hostStore := testutil.MustMemoryStore(t, escrow, owner.Address(), cfg, group, 1_000_000)
			h, err := host.NewHost(hostSM, signer, stub.NewInferenceEngine(), escrow, group, nil, host.WithStorage(hostStore), host.WithChainOracle(oracle))
			require.NoError(t, err)
			defer h.Close()
			client := &InProcessClient{Host: h}
			session, err := NewSession(newSM(source), owner, escrow, group, []HostClient{client}, verifier, WithCollectRetry(0, time.Millisecond, time.Second))
			require.NoError(t, err)
			// Reach settlement without delivering any state signatures. The oracle
			// outage affects signing, not verification of the carried light blocks.
			oracle.unavailable.Store(true)
			session.mu.Lock()
			first, _, err := session.composeDiffLocked(context.Background(), []*types.DevshardTx{{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}}})
			session.mu.Unlock()
			require.NoError(t, err)
			session.mu.Lock()
			last, _, err := session.composeDiffLocked(context.Background(), nil)
			session.mu.Unlock()
			require.NoError(t, err)
			require.Equal(t, types.PhaseSettlement, session.sm.Phase())
			resp, err := client.Send(context.Background(), host.HostRequest{Diffs: []types.Diff{first, last}, Nonce: last.Nonce}, nil, nil)
			require.NoError(t, err)
			require.Empty(t, resp.StateSig)
			session.mu.Lock()
			require.NoError(t, session.processResponse(0, resp, last.Nonce))
			session.mu.Unlock()
			require.Equal(t, last.Nonce, session.hostSyncNonce[0])
			oracle.unavailable.Store(false)
			if !remainsFresh {
				for range 4 {
					_, err = source.AdvanceOne()
					require.NoError(t, err)
				}
			}
			rootBefore, err := hostSM.ComputeStateRoot()
			require.NoError(t, err)
			weight, threshold, _ := session.CollectSignatures(context.Background(), last.Nonce)
			if remainsFresh {
				require.GreaterOrEqual(t, weight, threshold, "a caught-up host must retry its withheld signature")
				require.NoError(t, session.Finalize(context.Background()))
			} else {
				require.Zero(t, weight, "retry still applies the normal freshness policy")
			}
			require.Equal(t, last.Nonce, session.Nonce(), "retry must not consume another nonce")
			require.Equal(t, last.Nonce, h.LatestNonce())
			rootAfter, err := hostSM.ComputeStateRoot()
			require.NoError(t, err)
			require.Equal(t, rootBefore, rootAfter)
		})
	}
}
