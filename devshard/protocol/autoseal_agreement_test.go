package protocol

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
	"devshard/user"
)

const (
	autoSealTestInferenceSealGraceNonces     = 2
	autoSealTestInferenceSealGraceSeconds   = 5
	autoSealTestBaseConfirmedAt     = 10_000
	autoSealAgreementNumHosts       = 16
	autoSealAgreementPipelinedCount = 80
)

type autoSealEnv struct {
	session   *user.Session
	hosts     []*host.Host
	hostSMs   []*state.StateMachine
	userSM    *state.StateMachine
	user      *signing.Secp256k1Signer
	hostSigners []*signing.Secp256k1Signer
	group     []types.SlotAssignment
	escrowID  string
}

func autoSealTestConfig(numHosts int) types.SessionConfig {
	return types.NormalizeSessionConfig(types.SessionConfig{
		RefusalTimeout:             60,
		ExecutionTimeout:           1200,
		TokenPrice:                 1,
		VoteThreshold:              uint32(numHosts) / 2,
		ValidationRate:             0,
		FeePerNonce:                0,
		InferenceSealGraceNonces:            autoSealTestInferenceSealGraceNonces,
		InferenceSealGraceSeconds: autoSealTestInferenceSealGraceSeconds,
	}, numHosts)
}

func setupAutoSealEnv(t *testing.T, numHosts int, balance uint64) *autoSealEnv {
	t.Helper()

	hostSigners := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := autoSealTestConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()
	escrowID := "escrow-autoseal"

	hosts := make([]*host.Host, numHosts)
	hostSMs := make([]*state.StateMachine, numHosts)
	clients := make([]user.HostClient, numHosts)
	for i := range hostSigners {
		sm, err := state.NewStateMachine(escrowID, config, group, balance, userSigner.Address(), verifier,
			testutil.MustMemoryStore(t, escrowID, userSigner.Address(), config, group, balance))
		require.NoError(t, err)
		hostSMs[i] = sm
		h, err := host.NewHost(sm, hostSigners[i], stub.NewInferenceEngine(), escrowID, group, nil, host.WithGrace(100))
		require.NoError(t, err)
		hosts[i] = h
		clients[i] = &user.InProcessClient{Host: h}
	}

	userSM, err := state.NewStateMachine(escrowID, config, group, balance, userSigner.Address(), verifier,
		testutil.MustMemoryStore(t, escrowID, userSigner.Address(), config, group, balance))
	require.NoError(t, err)
	session, err := user.NewSession(userSM, userSigner, escrowID, group, clients, verifier)
	require.NoError(t, err)

	return &autoSealEnv{
		session:     session,
		hosts:       hosts,
		hostSMs:     hostSMs,
		userSM:      userSM,
		user:        userSigner,
		hostSigners: hostSigners,
		group:       group,
		escrowID:    escrowID,
	}
}

func assertSMParity(t *testing.T, userSM, hostSM *state.StateMachine, nonce uint64) {
	t.Helper()

	userSt := userSM.SnapshotState()
	hostSt := hostSM.SnapshotState()

	require.Equal(t, userSt.LatestNonce, hostSt.LatestNonce, "nonce %d latest_nonce", nonce)
	require.Equal(t, userSt.SealedAcc, hostSt.SealedAcc, "nonce %d sealed_acc", nonce)
	require.Equal(t, userSt.Balance, hostSt.Balance, "nonce %d balance", nonce)
	require.Equal(t, len(userSt.Inferences), len(hostSt.Inferences), "nonce %d live_inferences_count", nonce)
	require.Equal(t, len(userSM.ExportSealedNonces()), len(hostSM.ExportSealedNonces()),
		"nonce %d sealed_inferences_count", nonce)

	for id, userRec := range userSt.Inferences {
		hostRec, ok := hostSt.Inferences[id]
		require.True(t, ok, "nonce %d id %d live on user but missing on host", nonce, id)
		require.Equal(t, userRec.Status, hostRec.Status, "nonce %d id %d status", nonce, id)
		require.Equal(t, userRec.ConfirmedAt, hostRec.ConfirmedAt, "nonce %d id %d confirmed_at", nonce, id)
	}

	userRoot, err := userSM.ComputeStateRoot()
	require.NoError(t, err)
	hostRoot, err := hostSM.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, userRoot, hostRoot, "nonce %d state_root", nonce)
}

