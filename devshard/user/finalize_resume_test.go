package user

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/logging"
	"devshard/signing"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

type corruptStateHashClient struct {
	inner HostClient
	hits  int
}

func (c *corruptStateHashClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	resp, err := c.inner.Send(ctx, req, stream, receiptHandler)
	if err != nil || resp == nil || req.Payload != nil || len(resp.StateHash) == 0 {
		return resp, err
	}
	c.hits++
	bad := make([]byte, len(resp.StateHash))
	copy(bad, resp.StateHash)
	bad[0] ^= 0xff
	resp.StateHash = bad
	resp.StateSig = nil
	return resp, nil
}

func finalizeTestParams() InferenceParams {
	return InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
}

func TestFinalize_SkipsHostWithRejectedResponse(t *testing.T) {
	numHosts := 4
	session, _, _ := setupSession(t, numHosts, 100000, 100)
	ctx := context.Background()
	for i := 0; i < numHosts; i++ {
		_, err := session.SendInference(ctx, finalizeTestParams())
		require.NoError(t, err)
	}
	preFinalize := len(session.Diffs())

	liar := &corruptStateHashClient{inner: session.clients[2]}
	session.clients[2] = liar

	require.NoError(t, session.Finalize(ctx))
	require.Greater(t, liar.hits, 0)
	require.Equal(t, types.PhaseSettlement, session.StateMachine().Phase())
	require.Equal(t, preFinalize+numHosts+1, len(session.Diffs()))
	require.Equal(t, session.StateMachine().FinalizeNonce()+uint64(numHosts), session.Nonce())
	require.True(t, session.HasQuorumAt(session.Nonce()))

	finalSigs := session.Signatures()[session.Nonce()]
	_, liarSigned := finalSigs[2]
	require.False(t, liarSigned)
}

func TestFinalize_ResumesFromFinalizingPhase(t *testing.T) {
	numHosts := 4
	session, _, _ := setupSession(t, numHosts, 100000, 100)
	ctx := context.Background()
	for i := 0; i < numHosts; i++ {
		_, err := session.SendInference(ctx, finalizeTestParams())
		require.NoError(t, err)
	}
	preFinalize := len(session.Diffs())

	require.NoError(t, session.sendDiffRound(ctx, []*types.DevshardTx{{Tx: &types.DevshardTx_FinalizeRound{
		FinalizeRound: &types.MsgFinalizeRound{},
	}}}))
	require.NoError(t, session.sendDiffRound(ctx, nil))
	require.Equal(t, types.PhaseFinalizing, session.StateMachine().Phase())
	finalizeNonce := session.StateMachine().FinalizeNonce()
	require.NotZero(t, finalizeNonce)

	require.NoError(t, session.Finalize(ctx))
	require.Equal(t, types.PhaseSettlement, session.StateMachine().Phase())
	require.Equal(t, finalizeNonce+uint64(numHosts), session.Nonce())
	require.Equal(t, preFinalize+numHosts+1, len(session.Diffs()))
	require.True(t, session.HasQuorumAt(session.Nonce()))
}

func TestFinalize_ResumesAfterRecoveryWithLiarStillPresent(t *testing.T) {
	numHosts := 4
	store, err := storage.NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}
	liar := &corruptStateHashClient{inner: clients[1]}
	clients[1] = liar

	userSM := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier, WithStorage(store))
	require.NoError(t, err)

	ctx := context.Background()
	for i := 0; i < numHosts; i++ {
		_, err := session.SendInference(ctx, finalizeTestParams())
		require.NoError(t, err)
	}
	preFinalize := len(session.Diffs())

	err = session.sendDiffRound(ctx, []*types.DevshardTx{{Tx: &types.DevshardTx_FinalizeRound{
		FinalizeRound: &types.MsgFinalizeRound{},
	}}})
	require.ErrorIs(t, err, ErrHostResponseRejected)
	require.ErrorIs(t, err, types.ErrStateHashMismatch)
	require.Equal(t, types.PhaseFinalizing, session.StateMachine().Phase())
	finalizeNonce := session.StateMachine().FinalizeNonce()
	require.NoError(t, session.FlushSnapshot())

	recovered, _, err := RecoverSession(store, user, verifier, "escrow-1", testutil.RuntimeTestVersion, group, clients)
	require.NoError(t, err)
	require.Equal(t, types.PhaseFinalizing, recovered.StateMachine().Phase())
	require.Equal(t, finalizeNonce, recovered.StateMachine().FinalizeNonce())

	require.NoError(t, recovered.Finalize(ctx))
	require.Equal(t, types.PhaseSettlement, recovered.StateMachine().Phase())
	require.Equal(t, finalizeNonce+uint64(numHosts), recovered.Nonce())
	require.Equal(t, uint64(preFinalize+numHosts+1), recovered.Nonce())
	require.True(t, recovered.HasQuorumAt(recovered.Nonce()))

	_, liarSigned := recovered.Signatures()[recovered.Nonce()][1]
	require.False(t, liarSigned)
}

