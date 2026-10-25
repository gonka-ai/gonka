package user_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
	"devshard/user"

	"github.com/stretchr/testify/require"
)

func TestRecoverSessionFromHostsRebuildsRealHostStatsAndFinalizes(t *testing.T) {
	const (
		escrowID       = "2039"
		initialBalance = uint64(1_000_000)
	)
	creator := testutil.MustGenerateKey(t)
	hostSigners := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	group := testutil.MakeGroup(hostSigners)
	verifier := signing.NewSecp256k1Verifier()
	config := types.NormalizeSessionConfig(types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       10,
		VoteThreshold:    1,
		ValidationRate:   0,
	}, len(group))

	clients := make([]user.HostClient, len(group))
	for i, hs := range hostSigners {
		hStore := storage.NewMemory()
		t.Cleanup(func() { _ = hStore.Close() })
		require.NoError(t, hStore.CreateSession(storage.CreateSessionParams{
			EscrowID:       escrowID,
			EpochID:        1,
			Version:        testutil.RuntimeTestVersion,
			CreatorAddr:    creator.Address(),
			Config:         config,
			Group:          group,
			InitialBalance: initialBalance,
		}))
		hSM, err := state.NewStateMachine(escrowID, config, group, initialBalance, creator.Address(), verifier, hStore,
			state.WithVersion(testutil.RuntimeTestVersion))
		require.NoError(t, err)
		h, err := host.NewHost(hSM, hs, stub.NewInferenceEngine(), escrowID, group, nil,
			host.WithStorage(hStore), host.WithVerifier(verifier))
		require.NoError(t, err)
		clients[i] = &user.InProcessClient{Host: h}
	}

	userPath := filepath.Join(t.TempDir(), "user.db")
	userStore, err := storage.NewSQLite(userPath)
	require.NoError(t, err)
	require.NoError(t, userStore.CreateSession(storage.CreateSessionParams{
		EscrowID:       escrowID,
		EpochID:        1,
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    creator.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: initialBalance,
	}))
	userSM, err := state.NewStateMachine(escrowID, config, group, initialBalance, creator.Address(), verifier, userStore,
		state.WithVersion(testutil.RuntimeTestVersion))
	require.NoError(t, err)
	liveSess, err := user.NewSession(userSM, creator, escrowID, group, clients, verifier,
		user.WithStorage(userStore), user.WithDisableHeightSyncHeartbeat(true))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Serve real inferences so balance decreases and host stats accumulate.
	prompt := []byte(`{"messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)
	_, err = liveSess.SendInference(ctx, user.InferenceParams{
		Model:       "Qwen/Test",
		Prompt:      prompt,
		InputLength: uint64(len(prompt)),
		MaxTokens:   32,
		StartedAt:   time.Now().Unix(),
	})
	require.NoError(t, err)
	require.NoError(t, liveSess.SendPendingDiff(ctx))

	beforeCorruption := userSM.SnapshotState()
	require.Less(t, beforeCorruption.Balance, initialBalance, "served inference must spend escrow balance")
	var totalEarnedBefore uint64
	for _, st := range beforeCorruption.HostStats {
		totalEarnedBefore += st.Cost
	}
	require.Greater(t, totalEarnedBefore, uint64(0), "hosts must have real earned stats")
	require.NoError(t, liveSess.Close())

	// Corrupt the local SQLite journal with a trailing gap so RecoverSession fails with ErrLocalStateUnrecoverable.
	corruptStore, err := storage.NewSQLite(userPath)
	require.NoError(t, err)
	require.NoError(t, corruptStore.AppendDiff(escrowID, types.DiffRecord{Diff: types.Diff{Nonce: liveSess.Nonce() + 5}}))
	_, _, recErr := user.RecoverSession(corruptStore, creator, verifier, escrowID, testutil.RuntimeTestVersion, group, clients)
	require.ErrorIs(t, recErr, user.ErrLocalStateUnrecoverable)
	require.NoError(t, corruptStore.Close())

	// Recover session state from hosts, finalize, and verify on-chain settlement payload.
	recoveredSess, recoveredSM, err := user.RecoverSessionFromHosts(
		ctx, creator, verifier, escrowID, testutil.RuntimeTestVersion,
		1, creator.Address(), initialBalance, config, group, clients,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = recoveredSess.Close() })

	require.NoError(t, recoveredSess.Finalize(ctx))
	afterFinalize := recoveredSM.SnapshotState()
	require.Equal(t, types.PhaseSettlement, afterFinalize.Phase)
	require.Equal(t, beforeCorruption.Balance, afterFinalize.Balance, "owner deposit remainder preserved")
	require.Equal(t, beforeCorruption.HostStats, afterFinalize.HostStats, "hosts real stats preserved")

	settlement, err := state.BuildSettlement(
		escrowID,
		afterFinalize,
		recoveredSess.Signatures()[recoveredSess.Nonce()],
		recoveredSess.Nonce(),
	)
	require.NoError(t, err)
	_, err = state.VerifySettlement(*settlement, group, verifier, afterFinalize.WarmKeys)
	require.NoError(t, err)
}

func TestRecoverSessionClassifiesGroupAndVersionMismatchAsUnrecoverable(t *testing.T) {
	creator := testutil.MustGenerateKey(t)
	h0 := testutil.MustGenerateKey(t)
	h1 := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{h0})
	verifier := signing.NewSecp256k1Verifier()

	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "2039-m",
		EpochID:        1,
		Version:        "v1",
		CreatorAddr:    creator.Address(),
		Group:          group,
		InitialBalance: 1000,
	}))

	_, _, err := user.RecoverSession(store, creator, verifier, "2039-m", "v2", group, make([]user.HostClient, 1))
	require.ErrorIs(t, err, user.ErrLocalStateUnrecoverable)
	require.ErrorContains(t, err, "session version mismatch")

	otherGroup := testutil.MakeGroup([]*signing.Secp256k1Signer{h1})
	_, _, err = user.RecoverSession(store, creator, verifier, "2039-m", "v1", otherGroup, make([]user.HostClient, 1))
	require.ErrorIs(t, err, user.ErrLocalStateUnrecoverable)
	require.ErrorContains(t, err, "group mismatch")
}
