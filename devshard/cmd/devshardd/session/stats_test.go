package session

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

const statsTestRoutePrefix = "/devshard/test"

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

type countingListStore struct {
	storage.Storage
	listCalls int
}

func (s *countingListStore) ListActiveSessions() ([]storage.ActiveSession, error) {
	s.listCalls++
	return s.Storage.ListActiveSessions()
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

func createStoredSession(t *testing.T, store storage.Storage, escrowID string, epochID uint64, numDiffs int) ([]types.SlotAssignment, *signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	return createStoredSessionWithVersion(t, store, escrowID, epochID, testutil.RuntimeTestVersion, numDiffs)
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
		state.WithVersion(version),
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

func TestStatsShardsListsNonPrunedActiveWithoutDetails(t *testing.T) {
	base := newManagerTestStore(t)
	_, _, hostSigner := createStoredSession(t, base, "escrow-current", 7, 0)
	createStoredSession(t, base, "escrow-old", 6, 0)

	counting := &countingListStore{Storage: currentEpochStore{Storage: base, epoch: 7}}
	mgr := NewHostManager(counting, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)

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
	// Lexicographic escrow id order from boundVersionActiveSessions.
	require.Equal(t, []string{"escrow-current", "escrow-old"}, resp.ActiveEscrows)
	require.Len(t, resp.Shards, 2)
	require.Equal(t, "escrow-current", resp.Shards[0].EscrowID)
	require.Equal(t, uint64(7), resp.Shards[0].EpochID)
	require.Equal(t, "escrow-old", resp.Shards[1].EscrowID)
	require.Equal(t, uint64(6), resp.Shards[1].EpochID)

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
	createStoredSessionWithVersion(t, base, "escrow-v2", 7, "foreign", 0)

	store := currentEpochStore{Storage: base, epoch: 7}
	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)

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
	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)

	rec := requestStats(t, mgr, statsTestRoutePrefix, "/stats/shards")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Contains(t, rec.Body.String(), "escrow-ok")
	require.NotContains(t, rec.Body.String(), "escrow-bad-meta")
}

func TestStatsShardDetailReturnsStatsOnly(t *testing.T) {
	base := newManagerTestStore(t)
	group, user, hostSigner := createStoredSession(t, base, "escrow-detail", 7, 1)
	store := currentEpochStore{Storage: base, epoch: 7}
	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}
	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-detail",
			EpochID:        7,
			Amount:         100000000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)
	require.NoError(t, mgr.RecoverSessions())

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
	require.Equal(t, testutil.RuntimeTestVersion, resp.Version)
	require.Equal(t, types.EffectiveStateRootAndProtocolVersion, resp.StateRootAndProtocolVersion)
	require.Len(t, resp.HostStats, len(group))
	require.Equal(t, group, resp.Group)

	cached := requestStats(t, mgr, statsTestRoutePrefix, "/stats/shards/escrow-detail")
	require.Equal(t, http.StatusOK, cached.Code, "body: %s", cached.Body.String())
	require.Equal(t, rec.Body.String(), cached.Body.String())
}

func TestStatsShardDetailServesPriorEpochSession(t *testing.T) {
	base := newManagerTestStore(t)
	group, user, hostSigner := createStoredSession(t, base, "escrow-prior", 6, 1)
	// Chain has advanced; session remains active until prune.
	store := currentEpochStore{Storage: base, epoch: 7}
	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}
	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "escrow-prior",
			EpochID:        6,
			Amount:         100000000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)
	require.NoError(t, mgr.RecoverSessions())

	rec := requestStats(t, mgr, statsTestRoutePrefix, "/stats/shards/escrow-prior")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var resp struct {
		EscrowID string `json:"escrow_id"`
		EpochID  uint64 `json:"epoch_id"`
		Nonce    uint64 `json:"nonce"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "escrow-prior", resp.EscrowID)
	require.Equal(t, uint64(6), resp.EpochID)
	require.Equal(t, uint64(1), resp.Nonce)
}

func TestStatsShardDetailSkipsForeignVersionSession(t *testing.T) {
	base := newManagerTestStore(t)
	_, _, hostSigner := createStoredSessionWithVersion(t, base, "escrow-v2", 7, "foreign", 0)

	store := currentEpochStore{Storage: base, epoch: 7}
	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)

	rec := requestStats(t, mgr, "/v1/devshard", "/stats/shards/escrow-v2")
	require.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
}
