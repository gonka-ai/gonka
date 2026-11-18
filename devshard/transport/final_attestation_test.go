package transport

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"devshard"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/gossip"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type finalAttestationPeer struct {
	host *host.Host
	got  chan types.GossipSig
}

func (p *finalAttestationPeer) GossipNonce(_ context.Context, nonce uint64, root, sig []byte, slot uint32) error {
	err := p.host.AccumulateGossipSig(nonce, root, sig, slot)
	p.got <- types.GossipSig{Nonce: nonce, StateHash: root, Sig: sig, SlotID: slot}
	return err
}

func (*finalAttestationPeer) GossipTxs(context.Context, []*types.DevshardTx) error { return nil }

func TestServeInferenceFinalAttestationGossipsOnlyAnAuthorizedSlot(t *testing.T) {
	for _, revoked := range []uint32{0, 1} {
		t.Run(fmt.Sprintf("revoked-slot-%d", revoked), func(t *testing.T) {
			const escrow = "final-attestation"
			ctx := context.Background()
			owner, warm, validator := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
			cold := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
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
			newMachine := func() (*state.StateMachine, *storage.Memory) {
				store := testutil.MustMemoryStore(t, escrow, owner.Address(), cfg, group, 1_000_000)
				sm, err := state.NewStateMachine(escrow, cfg, group, 1_000_000, owner.Address(), verifier, store,
					state.WithVersion("v7"), state.WithMainnetHeights(oracle),
					state.WithWarmKeyHeightChecker(func(c, w string, id uint64, height int64, _ time.Time) (bool, error) {
						return w == warm.Address() && id == 1 && (c == cold[0].Address() || c == cold[1].Address()) &&
							(c != cold[revoked].Address() || revokedAt == 0 || height < revokedAt), nil
					}))
				require.NoError(t, err)
				return sm, store
			}
			sm, store := newMachine()
			peerSM, peerStore := newMachine()
			var root []byte
			for sm.Phase() != types.PhaseSettlement {
				height, err := sm.BuildMainnetHeightTx(ctx)
				require.NoError(t, err)
				txs := []*types.DevshardTx{height}
				if sm.LatestNonce() == 0 {
					txs = append(txs, testutil.WarmKeyBindTx(0, warm.Address(), 1), testutil.WarmKeyBindTx(1, warm.Address(), 1),
						&types.DevshardTx{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}})
				}
				nonce := sm.LatestNonce() + 1
				root, err = sm.ApplyLocal(nonce, txs)
				require.NoError(t, err)
				peerRoot, err := peerSM.ApplyLocal(nonce, txs)
				require.NoError(t, err)
				require.Equal(t, root, peerRoot)
				record := types.DiffRecord{Diff: testutil.SignDiffWithRoot(t, owner, escrow, nonce, txs, root), StateHash: root, HostAdmitted: true}
				require.NoError(t, store.AppendDiff(escrow, record))
				require.NoError(t, peerStore.AppendDiff(escrow, record))
			}
			nonce := sm.LatestNonce()
			frozen := sm.SnapshotState()
			revokedAt = sm.MainnetCheckpoint().Height + 1
			for range 4 {
				_, err = oracle.AdvanceOne()
				require.NoError(t, err)
			}
			newHost := func(machine *state.StateMachine, db storage.Storage, key signing.Signer) *host.Host {
				h, err := host.NewHost(machine, key, stub.NewInferenceEngine(), escrow, group, nil,
					host.WithStorage(db), host.WithVerifier(verifier), host.WithChainOracle(oracle), host.WithMaxNonceProvider(devshard.StaticMaxNonce(uint32(nonce))))
				require.NoError(t, err)
				t.Cleanup(h.Close)
				return h
			}
			h := newHost(sm, store, warm)
			peer := &finalAttestationPeer{host: newHost(peerSM, peerStore, cold[2]), got: make(chan types.GossipSig, 1)}
			content, err := proto.Marshal(&types.StateSignatureContent{EscrowId: escrow, Nonce: nonce, StateRoot: root})
			require.NoError(t, err)
			coldSig, err := cold[revoked].Sign(content)
			require.NoError(t, err)
			require.NoError(t, peerStore.AddSignature(escrow, nonce, revoked, coldSig))
			g := gossip.NewGossip(escrow, 0, []gossip.PeerClient{peer}, nil)
			srv, err := NewServer(h, store, verifier, owner.Address(), WithServerGossip(g))
			require.NoError(t, err)
			body, err := json.Marshal(InferenceRequest{Nonce: nonce})
			require.NoError(t, err)
			sink := NewChatFrameSink(func([]byte) error { return nil })
			require.NoError(t, srv.ServeInference(ctx, InferenceCall{SessionID: escrow, Sender: owner.Address(), Body: body, Source: "test", Sink: sink}))
			require.NoError(t, sink.Close())
			select {
			case sent := <-peer.got:
				require.Equal(t, uint32(1)-revoked, sent.SlotID, "gossip must keep the slot chosen by final attestation")
				require.Equal(t, nonce, sent.Nonce)
				require.Equal(t, root, sent.StateHash)
			case <-time.After(time.Second):
				t.Fatal("final attestation was not gossiped")
			}
			sigs, err := peerStore.GetSignatures(escrow, nonce)
			require.NoError(t, err)
			require.Equal(t, coldSig, sigs[revoked], "the revoked warm key must not overwrite the participant's cold signature")
			require.NotEmpty(t, sigs[uint32(1)-revoked])
			require.Equal(t, frozen, sm.SnapshotState())
			require.Equal(t, frozen, peerSM.SnapshotState())
		})
	}
}
