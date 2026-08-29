package process

import (
	"encoding/json"
	"net/http"
	"testing"

	"versioned/internal/config"
	"versioned/internal/oracle"
)

func TestStorageProofAggregatesEveryRunningChild(t *testing.T) {
	mgr := NewManager(config.Config{BasePort: 5000})
	addStorageProofChild(t, mgr, "v4", "database-1", true)
	addStorageProofChild(t, mgr, "v5", "database-1", true)

	identity, err := mgr.StorageIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if identity.Identity != "database-1" || identity.Children != 2 {
		t.Fatalf("StorageIdentity() = %+v", identity)
	}
	challenge, err := mgr.StorageChallenge(t.Context(), "read", "8aa1c262-ea39-43c2-928c-263e966cc9b4")
	if err != nil {
		t.Fatal(err)
	}
	if !challenge.Found || challenge.Children != 2 {
		t.Fatalf("StorageChallenge() = %+v", challenge)
	}
}

func TestStorageProofFailsOnChildIdentityMismatch(t *testing.T) {
	mgr := NewManager(config.Config{BasePort: 5000})
	addStorageProofChild(t, mgr, "v4", "database-1", true)
	addStorageProofChild(t, mgr, "v5", "database-2", true)
	if _, err := mgr.StorageIdentity(t.Context()); err == nil {
		t.Fatal("StorageIdentity() succeeded with mismatched child storage")
	}
}

func TestStorageProofFailsClosedForLegacyChild(t *testing.T) {
	mgr := NewManager(config.Config{BasePort: 5000})
	mgr.processes["v4"] = &child{
		version: oracle.Version{Name: "v4"},
		status:  statusRunning,
		done:    make(chan struct{}),
	}
	if _, err := mgr.StorageIdentity(t.Context()); err == nil {
		t.Fatal("StorageIdentity() succeeded for child without admin storage proof")
	}
}

func addStorageProofChild(t *testing.T, mgr *Manager, version, identity string, found bool) {
	t.Helper()
	port, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/storage/identity" && r.URL.Path != "/storage/challenge" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(childStorageProof{Identity: identity, Found: found})
	}))
	t.Cleanup(shutdown)
	c := &child{
		version: oracle.Version{Name: version},
		status:  statusRunning,
		done:    make(chan struct{}),
	}
	c.adminPort.Store(int64(port))
	mgr.processes[version] = c
}
