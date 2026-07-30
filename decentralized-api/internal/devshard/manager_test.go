package devshard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime/pprof"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"decentralized-api/payloadstorage"

	"devshard/bridge"
	"devshard/host"
	devshardserver "devshard/server"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

// runtimeTestVersion is the devshard binary tag used in dapi manager tests
// (matches versiond / embedded devshard without versiond).
const runtimeTestVersion = "v1"
const statsTestRoutePrefix = "/devshard/test"

// mockBridge implements bridge.MainnetBridge for testing recovery.
type mockBridge struct {
	escrow *bridge.EscrowInfo
}

func (b *mockBridge) GetEscrow(string) (*bridge.EscrowInfo, error) {
	return b.escrow, nil
}

func (b *mockBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	return &bridge.HostInfo{Address: address, URL: "http://localhost"}, nil
}

func (b *mockBridge) GetValidationThreshold(uint64, string) (*bridge.Decimal, error) {
	return nil, bridge.ErrNotImplemented
}

func (b *mockBridge) VerifyWarmKey(string, string) (bool, error) { return false, nil }

func (b *mockBridge) OnEscrowCreated(bridge.EscrowInfo) error { return bridge.ErrNotImplemented }
func (b *mockBridge) OnSettlementProposed(string, []byte, uint64) error {
	return bridge.ErrNotImplemented
}
func (b *mockBridge) OnSettlementFinalized(string) error { return bridge.ErrNotImplemented }
func (b *mockBridge) SubmitDisputeState(string, []byte, uint64, map[uint32][]byte) error {
	return bridge.ErrNotImplemented
}

var _ bridge.MainnetBridge = (*mockBridge)(nil)

type countingBridge struct {
	escrow         *bridge.EscrowInfo
	getEscrowCalls int
}

func (b *countingBridge) GetEscrow(string) (*bridge.EscrowInfo, error) {
	b.getEscrowCalls++
	return b.escrow, nil
}

func (b *countingBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	return &bridge.HostInfo{Address: address, URL: "http://localhost"}, nil
}

func (b *countingBridge) GetValidationThreshold(uint64, string) (*bridge.Decimal, error) {
	return nil, bridge.ErrNotImplemented
}

func (b *countingBridge) VerifyWarmKey(string, string) (bool, error) { return false, nil }

func (b *countingBridge) OnEscrowCreated(bridge.EscrowInfo) error { return bridge.ErrNotImplemented }
func (b *countingBridge) OnSettlementProposed(string, []byte, uint64) error {
	return bridge.ErrNotImplemented
}
func (b *countingBridge) OnSettlementFinalized(string) error { return bridge.ErrNotImplemented }
func (b *countingBridge) SubmitDisputeState(string, []byte, uint64, map[uint32][]byte) error {
	return bridge.ErrNotImplemented
}

var _ bridge.MainnetBridge = (*countingBridge)(nil)

func countHostValidationWorkers() int {
	var buf bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
	return bytes.Count(buf.Bytes(), []byte("devshard/host.(*Host).startValidationWorkers.func1"))
}

type payloadEntry struct {
	prompt   []byte
	response []byte
}

type mockPayloadStore struct {
	byEpoch map[uint64]map[string]payloadEntry
}

func (m *mockPayloadStore) Store(_ context.Context, inferenceID string, epochID uint64, prompt, response []byte) error {
	if m.byEpoch == nil {
		m.byEpoch = make(map[uint64]map[string]payloadEntry)
	}
	if m.byEpoch[epochID] == nil {
		m.byEpoch[epochID] = make(map[string]payloadEntry)
	}
	m.byEpoch[epochID][inferenceID] = payloadEntry{prompt: prompt, response: response}
	return nil
}

func (m *mockPayloadStore) Retrieve(_ context.Context, inferenceID string, epochID uint64) ([]byte, []byte, error) {
	if entries := m.byEpoch[epochID]; entries != nil {
		if entry, ok := entries[inferenceID]; ok {
			return entry.prompt, entry.response, nil
		}
	}
	return nil, nil, payloadstorage.ErrNotFound
}

func (m *mockPayloadStore) PruneEpoch(context.Context, uint64) error { return nil }

func (m *mockPayloadStore) DeleteInference(_ context.Context, inferenceID string, epochID uint64) error {
	if entries := m.byEpoch[epochID]; entries != nil {
		if _, ok := entries[inferenceID]; ok {
			delete(entries, inferenceID)
			return nil
		}
	}
	return payloadstorage.ErrNotFound
}

type currentEpochStore struct {
	storage.Storage
	epoch uint64
}

func (s currentEpochStore) CurrentEpochID() uint64 { return s.epoch }

type metaErrorStore struct {
	storage.Storage
	epoch     uint64
	escrowID  string
	metaError error
}

func (s metaErrorStore) CurrentEpochID() uint64 { return s.epoch }

func (s metaErrorStore) GetSessionMeta(escrowID string) (*storage.SessionMeta, error) {
	if escrowID == s.escrowID {
		return nil, s.metaError
	}
	return s.Storage.GetSessionMeta(escrowID)
}

type failingCreateStore struct {
	storage.Storage
	err error
}

func (s *failingCreateStore) CreateSession(storage.CreateSessionParams) error {
	return s.err
}

type countingListStore struct {
	storage.Storage
	listCalls int
}

func (s *countingListStore) ListActiveSessions() ([]storage.ActiveSession, error) {
	s.listCalls++
	return s.Storage.ListActiveSessions()
}

func captureInfoLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	currentLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(currentLogger) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return &buf
}

func readLogEntries(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	var entries []map[string]any
	for {
		var entry map[string]any
		err := decoder.Decode(&entry)
		if errors.Is(err, io.EOF) {
			return entries
		}
		require.NoError(t, err)
		entries = append(entries, entry)
	}
}

func requireLogEntry(t *testing.T, entries []map[string]any, msg string) map[string]any {
	t.Helper()
	for _, entry := range entries {
		if entry["msg"] == msg {
			return entry
		}
	}
	require.Failf(t, "missing log entry", "msg=%q entries=%v", msg, entries)
	return nil
}

type rangeRecordingStore struct {
	storage.Storage
	from uint64
	to   uint64
}

func (s *rangeRecordingStore) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	s.from = fromNonce
	s.to = toNonce
	return s.Storage.GetDiffs(escrowID, fromNonce, toNonce)
}

type blockingMetaStore struct {
	storage.Storage
	release     <-chan struct{}
	bothStarted chan<- struct{}
	once        sync.Once
	mu          sync.Mutex
	started     int
}

func (s *blockingMetaStore) GetSessionMeta(escrowID string) (*storage.SessionMeta, error) {
	s.mu.Lock()
	s.started++
	if s.started == 2 {
		s.once.Do(func() { close(s.bothStarted) })
	}
	s.mu.Unlock()

	<-s.release
	return s.Storage.GetSessionMeta(escrowID)
}

func mustGenerateKey(t *testing.T) *signing.Secp256k1Signer {
	t.Helper()
	s, err := signing.GenerateKey()
	require.NoError(t, err)
	return s
}

func makeGroup(signers []*signing.Secp256k1Signer) []types.SlotAssignment {
	group := make([]types.SlotAssignment, len(signers))
	for i, s := range signers {
		group[i] = types.SlotAssignment{
			SlotID:           uint32(i),
			ValidatorAddress: s.Address(),
		}
	}
	return group
}

func defaultConfig(n int) types.SessionConfig {
	return types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    uint32(n) / 2,
		ValidationRate:   types.DefaultValidationRate,
	}
}

func startTx(inferenceID uint64) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId: inferenceID,
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
	}}}
}

func signDiffWithRoot(t *testing.T, signer signing.Signer, escrowID string, nonce uint64, txs []*types.DevshardTx, postStateRoot []byte) types.Diff {
	t.Helper()
	content := &types.DiffContent{Nonce: nonce, Txs: txs, EscrowId: escrowID, PostStateRoot: postStateRoot}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(content)
	require.NoError(t, err)
	sig, err := signer.Sign(data)
	require.NoError(t, err)
	return types.Diff{Nonce: nonce, Txs: txs, UserSig: sig, PostStateRoot: postStateRoot}
}

func newManagerTestStore(t *testing.T) *storage.SQLite {
	t.Helper()
	db, err := storage.NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

// populateStore creates a session and appends diffs. Returns group, user signer,
// and the first host signer (for use as HostManager signer -- must be in group).
func populateStore(t *testing.T, store storage.Storage, numDiffs int) ([]types.SlotAssignment, *signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	config := defaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        runtimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000000,
	}))

	sm, err := state.NewStateMachine("escrow-1", config, group, 100000000, user.Address(), verifier, store,
		state.WithStateRootAndProtocolVersion(types.EffectiveStateRootAndProtocolVersion),
	)
	require.NoError(t, err)

	for i := uint64(1); i <= uint64(numDiffs); i++ {
		txs := []*types.DevshardTx{startTx(i)}
		root, err := sm.ApplyLocal(i, txs)
		require.NoError(t, err)

		diff := signDiffWithRoot(t, user, "escrow-1", i, txs, root)
		rec := types.DiffRecord{
			Diff:      diff,
			StateHash: root,
		}
		require.NoError(t, store.AppendDiff("escrow-1", rec))
	}

	return group, user, hosts[0]
}

func createStoredSession(t *testing.T, store storage.Storage, escrowID string, epochID uint64, numDiffs int) ([]types.SlotAssignment, *signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	return createStoredSessionWithVersion(t, store, escrowID, epochID, runtimeTestVersion, numDiffs)
}

func createStoredSessionWithVersion(t *testing.T, store storage.Storage, escrowID string, epochID uint64, version string, numDiffs int) ([]types.SlotAssignment, *signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	config := defaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       escrowID,
		EpochID:        epochID,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000000,
		Version:        version,
	}))

	sm, err := state.NewStateMachine(escrowID, config, group, 100000000, user.Address(), verifier, store,
		state.WithStateRootAndProtocolVersion(types.EffectiveStateRootAndProtocolVersion),
	)
	require.NoError(t, err)
	for i := uint64(1); i <= uint64(numDiffs); i++ {
		txs := []*types.DevshardTx{startTx(i)}
		root, err := sm.ApplyLocal(i, txs)
		require.NoError(t, err)
		require.NoError(t, store.AppendDiff(escrowID, types.DiffRecord{
			Diff:      signDiffWithRoot(t, user, escrowID, i, txs, root),
			StateHash: root,
		}))
	}
	return group, user, hosts[0]
}

