package accounting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"

	"devshard/types"
)

// The Postgres ledger stores one escrow as a set of rows instead of a single
// JSON blob, so two gateways writing the same escrow merge instead of
// overwriting each other. Every field has an explicit merge rule:
//
//   - counters and per-slot challenge/invalid counts are additive: each writer
//     owns its own rows (escrow_id, writer_id, ...) holding its own contribution,
//     and a reader sums across writers. A writer only ever rewrites its own rows,
//     so a retry is idempotent and a peer's counts are never touched.
//   - host stats mirror absolute on-chain per-host numbers (the tracker merges
//     them with max, see maxHostStats), so they are shared rows merged with
//     GREATEST.
//   - latest_nonce is a watermark merged with GREATEST; phase is monotonic and
//     merged by rank.
//   - invalid nonces are a set, merged by union (INSERT ... ON CONFLICT DO NOTHING).
//   - escrow metadata (epoch, model, slots, timeouts) is identity written at
//     registration; RegisterEscrow rejects conflicting metadata, so writers agree
//     and last-write is harmless.
//
// A writer's own contribution is derived at flush time as
// (in-memory total) - (peer contribution observed at Load), clamped at zero.
// Peer counts already folded into the in-memory total therefore stay attributed
// to the peer, and a writer never publishes a number it did not observe.

const (
	// accountingWriterIDEnv names this process's ledger rows. A stable value
	// (pod name, host) keeps one row set per instance across restarts; an
	// unstable one only leaves behind stale rows, it cannot double count,
	// because rows from earlier incarnations are read as peer contributions.
	accountingWriterIDEnv = "DEVSHARD_ACCOUNTING_WRITER_ID"
	// accountingLegacyWriterID owns rows imported from the pre-row blob layout.
	// They are a frozen historical contribution nobody rewrites.
	accountingLegacyWriterID  = "_legacy_blob"
	defaultAccountingWriterID = "default"
)

// accountingWriterID resolves this process's writer identity.
func accountingWriterID() string {
	if id := strings.TrimSpace(os.Getenv(accountingWriterIDEnv)); id != "" {
		return id
	}
	if host, err := os.Hostname(); err == nil {
		if host = strings.TrimSpace(host); host != "" {
			return host
		}
	}
	return defaultAccountingWriterID
}

// escrowContribution is the additive part of one escrow's state: the numbers a
// single writer claims. Sums across writers reconstruct the escrow total.
type escrowContribution struct {
	counters map[CounterKey]uint64
	slots    map[uint32]slotCounts
}

// slotCounts are the per-slot additive counts: unresolved challenges and
// invalidated inferences.
type slotCounts struct {
	openChallenges uint64
	invalidNonces  uint64
}

func newEscrowContribution() *escrowContribution {
	return &escrowContribution{
		counters: make(map[CounterKey]uint64),
		slots:    make(map[uint32]slotCounts),
	}
}

func (c *escrowContribution) addCounter(key CounterKey, count uint64) {
	if count == 0 {
		return
	}
	c.counters[key] += count
}

func (c *escrowContribution) addSlot(slot uint32, openChallenges, invalidNonces uint64) {
	if openChallenges == 0 && invalidNonces == 0 {
		return
	}
	current := c.slots[slot]
	current.openChallenges += openChallenges
	current.invalidNonces += invalidNonces
	c.slots[slot] = current
}

func (c *escrowContribution) empty() bool {
	return c == nil || (len(c.counters) == 0 && len(c.slots) == 0)
}

// minus returns the caller's own share of a total: the total minus what peers
// already published. Saturating at zero keeps a local decrement (a counter
// re-keyed by reclassification) from cancelling a peer's rows.
func (c *escrowContribution) minus(peer *escrowContribution) *escrowContribution {
	out := newEscrowContribution()
	if c == nil {
		return out
	}
	for key, count := range c.counters {
		var peerCount uint64
		if peer != nil {
			peerCount = peer.counters[key]
		}
		if mine := saturatingSub(count, peerCount); mine > 0 {
			out.counters[key] = mine
		}
	}
	for slot, counts := range c.slots {
		var peerCounts slotCounts
		if peer != nil {
			peerCounts = peer.slots[slot]
		}
		mine := slotCounts{
			openChallenges: saturatingSub(counts.openChallenges, peerCounts.openChallenges),
			invalidNonces:  saturatingSub(counts.invalidNonces, peerCounts.invalidNonces),
		}
		if mine.openChallenges > 0 || mine.invalidNonces > 0 {
			out.slots[slot] = mine
		}
	}
	return out
}

