package host

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
	"devshard/chainoracle/blocks/headertime"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type terminalOracle struct {
	*mockoracle.Mock
	down     atomic.Bool
	conflict atomic.Bool
	anchor   int64
}

func (o *terminalOracle) Latest(ctx context.Context) (*blocks.Header, error) {
	if o.down.Load() {
		return nil, errors.New("tip unavailable")
	}
	return o.Mock.Latest(ctx)
}
func (o *terminalOracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	h, err := o.Mock.At(ctx, height)
	if err == nil && h != nil && o.conflict.Load() && height == o.anchor {
		clone := *h
		clone.BlockHash = bytes.Clone(h.BlockHash)
		clone.BlockHash[0] ^= 1
		h = &clone
	}
	return h, err
}

func (o *terminalOracle) AtWait(ctx context.Context, height int64, _ time.Duration) (*blocks.Header, error) {
	return o.At(ctx, height)
}

type terminalFixture struct {
	sm               *state.StateMachine
	store            *storage.SQLite
	dir              string
	source           *terminalOracle
	cfg              types.SessionConfig
	cold, warm, user *signing.Secp256k1Signer
	group            []types.SlotAssignment
}

// The rows model a host's durably admitted final prefix before an outage.
// Their authentic checkpoint can already be old when this process restarts.
func newTerminalFixture(t *testing.T, age time.Duration, admitted, terminal bool) *terminalFixture {
	t.Helper()
	f := &terminalFixture{cold: testutil.MustGenerateKey(t), warm: testutil.MustGenerateKey(t), user: testutil.MustGenerateKey(t), dir: t.TempDir()}
	validator := testutil.MustGenerateKey(t)
	addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
	require.NoError(t, err)
	oracle, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "terminal-retry", Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-age), BlockInterval: time.Second})
	require.NoError(t, err)
	anchor, err := oracle.AdvanceOne()
	require.NoError(t, err)
	f.source = &terminalOracle{Mock: oracle, anchor: anchor.Height}
	f.group = testutil.MakeGroup([]*signing.Secp256k1Signer{f.cold})
	f.cfg = testutil.DefaultConfig(1)
	f.cfg.MainnetAnchorHeight, f.cfg.MainnetAnchorHash = anchor.Height, anchor.BlockHash
	f.store, err = storage.NewSQLite(f.dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.store.Close() })
	require.NoError(t, f.store.CreateSession(storage.CreateSessionParams{EscrowID: "123", EpochID: 1, Version: "v7", CreatorAddr: f.user.Address(), Config: f.cfg, Group: f.group, InitialBalance: 1_000_000}))
	f.sm = f.newSM(t)
	proof, cp, err := lightproof.Build(context.Background(), lightproof.Checkpoint{Height: anchor.Height, Hash: anchor.BlockHash}, anchor.Height, oracle)
	require.NoError(t, err)
	prelude := &types.DevshardTx{Tx: &types.DevshardTx_MainnetHeight{MainnetHeight: &types.MsgMainnetHeight{Height: cp.Height, TimeUnixNano: cp.Time.UnixNano(), LightBlocks: proof}}}
	for n := uint64(1); n <= 2; n++ {
		txs := []*types.DevshardTx{prelude}
		if n == 1 {
			txs = append(txs, testutil.WarmKeyBindTx(0, f.warm.Address(), 1))
			if terminal {
				txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}})
			}
		}
		root, err := f.sm.ApplyLocal(n, txs)
		require.NoError(t, err)
		require.NoError(t, f.store.AppendDiff("123", types.DiffRecord{Diff: testutil.SignDiffWithRoot(t, f.user, "123", n, txs, root), StateHash: root, HostAdmitted: admitted}))
		prelude = proto.Clone(prelude).(*types.DevshardTx)
		prelude.GetMainnetHeight().LightBlocks = nil
	}
	return f
}
func (f *terminalFixture) newSM(t *testing.T) *state.StateMachine {
	t.Helper()
	sm, err := state.NewStateMachine("123", f.cfg, f.group, 1_000_000, f.user.Address(), signing.NewSecp256k1Verifier(), f.store,
		state.WithVersion("v7"), state.WithMainnetHeights(f.source), state.WithHeaderTimes(headertime.New(f.source)),
		state.WithWarmKeyResolver(func(_, cold string) (bool, error) { return cold == f.cold.Address(), nil }),
		state.WithWarmKeyHeightChecker(func(c, w string, id uint64, _ int64, _ time.Time) (bool, error) {
			return c == f.cold.Address() && w == f.warm.Address() && id == 1, nil
		}))
	require.NoError(t, err)
	return sm
}
func (f *terminalFixture) advance(t *testing.T, n int) {
	t.Helper()
	for range n {
		_, err := f.source.AdvanceOne()
		require.NoError(t, err)
	}
}
func (f *terminalFixture) host(t *testing.T, key signing.Signer, opts ...HostOption) *Host {
	t.Helper()
	base := []HostOption{WithStorage(f.store), WithChainOracle(f.source), WithVerifier(signing.NewSecp256k1Verifier()), WithMaxNonceProvider(devshard.StaticMaxNonce(2))}
	h, err := NewHost(f.sm, key, stub.NewInferenceEngine(), "123", f.group, nil, append(base, opts...)...)
	require.NoError(t, err)
	t.Cleanup(h.Close)
	return h
}

