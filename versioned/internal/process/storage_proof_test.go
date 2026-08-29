package process

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"versioned/internal/config"
	"versioned/internal/oracle"
)

type fakeProofDatabase struct {
	mu       sync.Mutex
	identity string
	nonces   map[string]bool
}

func newFakeProofDatabase(identity string) *fakeProofDatabase {
	return &fakeProofDatabase{identity: identity, nonces: make(map[string]bool)}
}

func (db *fakeProofDatabase) handler(w http.ResponseWriter, r *http.Request) {
	db.mu.Lock()
	defer db.mu.Unlock()
	proof := childStorageProof{Identity: db.identity}
	switch r.URL.Path {
	case "/storage/identity":
	case "/storage/challenge":
		var request storageChallengeRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch request.Operation {
		case "write":
			db.nonces[request.Nonce] = true
			proof.Found = true
		case "read":
			proof.Found = db.nonces[request.Nonce]
		default:
			http.Error(w, "bad operation", http.StatusBadRequest)
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(proof)
}

func TestStorageProofAddressesOneGeneration(t *testing.T) {
	mgr := newStorageProofManager()
	if _, err := mgr.StorageIdentity(t.Context()); err == nil {
		t.Fatal("StorageIdentity() succeeded before any HA child was running")
	}
	db1 := newFakeProofDatabase("cloned-database")
	db2 := newFakeProofDatabase("cloned-database")
	first := addStorageProofChild(t, mgr, "v4", db1)
	second := addStorageProofChild(t, mgr, "v5", db2)

	identity, err := mgr.StorageIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if identity.Identity != "cloned-database" || identity.Children != 2 || len(identity.Targets) != 2 {
		t.Fatalf("StorageIdentity() = %+v", identity)
	}

	const nonce = "8aa1c262-ea39-43c2-928c-263e966cc9b4"
	written, err := mgr.StorageChallenge(t.Context(), identity.Snapshot, generationName(first), "write", nonce)
	if err != nil || !written.Found {
		t.Fatalf("write challenge = %+v, %v", written, err)
	}
	seenByWriter, err := mgr.StorageChallenge(t.Context(), identity.Snapshot, generationName(first), "read", nonce)
	if err != nil || !seenByWriter.Found {
		t.Fatalf("writer read challenge = %+v, %v", seenByWriter, err)
	}
	seenByClone, err := mgr.StorageChallenge(t.Context(), identity.Snapshot, generationName(second), "read", nonce)
	if err != nil {
		t.Fatal(err)
	}
	if seenByClone.Found {
		t.Fatal("addressed write was broadcast to an independent cloned database")
	}
}

func TestStorageProofRejectsTransitioningGenerationsAndStaleSnapshot(t *testing.T) {
	mgr := newStorageProofManager()
	current := addStorageProofChild(t, mgr, "v4", newFakeProofDatabase("database-1"))
	identity, err := mgr.StorageIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	candidate := haProofChild("v5", 99)
	for _, state := range []generationState{statusStarting, statusRetiring, statusDraining} {
		candidate.status = state
		mgr.children[candidate] = struct{}{}
		if _, err := mgr.StorageIdentity(t.Context()); err == nil {
			t.Fatalf("StorageIdentity() succeeded while an HA generation was %s", state)
		}
		delete(mgr.children, candidate)
	}
	candidate.status = statusRunning
	mgr.children[candidate] = struct{}{}
	if _, err := mgr.StorageIdentity(t.Context()); err == nil {
		t.Fatal("StorageIdentity() succeeded for an unpublished running candidate")
	}
	delete(mgr.children, candidate)

	current.proofGeneration++
	if _, err := mgr.StorageChallenge(
		t.Context(),
		identity.Snapshot,
		generationName(current),
		"read",
		"8aa1c262-ea39-43c2-928c-263e966cc9b4",
	); err == nil {
		t.Fatal("StorageChallenge() accepted a stale generation snapshot")
	}
}

func TestStorageProofExcludesDeclaredNonHAChildren(t *testing.T) {
	mgr := newStorageProofManager()
	addStorageProofChild(t, mgr, "v5", newFakeProofDatabase("database-1"))
	nonHA := &child{
		version:      oracle.Version{Name: "v4"},
		status:       statusRunning,
		done:         make(chan struct{}),
		haDeployment: boolPointer(false),
	}
	mgr.children[nonHA] = struct{}{}
	mgr.processes["v4"] = nonHA

	proof, err := mgr.StorageIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if proof.Children != 1 || len(proof.Targets) != 1 || proof.Targets[0].Version != "v5" {
		t.Fatalf("StorageIdentity() included non-HA child: %+v", proof)
	}
}

func TestStorageProofFailsClosedForHAChildWithoutProofAPI(t *testing.T) {
	mgr := newStorageProofManager()
	legacy := haProofChild("v5", 1)
	mgr.children[legacy] = struct{}{}
	mgr.processes["v5"] = legacy
	if _, err := mgr.StorageIdentity(t.Context()); err == nil {
		t.Fatal("StorageIdentity() succeeded for HA child without admin storage proof")
	}
}

func TestStorageProofFailsOnChildIdentityMismatch(t *testing.T) {
	mgr := newStorageProofManager()
	addStorageProofChild(t, mgr, "v4", newFakeProofDatabase("database-1"))
	addStorageProofChild(t, mgr, "v5", newFakeProofDatabase("database-2"))
	if _, err := mgr.StorageIdentity(t.Context()); err == nil {
		t.Fatal("StorageIdentity() succeeded with mismatched child storage")
	}
}

func newStorageProofManager() *Manager {
	return NewManager(config.Config{BasePort: 5000, BinaryName: "devshardd"})
}

func addStorageProofChild(t *testing.T, mgr *Manager, version string, db *fakeProofDatabase) *child {
	t.Helper()
	port, shutdown := startLocalHTTPServer(t, http.HandlerFunc(db.handler))
	t.Cleanup(shutdown)
	mgr.nextProofGeneration++
	c := haProofChild(version, mgr.nextProofGeneration)
	c.adminPort.Store(int64(port))
	mgr.children[c] = struct{}{}
	mgr.processes[version] = c
	return c
}

func haProofChild(version string, generation uint64) *child {
	return &child{
		version:         oracle.Version{Name: version},
		storageMode:     storageModePostgres,
		haDeployment:    boolPointer(true),
		proofGeneration: generation,
		status:          statusRunning,
		done:            make(chan struct{}),
	}
}

func generationName(c *child) string {
	return fmt.Sprintf("%d", c.proofGeneration)
}

func boolPointer(value bool) *bool {
	return &value
}