func assertUserHostStateParity(t *testing.T, env *autoSealEnv, hostIdx int, nonce uint64) {
	t.Helper()
	assertSMParity(t, env.userSM, env.hostSMs[hostIdx], nonce)
}

func diffPostStateRootForNonce(t *testing.T, session *user.Session, nonce uint64) []byte {
	t.Helper()
	for _, d := range session.Diffs() {
		if d.Nonce == nonce {
			return d.PostStateRoot
		}
	}
	t.Fatalf("no diff for nonce %d", nonce)
	return nil
}

func applySignedDiffToUserAndHost(t *testing.T, env *autoSealEnv, nonce uint64, txs []*types.DevshardTx) types.Diff {
	t.Helper()

	postRoot, applied, err := env.userSM.ApplyLocalBestEffort(nonce, txs)
	require.NoError(t, err)

	diff := testutil.SignDiffWithRoot(t, env.user, env.escrowID, nonce, applied, postRoot)
	for i, hostSM := range env.hostSMs {
		_, err = hostSM.ApplyDiff(diff)
		require.NoError(t, err, "host %d apply nonce %d", i, nonce)
	}

	for _, hostSM := range env.hostSMs {
		assertSMParity(t, env.userSM, hostSM, nonce)
	}
	return diff
}

func (env *autoSealEnv) startConfirm(t *testing.T, inferenceID, startNonce uint64, confirmedAt int64) {
	t.Helper()
	applySignedDiffToUserAndHost(t, env, startNonce, []*types.DevshardTx{testutil.StartTx(inferenceID)})

	executorSlot := uint32(inferenceID % uint64(len(env.group)))
	execSig := testutil.SignExecutorReceipt(t, env.hostSigners[executorSlot], env.escrowID, inferenceID,
		testutil.TestPromptHash[:], "llama", 100, 50, 1000, confirmedAt)
	confirmTx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: inferenceID, ExecutorSig: execSig, ConfirmedAt: confirmedAt,
	}}}
	applySignedDiffToUserAndHost(t, env, startNonce+1, []*types.DevshardTx{confirmTx})
}

func (env *autoSealEnv) finishInference(t *testing.T, inferenceID, finishNonce uint64) {
	t.Helper()
	executorSlot := uint32(inferenceID % uint64(len(env.group)))
	finishMsg := &types.MsgFinishInference{
		InferenceId:  inferenceID,
		ResponseHash: append([]byte(nil), stub.NewInferenceEngine().ResponseHash...),
		InputTokens:  80,
		OutputTokens: 40,
		ExecutorSlot: executorSlot,
		EscrowId:     env.escrowID,
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, env.hostSigners[executorSlot], finishMsg)
	finishTx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: finishMsg}}
	applySignedDiffToUserAndHost(t, env, finishNonce, []*types.DevshardTx{finishTx})
}

func (env *autoSealEnv) bumpClock(t *testing.T, startNonce uint64, confirmedAt int64) uint64 {
	t.Helper()
	inferenceID := startNonce
	env.startConfirm(t, inferenceID, startNonce, confirmedAt)
	return startNonce + 2
}

func (env *autoSealEnv) advanceToNextAutoSealNonce(t *testing.T, after uint64) uint64 {
	t.Helper()
	target := state.NextAutoSealNonce(after)
	for n := after + 1; n <= target; n++ {
		applySignedDiffToUserAndHost(t, env, n, nil)
	}
	return target + 1
}

func (env *autoSealEnv) advanceClockPastGrace(t *testing.T, startNonce, inferenceID uint64) uint64 {
	t.Helper()
	window := len(env.group) * 3 // state.stateClockWindowFactor
	targetConfirmedAt := autoSealTestBaseConfirmedAt + int64(inferenceID) + int64(autoSealTestInferenceSealGraceSeconds) + 1
	for bump := 0; bump < window+5; bump++ {
		startNonce = env.bumpClock(t, startNonce, targetConfirmedAt+int64(bump))
		if _, live := env.userSM.SnapshotState().Inferences[inferenceID]; !live {
			return startNonce
		}
		startNonce = env.advanceToNextAutoSealNonce(t, startNonce-1)
		if _, live := env.userSM.SnapshotState().Inferences[inferenceID]; !live {
			return startNonce
		}
	}
	t.Fatalf("inference %d did not seal after clock bumps from nonce %d", inferenceID, startNonce)
	return startNonce
}