func requestStats(t *testing.T, mgr *HostManager, prefix string, path string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	mgr.Register(e.Group(prefix))
	req := httptest.NewRequest(http.MethodGet, prefix+path, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestStatsShardsListsCurrentEpochWithoutDetails(t *testing.T) {
	base := newManagerTestStore(t)
	_, _, hostSigner := createStoredSession(t, base, "escrow-current", 7, 0)
	createStoredSession(t, base, "escrow-old", 6, 0)

	counting := &countingListStore{Storage: currentEpochStore{Storage: base, epoch: 7}}
	mgr := NewHostManager(counting, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, &mockBridge{}, nil, nil)
	mgr.SetReady()

	rec := requestStats(t, mgr, statsTestRoutePrefix, "/stats/shards")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.NotContains(t, rec.Body.String(), "host_stats")
	require.NotContains(t, rec.Body.String(), "proof")
	require.NotContains(t, rec.Body.String(), "signatures")
	require.NotContains(t, rec.Body.String(), "inferences")

	var resp struct {
		CurrentEpochID uint64   `json:"current_epoch_id"`
		ActiveEscrows  []string `json:"active_escrows"`
		Shards         []struct {
			EscrowID string `json:"escrow_id"`
			EpochID  uint64 `json:"epoch_id"`
		} `json:"shards"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, uint64(7), resp.CurrentEpochID)
	require.Equal(t, []string{"escrow-current"}, resp.ActiveEscrows)
	require.Len(t, resp.Shards, 1)
	require.Equal(t, "escrow-current", resp.Shards[0].EscrowID)
	require.Equal(t, uint64(7), resp.Shards[0].EpochID)

	cached := requestStats(t, mgr, statsTestRoutePrefix, "/stats/shards")
	require.Equal(t, http.StatusOK, cached.Code, "body: %s", cached.Body.String())
	require.Equal(t, rec.Body.String(), cached.Body.String())
	require.Equal(t, 1, counting.listCalls)

	rootMounted := requestStats(t, mgr, "", "/stats/shards")
	require.Equal(t, http.StatusOK, rootMounted.Code, "body: %s", rootMounted.Body.String())
}

func TestStatsShardsSkipsForeignVersionSessions(t *testing.T) {
	base := newManagerTestStore(t)
	_, _, hostSigner := createStoredSession(t, base, "escrow-v1", 7, 0)
	createStoredSessionWithVersion(t, base, "escrow-v2", 7, "v2", 0)

	store := currentEpochStore{Storage: base, epoch: 7}
	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, &mockBridge{}, nil, nil)
	mgr.SetReady()

	rec := requestStats(t, mgr, "/v1/devshard", "/stats/shards")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Contains(t, rec.Body.String(), "escrow-v1")
	require.NotContains(t, rec.Body.String(), "escrow-v2")
}

func TestStatsShardsSkipsSessionsWithUnreadableMeta(t *testing.T) {
	base := newManagerTestStore(t)
	_, _, hostSigner := createStoredSession(t, base, "escrow-ok", 7, 0)
	createStoredSession(t, base, "escrow-bad-meta", 7, 0)

	store := metaErrorStore{
		Storage:   base,
		epoch:     7,
		escrowID:  "escrow-bad-meta",
		metaError: storage.ErrSessionNotFound,
	}
	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, &mockBridge{}, nil, nil)
	mgr.SetReady()

	rec := requestStats(t, mgr, statsTestRoutePrefix, "/stats/shards")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Contains(t, rec.Body.String(), "escrow-ok")
	require.NotContains(t, rec.Body.String(), "escrow-bad-meta")
}

func TestStatsShardDetailReturnsStatsOnly(t *testing.T) {
	base := newManagerTestStore(t)
	group, _, hostSigner := createStoredSession(t, base, "escrow-detail", 7, 1)
	store := currentEpochStore{Storage: base, epoch: 7}
	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, &mockBridge{}, nil, nil)
	mgr.SetReady()

	rec := requestStats(t, mgr, statsTestRoutePrefix, "/stats/shards/escrow-detail")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.NotContains(t, rec.Body.String(), "inferences")
	require.NotContains(t, rec.Body.String(), "proof")
	require.NotContains(t, rec.Body.String(), "signatures")
	require.NotContains(t, rec.Body.String(), "warm_keys")

	var resp struct {
		EscrowID                    string `json:"escrow_id"`
		EpochID                     uint64 `json:"epoch_id"`
		Nonce                       uint64 `json:"nonce"`
		Version                     string `json:"version"`
		StateRootAndProtocolVersion string `json:"state_root_and_protocol_version"`
		HostStats                   map[string]struct {
			Missed               uint32 `json:"missed"`
			Invalid              uint32 `json:"invalid"`
			Cost                 uint64 `json:"cost"`
			RequiredValidations  uint32 `json:"required_validations"`
			CompletedValidations uint32 `json:"completed_validations"`
		} `json:"host_stats"`
		ValidationObservability struct {
			BySlot map[string]struct {
				RequiredValidations  uint32 `json:"required_validations"`
				CompletedValidations uint32 `json:"completed_validations"`
			} `json:"by_slot"`
			Totals struct {
				RequiredValidations  uint64 `json:"required_validations"`
				CompletedValidations uint64 `json:"completed_validations"`
			} `json:"totals"`
		} `json:"validation_observability"`
		Group []types.SlotAssignment `json:"group"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "escrow-detail", resp.EscrowID)
	require.Equal(t, uint64(7), resp.EpochID)
	require.Equal(t, uint64(1), resp.Nonce)
	require.Equal(t, runtimeTestVersion, resp.Version)
	require.Equal(t, types.EffectiveStateRootAndProtocolVersion, resp.StateRootAndProtocolVersion)
	require.Len(t, resp.HostStats, len(group))
	require.Equal(t, group, resp.Group)

	cached := requestStats(t, mgr, statsTestRoutePrefix, "/stats/shards/escrow-detail")
	require.Equal(t, http.StatusOK, cached.Code, "body: %s", cached.Body.String())
	require.Equal(t, rec.Body.String(), cached.Body.String())
}

func TestStatsShardDetailSkipsForeignVersionSession(t *testing.T) {
	base := newManagerTestStore(t)
	_, _, hostSigner := createStoredSessionWithVersion(t, base, "escrow-v2", 7, "v2", 0)

	store := currentEpochStore{Storage: base, epoch: 7}
	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, &mockBridge{}, nil, nil)
	mgr.SetReady()

	before := countHostValidationWorkers()
	rec := requestStats(t, mgr, "/v1/devshard", "/stats/shards/escrow-v2")
	require.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, before, countHostValidationWorkers(), "foreign version stats detail must not materialize a host")
}

