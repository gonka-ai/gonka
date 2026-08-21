package accounting

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"devshard/types"
)

// Store persists Tracker snapshots. The concrete backend is SQLite and/or
// Postgres depending on DEVSHARD_STORAGE_MODE / PGHOST (see OpenStore).
type Store struct {
	backend   storeBackend
	retention uint64
	path      string // sqlite path (migration source / sqlite mode)
	// saveMu serializes taking a snapshot with writing it. Flush runs from the
	// snapshot ticker and from settle/retire concurrently, and the Postgres
	// backend persists counters as absolute values, so a save that started from
	// an older snapshot must never land after a newer one.
	saveMu sync.Mutex
}

type storeBackend interface {
	Load(ctx context.Context, t *Tracker) error
	// Save persists snap. SQLite replaces the whole DB; Postgres upserts dirty
	// escrows and deletes pruned IDs so concurrent gateway writers do not wipe
	// each other's rows.
	Save(ctx context.Context, snap storeSnapshot, dirtyIDs, deletedIDs []string) error
	Close() error
}

type escrowBlob struct {
	Meta      EscrowMetadata             `json:"meta"`
	Latest    uint64                     `json:"latest"`
	HostStats map[uint32]types.HostStats `json:"host_stats"`
	Counters  []counterBlob              `json:"counters"`
	// The per-nonce sets: replicated facts are persisted by identity so they can
	// be merged by union rather than by arithmetic.
	ProtocolOnly []nonceSlot     `json:"protocol_only,omitempty"`
	Challenges   []challengeBlob `json:"challenges,omitempty"`
	Invalid      []nonceSlot     `json:"invalid,omitempty"`
	// Written by the pre-set layout only; carried forward, never regrown.
	ChallengeBySlot map[uint32]uint64 `json:"challenge_by_slot,omitempty"`
	InvalidBySlot   map[uint32]uint64 `json:"invalid_by_slot,omitempty"`
	InvalidNonces   []uint64          `json:"invalid_nonces,omitempty"`
}

type counterBlob struct {
	Key   CounterKey `json:"key"`
	Count uint64     `json:"count"`
}

// nonceSlot is one member of a per-nonce set, with the slot it was attributed
// to. Invalidations and challenges land on the executor slot from the verdict,
// which is not derivable from the nonce, so the slot is stored with it.
type nonceSlot struct {
	Nonce uint64 `json:"nonce"`
	Slot  uint32 `json:"slot"`
}

type challengeBlob struct {
	Nonce    uint64 `json:"nonce"`
	Slot     uint32 `json:"slot"`
	Resolved bool   `json:"resolved,omitempty"`
}

type storeSnapshot struct {
	UpdatedAt    time.Time
	WriterErrors uint64
	Escrows      []escrowBlob
}

// OpenStore opens the accounting backend selected by DEVSHARD_STORAGE_MODE.
// sqlitePath is the local SQLite file used in sqlite mode and as the migration
// source when opening Postgres.
func OpenStore(sqlitePath string, retention uint64) (*Store, error) {
	return OpenStoreContext(context.Background(), sqlitePath, retention)
}

func (s *Store) Close() error {
	if s == nil || s.backend == nil {
		return nil
	}
	return s.backend.Close()
}

func (s *Store) Load(ctx context.Context, t *Tracker) error {
	if s == nil || s.backend == nil || t == nil {
		return nil
	}
	return s.backend.Load(ctx, t)
}

func (s *Store) Save(ctx context.Context, t *Tracker) error {
	if s == nil || s.backend == nil || t == nil {
		return nil
	}
	// Held across both calls: takePersistSnapshot clears the dirty set, so an
	// interleaved older write would be lost permanently rather than corrected on
	// the next tick.
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	snap, dirtyIDs, deletedIDs := t.takePersistSnapshot(s.retention)
	if err := s.backend.Save(ctx, snap, dirtyIDs, deletedIDs); err != nil {
		t.restorePersistState(dirtyIDs, deletedIDs)
		return err
	}
	return nil
}

