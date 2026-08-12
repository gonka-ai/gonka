package session

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/stub"
)

func TestLeadingZeroEscrowAliasRejectedAtBind(t *testing.T) {
	const escrowID = "9901"
	const alias = "09901"

	mgr, store, user, _ := setupBindTestManager(t, escrowID)
	e := echo.New()
	mgr.Register(e.Group(""))

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	rec := signedPOST(t, e, user, "/sessions/"+escrowID+"/chat/completions", escrowID, body)
	_, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err, "canonical bind must create the session; http=%d body=%s", rec.Code, rec.Body.String())

	aliasRec := signedPOST(t, e, user, "/sessions/"+alias+"/chat/completions", alias, body)
	require.Equal(t, http.StatusBadRequest, aliasRec.Code, "body: %s", aliasRec.Body.String())

	_, err = store.GetSessionMeta(alias)
	require.ErrorIs(t, err, storage.ErrSessionNotFound, "alias must not create a second durable session")

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, escrowID, active[0].EscrowID)
	require.Equal(t, []string{escrowID}, mgr.ActiveEscrowIDs())
}

func TestLeadingZeroEscrowAliasRejectedOnSessionRoutes(t *testing.T) {
	const escrowID = "9902"
	const alias = "09902"
	mgr, _, _, hostSigner := setupBindTestManager(t, escrowID)
	e := echo.New()
	mgr.Register(e.Group(""))

	for _, path := range []string{
		"/sessions/" + alias + "/diffs",
		"/sessions/" + alias + "/mempool",
		"/sessions/" + alias + "/signatures",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, "path=%s body=%s", path, rec.Body.String())
	}

	gossip := signedPOST(t, e, hostSigner, "/sessions/"+alias+"/gossip/nonce", alias, []byte(`{"nonce":1}`))
	require.Equal(t, http.StatusBadRequest, gossip.Code, "body: %s", gossip.Body.String())
}

func TestBindOwnerChatRejectsAliasBeforeAuth(t *testing.T) {
	const escrowID = "9903"
	const alias = "009903"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)

	e := echo.New()
	e.POST("/sessions/:id/chat/completions", func(c echo.Context) error {
		_, err := mgr.BindOwnerChat(c)
		return err
	})

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	rec := signedPOST(t, e, user, "/sessions/"+alias+"/chat/completions", alias, body)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())

	_, err := store.GetSessionMeta(alias)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestCreateRejectsNonCanonicalEscrowID(t *testing.T) {
	mgr, _, _, _ := setupBindTestManager(t, "9904")

	_, err := mgr.create("09904", nil)
	require.ErrorContains(t, err, "invalid escrow id")

	_, err = mgr.getOrCreate("09904", nil)
	require.ErrorContains(t, err, "invalid escrow id")
}

func seedNonCanonicalStoredSession(t *testing.T, store storage.Storage, canonical, alias string) *signing.Secp256k1Signer {
	t.Helper()
	_, _, hostSigner := createStoredSession(t, store, canonical, 7, 0)

	meta, err := store.GetSessionMeta(canonical)
	require.NoError(t, err)
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       alias,
		EpochID:        meta.EpochID,
		Version:        meta.Version,
		CreatorAddr:    meta.CreatorAddr,
		Config:         meta.Config,
		Group:          meta.Group,
		InitialBalance: meta.InitialBalance,
	}))
	return hostSigner
}

func TestRecoverSessionsRetiresNonCanonicalStoredSessions(t *testing.T) {
	store := newManagerTestStore(t)
	hostSigner := seedNonCanonicalStoredSession(t, store, "9905", "09905")

	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)
	require.NoError(t, mgr.RecoverSessions())

	require.Equal(t, []string{"9905"}, mgr.ActiveEscrowIDs())

	meta, err := store.GetSessionMeta("09905")
	require.NoError(t, err)
	require.NotEqual(t, "active", meta.Status, "non-canonical row must be retired, not left active")

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, "9905", active[0].EscrowID)
}

func TestStatsDetailCannotResurrectNonCanonicalSession(t *testing.T) {
	store := newManagerTestStore(t)
	hostSigner := seedNonCanonicalStoredSession(t, store, "9906", "09906")

	mgr := NewHostManager(currentEpochStore{Storage: store, epoch: 7}, hostSigner,
		stub.NewInferenceEngine(), stub.NewValidationEngine(), nil,
		testutil.RuntimeTestVersion, &mockBridge{}, nil, nil)

	rec := requestStats(t, mgr, statsTestRoutePrefix, "/stats/shards/09906")
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())

	_, loaded := mgr.existingServer("09906")
	require.False(t, loaded, "alias session must not be loaded into memory")
	require.Empty(t, mgr.ActiveEscrowIDs())

	_, err := mgr.SessionServerExisting("09906")
	require.ErrorContains(t, err, "invalid escrow id")
}
