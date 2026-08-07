package session

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"
)

func setupBindTestManager(t *testing.T, escrowID string) (*HostManager, *storage.SQLite, *signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
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
			EscrowID:       escrowID,
			EpochID:        7,
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
			TokenPrice:     1,
		},
	}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)
	return mgr, store, user, hosts[0]
}

func signedPOST(t *testing.T, e *echo.Echo, signer *signing.Secp256k1Signer, path, escrowID string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	ts := time.Now().Unix()
	sig, err := transport.SignRequest(signer, escrowID, body, ts)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, "application/json")
	req.Header.Set(transport.HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(transport.HeaderTimestamp, strconv.FormatInt(ts, 10))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestObsGET_DoesNotBindSession(t *testing.T) {
	const escrowID = "obs-unbound"
	mgr, store, _, _ := setupBindTestManager(t, escrowID)

	e := echo.New()
	mgr.Register(e.Group(""))

	req := httptest.NewRequest(http.MethodGet, "/sessions/"+escrowID+"/diffs", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())

	_, err := store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Empty(t, active)
}

func TestObsMempoolSignatures_DoNotBindSession(t *testing.T) {
	const escrowID = "obs-unbound-2"
	mgr, store, _, _ := setupBindTestManager(t, escrowID)
	e := echo.New()
	mgr.Register(e.Group(""))

	for _, path := range []string{
		"/sessions/" + escrowID + "/mempool",
		"/sessions/" + escrowID + "/signatures",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code, "path=%s body=%s", path, rec.Body.String())
	}

	_, err := store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestGossipUnbound_DoesNotBindSession(t *testing.T) {
	const escrowID = "gossip-unbound"
	mgr, store, _, hostSigner := setupBindTestManager(t, escrowID)
	e := echo.New()
	mgr.Register(e.Group(""))

	body := []byte(`{"nonce":1}`)
	rec := signedPOST(t, e, hostSigner, "/sessions/"+escrowID+"/gossip/nonce", escrowID, body)
	require.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())

	_, err := store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestOwnerChat_BindsSession(t *testing.T) {
	const escrowID = "owner-bind"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)
	e := echo.New()
	mgr.Register(e.Group(""))

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	rec := signedPOST(t, e, user, "/sessions/"+escrowID+"/chat/completions", escrowID, body)
	// Inference may fail downstream; bind must have happened regardless.
	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err, "owner chat must CreateSession; http=%d body=%s", rec.Code, rec.Body.String())
	require.Equal(t, testutil.RuntimeTestVersion, meta.Version)
	require.Equal(t, user.Address(), meta.CreatorAddr)
}

func TestOwnerChat_SettledEscrow_DoesNotBindSession(t *testing.T) {
	const escrowID = "owner-bind-settled"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)
	mgr.bridge.(*mockBridge).escrow.Settled = true
	e := echo.New()
	mgr.Register(e.Group(""))

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	rec := signedPOST(t, e, user, "/sessions/"+escrowID+"/chat/completions", escrowID, body)
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())

	_, err := store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Empty(t, active)
}

func TestOwnerChat_SettledLocalRow_ReturnsConflict(t *testing.T) {
	const escrowID = "owner-bind-settled-row"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)
	e := echo.New()
	mgr.Register(e.Group(""))

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	signedPOST(t, e, user, "/sessions/"+escrowID+"/chat/completions", escrowID, body)
	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err, "precondition: first chat must bind the session")
	require.Equal(t, "active", meta.Status)

	require.NoError(t, mgr.HandleSettlementFinalized(escrowID))

	rec := signedPOST(t, e, user, "/sessions/"+escrowID+"/chat/completions", escrowID, body)
	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, transport.DevshardErrorEscrowSettled, rec.Header().Get(transport.HeaderDevshardError))
}

type countingGetEscrowBridge struct {
	bridge.MainnetBridge
	calls int
}

func (b *countingGetEscrowBridge) GetEscrow(escrowID string) (*bridge.EscrowInfo, error) {
	b.calls++
	return b.MainnetBridge.GetEscrow(escrowID)
}

func TestOwnerChat_FirstBindSingleGetEscrow(t *testing.T) {
	const escrowID = "owner-bind-once"
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
	inner := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       escrowID,
			EpochID:        7,
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
			TokenPrice:     1,
		},
	}
	br := &countingGetEscrowBridge{MainnetBridge: inner}
	mgr := NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)

	e := echo.New()
	mgr.Register(e.Group(""))
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	_ = signedPOST(t, e, user, "/sessions/"+escrowID+"/chat/completions", escrowID, body)

	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err)
	require.Equal(t, testutil.RuntimeTestVersion, meta.Version)
	require.Equal(t, 1, br.calls, "first-bind path must GetEscrow once (reuse for BuildGroup + CreateSession)")
}

func TestNonOwnerChat_DoesNotBindSession(t *testing.T) {
	const escrowID = "non-owner-bind"
	mgr, store, _, hostSigner := setupBindTestManager(t, escrowID)
	e := echo.New()
	mgr.Register(e.Group(""))

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	rec := signedPOST(t, e, hostSigner, "/sessions/"+escrowID+"/chat/completions", escrowID, body)
	require.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())

	_, err := store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestSessionServerExisting_NoCreate(t *testing.T) {
	const escrowID = "existing-miss"
	mgr, store, _, _ := setupBindTestManager(t, escrowID)

	_, err := mgr.SessionServerExisting(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestGetOrCreate_RecoversBeforeCreate(t *testing.T) {
	store := newManagerTestStore(t)
	_, user, hostSigner := populateStore(t, store, 2)

	addresses := []string{hostSigner.Address()}
	// populateStore uses 3 hosts; rebuild addresses from meta.
	meta, err := store.GetSessionMeta("1")
	require.NoError(t, err)
	addresses = make([]string, len(meta.Group))
	for i, s := range meta.Group {
		addresses[i] = s.ValidatorAddress
	}

	br := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID:       "1",
			EpochID:        7,
			Amount:         100000,
			CreatorAddress: user.Address(),
			Slots:          addresses,
		},
	}
	mgr := NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil)

	srv, err := mgr.getOrCreate("1", nil)
	require.NoError(t, err)
	require.Equal(t, uint64(2), srv.Host().SnapshotState().LatestNonce)
}
