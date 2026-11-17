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
	"devshard"
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
			h, err := host.NewHost(hostSM, signer, stub.NewInferenceEngine(), escrow, group, nil, host.WithStorage(hostStore), host.WithChainOracle(oracle), host.WithMaxNonceProvider(devshard.StaticMaxNonce(2)))
			require.NoError(t, err)
			defer h.Close()
			client := &InProcessClient{Host: h}
			session, err := NewSession(newSM(source), owner, escrow, group, []HostClient{client}, verifier, WithCollectRetry(0, time.Millisecond, time.Second))
			require.NoError(t, err)
			session.SetChainMaxNonce(2)
			// The public finalizer persists the last allowed diff, but the host
			// cannot sign during the oracle outage. A later retry has no nonce
			// left for changing the frozen checkpoint.
			oracle.unavailable.Store(true)
			require.ErrorContains(t, session.Finalize(context.Background()), "insufficient signatures")
			require.Equal(t, types.PhaseSettlement, session.sm.Phase())
			lastNonce := session.Nonce()
			require.Equal(t, uint64(2), lastNonce)
			meta, err := hostStore.GetSessionMeta(escrow)
			require.NoError(t, err)
			require.Equal(t, lastNonce, meta.HostAdmissionNonce)
			require.Equal(t, lastNonce, session.hostSyncNonce[0])
			oracle.unavailable.Store(false)
			if !remainsFresh {
				for range 4 {
					_, err = source.AdvanceOne()
					require.NoError(t, err)
				}
			}
			rootBefore, err := hostSM.ComputeStateRoot()
			require.NoError(t, err)
			weight, threshold, _ := session.CollectSignatures(context.Background(), lastNonce)
			require.GreaterOrEqual(t, weight, threshold, "the admitted terminal root can be signed using a fresh live tip")
			require.NoError(t, session.Finalize(context.Background()))
			require.Equal(t, lastNonce, session.Nonce(), "retry must not consume another nonce")
			require.Equal(t, lastNonce, h.LatestNonce())
			rootAfter, err := hostSM.ComputeStateRoot()
			require.NoError(t, err)
			require.Equal(t, rootBefore, rootAfter)
		})
	}
}
