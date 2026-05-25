package devshard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	devshardpkg "devshard"
	mlnodepkg "devshard/mlnode"
	mlnodegen "devshard/mlnode/gen"
)

const validEnvelope = `{"version":1,"session_id":"s","nonce":1,"recipient_key_id":"0123456789abcdef"}`

type fakeAcquirer struct {
	endpoint   string
	acquires   atomic.Int32
	releases   atomic.Int32
	mu         sync.Mutex
	keys       []string
	outcomes   []mlnodegen.ReleaseOutcome
	releaseCtx []error
	acquireErr error
	releaseErr error
}

func (f *fakeAcquirer) AcquireByKey(_ context.Context, key string) (*mlnodegen.AcquireMLNodeResponse, error) {
	f.mu.Lock()
	f.keys = append(f.keys, key)
	f.mu.Unlock()
	f.acquires.Add(1)
	if f.acquireErr != nil {
		return nil, f.acquireErr
	}
	return &mlnodegen.AcquireMLNodeResponse{LockId: "lock", Endpoint: f.endpoint, NodeId: "node-1"}, nil
}

func (f *fakeAcquirer) Release(ctx context.Context, _ string, outcome mlnodegen.ReleaseOutcome) error {
	f.mu.Lock()
	f.outcomes = append(f.outcomes, outcome)
	f.releaseCtx = append(f.releaseCtx, ctx.Err())
	f.mu.Unlock()
	f.releases.Add(1)
	return f.releaseErr
}