type hideInferenceMempoolClient struct {
	inner HostClient
}

func (c *hideInferenceMempoolClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	resp, err := c.inner.Send(ctx, req, stream, receiptHandler)
	if err == nil && resp != nil && req.Payload != nil {
		resp.Mempool = nil
	}
	return resp, err
}

type storedFinalizeFixture struct {
	store   *storage.SQLite
	group   []types.SlotAssignment
	user    *signing.Secp256k1Signer
	clients []HostClient
	session *Session
}

func newStoredFinalizeFixture(t *testing.T, numHosts int, wrap func(i int, c HostClient) HostClient) *storedFinalizeFixture {
	t.Helper()
	store, err := storage.NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(100))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
		if wrap != nil {
			clients[i] = wrap(i, clients[i])
		}
	}

	userSM := newTestStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier, WithStorage(store))
	require.NoError(t, err)

	ctx := context.Background()
	for i := 0; i < numHosts; i++ {
		_, err := session.SendInference(ctx, finalizeTestParams())
		require.NoError(t, err)
	}
	return &storedFinalizeFixture{store: store, group: group, user: user, clients: clients, session: session}
}

func (f *storedFinalizeFixture) recover(t *testing.T) *Session {
	t.Helper()
	recovered, _, err := RecoverSession(f.store, f.user, signing.NewSecp256k1Verifier(), "escrow-1", testutil.RuntimeTestVersion, f.group, f.clients)
	require.NoError(t, err)
	return recovered
}

func runFinalizeRounds(t *testing.T, session *Session, rounds int) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, session.sendDiffRound(ctx, []*types.DevshardTx{{Tx: &types.DevshardTx_FinalizeRound{
		FinalizeRound: &types.MsgFinalizeRound{},
	}}}))
	for i := 1; i < rounds; i++ {
		require.NoError(t, session.sendDiffRound(ctx, nil))
	}
}

func TestFinalize_ResumeRecoversPendingTxsQueuedBeforeCrash(t *testing.T) {
	numHosts := 4
	ctx := context.Background()
	wrap := func(i int, c HostClient) HostClient {
		if i == 0 {
			return &hideInferenceMempoolClient{inner: c}
		}
		return c
	}

	reference := newStoredFinalizeFixture(t, numHosts, wrap)
	require.NoError(t, reference.session.Finalize(ctx))
	require.Equal(t, types.PhaseSettlement, reference.session.StateMachine().Phase())
	referenceState := reference.session.StateMachine().SnapshotState()
	require.NotEqual(t, uint64(100000), referenceState.Balance)

	crashed := newStoredFinalizeFixture(t, numHosts, wrap)
	runFinalizeRounds(t, crashed.session, numHosts)
	require.Equal(t, types.PhaseFinalizing, crashed.session.StateMachine().Phase())
	require.Equal(t, crashed.session.StateMachine().FinalizeNonce()+uint64(numHosts-1), crashed.session.Nonce())
	require.True(t, HasMsgFinish(crashed.session.PendingTxs(), 4))
	require.NoError(t, crashed.session.FlushSnapshot())

	recovered := crashed.recover(t)
	require.Equal(t, types.PhaseFinalizing, recovered.StateMachine().Phase())
	require.Empty(t, recovered.PendingTxs())

	require.NoError(t, recovered.Finalize(ctx))
	require.Equal(t, types.PhaseSettlement, recovered.StateMachine().Phase())
	require.Equal(t, reference.session.Nonce(), recovered.Nonce())
	require.True(t, recovered.HasQuorumAt(recovered.Nonce()))

	recoveredState := recovered.StateMachine().SnapshotState()
	require.Equal(t, referenceState.Balance, recoveredState.Balance)
	require.Equal(t, referenceState.Fees, recoveredState.Fees)
	require.Equal(t, referenceState.SealedAcc, recoveredState.SealedAcc)
	for slot, hs := range referenceState.HostStats {
		require.Equal(t, hs.Cost, recoveredState.HostStats[slot].Cost, "slot %d cost", slot)
	}
}