func TestRecoverSessions_HappyPath(t *testing.T) {
	store := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, store, 10)

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	signer := hostSigner
	engine := stub.NewInferenceEngine()
	validator := stub.NewValidationEngine()

	mgr := NewHostManager(store, signer, engine, validator, runtimeTestVersion, br, nil, nil)
	err := mgr.RecoverSessions()
	require.NoError(t, err)

	mgr.mu.RLock()
	srv, ok := mgr.sessions["escrow-1"]
	mgr.mu.RUnlock()
	require.True(t, ok, "session should exist after recovery")
	require.NotNil(t, srv)
	require.NotNil(t, srv.Host())
}

func TestRecoverSessions_SkipsForeignVersionSessions(t *testing.T) {
	store := newManagerTestStore(t)
	_, _, hostSigner := createStoredSessionWithVersion(t, store, "escrow-v2", 7, "v2", 1)

	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, &mockBridge{}, nil, nil)
	require.NoError(t, mgr.RecoverSessions())

	mgr.mu.RLock()
	_, ok := mgr.sessions["escrow-v2"]
	mgr.mu.RUnlock()
	require.False(t, ok, "foreign-version session must be skipped, not treated as failed recovery")
}

func TestHostManager_EvictBeforeClosesOldSessions(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	config := defaultConfig(3)
	for _, sess := range []struct {
		escrowID string
		epochID  uint64
	}{
		{escrowID: "escrow-old", epochID: 5},
		{escrowID: "escrow-current", epochID: 7},
	} {
		require.NoError(t, store.CreateSession(storage.CreateSessionParams{
			EscrowID:       sess.escrowID,
			EpochID:        sess.epochID,
			Version:        runtimeTestVersion,
			CreatorAddr:    user.Address(),
			Config:         config,
			Group:          group,
			InitialBalance: 100000000,
		}))
	}

	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, &mockBridge{}, nil, nil)
	require.NoError(t, mgr.RecoverSessions())
	t.Cleanup(func() { _ = mgr.Close() })
	beforeEvict := countHostValidationWorkers()
	require.GreaterOrEqual(t, beforeEvict, 2*20)

	evicted := mgr.EvictBefore(6)
	require.Equal(t, 1, evicted)
	require.Eventually(t, func() bool {
		return countHostValidationWorkers() <= beforeEvict-20
	}, time.Second, 10*time.Millisecond)

	mgr.mu.RLock()
	_, oldOK := mgr.sessions["escrow-old"]
	_, currentOK := mgr.sessions["escrow-current"]
	mgr.mu.RUnlock()
	require.False(t, oldOK)
	require.True(t, currentOK)
}

func TestRecoverSessions_LogsRecoveryDurations(t *testing.T) {
	store := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, store, 1)
	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}
	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	logs := captureInfoLogs(t)

	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	require.NoError(t, mgr.RecoverSessions())

	entries := readLogEntries(t, logs)
	started := requireLogEntry(t, entries, "starting devshard session recovery")
	require.Equal(t, float64(1), started["session_count"])
	require.Equal(t, float64(1), started["worker_count"])

	session := requireLogEntry(t, entries, "recovered devshard session")
	require.Equal(t, "escrow-1", session["escrow_id"])
	require.Contains(t, session, "duration")

	completed := requireLogEntry(t, entries, "completed devshard session recovery")
	require.Equal(t, float64(1), completed["session_count"])
	require.Equal(t, float64(1), completed["worker_count"])
	require.Contains(t, completed, "duration")
}

func TestRecoverSessions_LoadsEscrowsInParallel(t *testing.T) {
	base := newManagerTestStore(t)
	hostSigner := mustGenerateKey(t)
	hosts := []*signing.Secp256k1Signer{
		hostSigner,
		mustGenerateKey(t),
		mustGenerateKey(t),
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	for _, escrowID := range []string{"escrow-a", "escrow-b"} {
		require.NoError(t, base.CreateSession(storage.CreateSessionParams{
			EscrowID:       escrowID,
			Version:        runtimeTestVersion,
			CreatorAddr:    user.Address(),
			Config:         defaultConfig(3),
			Group:          group,
			InitialBalance: 100000,
		}))
	}

	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	bothStarted := make(chan struct{})
	store := &blockingMetaStore{
		Storage:     base,
		release:     release,
		bothStarted: bothStarted,
	}
	br := &mockBridge{}
	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)

	errCh := make(chan error, 1)
	go func() {
		errCh <- mgr.RecoverSessions()
	}()

	select {
	case <-bothStarted:
	case <-time.After(time.Second):
		t.Fatal("expected at least two sessions to enter recovery concurrently")
	}

	releaseOnce.Do(func() { close(release) })
	require.NoError(t, <-errCh)
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	require.Len(t, mgr.sessions, 2)
}

