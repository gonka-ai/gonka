package session

import (
	"context"
	"fmt"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"devshard/bridge"
	"devshard/chainoracle/blocks/headertime"
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

type recoveryGrantSource struct{ entry bridge.WarmKeyEntry }

func (s recoveryGrantSource) LiveWarmKeys(string) ([]bridge.WarmKeyEntry, error) { return nil, nil }
func (s recoveryGrantSource) WarmKeyEntry(c string, id uint64) (bridge.WarmKeyEntry, bool, error) {
	return s.entry, s.entry.Granter == c && s.entry.ID == id, nil
}
func (s recoveryGrantSource) WarmKeyEntryAtHeight(c string, id uint64, _ int64) (bridge.WarmKeyEntry, bool, error) {
	return s.WarmKeyEntry(c, id)
}

func TestWarmAdmissionSnapshotCannotBlessUncheckedHistory(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		t.Run(fmt.Sprintf("admitted-%t", admitted), func(t *testing.T) {
			validator, cold, warm, user := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
			addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
			require.NoError(t, err)
			oracle, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "warm-snapshot", Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
			require.NoError(t, err)
			anchor, err := oracle.AdvanceOne()
			require.NoError(t, err)
			cfg := testutil.DefaultConfig(1)
			cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, anchor.BlockHash
			group := testutil.MakeGroup([]*signing.Secp256k1Signer{cold})
			store, err := storage.NewSQLite(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: "1", EpochID: 1, Version: "v7", CreatorAddr: user.Address(), Config: cfg, Group: group, InitialBalance: 1_000_000}))
			grants := recoveryGrantSource{entry: bridge.WarmKeyEntry{Granter: cold.Address(), Grantee: warm.Address(), ID: 1, GrantedAtHeight: anchor.Height, RevokedAtHeight: anchor.Height + 1, End: "REVOKED"}}
			sm, err := state.NewStateMachine("1", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), store, state.WithVersion("v7"), state.WithMainnetHeights(oracle), state.WithWarmKeyHeightChecker(func(c, w string, id uint64, h int64, at time.Time) (bool, error) {
				return bridge.CheckWarmKeyAtHeight(grants, c, w, id, h, at)
			}))
			require.NoError(t, err)
			for _, tx := range []*types.DevshardTx{testutil.WarmKeyBindTx(0, warm.Address(), 1), testutil.SignedWarmKeyRevokeTx(t, 0, warm, 1, "1")} {
				height, err := sm.BuildMainnetHeightTx(context.Background())
				require.NoError(t, err)
				n := sm.LatestNonce() + 1
				txs := []*types.DevshardTx{height, tx}
				root, err := sm.ApplyLocal(n, txs)
				require.NoError(t, err)
				require.NoError(t, store.AppendDiff("1", types.DiffRecord{Diff: testutil.SignDiffWithRoot(t, user, "1", n, txs, root), StateHash: root, HostAdmitted: admitted}))
			}
			require.NoError(t, saveHostSnapshot(store, sm, "1", 2))
			_, err = oracle.AdvanceOne()
			require.NoError(t, err)
			mgr := NewHostManager(store, cold, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, "v7", &mockBridge{}, nil, nil)
			mgr.SetWarmKeyGrantSource(grants)
			mgr.headerTimes = headertime.New(
				headertime.WithFiller(headertime.OracleFiller(oracle, headertime.OriginChain, false)),
				headertime.WithLatestOracle(oracle),
				headertime.WithLightBlockSource(oracle),
			)
			mgr.chainOracle = oracle
			srv, _, err := mgr.recoverStoredSession("1")
			if admitted {
				require.NoError(t, err)
				t.Cleanup(func() { closeTransportServer(srv) })
				require.Equal(t, uint64(2), srv.Host().LatestNonce())
				response, err := srv.Host().HandleRequest(context.Background(), host.HostRequest{Nonce: 2})
				require.NoError(t, err)
				require.NotEmpty(t, response.StateSig, "previously admitted history remains signable after restart and revocation")
			} else {
				require.ErrorIs(t, err, types.ErrInvalidWarmKeyEntry)
				require.Nil(t, srv)
				meta, err := store.GetSessionMeta("1")
				require.NoError(t, err)
				require.Equal(t, uint64(1), meta.HostAdmissionNonce, "only the independently revalidated prefix may be marked")
			}
		})
	}
}
