package peerrpcmembers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestControlEndpointRejectsWithoutToken(t *testing.T) {
	store := &Store{}
	var forwarded int
	h := Handler("secret", store, func(Snapshot) { forwarded++ })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, Path, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: got %d", rec.Code)
	}
	if forwarded != 0 {
		t.Fatalf("forwarded %d times without a token", forwarded)
	}

	req := httptest.NewRequest(http.MethodPost, Path, mustSnap(t, "router-a", "versiond1", []Member{{
		ID: "versiond1", Addr: "versiond1:8080", ReadyVersions: []string{"v2"},
	}}))
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d", rec.Code)
	}
	if forwarded != 0 || store.Recipient() != "" {
		t.Fatalf("rejected post was stored: forwarded=%d recipient=%q", forwarded, store.Recipient())
	}
}

func TestUnionAndForwardPerVersion(t *testing.T) {
	store := &Store{}
	var got Snapshot
	h := Handler("secret", store, func(snap Snapshot) { got = snap })

	post := func(router, recipient string, members []Member) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, Path, mustSnap(t, router, recipient, members))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("post %s: got %d %s", router, rec.Code, rec.Body.String())
		}
	}
	post("router-a", "versiond1", []Member{
		{ID: "versiond1", Addr: "10.0.0.1:8080", ReadyVersions: []string{"v2"}},
		{ID: "versiond2", Addr: "10.0.0.2:8080", ReadyVersions: []string{"v2"}},
	})
	post("router-b", "versiond1", []Member{
		{ID: "versiond1", Addr: "10.0.0.1:8080", ReadyVersions: []string{"v4"}},
		{ID: "versiond2", Addr: "10.0.0.2:8080", ReadyVersions: []string{"v2"}},
	})

	want := Snapshot{
		RecipientID: "versiond1",
		Members: []Member{
			{ID: "versiond1", Addr: "10.0.0.1:8080", ReadyVersions: []string{"v2", "v4"}},
			{ID: "versiond2", Addr: "10.0.0.2:8080", ReadyVersions: []string{"v2"}},
		},
	}
	if got.RecipientID != want.RecipientID || !reflect.DeepEqual(got.Members, want.Members) {
		t.Fatalf("union: got %+v", got)
	}

	var pushed []ChildPush
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != ChildPath {
			t.Errorf("child request %s %s", r.Method, r.URL.Path)
		}
		var push ChildPush
		if err := json.NewDecoder(r.Body).Decode(&push); err != nil {
			t.Errorf("decode push: %v", err)
		}
		pushed = append(pushed, push)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	if err := Forward(t.Context(), srv.Client(), got.Members, got.RecipientID, []Target{
		{Version: "v2", AdminURL: srv.URL},
		{Version: "v4", AdminURL: srv.URL},
	}); err != nil {
		t.Fatal(err)
	}
	wantPush := []ChildPush{
		{
			InstanceID: "versiond1@v2",
			Members: []ChildMember{
				{ID: "versiond1@v2"},
				{ID: "versiond2@v2"},
			},
		},
		{
			InstanceID: "versiond1@v4",
			Members:    []ChildMember{{ID: "versiond1@v4"}},
		},
	}
	if !reflect.DeepEqual(pushed, wantPush) {
		t.Fatalf("pushes: got %+v", pushed)
	}
}

func TestRouterPublishMatchesGoHash(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	script := filepath.Join(filepath.Dir(file), "..", "..", "..", "versiond-router", "publish-members")
	dir := t.TempDir()
	endpoints := filepath.Join(dir, "endpoints.json")
	ready := filepath.Join(dir, "ready.json")
	if err := os.WriteFile(endpoints, []byte(`[
	  {"id":"versiond","host":"versiond","port":8080},
	  {"id":"versiond-b","host":"10.20.0.12","port":18080},
	  {"id":"versiond-c","host":"10.20.0.13"}
	]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ready, []byte(`{
	  "versiond":["v2"],
	  "versiond-b":["v4","v2"]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(script)
	cmd.Env = append(os.Environ(),
		"VERSIOND_POOL_ENDPOINTS=",
		"VERSIOND_POOL_HOST=",
		"VERSIOND_POOL_ENDPOINTS_FILE="+endpoints,
		"VERSIOND_READY_FILE="+ready,
		"VERSIOND_ROUTER_ID=router-a",
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("publish-members: %v\n%s", err, out)
	}
	var snap Snapshot
	if err := json.Unmarshal(out, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Hash != Hash(snap.Members) {
		t.Fatalf("hash %s, go %s\ncanon %q", snap.Hash, Hash(snap.Members), Canonical(snap.Members))
	}
	want := []Member{
		{ID: "versiond", Addr: "versiond:8080", ReadyVersions: []string{"v2"}},
		{ID: "versiond-b", Addr: "10.20.0.12:18080", ReadyVersions: []string{"v2", "v4"}},
	}
	if !reflect.DeepEqual(snap.Members, want) {
		t.Fatalf("members: %+v", snap.Members)
	}
}

func mustSnap(t *testing.T, router, recipient string, members []Member) io.Reader {
	t.Helper()
	snap := Snapshot{RouterID: router, RecipientID: recipient, Hash: Hash(members), Members: members}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(b)
}