func TestFinalize_RefusesFinalizingSnapshotWithoutFinalizeNonce(t *testing.T) {
	numHosts := 4
	ctx := context.Background()
	f := newStoredFinalizeFixture(t, numHosts, nil)
	runFinalizeRounds(t, f.session, 2)
	require.Equal(t, types.PhaseFinalizing, f.session.StateMachine().Phase())
	require.NoError(t, f.session.FlushSnapshot())

	nonce, data, err := f.store.LoadSnapshot("escrow-1")
	require.NoError(t, err)
	var snap sessionSnapshot
	require.NoError(t, json.Unmarshal(data, &snap))
	require.NotZero(t, snap.State.FinalizeNonce)
	snap.State.FinalizeNonce = 0
	data, err = json.Marshal(snap)
	require.NoError(t, err)
	require.NoError(t, f.store.SaveSnapshot("escrow-1", nonce, data))

	recovered := f.recover(t)
	require.Equal(t, types.PhaseFinalizing, recovered.StateMachine().Phase())
	require.Zero(t, recovered.StateMachine().FinalizeNonce())
	before := recovered.Nonce()

	err = recovered.Finalize(ctx)
	require.ErrorIs(t, err, ErrLocalStateUnrecoverable)
	require.Equal(t, before, recovered.Nonce())
	require.Equal(t, types.PhaseFinalizing, recovered.StateMachine().Phase())
}

type admissionGateClient struct {
	inner  HostClient
	reject atomic.Bool
}

func (c *admissionGateClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	if c.reject.Load() {
		return nil, errors.New("admission: host quarantined")
	}
	return c.inner.Send(ctx, req, stream, receiptHandler)
}

func (c *admissionGateClient) WithoutAdmission() any { return c.inner }

func TestFinalize_ResumeRefreshBypassesAdmission(t *testing.T) {
	numHosts := 4
	ctx := context.Background()
	var gate *admissionGateClient
	wrap := func(i int, c HostClient) HostClient {
		if i != 0 {
			return c
		}
		gate = &admissionGateClient{inner: &hideInferenceMempoolClient{inner: c}}
		return gate
	}

	reference := newStoredFinalizeFixture(t, numHosts, wrap)
	require.NoError(t, reference.session.Finalize(ctx))
	referenceState := reference.session.StateMachine().SnapshotState()

	crashed := newStoredFinalizeFixture(t, numHosts, wrap)
	runFinalizeRounds(t, crashed.session, numHosts)
	require.True(t, HasMsgFinish(crashed.session.PendingTxs(), 4))
	require.NoError(t, crashed.session.FlushSnapshot())

	recovered := crashed.recover(t)
	require.Empty(t, recovered.PendingTxs())
	gate.reject.Store(true)

	require.NoError(t, recovered.Finalize(ctx))
	require.Equal(t, types.PhaseSettlement, recovered.StateMachine().Phase())
	require.True(t, recovered.HasQuorumAt(recovered.Nonce()))

	recoveredState := recovered.StateMachine().SnapshotState()
	require.Equal(t, referenceState.Balance, recoveredState.Balance)
	require.Equal(t, referenceState.SealedAcc, recoveredState.SealedAcc)
	for slot, hs := range referenceState.HostStats {
		require.Equal(t, hs.Cost, recoveredState.HostStats[slot].Cost, "slot %d cost", slot)
	}
}

type logRecord struct {
	msg string
	kv  map[string]any
}

type captureLogger struct {
	mu      sync.Mutex
	records []logRecord
}

func (l *captureLogger) add(msg string, keyvals []any) {
	kv := make(map[string]any, len(keyvals)/2)
	for i := 0; i+1 < len(keyvals); i += 2 {
		if k, ok := keyvals[i].(string); ok {
			kv[k] = keyvals[i+1]
		}
	}
	l.mu.Lock()
	l.records = append(l.records, logRecord{msg: msg, kv: kv})
	l.mu.Unlock()
}

func (l *captureLogger) Info(msg string, kv ...any)  { l.add(msg, kv) }
func (l *captureLogger) Error(msg string, kv ...any) { l.add(msg, kv) }
func (l *captureLogger) Warn(msg string, kv ...any)  { l.add(msg, kv) }
func (l *captureLogger) Debug(msg string, kv ...any) { l.add(msg, kv) }