func TestTerminalSigningRecoversAdmittedRootAfterSQLiteRestart(t *testing.T) {
	for _, age := range []time.Duration{10 * time.Second, 3 * time.Minute} {
		t.Run(age.String(), func(t *testing.T) {
			f := newTerminalFixture(t, age, true, true)
			f.advance(t, max(4, int(age/time.Second)-3))
			require.ErrorIs(t, f.sm.CheckMainnetHeightFreshness(context.Background(), time.Now()), types.ErrHeaderPending)
			before := f.sm.SnapshotState()
			root, err := f.sm.ComputeStateRoot()
			require.NoError(t, err)
			require.NoError(t, f.store.Close())
			f.store, err = storage.NewSQLite(f.dir)
			require.NoError(t, err)
			f.sm = f.newSM(t)
			records, err := f.store.GetDiffs("123", 1, 2)
			require.NoError(t, err)
			for _, rec := range records {
				_, err = ReplayStoredDiff(context.Background(), f.sm, f.store, "123", rec)
				require.NoError(t, err)
			}
			h := f.host(t, f.warm)
			response, err := h.HandleRequest(context.Background(), HostRequest{Nonce: 2})
			require.NoError(t, err)
			require.NotEmpty(t, response.StateSig)
			require.Equal(t, root, response.StateHash)
			require.Equal(t, before, f.sm.SnapshotState(), "fresh evidence must not mutate the frozen state")
			_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{{Nonce: 3}}, Nonce: 3})
			require.ErrorIs(t, err, types.ErrNonceLimitExceeded)
			records, err = f.store.GetDiffs("123", 3, 3)
			require.NoError(t, err)
			require.Empty(t, records)
		})
	}
}

type conflictingTerminalRoot struct{ storage.Storage }

func (s conflictingTerminalRoot) DiffStateHash(string, uint64) ([]byte, bool, error) {
	return []byte("different root"), true, nil
}

func TestTerminalSigningKeepsRecoveryBoundaries(t *testing.T) {
	for _, scenario := range []string{"revoked", "expired", "grant-pending", "tip-down", "tip-stale", "canonical-conflict", "unmarked", "wrong-root", "unbound", "no-store", "active"} {
		t.Run(scenario, func(t *testing.T) {
			age := 10 * time.Second
			if scenario == "tip-stale" {
				age = 3 * time.Minute
			}
			f := newTerminalFixture(t, age, scenario != "unmarked", scenario != "active")
			if scenario != "tip-stale" {
				f.advance(t, 4)
			}
			switch scenario {
			case "revoked", "expired", "grant-pending":
				cp := f.sm.MainnetCheckpoint()
				f.sm.SetWarmKeyHeightChecker(func(c, w string, id uint64, h int64, at time.Time) (bool, error) {
					if scenario == "grant-pending" && h > cp.Height {
						return false, errors.New("grant view behind")
					}
					allowed := h == cp.Height
					if scenario == "expired" {
						allowed = !at.After(cp.Time.Add(time.Second))
					}
					return c == f.cold.Address() && w == f.warm.Address() && id == 1 && allowed, nil
				})
			case "tip-down":
				f.source.down.Store(true)
			case "canonical-conflict":
				f.source.conflict.Store(true)
			}
			key := signing.Signer(f.warm)
			var opts []HostOption
			if scenario == "unbound" {
				key = testutil.MustGenerateKey(t)
			}
			if scenario == "wrong-root" {
				opts = append(opts, WithStorage(conflictingTerminalRoot{f.store}))
			}
			if scenario == "no-store" {
				opts = append(opts, WithStorage(nil))
			}
			h := f.host(t, key, opts...)
			before := f.sm.SnapshotState()
			response, err := h.HandleRequest(context.Background(), HostRequest{Nonce: 2})
			if scenario == "canonical-conflict" {
				require.ErrorIs(t, err, types.ErrHeaderMismatch)
			} else {
				require.NoError(t, err)
				require.Empty(t, response.StateSig)
			}
			require.Equal(t, before, f.sm.SnapshotState())
		})
	}
}

func TestTerminalRecoveryGossipKeepsOnlyCurrentlyAuthorizedSlots(t *testing.T) {
	f := newWarmRecoveryFixture(t)
	f.apply(testutil.WarmKeyBindTx(1, f.warm[0].Address(), 1), testutil.WarmKeyBindTx(2, f.warm[0].Address(), 1))
	f.apply(&types.DevshardTx{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}})
	for f.sm.Phase() != types.PhaseSettlement {
		f.apply()
	}
	frozen := f.sm.MainnetCheckpoint()
	f.sm.SetWarmKeyHeightChecker(func(c, w string, id uint64, h int64, _ time.Time) (bool, error) {
		return w == f.warm[0].Address() && id == 1 && (c != f.cold[2].Address() || h == frozen.Height), nil
	})
	for range 4 {
		_, err := f.oracle.(interface {
			AdvanceOne() (*blocks.Header, error)
		}).AdvanceOne()
		require.NoError(t, err)
	}
	h := f.host(f.warm[0], stub.NewInferenceEngine())
	records, err := f.store.GetDiffs("warm-recovery", h.LatestNonce(), h.LatestNonce())
	require.NoError(t, err)
	sigs, err := h.ApplyRecoveredDiffs(context.Background(), []types.Diff{records[0].Diff})
	require.NoError(t, err)
	require.Len(t, sigs, 1)
	require.Equal(t, uint32(1), sigs[0].SlotID)
	stored, err := f.store.GetSignatures("warm-recovery", h.LatestNonce())
	require.NoError(t, err)
	require.Contains(t, stored, uint32(1))
	require.NotContains(t, stored, uint32(2))
}
