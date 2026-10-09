package process

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type StorageProofTarget struct {
	Generation                string `json:"generation"`
	Version                   string `json:"version"`
	PoolMaxConnections        int32  `json:"pool_max_connections"`
	ServerMaxConnections      int32  `json:"server_max_connections"`
	ServerReservedConnections int32  `json:"server_reserved_connections"`
}

// StorageProof carries a stable generation snapshot and either the aggregate
// lineage identity or one addressed generation's challenge result.
type StorageProof struct {
	Identity   string               `json:"identity"`
	Found      bool                 `json:"found,omitempty"`
	Children   int                  `json:"children"`
	Snapshot   string               `json:"snapshot"`
	Generation string               `json:"generation,omitempty"`
	Targets    []StorageProofTarget `json:"targets,omitempty"`
}

type childStorageProof struct {
	Identity                  string `json:"identity"`
	Found                     bool   `json:"found,omitempty"`
	PoolMaxConnections        int32  `json:"pool_max_connections"`
	ServerMaxConnections      int32  `json:"server_max_connections"`
	ServerReservedConnections int32  `json:"server_reserved_connections"`
}

type storageChallengeRequest struct {
	Operation string `json:"operation"`
	Nonce     string `json:"nonce"`
}

type storageProofSnapshot struct {
	token   string
	targets []storageProofSnapshotTarget
}

type storageProofSnapshotTarget struct {
	generation string
	version    string
	child      *child
}

type storageProofResult struct {
	target storageProofSnapshotTarget
	proof  childStorageProof
	err    error
}

const childStorageProofTimeout = 3 * time.Second

// StorageIdentity reads the lineage marker through every stable HA child. The
// returned target list is the address book for per-generation challenges.
func (m *Manager) StorageIdentity(ctx context.Context) (StorageProof, error) {
	snapshot, err := m.storageProofSnapshot()
	if err != nil {
		return StorageProof{}, err
	}

	results := make(chan storageProofResult, len(snapshot.targets))
	for _, target := range snapshot.targets {
		go func(target storageProofSnapshotTarget) {
			proof, requestErr := requestChildStorageProof(ctx, target.child, http.MethodGet, "/storage/identity", nil)
			results <- storageProofResult{target: target, proof: proof, err: requestErr}
		}(target)
	}

	proof := StorageProof{
		Children: len(snapshot.targets),
		Snapshot: snapshot.token,
		Targets:  make([]StorageProofTarget, 0, len(snapshot.targets)),
	}
	var errs []error
	for range snapshot.targets {
		result := <-results
		if result.err != nil {
			errs = append(errs, fmt.Errorf("child %s: %w", result.target.version, result.err))
			continue
		}
		if result.proof.Identity == "" {
			errs = append(errs, fmt.Errorf("child %s: empty storage identity", result.target.version))
			continue
		}
		if result.proof.PoolMaxConnections <= 0 {
			errs = append(errs, fmt.Errorf("child %s: invalid PostgreSQL pool capacity", result.target.version))
			continue
		}
		if result.proof.ServerMaxConnections <= result.proof.ServerReservedConnections {
			errs = append(errs, fmt.Errorf("child %s: invalid PostgreSQL server capacity", result.target.version))
			continue
		}
		if proof.Identity == "" {
			proof.Identity = result.proof.Identity
		} else if proof.Identity != result.proof.Identity {
			errs = append(errs, fmt.Errorf("child %s: storage identity mismatch", result.target.version))
		}
		proof.Targets = append(proof.Targets, StorageProofTarget{
			Generation:                result.target.generation,
			Version:                   result.target.version,
			PoolMaxConnections:        result.proof.PoolMaxConnections,
			ServerMaxConnections:      result.proof.ServerMaxConnections,
			ServerReservedConnections: result.proof.ServerReservedConnections,
		})
	}
	if err := errors.Join(errs...); err != nil {
		return StorageProof{}, err
	}
	sort.Slice(proof.Targets, func(i, j int) bool { return proof.Targets[i].Generation < proof.Targets[j].Generation })
	if err := m.verifyStorageProofSnapshot(snapshot.token); err != nil {
		return StorageProof{}, err
	}
	return proof, nil
}

