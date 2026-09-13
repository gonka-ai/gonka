package session

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
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
	"devshard/types"
)

func setupBindTestManager(t *testing.T, escrowID string) (*HostManager, *storage.SQLite, *signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	mgr, store, user, hosts := setupBindTestGroup(t, escrowID)
	return mgr, store, user, hosts[0]
}

func setupBindTestGroup(t *testing.T, escrowID string) (*HostManager, *storage.SQLite, *signing.Secp256k1Signer, []*signing.Secp256k1Signer) {
	t.Helper()
	return setupBindTestGroupSignedBy(t, escrowID, 0)
}

func setupBindTestGroupSignedBy(t *testing.T, escrowID string, localSlot int) (*HostManager, *storage.SQLite, *signing.Secp256k1Signer, []*signing.Secp256k1Signer) {
	t.Helper()
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	require.GreaterOrEqual(t, localSlot, 0)
	require.Less(t, localSlot, len(hosts))
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
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hosts[localSlot], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
	return mgr, store, user, hosts
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
	const escrowID = "9701"
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
	const escrowID = "9702"
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

func TestGossipUnbound_BindsSession(t *testing.T) {
	const escrowID = "9703"
	mgr, store, user, hostSigner := setupBindTestManager(t, escrowID)
	e := echo.New()
	mgr.Register(e.Group(""))

	body := []byte(`{"nonce":1}`)
	_ = signedPOST(t, e, hostSigner, "/sessions/"+escrowID+"/gossip/nonce", escrowID, body)
	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err, "group-peer gossip must CreateSession so a missed owner chat can still be challenged")
	require.Equal(t, user.Address(), meta.CreatorAddr)
}

func TestGossipUnbound_StrangerDoesNotBindSession(t *testing.T) {
	const escrowID = "9712"
	mgr, store, _, _ := setupBindTestManager(t, escrowID)
	e := echo.New()
	mgr.Register(e.Group(""))

	stranger := mustGenerateKey(t)
	body := []byte(`{"nonce":1}`)
	rec := signedPOST(t, e, stranger, "/sessions/"+escrowID+"/gossip/nonce", escrowID, body)
	require.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())

	_, err := store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestChallengeReceiptUnbound_BindsAndAppliesStart(t *testing.T) {
	const escrowID = "9713"
	// This process is slot 1 so inference 1 (nonce 1) assigns it as executor.
	mgr, store, user, hosts := setupBindTestGroupSignedBy(t, escrowID, 1)
	e := echo.New()
	mgr.Register(e.Group(""))

	const inferenceID uint64 = 1
	diff := testutil.SignDiff(t, user, escrowID, inferenceID, []*types.DevshardTx{testutil.StartTx(inferenceID)})
	dj, err := transport.DiffToJSON(diff)
	require.NoError(t, err)
	body, err := json.Marshal(transport.ChallengeReceiptRequest{
		InferenceID: inferenceID,
		Payload: &transport.PayloadJSON{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   testutil.TestMaxTokens,
			StartedAt:   1000,
		},
		Diffs: []transport.DiffJSON{dj},
	})
	require.NoError(t, err)

	challenger := hosts[2]
	require.NotEqual(t, hosts[1].Address(), challenger.Address())
	rec := signedPOST(t, e, challenger, "/sessions/"+escrowID+"/challenge-receipt", escrowID, body)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err, "challenge-receipt on a cold host must CreateSession")
	require.Equal(t, user.Address(), meta.CreatorAddr)

	var resp transport.ChallengeReceiptResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Receipt, "executor must sign a receipt after applying the missed StartInference")

	txs, err := transport.DevshardTxsFromBytes(resp.Mempool)
	require.NoError(t, err)
	found := false
	for _, tx := range txs {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == inferenceID {
			found = true
			break
		}
	}
	require.True(t, found, "recovery mempool must include MsgConfirmStart for the challenged inference")
}

func TestChallengeReceiptUnbound_StrangerDoesNotBind(t *testing.T) {
	const escrowID = "9714"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)
	e := echo.New()
	mgr.Register(e.Group(""))

	diff := testutil.SignDiff(t, user, escrowID, 1, []*types.DevshardTx{testutil.StartTx(3)})
	dj, err := transport.DiffToJSON(diff)
	require.NoError(t, err)
	body, err := json.Marshal(transport.ChallengeReceiptRequest{
		InferenceID: 3,
		Payload:     &transport.PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000},
		Diffs:       []transport.DiffJSON{dj},
	})
	require.NoError(t, err)

	rec := signedPOST(t, e, mustGenerateKey(t), "/sessions/"+escrowID+"/challenge-receipt", escrowID, body)
	require.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())
	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

func TestOwnerChat_BindsSession(t *testing.T) {
	const escrowID = "9704"
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

func TestHeightSyncSeed_BindsSession(t *testing.T) {
	const escrowID = "9711"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)
	e := echo.New()
	mgr.Register(e.Group(""))

	rec := signedPOST(t, e, user, "/sessions/"+escrowID+"/height-sync", escrowID, []byte("{}"))
	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err, "owner seed must CreateSession; http=%d body=%s", rec.Code, rec.Body.String())
	require.Equal(t, testutil.RuntimeTestVersion, meta.Version)
	require.Equal(t, user.Address(), meta.CreatorAddr)
}

