package user

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	"devshard/storage"
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

// Drop local response metadata as HTTP and Connect do. Collection must use the
// existing slot-indexed signature endpoint, never an in-process-only hint.
type wireSigningRetryClient struct{ *InProcessClient }

type sendOnlySigningRetryClient struct{ HostClient }

func (c *wireSigningRetryClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	resp, err := c.InProcessClient.Send(ctx, req, stream, receiptHandler)
	if resp != nil {
		copy := *resp
		copy.StateSigningSlots = nil
		resp = &copy
	}
	return resp, err
}

func TestCollectFinalSignaturesPreservesIssuerSlotsAfterPartialRevocation(t *testing.T) {
	for _, mode := range []string{"caught-up", "catch-up-response", "direct-final-response", "no-signature-endpoint"} {
		t.Run(mode, func(t *testing.T) {
			const escrow = "retry-partial"
			ctx := context.Background()
			owner, warm, validator := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
			cold := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
			group := testutil.MakeGroup(cold)
			addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
			require.NoError(t, err)
			oracle, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: escrow, Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
			require.NoError(t, err)
			anchor, err := oracle.AdvanceOne()
			require.NoError(t, err)
			cfg := testutil.DefaultConfig(len(group))
			cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, bytes.Clone(anchor.BlockHash)
			verifier := signing.NewSecp256k1Verifier()
			var revokedAt int64
			newSM := func() (*state.StateMachine, *storage.Memory) {
				store := testutil.MustMemoryStore(t, escrow, owner.Address(), cfg, group, 1_000_000)
				sm, err := state.NewStateMachine(escrow, cfg, group, 1_000_000, owner.Address(), verifier, store,
					state.WithVersion("v7"), state.WithMainnetHeights(oracle),
					state.WithWarmKeyHeightChecker(func(c, w string, id uint64, height int64, _ time.Time) (bool, error) {
						return w == warm.Address() && id == 1 && (c == cold[0].Address() || c == cold[1].Address()) &&
							(c != cold[0].Address() || revokedAt == 0 || height < revokedAt), nil
					}))
				require.NoError(t, err)
				return sm, store
			}
			gatewaySM, gatewayStore := newSM()
			session, err := NewSession(gatewaySM, owner, escrow, group, make([]HostClient, len(group)), verifier,
				WithStorage(gatewayStore), WithCollectRetry(0, time.Millisecond, time.Second))
			require.NoError(t, err)
			t.Cleanup(func() { _ = session.Close() })
			hostSM, hostStore := newSM()
			for gatewaySM.Phase() != types.PhaseSettlement {
				var txs []*types.DevshardTx
				if session.Nonce() == 0 {
					txs = []*types.DevshardTx{testutil.WarmKeyBindTx(0, warm.Address(), 1), testutil.WarmKeyBindTx(1, warm.Address(), 1),
						{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}}}
				}
				session.mu.Lock()
				diff, _, err := session.composeDiffLocked(ctx, txs)
				session.mu.Unlock()
				require.NoError(t, err)
				root, err := hostSM.ApplyLocal(diff.Nonce, diff.Txs)
				require.NoError(t, err)
				require.Equal(t, diff.PostStateRoot, root)
				require.NoError(t, hostStore.AppendDiff(escrow, types.DiffRecord{Diff: diff, StateHash: root, HostAdmitted: true}))
			}
			nonce := session.Nonce()
			session.SetChainMaxNonce(uint32(nonce))
			before := gatewaySM.SnapshotState()
			revokedAt = gatewaySM.MainnetCheckpoint().Height + 1
			for range 4 {
				_, err = oracle.AdvanceOne()
				require.NoError(t, err)
			}
			newClient := func(key signing.Signer) HostClient {
				h, err := host.NewHost(hostSM, key, stub.NewInferenceEngine(), escrow, group, nil,
					host.WithStorage(hostStore), host.WithVerifier(verifier), host.WithChainOracle(oracle), host.WithMaxNonceProvider(devshard.StaticMaxNonce(uint32(nonce))))
				require.NoError(t, err)
				t.Cleanup(h.Close)
				return &wireSigningRetryClient{&InProcessClient{Host: h}}
			}
			client := newClient(warm)
			session.clients = []HostClient{client, client}
			if mode == "no-signature-endpoint" {
				session.clients = []HostClient{&sendOnlySigningRetryClient{client}, &sendOnlySigningRetryClient{client}}
			}
			if mode != "catch-up-response" {
				for i := range group {
					session.hostSyncNonce[i] = nonce
				}
			}
			if mode == "direct-final-response" {
				resp, err := client.Send(ctx, host.HostRequest{Nonce: nonce}, nil, nil)
				require.NoError(t, err)
				require.NoError(t, session.ProcessResponse(0, resp, nonce))
			}
			weight, threshold, _ := session.CollectSignatures(ctx, nonce)
			require.Less(t, weight, threshold, "the still-authorized B signature must not also count for revoked A")
			expectedWeight := uint32(1)
			if mode == "no-signature-endpoint" {
				expectedWeight = 0
			}
			require.Equal(t, expectedWeight, weight)
			sigs := session.Signatures()[nonce]
			require.NotContains(t, sigs, uint32(0))
			if mode != "no-signature-endpoint" {
				require.NotEmpty(t, sigs[1])
			}
			issued, err := hostStore.GetSignatures(escrow, nonce)
			require.NoError(t, err)
			require.NotContains(t, issued, uint32(0))
			require.NotEmpty(t, issued[1])
			// The original cold participant can still finish collection without
			// another diff, new warm binding, or change to the frozen result.
			session.clients[0] = newClient(cold[0])
			if mode == "no-signature-endpoint" {
				// Direct cold signatures remain usable by clients without the
				// optional index endpoint; they identify the participant.
				session.clients = []HostClient{&sendOnlySigningRetryClient{session.clients[0]}, &sendOnlySigningRetryClient{newClient(cold[1])}}
			}
			weight, threshold, _ = session.CollectSignatures(ctx, nonce)
			require.GreaterOrEqual(t, weight, threshold)
			require.NoError(t, session.Finalize(ctx))
			require.Equal(t, nonce, session.Nonce())
			require.Equal(t, before, gatewaySM.SnapshotState())
			require.Equal(t, before, hostSM.SnapshotState())
		})
	}
}