// StorageChallenge addresses exactly one child generation. Deployment tooling
// repeats this for every writer and asks every target to read that writer's
// unique nonce.
func (m *Manager) StorageChallenge(
	ctx context.Context,
	snapshotToken string,
	generation string,
	operation string,
	nonce string,
) (StorageProof, error) {
	if operation != "write" && operation != "read" {
		return StorageProof{}, fmt.Errorf("invalid storage challenge operation %q", operation)
	}
	snapshot, err := m.storageProofSnapshot()
	if err != nil {
		return StorageProof{}, err
	}
	if snapshotToken == "" || snapshot.token != snapshotToken {
		return StorageProof{}, errors.New("storage proof generation snapshot changed")
	}
	var selected *storageProofSnapshotTarget
	for i := range snapshot.targets {
		if snapshot.targets[i].generation == generation {
			selected = &snapshot.targets[i]
			break
		}
	}
	if selected == nil {
		return StorageProof{}, fmt.Errorf("unknown storage proof generation %q", generation)
	}

	body, err := json.Marshal(storageChallengeRequest{Operation: operation, Nonce: nonce})
	if err != nil {
		return StorageProof{}, err
	}
	childProof, err := requestChildStorageProof(ctx, selected.child, http.MethodPost, "/storage/challenge", body)
	if err != nil {
		return StorageProof{}, fmt.Errorf("child %s: %w", selected.version, err)
	}
	if err := m.verifyStorageProofSnapshot(snapshot.token); err != nil {
		return StorageProof{}, err
	}
	return StorageProof{
		Identity:   childProof.Identity,
		Found:      childProof.Found,
		Children:   len(snapshot.targets),
		Snapshot:   snapshot.token,
		Generation: selected.generation,
	}, nil
}

func (m *Manager) storageProofSnapshot() (storageProofSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.devshardAdminEligible() {
		return storageProofSnapshot{}, errors.New("storage proof is available only for devshard children")
	}

	targets := make([]storageProofSnapshotTarget, 0, len(m.children))
	for c := range m.children {
		if childDone(c) {
			continue
		}
		if c.haDeployment == nil {
			return storageProofSnapshot{}, fmt.Errorf("child %s has not completed HA preflight", c.version.Name)
		}
		if !*c.haDeployment {
			continue
		}
		if c.status != statusRunning {
			return storageProofSnapshot{}, fmt.Errorf("HA child %s is %s", c.version.Name, c.status)
		}
		if current := m.processes[c.version.Name]; current != c {
			return storageProofSnapshot{}, fmt.Errorf("HA child %s is not the current routed generation", c.version.Name)
		}
		if c.storageMode != storageModePostgres {
			return storageProofSnapshot{}, fmt.Errorf("HA child %s storage mode is %q", c.version.Name, c.storageMode)
		}
		if c.adminAddr() == "" || c.proofGeneration == 0 {
			return storageProofSnapshot{}, fmt.Errorf("HA child %s has no storage proof generation", c.version.Name)
		}
		targets = append(targets, storageProofSnapshotTarget{
			generation: strconv.FormatUint(c.proofGeneration, 10),
			version:    c.version.Name,
			child:      c,
		})
	}
	if len(targets) == 0 {
		return storageProofSnapshot{}, errors.New("no stable HA devshard children")
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].generation == targets[j].generation {
			return targets[i].version < targets[j].version
		}
		return targets[i].generation < targets[j].generation
	})

	hash := sha256.New()
	_, _ = io.WriteString(hash, m.proofEpoch)
	for _, target := range targets {
		_, _ = io.WriteString(hash, "\x00"+target.generation+"\x00"+target.version)
	}
	return storageProofSnapshot{
		token:   hex.EncodeToString(hash.Sum(nil)),
		targets: targets,
	}, nil
}

func (m *Manager) verifyStorageProofSnapshot(expected string) error {
	current, err := m.storageProofSnapshot()
	if err != nil {
		return err
	}
	if current.token != expected {
		return errors.New("storage proof generation snapshot changed")
	}
	return nil
}

func requestChildStorageProof(
	ctx context.Context,
	c *child,
	method string,
	path string,
	body []byte,
) (childStorageProof, error) {
	addr := c.adminAddr()
	if addr == "" {
		return childStorageProof{}, errors.New("child has no storage-proof admin API")
	}
	requestCtx, cancel := context.WithTimeout(ctx, childStorageProofTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, "http://"+addr+path, bytes.NewReader(body))
	if err != nil {
		return childStorageProof{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return childStorageProof{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return childStorageProof{}, fmt.Errorf("storage proof returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	var proof childStorageProof
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&proof); err != nil {
		return childStorageProof{}, fmt.Errorf("decode storage proof: %w", err)
	}
	return proof, nil
}