func TestRecoverSessions_UsesSnapshotBeforeReplay(t *testing.T) {
	store := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, store, 750)
	verifier := signing.NewSecp256k1Verifier()
	config := defaultConfig(3)

	sm, err := state.NewStateMachine("escrow-1", config, group, 100000000, user.Address(), verifier, store,
		state.WithStateRootAndProtocolVersion(types.EffectiveStateRootAndProtocolVersion),
	)
	require.NoError(t, err)
	records, err := store.GetDiffs("escrow-1", 1, host.SnapshotInterval)
	require.NoError(t, err)
	for _, rec := range records {
		_, err := sm.ApplyLocal(rec.Nonce, rec.Txs)
		require.NoError(t, err)
	}
	require.NoError(t, saveHostSnapshot(store, sm, "escrow-1", host.SnapshotInterval))
	_, snapshotData, err := store.LoadSnapshot("escrow-1")
	require.NoError(t, err)
	snapshotState, _, _, err := host.UnmarshalStateSnapshotWithCommitted(snapshotData)
	require.NoError(t, err)
	require.NotNil(t, snapshotState)
	require.NotEqual(t, '{', snapshotData[0], "persisted snapshots must be protobuf")

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}
	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	recording := &rangeRecordingStore{Storage: store}
	mgr := NewHostManager(recording, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	require.NoError(t, mgr.RecoverSessions())
	require.Equal(t, uint64(host.SnapshotInterval+1), recording.from)
	require.Equal(t, uint64(750), recording.to)

	mgr.mu.RLock()
	srv := mgr.sessions["escrow-1"]
	mgr.mu.RUnlock()
	require.NotNil(t, srv)
	require.Equal(t, uint64(750), srv.Host().SnapshotState().LatestNonce)
}

func TestRecoverSessions_Nonce0(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)

	// Create a session with no diffs (nonce 0).
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        runtimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         defaultConfig(3),
		Group:          group,
		InitialBalance: 100000,
	}))

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	err := mgr.RecoverSessions()
	require.NoError(t, err)

	// Session must be registered despite nonce 0.
	mgr.mu.RLock()
	srv, ok := mgr.sessions["escrow-1"]
	mgr.mu.RUnlock()
	require.True(t, ok, "nonce-0 session must be registered after recovery")
	require.NotNil(t, srv)
	require.NotNil(t, srv.Host())

	// Subsequent getOrCreate must return the same session without error.
	srv2, err := mgr.getOrCreate("escrow-1")
	require.NoError(t, err)
	require.Equal(t, srv, srv2)
}

func TestHostManager_CreateCallsGetEscrowOnce(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	for i, s := range hosts {
		addresses[i] = s.Address()
	}

	br := &countingBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-new",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
			TokenPrice:     1,
		},
	}

	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	mgr.SetReady()

	srv, err := mgr.getOrCreate("escrow-new")
	require.NoError(t, err)
	require.NotNil(t, srv)
	require.Equal(t, 1, br.getEscrowCalls, "host bind must query escrow once per create")
	require.Len(t, srv.Host().Group(), 3)
}

func TestGetOrCreate_RecoversExistingStoredSession(t *testing.T) {
	store := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, store, 3)

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	srv, err := mgr.getOrCreate("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(3), srv.Host().LatestNonce(), "existing storage session should be replayed before serving")
}

func TestSessionServer_DefaultsToInitializing(t *testing.T) {
	store := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, store, 1)

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)

	_, err := mgr.SessionServer("escrow-1")
	require.ErrorIs(t, err, devshardserver.ErrInitializing)
}

func TestSessionServer_UnavailableIncludesCause(t *testing.T) {
	store := newManagerTestStore(t)
	mgr := NewHostManager(store, mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, &mockBridge{}, nil, nil)
	mgr.SetUnavailable(errors.New("boom"))

	_, err := mgr.SessionServer("escrow-1")
	require.ErrorIs(t, err, devshardserver.ErrInitializing)
	require.Contains(t, err.Error(), "boom")
}

func TestSessionServer_GatedUntilReady(t *testing.T) {
	store := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, store, 1)

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	mgr.SetReady()

	srv, err := mgr.SessionServer("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(1), srv.Host().LatestNonce())
}

func TestSessionServer_StartsRegisteredHost(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	for i, h := range hosts {
		addresses[i] = h.Address()
	}
	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-start",
			EpochID:        7,
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	mgr.SetReady()

	before := countHostValidationWorkers()
	srv, err := mgr.SessionServer("escrow-start")
	require.NoError(t, err)
	t.Cleanup(srv.Host().Close)
	require.Eventually(t, func() bool {
		return countHostValidationWorkers() >= before+20
	}, time.Second, 10*time.Millisecond)
}