// TestProtocol_UserHostApply_AutoSealAgreement_ExplicitClock verifies that the
// user compose path (ApplyLocalBestEffort) and host apply path (ApplyDiff) stay
// identical through deterministic auto-seal, including sealed_acc updates.
func TestProtocol_UserHostApply_AutoSealAgreement_ExplicitClock(t *testing.T) {
	env := setupAutoSealEnv(t, 5, 1_000_000)

	confirmedAt := int64(autoSealTestBaseConfirmedAt + 1)
	env.startConfirm(t, 1, 1, confirmedAt)
	env.finishInference(t, 1, 3)

	require.Equal(t, types.StatusFinished, env.userSM.SnapshotState().Inferences[1].Status)
	require.Empty(t, env.userSM.ExportSealedNonces(), "finished inference should not seal before clock gate")

	env.advanceClockPastGrace(t, 4, 1)
	require.NotEmpty(t, env.userSM.SnapshotState().SealedAcc, "expected non-empty sealed_acc after auto-seal")
	require.Contains(t, env.userSM.ExportSealedNonces(), uint64(1))

	assertUserHostStateParity(t, env, 0, env.userSM.LatestNonce())
}

// TestProtocol_UserHost_SessionAutoSealAgreement_Sequential runs the full session
// compose/send/process loop and asserts user and target host remain aligned on
// every successful nonce, including post_state_root and sealed_acc.
func TestProtocol_UserHost_SessionAutoSealAgreement_Sequential(t *testing.T) {
	env := setupAutoSealEnv(t, autoSealAgreementNumHosts, 10_000_000)
	ctx := context.Background()
	params := defaultParams()

	for i := 0; i < autoSealAgreementPipelinedCount; i++ {
		prepared, err := env.session.PrepareInference(params)
		require.NoError(t, err, "prepare inference %d", i+1)

		resp, err := env.session.SendOnly(ctx, prepared, nil, nil)
		require.NoError(t, err, "send inference %d", i+1)
		require.NotEmpty(t, resp.StateHash, "host should return state hash at nonce %d", prepared.Nonce())

		diffRoot := diffPostStateRootForNonce(t, env.session, prepared.Nonce())
		require.NotEmpty(t, diffRoot)
		require.Equal(t, diffRoot, resp.StateHash,
			"nonce %d signed post_state_root must match host state hash", prepared.Nonce())

		require.NoError(t, env.session.ProcessResponse(prepared.HostIdx(), resp, prepared.Nonce()))
		assertUserHostStateParity(t, env, prepared.HostIdx(), prepared.Nonce())
	}
}

// TestProtocol_UserHost_SessionAutoSealAgreement_Pipelined composes two diffs
// locally before the first is sent (production pipelining). The first send ships
// a catch-up snapshot frozen at prepare time; the second send brings the host
// fully in sync with the user session.
func TestProtocol_UserHost_SessionAutoSealAgreement_Pipelined(t *testing.T) {
	env := setupAutoSealEnv(t, autoSealAgreementNumHosts, 10_000_000)
	ctx := context.Background()
	params := defaultParams()

	const pairs = autoSealAgreementPipelinedCount / 2
	for i := 0; i < pairs; i++ {
		first, err := env.session.PrepareInference(params)
		require.NoError(t, err, "prepare first inference pair %d", i+1)

		second, err := env.session.PrepareInference(params)
		require.NoError(t, err, "pipeline prepare second inference pair %d", i+1)

		resp1, err := env.session.SendOnly(ctx, first, nil, nil)
		require.NoError(t, err, "send first inference pair %d", i+1)
		diffRoot1 := diffPostStateRootForNonce(t, env.session, first.Nonce())
		require.Equal(t, diffRoot1, resp1.StateHash,
			"nonce %d signed post_state_root must match host state hash", first.Nonce())

		resp2, err := env.session.SendOnly(ctx, second, nil, nil)
		require.NoError(t, err, "send second inference pair %d", i+1)
		diffRoot2 := diffPostStateRootForNonce(t, env.session, resp2.Nonce)
		require.Equal(t, diffRoot2, resp2.StateHash,
			"nonce %d signed post_state_root must match host state hash", resp2.Nonce)

		require.NoError(t, env.session.ProcessResponse(first.HostIdx(), resp1, first.Nonce()))
		require.NoError(t, env.session.ProcessResponse(second.HostIdx(), resp2, second.Nonce()))

		assertUserHostStateParity(t, env, second.HostIdx(), env.session.Nonce())
	}
}
