package user

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"devshard/bridge"
	"devshard/chainoracle/blocks/headertime"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/types"
	"github.com/stretchr/testify/require"
)

type localHistoricalGrants struct {
	*testutil.WarmKeyGrants
	lookups *atomic.Int32
}

func (g localHistoricalGrants) WarmKeyLogAtHeight(cold string, _ int64) (map[uint64]bridge.WarmKeyEntry, error) {
	entries, err := g.LiveWarmKeys(cold)
	out := make(map[uint64]bridge.WarmKeyEntry, len(entries))
	for _, entry := range entries {
		out[entry.ID] = entry
	}
	return out, err
}

func (g localHistoricalGrants) WarmKeyEntryAtHeight(cold string, id uint64, _ int64) (bridge.WarmKeyEntry, bool, error) {
	if g.lookups != nil {
		g.lookups.Add(1)
	}
	return g.WarmKeyEntry(cold, id)
}

func TestLocalSessionRecoversV7WarmHistory(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		name := "journal"
		if snapshot {
			name = "snapshot"
		}
		t.Run(name, func(t *testing.T) {
			owner, cold, warm := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
			validator := testutil.MustGenerateKey(t)
			address, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
			require.NoError(t, err)
			oracle, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "local-warm", Validators: []mockoracle.MockValidator{{Signer: validator, Address: address, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
			require.NoError(t, err)
			anchor, err := oracle.AdvanceOne()
			require.NoError(t, err)
			cfg := testutil.DefaultConfig(1)
			cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, bytes.Clone(anchor.BlockHash)
			group := testutil.MakeGroup([]*signing.Secp256k1Signer{cold})
			grants := localHistoricalGrants{WarmKeyGrants: testutil.NewWarmKeyGrants(), lookups: &atomic.Int32{}}
			grantID := grants.Grant(cold.Address(), warm.Address())
			dir := t.TempDir()
			store, err := storage.NewSQLite(dir)
			require.NoError(t, err)
			const escrow = "local-warm"
			require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: escrow, Version: "v7", CreatorAddr: owner.Address(), Config: cfg, Group: group, InitialBalance: 1_000_000}))
			verifier := signing.NewSecp256k1Verifier()
			times := headertime.New(headertime.WithFiller(headertime.OracleFiller(oracle, headertime.OriginChain, false)), headertime.WithLatestOracle(oracle), headertime.WithLightBlockSource(oracle))
			sm, err := state.NewStateMachine(escrow, cfg, group, 1_000_000, owner.Address(), verifier, store, state.WithVersion("v7"), state.WithMainnetHeights(oracle), state.WithHeaderTimes(times))
			require.NoError(t, err)
			writer, err := NewSession(sm, owner, escrow, group, make([]HostClient, 1), verifier, WithStorage(store), WithWarmKeyGrantSource(grants))
			require.NoError(t, err)
			writer.mu.Lock()
			_, _, err = writer.composeDiffLocked(context.Background(), nil)
			writer.mu.Unlock()
			require.NoError(t, err)
			root, err := sm.ComputeStateRoot()
			require.NoError(t, err)
			if snapshot {
				require.NoError(t, writer.FlushSnapshot())
			}
			require.NoError(t, writer.Close())
			local := LocalSessionConfig{PrivateKeyHex: owner.PrivateKeyHex(), EscrowID: escrow, StoragePath: dir, HeaderTimes: times}
			if !snapshot {
				wrong := localHistoricalGrants{WarmKeyGrants: testutil.NewWarmKeyGrants()}
				wrong.Grant(cold.Address(), testutil.MustGenerateKey(t).Address())
				local.WarmKeys = wrong
				_, _, err = NewLocalSession(local)
				require.ErrorIs(t, err, types.ErrWarmKeyUnconfirmed, "replay must verify a binding introduced by a diff")
			}
			grants.lookups.Store(0)
			local.WarmKeys = grants
			loaded, restored, err := NewLocalSession(local)
			require.NoError(t, err)
			defer loaded.Close()
			require.Equal(t, uint64(1), loaded.Nonce())
			require.Equal(t, warm.Address(), restored.WarmKeyHistory()[0][0].Addr)
			require.Equal(t, grantID, restored.WarmKeyHistory()[0][0].GrantID)
			recoveredRoot, err := restored.ComputeStateRoot()
			require.NoError(t, err)
			require.Equal(t, root, recoveredRoot)
			require.Positive(t, grants.lookups.Load(), "both snapshot and journal recovery must consult the height-aware source")
		})
	}
}