func TestSessionServer_FailedCreateDoesNotStartHost(t *testing.T) {
	base := newManagerTestStore(t)
	store := &failingCreateStore{Storage: base, err: storage.ErrEpochPruned}
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	for i, h := range hosts {
		addresses[i] = h.Address()
	}
	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-pruned",
			EpochID:        1,
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	mgr.SetReady()

	before := countHostValidationWorkers()
	_, err := mgr.SessionServer("escrow-pruned")
	require.ErrorIs(t, err, storage.ErrEpochPruned)
	require.Equal(t, before, countHostValidationWorkers())
}

func TestSessionServer_CachesFailedResolution(t *testing.T) {
	base := newManagerTestStore(t)
	store := &failingCreateStore{Storage: base, err: storage.ErrEpochPruned}
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	for i, h := range hosts {
		addresses[i] = h.Address()
	}
	br := &countingBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-cache-failure",
			EpochID:        1,
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	mgr.SetReady()

	_, err := mgr.SessionServer("escrow-cache-failure")
	require.ErrorIs(t, err, storage.ErrEpochPruned)
	_, err = mgr.SessionServer("escrow-cache-failure")
	require.ErrorIs(t, err, storage.ErrEpochPruned)
	require.Equal(t, 1, br.getEscrowCalls, "cached failure should avoid rebuilding the same broken escrow")
}

func TestHostManager_SweepsExpiredResolutionFailures(t *testing.T) {
	mgr := &HostManager{
		resolutionFailures: make(map[string]resolutionFailure),
	}
	now := time.Unix(1_000, 0)
	for i := 0; i <= maxResolutionFailures; i++ {
		mgr.resolutionFailures[fmt.Sprintf("expired-%d", i)] = resolutionFailure{
			err:       storage.ErrSessionNotFound,
			expiresAt: now.Add(-time.Second),
		}
	}

	mgr.rememberResolutionFailure("fresh", storage.ErrEpochPruned, now)

	require.Len(t, mgr.resolutionFailures, 1)
	require.Contains(t, mgr.resolutionFailures, "fresh")
	require.True(t, now.Before(mgr.resolutionFailures["fresh"].expiresAt))
}

func TestCreateSession_BindsConfiguredVersion(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	const standaloneVersion = "v1"
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), standaloneVersion, br, nil, nil)
	_, err := mgr.getOrCreate("escrow-1")
	require.NoError(t, err)

	meta, err := store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, standaloneVersion, meta.Version)
}

type stubRuntimeParams struct {
	params SessionParams
}

func (s stubRuntimeParams) SessionParams() SessionParams { return s.params }

func TestHostManager_Create_FreezesLiveParamsFromProvider(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:                  "escrow-1",
			Amount:                    100000,
			CreatorAddress:            user.Address(),
			Slots:                     addresses,
			TokenPrice:                1,
			InferenceSealGraceNonces:  123,
			InferenceSealGraceSeconds: 99,
			ValidationRate:            6000,
		},
	}

	live := SessionParams{
		RefusalTimeout:      90,
		ExecutionTimeout:    1800,
		ValidationRate:      9999, // lane B must not override lane A
		VoteThresholdFactor: 67,
	}
	provider := stubRuntimeParams{params: live}

	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	mgr.SetRuntimeParamsProvider(provider)
	_, err := mgr.getOrCreate("escrow-1")
	require.NoError(t, err)

	meta, err := store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint32(123), meta.Config.InferenceSealGraceNonces)
	require.Equal(t, uint32(99), meta.Config.InferenceSealGraceSeconds)
	require.Equal(t, uint32(6000), meta.Config.ValidationRate)
	require.Equal(t, uint32(2), meta.Config.VoteThreshold) // floor(3 * 67 / 100)
	require.Equal(t, int64(90), meta.Config.RefusalTimeout)
	require.Equal(t, int64(1800), meta.Config.ExecutionTimeout)

	live.ValidationRate = 1111
	require.Equal(t, uint32(6000), meta.Config.ValidationRate, "validation_rate comes from escrow (lane A), not live provider")
	require.Equal(t, uint32(123), meta.Config.InferenceSealGraceNonces, "grace comes from escrow snapshot, not live provider")
}

func TestHostManager_Create_SnapshotsFeesFromEscrow(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	const createFee = uint64(12_345)
	const perNonce = uint64(678)

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:          "escrow-1",
			Amount:            100000,
			CreatorAddress:    user.Address(),
			Slots:             addresses,
			TokenPrice:        1,
			CreateDevshardFee: createFee,
			FeePerNonce:       perNonce,
		},
	}

	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	srv, err := mgr.getOrCreate("escrow-1")
	require.NoError(t, err)

	st := srv.Host().SnapshotState()
	require.Equal(t, createFee, st.Config.CreateDevshardFee)
	require.Equal(t, perNonce, st.Config.FeePerNonce)
	require.Equal(t, createFee, st.Fees)
}

func TestHostManager_Create_DefaultValidationRateWhenEscrowOmits(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
			TokenPrice:     1,
			// ValidationRate unset (older chain/dapi)
		},
	}
	provider := stubRuntimeParams{params: SessionParams{ValidationRate: 0}}

	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	mgr.SetRuntimeParamsProvider(provider)
	_, err := mgr.getOrCreate("escrow-1")
	require.NoError(t, err)

	meta, err := store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, types.DefaultValidationRate, meta.Config.ValidationRate)
}