func newFakeMLBackend(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, encryptedMLPath) {
			http.Error(w, "wrong path", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestForwardEncrypted_Success(t *testing.T) {
	srv := newFakeMLBackend(http.StatusOK, "sealed-response")
	defer srv.Close()

	acq := &fakeAcquirer{endpoint: srv.URL}
	eng := &encryptedEngine{mlClient: acq, httpClient: srv.Client()}

	resp, err := eng.ForwardEncrypted(context.Background(), []byte(validEnvelope))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if string(resp) != "sealed-response" {
		t.Fatalf("body: got %q", resp)
	}
	if got, want := int(acq.acquires.Load()), 1; got != want {
		t.Fatalf("acquires: got %d want %d (no retry)", got, want)
	}
	if got, want := acq.keys[0], "0123456789abcdef"; got != want {
		t.Fatalf("recipient_key_id passed to acquire: got %q want %q", got, want)
	}
	if got := acq.outcomes; len(got) != 1 || got[0] != mlnodegen.ReleaseOutcome_SUCCESS {
		t.Fatalf("expected single SUCCESS release, got %v", got)
	}
}

func TestForwardEncrypted_EmptyBody(t *testing.T) {
	acq := &fakeAcquirer{}
	eng := &encryptedEngine{mlClient: acq, httpClient: http.DefaultClient}

	_, err := eng.ForwardEncrypted(context.Background(), nil)
	if !errors.Is(err, devshardpkg.EncryptedBadEnvelope) {
		t.Fatalf("expected EncryptedBadEnvelope, got %v", err)
	}
	if got := int(acq.acquires.Load()); got != 0 {
		t.Fatalf("must not acquire: got %d", got)
	}
}

func TestForwardEncrypted_MissingRecipientKey(t *testing.T) {
	acq := &fakeAcquirer{}
	eng := &encryptedEngine{mlClient: acq, httpClient: http.DefaultClient}

	_, err := eng.ForwardEncrypted(context.Background(), []byte(`{"version":1,"session_id":"s","nonce":1}`))
	if !errors.Is(err, devshardpkg.EncryptedBadEnvelope) {
		t.Fatalf("expected EncryptedBadEnvelope, got %v", err)
	}
	if got := int(acq.acquires.Load()); got != 0 {
		t.Fatalf("must not acquire when key id missing: got %d", got)
	}
}

func TestForwardEncrypted_MalformedRecipientKey(t *testing.T) {
	acq := &fakeAcquirer{}
	eng := &encryptedEngine{mlClient: acq, httpClient: http.DefaultClient}

	_, err := eng.ForwardEncrypted(context.Background(), []byte(`{"recipient_key_id":"too-short"}`))
	if !errors.Is(err, devshardpkg.EncryptedBadEnvelope) {
		t.Fatalf("expected EncryptedBadEnvelope, got %v", err)
	}
	if got := int(acq.acquires.Load()); got != 0 {
		t.Fatalf("must not acquire on malformed key id: got %d", got)
	}
}

func TestForwardEncrypted_NonHexRecipientKey(t *testing.T) {
	acq := &fakeAcquirer{}
	eng := &encryptedEngine{mlClient: acq, httpClient: http.DefaultClient}

	_, err := eng.ForwardEncrypted(context.Background(), []byte(`{"recipient_key_id":"gggggggggggggggg"}`))
	if !errors.Is(err, devshardpkg.EncryptedBadEnvelope) {
		t.Fatalf("expected EncryptedBadEnvelope, got %v", err)
	}
	if got := int(acq.acquires.Load()); got != 0 {
		t.Fatalf("must not acquire on invalid key id charset: got %d", got)
	}
}

func TestForwardEncrypted_KeyUnknownSurfaces(t *testing.T) {
	acq := &fakeAcquirer{acquireErr: mlnodepkg.ErrRecipientKeyUnknown}
	eng := &encryptedEngine{mlClient: acq, httpClient: http.DefaultClient}

	_, err := eng.ForwardEncrypted(context.Background(), []byte(validEnvelope))
	if !errors.Is(err, devshardpkg.EncryptedKeyUnknown) {
		t.Fatalf("expected EncryptedKeyUnknown, got %v", err)
	}
	if got := int(acq.releases.Load()); got != 0 {
		t.Fatalf("must not release when acquire failed: got %d", got)
	}
}

func TestForwardEncrypted_NoCapacityWrapsOtherAcquireErrors(t *testing.T) {
	acq := &fakeAcquirer{acquireErr: errors.New("queue full")}
	eng := &encryptedEngine{mlClient: acq, httpClient: http.DefaultClient}

	_, err := eng.ForwardEncrypted(context.Background(), []byte(validEnvelope))
	if !errors.Is(err, devshardpkg.EncryptedNoCapacity) {
		t.Fatalf("expected EncryptedNoCapacity, got %v", err)
	}
}

func TestForwardEncrypted_4xxReleasedAsApplicationError(t *testing.T) {
	srv := newFakeMLBackend(http.StatusBadRequest, "corrupt envelope")
	defer srv.Close()

	acq := &fakeAcquirer{endpoint: srv.URL}
	eng := &encryptedEngine{mlClient: acq, httpClient: srv.Client()}

	_, err := eng.ForwardEncrypted(context.Background(), []byte(validEnvelope))
	if err == nil {
		t.Fatal("expected error for 4xx")
	}
	if got := acq.outcomes; len(got) != 1 || got[0] != mlnodegen.ReleaseOutcome_APPLICATION_ERROR {
		t.Fatalf("expected single APPLICATION_ERROR release, got %v", got)
	}
}

func TestForwardEncrypted_5xxReleasedAsTransportError(t *testing.T) {
	srv := newFakeMLBackend(http.StatusInternalServerError, "boom")
	defer srv.Close()

	acq := &fakeAcquirer{endpoint: srv.URL}
	eng := &encryptedEngine{mlClient: acq, httpClient: srv.Client()}

	_, err := eng.ForwardEncrypted(context.Background(), []byte(validEnvelope))
	if err == nil {
		t.Fatal("expected error for 5xx")
	}
	if got := acq.outcomes; len(got) != 1 || got[0] != mlnodegen.ReleaseOutcome_TRANSPORT_ERROR {
		t.Fatalf("expected single TRANSPORT_ERROR release, got %v", got)
	}
}

func TestForwardEncrypted_MisdirectedOnTargetedIsApplicationError(t *testing.T) {
	srv := newFakeMLBackend(http.StatusMisdirectedRequest, "wrong key")
	defer srv.Close()

	acq := &fakeAcquirer{endpoint: srv.URL}
	eng := &encryptedEngine{mlClient: acq, httpClient: srv.Client()}

	_, err := eng.ForwardEncrypted(context.Background(), []byte(validEnvelope))
	if err == nil {
		t.Fatal("expected error on 421")
	}
	if got := acq.outcomes; len(got) != 1 || got[0] != mlnodegen.ReleaseOutcome_APPLICATION_ERROR {
		t.Fatalf("expected single APPLICATION_ERROR release on 421, got %v", got)
	}
	if got := int(acq.acquires.Load()); got != 1 {
		t.Fatalf("must not retry on misdirected (targeted routing), got %d acquires", got)
	}
}

func TestForwardEncrypted_ReleaseUsesDetachedContext(t *testing.T) {
	srv := newFakeMLBackend(http.StatusOK, "sealed-response")
	defer srv.Close()

	acq := &fakeAcquirer{endpoint: srv.URL}
	eng := &encryptedEngine{mlClient: acq, httpClient: srv.Client()}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := eng.ForwardEncrypted(ctx, []byte(validEnvelope))
	if err == nil {
		t.Fatal("expected error when request context is cancelled")
	}
	if got := int(acq.releases.Load()); got != 1 {
		t.Fatalf("release must still happen on cancelled request context, got %d", got)
	}
	if len(acq.releaseCtx) != 1 {
		t.Fatalf("expected one release context sample, got %d", len(acq.releaseCtx))
	}
	if acq.releaseCtx[0] != nil {
		t.Fatalf("release used cancelled context; expected detached context, got err=%v", acq.releaseCtx[0])
	}
}
