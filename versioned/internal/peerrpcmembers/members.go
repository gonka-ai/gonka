// Package peerrpcmembers is the membership list the router publishes to
// versiond and versiond forwards to each devshardd child.
package peerrpcmembers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

const (
	// Path is the control endpoint on versiond. It is not a public API:
	// callers must present the control token.
	Path = "/internal/peer-rpc/members"
	// ChildPath is the admin endpoint on devshardd.
	ChildPath = "/internal/peer-rpc/members"
)

// Member is one versiond the router can send traffic to.
type Member struct {
	ID            string   `json:"id"`
	Addr          string   `json:"addr"`
	ReadyVersions []string `json:"ready_versions"`
}

// Snapshot is one router's view. RecipientID is the versiond the post was
// addressed to. Hash covers Members only.
type Snapshot struct {
	RouterID    string   `json:"router_id"`
	RecipientID string   `json:"recipient_id,omitempty"`
	Hash        string   `json:"hash"`
	Members     []Member `json:"members"`
}

// ChildMember is one sibling of this version, already scoped to that version.
type ChildMember struct {
	ID string `json:"id"`
}

// ChildPush is what versiond sends to one child.
type ChildPush struct {
	InstanceID string        `json:"instance_id"`
	Members    []ChildMember `json:"members"`
}

// Target is one local child.
type Target struct {
	Version  string
	AdminURL string
}

// Canonical is the hash input: one line per member, sorted by id,
// "id\taddr\tver1,ver2\n" with versions sorted. The router script emits
// the same bytes.
func Canonical(members []Member) string {
	cp := append([]Member(nil), members...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].ID < cp[j].ID })
	var b strings.Builder
	for _, m := range cp {
		vers := append([]string(nil), m.ReadyVersions...)
		sort.Strings(vers)
		b.WriteString(m.ID)
		b.WriteByte('\t')
		b.WriteString(m.Addr)
		b.WriteByte('\t')
		b.WriteString(strings.Join(vers, ","))
		b.WriteByte('\n')
	}
	return b.String()
}

// Hash is the hex sha256 of Canonical.
func Hash(members []Member) string {
	sum := sha256.Sum256([]byte(Canonical(members)))
	return hex.EncodeToString(sum[:])
}

// Union merges router snapshots. A member is included if any router still
// lists it. Ready versions are the union. Disagreement can only add waiters.
func Union(lists ...[]Member) []Member {
	type acc struct {
		addr string
		vers map[string]struct{}
	}
	byID := map[string]*acc{}
	var order []string
	for _, list := range lists {
		for _, m := range list {
			if m.ID == "" {
				continue
			}
			cur := byID[m.ID]
			if cur == nil {
				cur = &acc{vers: map[string]struct{}{}}
				byID[m.ID] = cur
				order = append(order, m.ID)
			}
			if cur.addr == "" {
				cur.addr = m.Addr
			}
			for _, v := range m.ReadyVersions {
				if v != "" {
					cur.vers[v] = struct{}{}
				}
			}
		}
	}
	out := make([]Member, 0, len(order))
	for _, id := range order {
		cur := byID[id]
		vers := make([]string, 0, len(cur.vers))
		for v := range cur.vers {
			vers = append(vers, v)
		}
		sort.Strings(vers)
		out = append(out, Member{ID: id, Addr: cur.addr, ReadyVersions: vers})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ForChild keeps the members ready for version. Instance ids are
// "<member>@<version>" so two versions on one host do not share a row.
func ForChild(members []Member, version, selfID string) ChildPush {
	var out []ChildMember
	for _, m := range members {
		if m.ID == "" || !contains(m.ReadyVersions, version) {
			continue
		}
		out = append(out, ChildMember{ID: m.ID + "@" + version})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	push := ChildPush{Members: out}
	if selfID != "" {
		push.InstanceID = selfID + "@" + version
	}
	return push
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// Store keeps the latest snapshot from each router.
type Store struct {
	mu        sync.Mutex
	byRouter  map[string]Snapshot
	recipient string
}

// Put records snap and returns the union across routers.
func (s *Store) Put(snap Snapshot) Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byRouter == nil {
		s.byRouter = map[string]Snapshot{}
	}
	id := snap.RouterID
	if id == "" {
		id = "router"
	}
	s.byRouter[id] = snap
	if snap.RecipientID != "" {
		s.recipient = snap.RecipientID
	}
	lists := make([][]Member, 0, len(s.byRouter))
	for _, sn := range s.byRouter {
		lists = append(lists, sn.Members)
	}
	return Snapshot{RecipientID: s.recipient, Members: Union(lists...)}
}

// Recipient is the member id routers last addressed this versiond as.
func (s *Store) Recipient() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recipient
}

// Handler rejects callers that do not present the control token. A matching
// post replaces that router's list and calls forward with the union.
func Handler(token string, store *Store, forward func(Snapshot)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !bearerOK(r, token) {
			http.Error(w, "control access required", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			http.Error(w, "invalid membership", http.StatusBadRequest)
			return
		}
		var snap Snapshot
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&snap); err != nil {
			http.Error(w, "invalid membership", http.StatusBadRequest)
			return
		}
		if snap.Hash == "" || subtle.ConstantTimeCompare([]byte(snap.Hash), []byte(Hash(snap.Members))) != 1 {
			http.Error(w, "membership hash mismatch", http.StatusBadRequest)
			return
		}
		union := store.Put(snap)
		if forward != nil {
			forward(union)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func bearerOK(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	got := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(got) < len(prefix) || subtle.ConstantTimeCompare([]byte(got[:len(prefix)]), []byte(prefix)) != 1 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got[len(prefix):]), []byte(token)) == 1
}

// Forward puts each child's own version slice on its admin listener.
func Forward(ctx context.Context, client *http.Client, members []Member, selfID string, targets []Target) error {
	if client == nil {
		client = http.DefaultClient
	}
	var errs []error
	for _, target := range targets {
		if target.AdminURL == "" || target.Version == "" {
			continue
		}
		push := ForChild(members, target.Version, selfID)
		body, err := json.Marshal(push)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		url := strings.TrimRight(target.AdminURL, "/") + ChildPath
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			errs = append(errs, errors.New(target.Version+": "+resp.Status))
		}
	}
	return errors.Join(errs...)
}