func TestCreateSession_RejectsExistingDifferentVersion(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		EpochID:        7,
		Version:        "v1",
		CreatorAddr:    user.Address(),
		Config:         types.SessionConfigWithPrice(len(group), 1),
		Group:          group,
		InitialBalance: 100000,
	}))

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
			EpochID:        7,
			TokenPrice:     1,
		},
	}

	mgr := NewHostManager(
		store,
		hosts[0],
		stub.NewInferenceEngine(),
		stub.NewValidationEngine(),
		types.DevshardStateRootAndProtocolVersion,
		br,
		nil,
		nil,
	)
	_, err := mgr.getOrCreate("escrow-1")
	require.ErrorIs(t, err, storage.ErrSessionVersionConflict)
}

func TestCreateSession_DoesNotPersistWhenSignerNotInGroup(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	outsider := mustGenerateKey(t)
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}
	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
			EpochID:        7,
			TokenPrice:     1,
		},
	}

	mgr := NewHostManager(store, outsider, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	_, err := mgr.getOrCreate("escrow-1")
	require.ErrorIs(t, err, types.ErrHostNotInGroup)

	_, err = store.GetSessionMeta("escrow-1")
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestRetrievePayloadsFallsBackToChainEscrowEpoch(t *testing.T) {
	store := newManagerTestStore(t)
	payloadStore := &mockPayloadStore{}
	key := devshardserver.PayloadKey("460", 70)
	require.NoError(t, payloadStore.Store(context.Background(), key, 254, []byte("prompt"), []byte("response")))

	mgr := NewHostManager(
		store,
		mustGenerateKey(t),
		stub.NewInferenceEngine(),
		stub.NewValidationEngine(),
		runtimeTestVersion,
		&mockBridge{escrow: &bridge.EscrowInfo{EscrowID: "460", EpochID: 254}},
		payloadStore,
		nil,
	)

	prompt, response, epoch, err := mgr.retrievePayloadsWithAdjacentEpochs(context.Background(), "460", "70", 0)
	require.NoError(t, err)
	require.Equal(t, []byte("prompt"), prompt)
	require.Equal(t, []byte("response"), response)
	require.Equal(t, uint64(254), epoch)
}

func TestRetrievePayloadsKeyIncludesEscrowID(t *testing.T) {
	store := newManagerTestStore(t)
	payloadStore := &mockPayloadStore{}
	require.NoError(t, payloadStore.Store(
		context.Background(),
		devshardserver.PayloadKey("other", 70),
		254,
		[]byte("wrong"),
		[]byte("wrong"),
	))

	mgr := NewHostManager(
		store,
		mustGenerateKey(t),
		stub.NewInferenceEngine(),
		stub.NewValidationEngine(),
		runtimeTestVersion,
		&mockBridge{escrow: &bridge.EscrowInfo{EscrowID: "460", EpochID: 254}},
		payloadStore,
		nil,
	)

	_, _, _, err := mgr.retrievePayloadsWithAdjacentEpochs(context.Background(), "460", "70", 254)
	require.ErrorIs(t, err, payloadstorage.ErrNotFound)
}

func TestRetrievePayloadsEpochZeroFallsBackToCurrentEpoch(t *testing.T) {
	store := currentEpochStore{Storage: newManagerTestStore(t), epoch: 254}
	payloadStore := &mockPayloadStore{}
	key := devshardserver.PayloadKey("460", 70)
	require.NoError(t, payloadStore.Store(context.Background(), key, 253, []byte("prompt"), []byte("response")))

	mgr := NewHostManager(
		store,
		mustGenerateKey(t),
		stub.NewInferenceEngine(),
		stub.NewValidationEngine(),
		runtimeTestVersion,
		&mockBridge{},
		payloadStore,
		nil,
	)

	prompt, response, epoch, err := mgr.retrievePayloadsWithAdjacentEpochs(context.Background(), "460", "70", 0)
	require.NoError(t, err)
	require.Equal(t, []byte("prompt"), prompt)
	require.Equal(t, []byte("response"), response)
	require.Equal(t, uint64(253), epoch)
}

func TestRecoverSessions_EmptyStore(t *testing.T) {
	store := newManagerTestStore(t)
	signer := mustGenerateKey(t)
	br := &mockBridge{}

	mgr := NewHostManager(store, signer, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	err := mgr.RecoverSessions()
	require.NoError(t, err)

	mgr.mu.RLock()
	require.Empty(t, mgr.sessions)
	mgr.mu.RUnlock()
}

func TestRecoverSessions_StateRootMismatch(t *testing.T) {
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	config := defaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        runtimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier, store,
		state.WithStateRootAndProtocolVersion(types.EffectiveStateRootAndProtocolVersion),
	)
	require.NoError(t, err)

	// Diff 1: correct state hash.
	txs1 := []*types.DevshardTx{startTx(1)}
	root1, err := sm.ApplyLocal(1, txs1)
	require.NoError(t, err)
	diff1 := signDiffWithRoot(t, user, "escrow-1", 1, txs1, root1)
	require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{Diff: diff1, StateHash: root1}))

	// Diff 2: tampered state hash.
	txs2 := []*types.DevshardTx{startTx(2)}
	root2, err := sm.ApplyLocal(2, txs2)
	require.NoError(t, err)
	_ = root2
	diff2 := signDiffWithRoot(t, user, "escrow-1", 2, txs2, []byte("tampered"))
	require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{Diff: diff2, StateHash: []byte("tampered")}))

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	signer := mustGenerateKey(t)
	mgr := NewHostManager(store, signer, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	err = mgr.RecoverSessions()
	// RecoverSessions logs and skips corrupt sessions, does not return error.
	require.NoError(t, err)

	// The corrupt session should NOT be present in the sessions map.
	mgr.mu.RLock()
	_, ok := mgr.sessions["escrow-1"]
	mgr.mu.RUnlock()
	require.False(t, ok, "corrupt session should be skipped, not recovered")
}