func (l *captureLogger) find(msg string) []logRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []logRecord
	for _, r := range l.records {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

func TestFinalize_RejectedResponseLogIdentifiesHost(t *testing.T) {
	numHosts := 4
	session, _, _ := setupSession(t, numHosts, 100000, 100)
	ctx := context.Background()
	for i := 0; i < numHosts; i++ {
		_, err := session.SendInference(ctx, finalizeTestParams())
		require.NoError(t, err)
	}
	liarIdx := 2
	session.clients[liarIdx] = &corruptStateHashClient{inner: session.clients[liarIdx]}

	logs := &captureLogger{}
	logging.SetLogger(logs)
	t.Cleanup(func() { logging.SetLogger(logging.NewSlogAdapter()) })

	require.NoError(t, session.Finalize(ctx))

	records := logs.find("finalize: host response rejected, skipping host")
	require.Len(t, records, 1)
	kv := records[0].kv
	require.Equal(t, "escrow-1", kv["escrow"])
	require.Equal(t, liarIdx, kv["host"])
	require.Equal(t, session.group[liarIdx].ValidatorAddress, kv["validator"])
	require.Equal(t, session.StateMachine().FinalizeNonce()+uint64(liarIdx-1), kv["request_nonce"])
	err, ok := kv["error"].(error)
	require.True(t, ok)
	require.ErrorIs(t, err, types.ErrStateHashMismatch)
}

type cancelOnSendClient struct {
	inner      HostClient
	cancel     context.CancelFunc
	afterNonce uint64
}

func (c *cancelOnSendClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	resp, err := c.inner.Send(ctx, req, stream, receiptHandler)
	if len(req.Diffs) > 0 && req.Diffs[len(req.Diffs)-1].Nonce > c.afterNonce {
		c.cancel()
	}
	return resp, err
}

func TestFinalize_CancelledContextLeavesSessionResumable(t *testing.T) {
	numHosts := 4
	session, _, _ := setupSession(t, numHosts, 100000, 100)
	ctx := context.Background()
	for i := 0; i < numHosts; i++ {
		_, err := session.SendInference(ctx, finalizeTestParams())
		require.NoError(t, err)
	}
	runFinalizeRounds(t, session, 2)
	require.Equal(t, types.PhaseFinalizing, session.StateMachine().Phase())
	nonceBefore := session.Nonce()

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err := session.Finalize(cancelled)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, types.PhaseFinalizing, session.StateMachine().Phase())
	require.Equal(t, nonceBefore, session.Nonce())

	midCtx, midCancel := context.WithCancel(ctx)
	defer midCancel()
	nextHost := int((nonceBefore + 1) % uint64(numHosts))
	session.clients[nextHost] = &cancelOnSendClient{inner: session.clients[nextHost], cancel: midCancel, afterNonce: nonceBefore}
	err = session.Finalize(midCtx)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, types.PhaseFinalizing, session.StateMachine().Phase())
	require.Equal(t, nonceBefore+1, session.Nonce())

	require.NoError(t, session.Finalize(ctx))
	require.Equal(t, types.PhaseSettlement, session.StateMachine().Phase())
	require.Equal(t, session.StateMachine().FinalizeNonce()+uint64(numHosts), session.Nonce())
	require.True(t, session.HasQuorumAt(session.Nonce()))
}

type slowRefreshClient struct {
	inner      HostClient
	delay      time.Duration
	afterNonce uint64
}

func (c *slowRefreshClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	if req.Payload == nil && (len(req.Diffs) == 0 || req.Diffs[len(req.Diffs)-1].Nonce <= c.afterNonce) {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.inner.Send(ctx, req, stream, receiptHandler)
}

func TestFinalize_ResumeRefreshFansOutAcrossHosts(t *testing.T) {
	numHosts := 12
	session, _, _ := setupSession(t, numHosts, 1000000, 100)
	ctx := context.Background()
	for i := 0; i < numHosts; i++ {
		_, err := session.SendInference(ctx, finalizeTestParams())
		require.NoError(t, err)
	}
	runFinalizeRounds(t, session, 2)
	nonceBefore := session.Nonce()

	const delay = 100 * time.Millisecond
	for i := 1; i < numHosts; i++ {
		session.clients[i] = &slowRefreshClient{inner: session.clients[i], delay: delay, afterNonce: nonceBefore}
	}
	serialCost := time.Duration(numHosts-1) * delay
	started := time.Now()
	require.NoError(t, session.refreshPendingTxsFromHosts(ctx))
	elapsed := time.Since(started)
	require.GreaterOrEqual(t, elapsed, delay)
	require.Less(t, elapsed, serialCost/2)

	require.NoError(t, session.Finalize(ctx))
	require.Equal(t, types.PhaseSettlement, session.StateMachine().Phase())
	require.Equal(t, session.StateMachine().FinalizeNonce()+uint64(numHosts), session.Nonce())
	require.True(t, session.HasQuorumAt(session.Nonce()))
}
