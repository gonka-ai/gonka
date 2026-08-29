package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// StorageProof is the aggregate answer from every running devshard child on
// this versiond host.
type StorageProof struct {
	Identity string `json:"identity"`
	Found    bool   `json:"found,omitempty"`
	Children int    `json:"children"`
}

type childStorageProof struct {
	Identity string `json:"identity"`
	Found    bool   `json:"found,omitempty"`
}

type storageChallengeRequest struct {
	Operation string `json:"operation"`
	Nonce     string `json:"nonce"`
}

const childStorageProofTimeout = 3 * time.Second

// StorageIdentity reads the lineage marker through every running child's
// application storage. Equal identities are useful for detecting accidental
// database replacement, but callers must also run a two-way live challenge to
// prove that hosts share one writable PostgreSQL.
func (m *Manager) StorageIdentity(ctx context.Context) (StorageProof, error) {
	return m.aggregateChildStorageProof(ctx, http.MethodGet, "/storage/identity", nil)
}

// StorageChallenge executes a write or read through every running child's
// application storage pool.
func (m *Manager) StorageChallenge(ctx context.Context, operation, nonce string) (StorageProof, error) {
	if operation != "write" && operation != "read" {
		return StorageProof{}, fmt.Errorf("invalid storage challenge operation %q", operation)
	}
	body, err := json.Marshal(storageChallengeRequest{Operation: operation, Nonce: nonce})
	if err != nil {
		return StorageProof{}, err
	}
	return m.aggregateChildStorageProof(ctx, http.MethodPost, "/storage/challenge", body)
}

func (m *Manager) runningStorageChildren() []*child {
	m.mu.Lock()
	defer m.mu.Unlock()
	children := make([]*child, 0, len(m.processes))
	for _, c := range m.processes {
		if c != nil && c.status == statusRunning && !childDone(c) {
			children = append(children, c)
		}
	}
	sort.Slice(children, func(i, j int) bool { return children[i].version.Name < children[j].version.Name })
	return children
}

func (m *Manager) aggregateChildStorageProof(
	ctx context.Context,
	method string,
	path string,
	body []byte,
) (StorageProof, error) {
	children := m.runningStorageChildren()
	if len(children) == 0 {
		return StorageProof{}, errors.New("no running devshard children")
	}

	type result struct {
		version string
		proof   childStorageProof
		err     error
	}
	results := make(chan result, len(children))
	var wg sync.WaitGroup
	for _, c := range children {
		wg.Add(1)
		go func(c *child) {
			defer wg.Done()
			proof, err := requestChildStorageProof(ctx, c, method, path, body)
			results <- result{version: c.version.Name, proof: proof, err: err}
		}(c)
	}
	wg.Wait()
	close(results)

	aggregate := StorageProof{Found: true, Children: len(children)}
	var errs []error
	for result := range results {
		if result.err != nil {
			errs = append(errs, fmt.Errorf("child %s: %w", result.version, result.err))
			continue
		}
		if result.proof.Identity == "" {
			errs = append(errs, fmt.Errorf("child %s: empty storage identity", result.version))
			continue
		}
		if aggregate.Identity == "" {
			aggregate.Identity = result.proof.Identity
		} else if aggregate.Identity != result.proof.Identity {
			errs = append(errs, fmt.Errorf("child %s: storage identity mismatch", result.version))
		}
		aggregate.Found = aggregate.Found && result.proof.Found
	}
	if err := errors.Join(errs...); err != nil {
		return StorageProof{}, err
	}
	return aggregate, nil
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
		return childStorageProof{}, fmt.Errorf("storage proof returned %s: %s", resp.Status, bytes.TrimSpace(message))
	}
	var proof childStorageProof
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&proof); err != nil {
		return childStorageProof{}, fmt.Errorf("decode storage proof: %w", err)
	}
	return proof, nil
}