func TestOwnerChat_SettledEscrow_DoesNotBindSession(t *testing.T) {
	const escrowID = "9709"
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
	const escrowID = "9710"
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

func TestOwnerChat_RejectsChunkedBodyBeforeBinding(t *testing.T) {
	const escrowID = "9708"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)
	mgr.maxBodySize = 8
	e := echo.New()
	mgr.Register(e.Group(""))

	body := []byte("123456789")
	ts := time.Now().Unix()
	sig, err := transport.SignRequest(user, escrowID, body, ts)
	require.NoError(t, err)
	req := httptest.NewRequest(
		http.MethodPost,
		"/sessions/"+escrowID+"/chat/completions",
		bytes.NewReader(body),
	)
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	req.Header.Set(transport.HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(transport.HeaderTimestamp, strconv.FormatInt(ts, 10))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
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
	const escrowID = "9705"
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
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))

	e := echo.New()
	mgr.Register(e.Group(""))
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	_ = signedPOST(t, e, user, "/sessions/"+escrowID+"/chat/completions", escrowID, body)

	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err)
	require.Equal(t, testutil.RuntimeTestVersion, meta.Version)
	require.Equal(t, 1, br.calls, "first-bind path must GetEscrow once (reuse for BuildGroup + CreateSession)")
}

func TestOwnerChat_WarmedCacheStillGetEscrow(t *testing.T) {
	const escrowID = "9711"
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
	}
	user := mustGenerateKey(t)
	addresses := make([]string, len(hosts))
	urls := make(map[string]string, len(hosts))
	for i, h := range hosts {
		addresses[i] = h.Address()
		urls[h.Address()] = "http://localhost"
	}
	require.NoError(t, store.PutEscrowCache(storage.EscrowCacheInfo{
		EscrowID: escrowID, EpochID: 7, Amount: 100000, CreatorAddress: user.Address(),
		Slots: addresses, TokenPrice: 1, SlotURLs: urls,
	}))
	inner := &mockBridge{
		escrow: &bridge.EscrowInfo{
			EscrowID: escrowID, EpochID: 7, Amount: 100000,
			CreatorAddress: user.Address(), Slots: addresses, TokenPrice: 1,
		},
	}
	br := &countingGetEscrowBridge{MainnetBridge: inner}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))

	e := echo.New()
	mgr.Register(e.Group(""))
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	_ = signedPOST(t, e, user, "/sessions/"+escrowID+"/chat/completions", escrowID, body)

	require.Equal(t, 1, br.calls, "owner first chat still GetEscrow live even when escrow_cache is warm")
}

func TestNonOwnerChat_DoesNotBindSession(t *testing.T) {
	const escrowID = "9706"
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
	const escrowID = "9707"
	mgr, store, _, _ := setupBindTestManager(t, escrowID)

	_, err := mgr.SessionServerExisting(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	_, err = store.GetSessionMeta(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
}

type countingGetSessionMetaStore struct {
	storage.Storage
	gets atomic.Int32
}

func (s *countingGetSessionMetaStore) GetSessionMeta(escrowID string) (*storage.SessionMeta, error) {
	s.gets.Add(1)
	return s.Storage.GetSessionMeta(escrowID)
}

func TestSessionServerExisting_NegativeCachesMiss(t *testing.T) {
	counted := &countingGetSessionMetaStore{Storage: storage.NewMemory()}
	mgr := NewHostManager(counted, mustGenerateKey(t), nil, nil, nil, "v5", nil, nil, nil)
	t.Cleanup(func() { _ = mgr.Close() })

	const escrowID = "9708"
	_, err := mgr.SessionServerExisting(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
	first := counted.gets.Load()
	require.Greater(t, first, int32(0))

	_, err = mgr.SessionServerExisting(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)
	require.Equal(t, first, counted.gets.Load(), "cached miss must not recover again")
}

func TestSessionServerExisting_MissDoesNotOccupyRecoveryGate(t *testing.T) {
	mgr := NewHostManager(storage.NewMemory(), mustGenerateKey(t), nil, nil, nil, "v5", nil, nil, nil)
	t.Cleanup(func() { _ = mgr.Close() })

	const escrowID = "9709"
	_, err := mgr.SessionServerExisting(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	mgr.recoveryGate.mu.Lock()
	defer mgr.recoveryGate.mu.Unlock()
	require.Empty(t, mgr.recoveryGate.requested, "a miss must not occupy the demand set")
	require.Zero(t, mgr.recoveryGate.inFlight)
}

func TestOwnerChat_BindsAfterExistingMiss(t *testing.T) {
	const escrowID = "9712"
	mgr, store, user, _ := setupBindTestManager(t, escrowID)

	_, err := mgr.SessionServerExisting(escrowID)
	require.ErrorIs(t, err, storage.ErrSessionNotFound)

	e := echo.New()
	mgr.Register(e.Group(""))
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	rec := signedPOST(t, e, user, "/sessions/"+escrowID+"/chat/completions", escrowID, body)
	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err, "cached Existing miss must not block first bind; http=%d body=%s", rec.Code, rec.Body.String())
	require.Equal(t, testutil.RuntimeTestVersion, meta.Version)
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
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))

	srv, err := mgr.getOrCreate("1", nil)
	require.NoError(t, err)
	require.Equal(t, uint64(2), srv.Host().SnapshotState().LatestNonce)
}