func applyLoadedEscrow(t *Tracker, blob escrowBlob) error {
	meta, err := normalizeMetadata(blob.Meta)
	if err != nil {
		return err
	}
	escrow := &escrowState{
		Meta:            meta,
		Latest:          blob.Latest,
		HostStats:       make(map[uint32]types.HostStats, len(blob.HostStats)),
		Counters:        make(map[CounterKey]uint64, len(blob.Counters)),
		ProtocolOnly:    make(map[uint64]uint32, len(blob.ProtocolOnly)),
		Challenge:       make(map[uint64]challengeRecord, len(blob.Challenges)),
		Invalid:         make(map[uint64]uint32, len(blob.Invalid)),
		ChallengeBySlot: blob.ChallengeBySlot,
		InvalidBySlot:   blob.InvalidBySlot,
		Live:            make(map[uint64]*nonceState),
	}
	for _, entry := range blob.ProtocolOnly {
		escrow.ProtocolOnly[entry.Nonce] = entry.Slot
	}
	for _, entry := range blob.Challenges {
		escrow.Challenge[entry.Nonce] = challengeRecord{Slot: entry.Slot, Resolved: entry.Resolved}
	}
	for _, entry := range blob.Invalid {
		escrow.Invalid[entry.Nonce] = entry.Slot
	}
	// Pre-set invalid nonces are kept for deduplication only: their counts live in
	// InvalidBySlot, so promoting them into the set would count them twice.
	if len(blob.InvalidNonces) > 0 {
		escrow.InvalidLegacy = make(map[uint64]struct{}, len(blob.InvalidNonces))
		for _, nonce := range blob.InvalidNonces {
			escrow.InvalidLegacy[nonce] = struct{}{}
		}
	}
	for slot, stats := range blob.HostStats {
		escrow.HostStats[slot] = stats
	}
	for _, counter := range blob.Counters {
		if counter.Count > 0 {
			escrow.Counters[counter.Key] += counter.Count
		}
	}
	t.escrows[meta.EscrowID] = escrow
	return nil
}

func applyLoadedMeta(t *Tracker, key, value string) {
	switch key {
	case "updated_at":
		if updated, err := time.Parse(time.RFC3339Nano, value); err == nil {
			t.updated = updated
		}
	case "writer_errors":
		if count, err := strconv.ParseUint(value, 10, 64); err == nil {
			t.wrCount = count
		}
	}
}

func blobFromEscrow(escrow *escrowState) escrowBlob {
	blob := escrowBlob{
		Meta:            escrow.Meta,
		Latest:          escrow.Latest,
		HostStats:       make(map[uint32]types.HostStats, len(escrow.HostStats)),
		ProtocolOnly:    sortedNonceSlots(escrow.ProtocolOnly),
		Invalid:         sortedNonceSlots(escrow.Invalid),
		Challenges:      sortedChallenges(escrow.Challenge),
		ChallengeBySlot: copyUint32Map(escrow.ChallengeBySlot),
		InvalidBySlot:   copyUint32Map(escrow.InvalidBySlot),
		InvalidNonces:   sortedNonces(escrow.InvalidLegacy),
	}
	for slot, stats := range escrow.HostStats {
		blob.HostStats[slot] = stats
	}
	for _, key := range sortedCounterKeys(escrow.Counters) {
		blob.Counters = append(blob.Counters, counterBlob{Key: key, Count: escrow.Counters[key]})
	}
	return blob
}

func encodeEscrowBlob(blob escrowBlob) ([]byte, error) {
	return json.Marshal(blob)
}

func decodeEscrowBlob(raw []byte) (escrowBlob, error) {
	var blob escrowBlob
	if err := json.Unmarshal(raw, &blob); err != nil {
		return escrowBlob{}, err
	}
	return blob, nil
}

func copyUint32Map(in map[uint32]uint64) map[uint32]uint64 {
	out := make(map[uint32]uint64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sortedKeys(in map[string]struct{}) []string {
	out := make([]string, 0, len(in))
	for key := range in {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sortedNonceSlots(in map[uint64]uint32) []nonceSlot {
	if len(in) == 0 {
		return nil
	}
	out := make([]nonceSlot, 0, len(in))
	for nonce, slot := range in {
		out = append(out, nonceSlot{Nonce: nonce, Slot: slot})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Nonce < out[j].Nonce })
	return out
}

func sortedChallenges(in map[uint64]challengeRecord) []challengeBlob {
	if len(in) == 0 {
		return nil
	}
	out := make([]challengeBlob, 0, len(in))
	for nonce, rec := range in {
		out = append(out, challengeBlob{Nonce: nonce, Slot: rec.Slot, Resolved: rec.Resolved})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Nonce < out[j].Nonce })
	return out
}

func sortedNonces(in map[uint64]struct{}) []uint64 {
	if len(in) == 0 {
		return nil
	}
	out := make([]uint64, 0, len(in))
	for nonce := range in {
		out = append(out, nonce)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func requirePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("accounting database path is required")
	}
	return nil
}

func schemaVersionMeta() (string, string) {
	return "schema_version", strconv.Itoa(SchemaVersion)
}