func saturatingSub(a, b uint64) uint64 {
	if a <= b {
		return 0
	}
	return a - b
}

// contributionFromBlob reads the additive fields out of a snapshot blob.
func contributionFromBlob(blob escrowBlob) *escrowContribution {
	out := newEscrowContribution()
	for _, counter := range blob.Counters {
		out.addCounter(counter.Key, counter.Count)
	}
	for slot, count := range blob.ChallengeBySlot {
		out.addSlot(slot, count, 0)
	}
	for slot, count := range blob.InvalidBySlot {
		out.addSlot(slot, 0, count)
	}
	return out
}

// encodeCounterKey renders a counter key as canonical JSON (Go marshals struct
// fields in declaration order) plus a hash of it. The hash is the index key, so
// a long DetailReason cannot overflow the btree entry, while the JSON stays
// readable and decodable.
func encodeCounterKey(key CounterKey) (id string, payload []byte, err error) {
	payload, err = json.Marshal(key)
	if err != nil {
		return "", nil, fmt.Errorf("encode counter key: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), payload, nil
}

func decodeCounterKey(payload []byte) (CounterKey, error) {
	var key CounterKey
	if err := json.Unmarshal(payload, &key); err != nil {
		return CounterKey{}, fmt.Errorf("decode counter key: %w", err)
	}
	return key, nil
}

const accountingRowSchema = `
CREATE TABLE IF NOT EXISTS accounting_escrow_state (
	escrow_id TEXT PRIMARY KEY,
	creation_epoch BIGINT NOT NULL,
	model TEXT NOT NULL,
	meta_json BYTEA NOT NULL,
	phase TEXT NOT NULL,
	phase_rank INT NOT NULL,
	latest_nonce BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS accounting_escrow_state_epoch_model_idx
	ON accounting_escrow_state(creation_epoch, model);
CREATE TABLE IF NOT EXISTS accounting_escrow_counters (
	escrow_id TEXT NOT NULL,
	writer_id TEXT NOT NULL,
	counter_id TEXT NOT NULL,
	counter_key BYTEA NOT NULL,
	count BIGINT NOT NULL,
	PRIMARY KEY (escrow_id, writer_id, counter_id)
);
CREATE TABLE IF NOT EXISTS accounting_escrow_slot_counts (
	escrow_id TEXT NOT NULL,
	writer_id TEXT NOT NULL,
	slot_id BIGINT NOT NULL,
	open_challenges BIGINT NOT NULL,
	invalid_nonces BIGINT NOT NULL,
	PRIMARY KEY (escrow_id, writer_id, slot_id)
);
CREATE TABLE IF NOT EXISTS accounting_escrow_host_stats (
	escrow_id TEXT NOT NULL,
	slot_id BIGINT NOT NULL,
	missed BIGINT NOT NULL,
	invalid BIGINT NOT NULL,
	cost BIGINT NOT NULL,
	required_validations BIGINT NOT NULL,
	completed_validations BIGINT NOT NULL,
	PRIMARY KEY (escrow_id, slot_id)
);
CREATE TABLE IF NOT EXISTS accounting_escrow_invalid_nonces (
	escrow_id TEXT NOT NULL,
	nonce BIGINT NOT NULL,
	PRIMARY KEY (escrow_id, nonce)
);
CREATE TABLE IF NOT EXISTS accounting_writers (
	writer_id TEXT PRIMARY KEY,
	updated_at TEXT NOT NULL,
	writer_errors BIGINT NOT NULL
);
`

// loadedLedger is the merged read side: escrow totals plus, per escrow, the
// share peers own, which the next flush subtracts from its own totals.
type loadedLedger struct {
	blobs map[string]*escrowBlob
	peers map[string]*escrowContribution
}

func newLoadedLedger() *loadedLedger {
	return &loadedLedger{
		blobs: make(map[string]*escrowBlob),
		peers: make(map[string]*escrowContribution),
	}
}

func (l *loadedLedger) blob(escrowID string) *escrowBlob {
	blob := l.blobs[escrowID]
	if blob == nil {
		blob = &escrowBlob{
			HostStats:       make(map[uint32]types.HostStats),
			ChallengeBySlot: make(map[uint32]uint64),
			InvalidBySlot:   make(map[uint32]uint64),
		}
		blob.Meta.EscrowID = escrowID
		l.blobs[escrowID] = blob
	}
	return blob
}

func (l *loadedLedger) peer(escrowID string) *escrowContribution {
	peer := l.peers[escrowID]
	if peer == nil {
		peer = newEscrowContribution()
		l.peers[escrowID] = peer
	}
	return peer
}

// readLedger reconstructs every escrow from its rows: totals summed across
// writers for the additive fields, plus the peer-only share of those sums.
func readLedger(ctx context.Context, q pgxQuerier, writerID string) (*loadedLedger, error) {
	out := newLoadedLedger()

	stateRows, err := q.Query(ctx, `
		SELECT escrow_id, creation_epoch, model, meta_json, phase, latest_nonce
		FROM accounting_escrow_state`)
	if err != nil {
		return nil, fmt.Errorf("load accounting escrow state: %w", err)
	}
	for stateRows.Next() {
		var (
			escrowID, model, phase string
			creationEpoch, latest  int64
			metaJSON               []byte
		)
		if err := stateRows.Scan(&escrowID, &creationEpoch, &model, &metaJSON, &phase, &latest); err != nil {
			stateRows.Close()
			return nil, err
		}
		var meta EscrowMetadata
		if err := json.Unmarshal(metaJSON, &meta); err != nil {
			stateRows.Close()
			return nil, fmt.Errorf("decode accounting metadata for %s: %w", escrowID, err)
		}
		meta.EscrowID = escrowID
		meta.CreationEpoch = uint64(creationEpoch)
		meta.Model = model
		// phase and latest_nonce are the merged monotonic columns; the copies
		// inside meta_json may be from an older write.
		meta.Phase = EscrowPhase(phase)
		blob := out.blob(escrowID)
		blob.Meta = meta
		blob.Latest = uint64(latest)
	}
	if err := stateRows.Err(); err != nil {
		stateRows.Close()
		return nil, err
	}
	stateRows.Close()

	counterRows, err := q.Query(ctx, `
		SELECT escrow_id, writer_id, counter_key, count
		FROM accounting_escrow_counters`)
	if err != nil {
		return nil, fmt.Errorf("load accounting counters: %w", err)
	}
	for counterRows.Next() {
		var (
			escrowID, rowWriter string
			payload             []byte
			count               int64
		)
		if err := counterRows.Scan(&escrowID, &rowWriter, &payload, &count); err != nil {
			counterRows.Close()
			return nil, err
		}
		if count <= 0 {
			continue
		}
		key, err := decodeCounterKey(payload)
		if err != nil {
			counterRows.Close()
			return nil, err
		}
		blob := out.blob(escrowID)
		blob.Counters = append(blob.Counters, counterBlob{Key: key, Count: uint64(count)})
		if rowWriter != writerID {
			out.peer(escrowID).addCounter(key, uint64(count))
		}
	}
	if err := counterRows.Err(); err != nil {
		counterRows.Close()
		return nil, err
	}
	counterRows.Close()

	slotRows, err := q.Query(ctx, `
		SELECT escrow_id, writer_id, slot_id, open_challenges, invalid_nonces
		FROM accounting_escrow_slot_counts`)
	if err != nil {
		return nil, fmt.Errorf("load accounting slot counts: %w", err)
	}
	for slotRows.Next() {
		var (
			escrowID, rowWriter   string
			slotID, open, invalid int64
		)
		if err := slotRows.Scan(&escrowID, &rowWriter, &slotID, &open, &invalid); err != nil {
			slotRows.Close()
			return nil, err
		}
		slot := uint32(slotID)
		blob := out.blob(escrowID)
		if open > 0 {
			blob.ChallengeBySlot[slot] += uint64(open)
		}
		if invalid > 0 {
			blob.InvalidBySlot[slot] += uint64(invalid)
		}
		if rowWriter != writerID {
			out.peer(escrowID).addSlot(slot, uint64(max(open, 0)), uint64(max(invalid, 0)))
		}
	}
	if err := slotRows.Err(); err != nil {
		slotRows.Close()
		return nil, err
	}
	slotRows.Close()

	hostRows, err := q.Query(ctx, `
		SELECT escrow_id, slot_id, missed, invalid, cost, required_validations, completed_validations
		FROM accounting_escrow_host_stats`)
	if err != nil {
		return nil, fmt.Errorf("load accounting host stats: %w", err)
	}
	for hostRows.Next() {
		var (
			escrowID                                           string
			slotID, missed, invalid, cost, required, completed int64
		)
		if err := hostRows.Scan(&escrowID, &slotID, &missed, &invalid, &cost, &required, &completed); err != nil {
			hostRows.Close()
			return nil, err
		}
		out.blob(escrowID).HostStats[uint32(slotID)] = types.HostStats{
			Missed:               uint32(missed),
			Invalid:              uint32(invalid),
			Cost:                 uint64(cost),
			RequiredValidations:  uint32(required),
			CompletedValidations: uint32(completed),
		}
	}
	if err := hostRows.Err(); err != nil {
		hostRows.Close()
		return nil, err
	}
	hostRows.Close()

	nonceRows, err := q.Query(ctx, `SELECT escrow_id, nonce FROM accounting_escrow_invalid_nonces`)
	if err != nil {
		return nil, fmt.Errorf("load accounting invalid nonces: %w", err)
	}
	for nonceRows.Next() {
		var (
			escrowID string
			nonce    int64
		)
		if err := nonceRows.Scan(&escrowID, &nonce); err != nil {
			nonceRows.Close()
			return nil, err
		}
		blob := out.blob(escrowID)
		blob.InvalidNonces = append(blob.InvalidNonces, uint64(nonce))
	}
	if err := nonceRows.Err(); err != nil {
		nonceRows.Close()
		return nil, err
	}
	nonceRows.Close()

	for id, peer := range out.peers {
		if peer.empty() {
			delete(out.peers, id)
		}
	}
	return out, nil
}

// writeEscrowRows publishes one escrow. Shared fields are merged in SQL
// (GREATEST / rank / union); the additive fields are written as this writer's
// own absolute contribution, which makes a retry a no-op.
//
// The statements go out as one batch so an escrow still costs a single round
// trip, as it did when it was one blob upsert.
func writeEscrowRows(ctx context.Context, tx pgx.Tx, writerID string, blob escrowBlob, mine *escrowContribution) error {
	escrowID := blob.Meta.EscrowID
	batch := &pgx.Batch{}
	metaJSON, err := json.Marshal(blob.Meta)
	if err != nil {
		return fmt.Errorf("encode accounting metadata for %s: %w", escrowID, err)
	}
	batch.Queue(`
		INSERT INTO accounting_escrow_state (
			escrow_id, creation_epoch, model, meta_json, phase, phase_rank, latest_nonce
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (escrow_id) DO UPDATE SET
			creation_epoch = EXCLUDED.creation_epoch,
			model = EXCLUDED.model,
			meta_json = EXCLUDED.meta_json,
			phase = CASE
				WHEN EXCLUDED.phase_rank >= accounting_escrow_state.phase_rank THEN EXCLUDED.phase
				ELSE accounting_escrow_state.phase END,
			phase_rank = GREATEST(accounting_escrow_state.phase_rank, EXCLUDED.phase_rank),
			latest_nonce = GREATEST(accounting_escrow_state.latest_nonce, EXCLUDED.latest_nonce)`,
		escrowID, int64(blob.Meta.CreationEpoch), blob.Meta.Model, metaJSON,
		string(blob.Meta.Phase), phaseRank(blob.Meta.Phase), int64(blob.Latest),
	)

	counterIDs := make([]string, 0, len(mine.counters))
	for _, key := range sortedCounterKeys(mine.counters) {
		id, payload, err := encodeCounterKey(key)
		if err != nil {
			return err
		}
		counterIDs = append(counterIDs, id)
		batch.Queue(`
			INSERT INTO accounting_escrow_counters (escrow_id, writer_id, counter_id, counter_key, count)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (escrow_id, writer_id, counter_id) DO UPDATE SET
				counter_key = EXCLUDED.counter_key,
				count = EXCLUDED.count`,
			escrowID, writerID, id, payload, int64(mine.counters[key]),
		)
	}
	// Counter keys this writer no longer holds (a nonce reclassified into a
	// different key) are dropped, but only from this writer's own rows.
	batch.Queue(`
		DELETE FROM accounting_escrow_counters
		WHERE escrow_id = $1 AND writer_id = $2 AND counter_id <> ALL($3)`,
		escrowID, writerID, counterIDs,
	)

	slotIDs := make([]int64, 0, len(mine.slots))
	for slot, counts := range mine.slots {
		slotIDs = append(slotIDs, int64(slot))
		batch.Queue(`
			INSERT INTO accounting_escrow_slot_counts (escrow_id, writer_id, slot_id, open_challenges, invalid_nonces)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (escrow_id, writer_id, slot_id) DO UPDATE SET
				open_challenges = EXCLUDED.open_challenges,
				invalid_nonces = EXCLUDED.invalid_nonces`,
			escrowID, writerID, int64(slot), int64(counts.openChallenges), int64(counts.invalidNonces),
		)
	}
	batch.Queue(`
		DELETE FROM accounting_escrow_slot_counts
		WHERE escrow_id = $1 AND writer_id = $2 AND slot_id <> ALL($3)`,
		escrowID, writerID, slotIDs,
	)

	for slot, stats := range blob.HostStats {
		batch.Queue(`
			INSERT INTO accounting_escrow_host_stats (
				escrow_id, slot_id, missed, invalid, cost, required_validations, completed_validations
			) VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (escrow_id, slot_id) DO UPDATE SET
				missed = GREATEST(accounting_escrow_host_stats.missed, EXCLUDED.missed),
				invalid = GREATEST(accounting_escrow_host_stats.invalid, EXCLUDED.invalid),
				cost = GREATEST(accounting_escrow_host_stats.cost, EXCLUDED.cost),
				required_validations = GREATEST(accounting_escrow_host_stats.required_validations, EXCLUDED.required_validations),
				completed_validations = GREATEST(accounting_escrow_host_stats.completed_validations, EXCLUDED.completed_validations)`,
			escrowID, int64(slot), int64(stats.Missed), int64(stats.Invalid), int64(stats.Cost),
			int64(stats.RequiredValidations), int64(stats.CompletedValidations),
		)
	}

	if len(blob.InvalidNonces) > 0 {
		nonces := make([]int64, 0, len(blob.InvalidNonces))
		for _, nonce := range blob.InvalidNonces {
			nonces = append(nonces, int64(nonce))
		}
		batch.Queue(`
			INSERT INTO accounting_escrow_invalid_nonces (escrow_id, nonce)
			SELECT $1, unnest($2::bigint[])
			ON CONFLICT (escrow_id, nonce) DO NOTHING`,
			escrowID, nonces,
		)
	}

	results := tx.SendBatch(ctx, batch)
	for i := 0; i < batch.Len(); i++ {
		if _, err := results.Exec(); err != nil {
			results.Close()
			return fmt.Errorf("write accounting rows for %s: %w", escrowID, err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("write accounting rows for %s: %w", escrowID, err)
	}
	return nil
}

// deleteEscrowRows removes pruned escrows, including peer writers' rows: a
// retention prune drops the escrow from the ledger entirely.
func deleteEscrowRows(ctx context.Context, tx pgx.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, stmt := range []string{
		`DELETE FROM accounting_escrow_counters WHERE escrow_id = ANY($1)`,
		`DELETE FROM accounting_escrow_slot_counts WHERE escrow_id = ANY($1)`,
		`DELETE FROM accounting_escrow_host_stats WHERE escrow_id = ANY($1)`,
		`DELETE FROM accounting_escrow_invalid_nonces WHERE escrow_id = ANY($1)`,
		`DELETE FROM accounting_escrow_state WHERE escrow_id = ANY($1)`,
	} {
		batch.Queue(stmt, ids)
	}
	results := tx.SendBatch(ctx, batch)
	for i := 0; i < batch.Len(); i++ {
		if _, err := results.Exec(); err != nil {
			results.Close()
			return fmt.Errorf("delete pruned accounting escrows: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("delete pruned accounting escrows: %w", err)
	}
	return nil
}

// pgxQuerier is the read surface shared by *pgxpool.Pool and pgx.Tx.
type pgxQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}