func TestRecoverSessions_ValidationObs_NotReincremented(t *testing.T) {
	store := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, store, 3)
	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}
	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-1",
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	require.NoError(t, store.RecordValidationsAppliedOnce("escrow-1", []storage.ValidationObsEntry{
		{InferenceID: 1, SlotID: 0},
	}))
	before, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Len(t, before, 1)
	require.Equal(t, uint32(1), before[0].CompletedValidations)

	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	require.NoError(t, mgr.RecoverSessions())

	after, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func signProposerTx(t *testing.T, signer signing.Signer, msg proto.Message) []byte {
	t.Helper()
	cloned := proto.Clone(msg)
	if s, ok := cloned.(interface{ SetProposerSig([]byte) }); ok {
		s.SetProposerSig(nil)
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(cloned)
	require.NoError(t, err)
	sig, err := signer.Sign(data)
	require.NoError(t, err)
	return sig
}

func signExecutorReceipt(t *testing.T, signer signing.Signer, escrowID string, inferenceID uint64, promptHash []byte, model string, inputLength, maxTokens uint64, startedAt, confirmedAt int64) []byte {
	t.Helper()
	content := &types.ExecutorReceiptContent{
		InferenceId: inferenceID,
		PromptHash:  promptHash,
		Model:       model,
		InputLength: inputLength,
		MaxTokens:   maxTokens,
		StartedAt:   startedAt,
		EscrowId:    escrowID,
		ConfirmedAt: confirmedAt,
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(content)
	require.NoError(t, err)
	sig, err := signer.Sign(data)
	require.NoError(t, err)
	return sig
}

func TestStatsShardDetail_ValidationObservabilityAfterDiffApply(t *testing.T) {
	const escrowID = "escrow-obs"
	const epochID uint64 = 7

	base := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	group := makeGroup(hosts)
	config := defaultConfig(3)
	config.ValidationRate = 0
	verifier := signing.NewSecp256k1Verifier()
	engine := stub.NewInferenceEngine()

	require.NoError(t, base.CreateSession(storage.CreateSessionParams{
		EscrowID:       escrowID,
		EpochID:        epochID,
		Version:        runtimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100_000,
	}))

	sm, err := state.NewStateMachine(escrowID, config, group, 100_000, user.Address(), verifier, base,
		state.WithStateRootAndProtocolVersion(types.EffectiveStateRootAndProtocolVersion),
	)
	require.NoError(t, err)

	appendDiff := func(nonce uint64, txs []*types.DevshardTx) {
		root, err := sm.ApplyLocal(nonce, txs)
		require.NoError(t, err)
		require.NoError(t, base.AppendDiff(escrowID, types.DiffRecord{
			Diff:      signDiffWithRoot(t, user, escrowID, nonce, txs, root),
			StateHash: root,
		}))
	}

	appendDiff(1, []*types.DevshardTx{startTx(1)})
	execSig := signExecutorReceipt(t, hosts[1], escrowID, 1, nil, "llama", 100, 50, 1000, 2000)
	appendDiff(2, []*types.DevshardTx{{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 2000,
	}}}})
	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: engine.ResponseHash, InputTokens: 80, OutputTokens: 40,
		ExecutorSlot: 1, EscrowId: escrowID,
	}
	finishMsg.ProposerSig = signProposerTx(t, hosts[1], finishMsg)
	appendDiff(3, []*types.DevshardTx{{Tx: &types.DevshardTx_FinishInference{FinishInference: finishMsg}}})

	valMsg := &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0, Valid: true, EscrowId: escrowID}
	valMsg.ProposerSig = signProposerTx(t, hosts[0], valMsg)
	valTx := &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: valMsg}}

	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}
	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       escrowID,
			EpochID:        epochID,
			Amount:         100_000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}

	store := currentEpochStore{Storage: base, epoch: epochID}
	mgr := NewHostManager(store, hosts[0], engine, stub.NewValidationEngine(), runtimeTestVersion, br, nil, nil)
	mgr.SetReady()
	require.NoError(t, mgr.RecoverSessions())

	srv, err := mgr.SessionServer(escrowID)
	require.NoError(t, err)
	valDiff := signDiffWithRoot(t, user, escrowID, 4, []*types.DevshardTx{valTx}, nil)
	_, err = srv.Host().HandleRequest(context.Background(), host.HostRequest{Diffs: []types.Diff{valDiff}})
	require.NoError(t, err)
	// Observability is persisted asynchronously after apply; wait before the first
	// stats request so the 60s shard-detail cache is not populated with zeros.
	require.Eventually(t, func() bool {
		rows, err := base.GetValidationObservability(escrowID)
		if err != nil {
			return false
		}
		var total uint32
		for _, row := range rows {
			total += row.CompletedValidations
		}
		return total >= 1
	}, time.Second, 10*time.Millisecond)

	rec := requestStats(t, mgr, statsTestRoutePrefix, "/stats/shards/"+escrowID)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		ValidationObservability struct {
			Totals struct {
				CompletedValidations uint64 `json:"completed_validations"`
			} `json:"totals"`
		} `json:"validation_observability"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.GreaterOrEqual(t, resp.ValidationObservability.Totals.CompletedValidations, uint64(1))
}
