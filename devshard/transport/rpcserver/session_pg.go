package rpcserver

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SharedConfig selects the host version this process replicates.
type SharedConfig struct {
	HostAddress string
	Version     string
	// InstanceID is optional. Empty gets a random id until the router
	// publish arrives and SetPublishedBarrier replaces it.
	InstanceID string
}

// SharedSessions is the Postgres peer-session record. Each child keeps an
// in-memory copy filled from LISTEN/NOTIFY. Admission stays a map lookup.
type SharedSessions struct {
	pool    *pgxpool.Pool
	host    string
	version string
	apply   func([]sessionRow, []byte)

	applied atomic.Int64
	ready   atomic.Bool
	sqlN    atomic.Int64
	closed  atomic.Bool
	inst    atomic.Value // string

	startOnce sync.Once
	closeOnce sync.Once
	cancel    context.CancelFunc
	done      chan struct{}

	mu     sync.Mutex
	listen *pgx.Conn

	// pubMu guards the router membership list. usePublished is false until
	// the first publish, and the barrier then uses the member table.
	pubMu        sync.Mutex
	usePublished bool
	published    []string
	absent       map[string]struct{}
}

// OpenSharedSessions does not connect. EnableShared or Start does.
func OpenSharedSessions(pool *pgxpool.Pool, cfg SharedConfig) *SharedSessions {
	id := cfg.InstanceID
	if id == "" {
		id = newInstanceID()
	}
	s := &SharedSessions{
		pool:    pool,
		host:    cfg.HostAddress,
		version: cfg.Version,
	}
	s.inst.Store(id)
	return s
}

func (s *SharedSessions) instanceID() string {
	if s == nil {
		return ""
	}
	id, _ := s.inst.Load().(string)
	return id
}

// SetPublishedBarrier installs the member ids the router published for this
// version. self is this process's instance id. A nil call never happens;
// until the first call the barrier stays on the member table, which is the
// mixed-fleet fallback when an old versiond never forwards a list.
// A published id with no row is waited for until one barrier timeout, then
// skipped until it registers, so a member that will never write the table
// does not add a second to every later Attach.
func (s *SharedSessions) SetPublishedBarrier(self string, ids []string) {
	if s == nil {
		return
	}
	if self != "" {
		old := s.instanceID()
		if old != self {
			s.inst.Store(self)
			s.retireInstance(old)
		}
	}
	cp := append([]string(nil), ids...)
	s.pubMu.Lock()
	s.published = cp
	s.usePublished = true
	s.absent = map[string]struct{}{}
	s.pubMu.Unlock()
}

func (s *SharedSessions) retireInstance(id string) {
	if s == nil || s.pool == nil || id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s.sqlN.Add(1)
	_, _ = s.pool.Exec(ctx, `UPDATE devshard_peer_rpc_members SET ready = false WHERE instance_id = $1`, id)
}

func (s *SharedSessions) publishedOn() (bool, []string, map[string]struct{}) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if !s.usePublished {
		return false, nil, nil
	}
	ids := append([]string(nil), s.published...)
	absent := make(map[string]struct{}, len(s.absent))
	for id := range s.absent {
		absent[id] = struct{}{}
	}
	return true, ids, absent
}

func (s *SharedSessions) Ready() bool {
	return s != nil && s.ready.Load()
}

func (s *SharedSessions) SQLQueries() int64 {
	if s == nil {
		return 0
	}
	return s.sqlN.Load()
}

func (s *SharedSessions) Start() {
	if s == nil || s.pool == nil {
		return
	}
	s.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.done = make(chan struct{})
		go s.run(ctx)
	})
}

// Close stops LISTEN and the heartbeat. It does not delete session rows.
func (s *SharedSessions) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		if s.cancel != nil {
			s.cancel()
		}
		if s.done != nil {
			<-s.done
		}
		if s.pool == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		s.sqlN.Add(1)
		_, _ = s.pool.Exec(ctx, `UPDATE devshard_peer_rpc_members SET ready = false WHERE instance_id = $1`, s.instanceID())
	})
}

// DropListenForTest closes the LISTEN connection so the run loop reconnects.
func (s *SharedSessions) DropListenForTest() {
	if s == nil {
		return
	}
	s.mu.Lock()
	c := s.listen
	s.mu.Unlock()
	if c != nil {
		_ = c.Close(context.Background())
	}
}

func (s *SharedSessions) run(ctx context.Context) {
	defer close(s.done)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.heartbeatLoop(ctx)
	}()
	s.listenLoop(ctx)
	wg.Wait()
}

func (s *SharedSessions) listenLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil || s.closed.Load() {
			return
		}
		err := s.listenOnce(ctx)
		if ctx.Err() != nil || s.closed.Load() {
			return
		}
		if err != nil {
			slog.Warn("devshard peer rpc session listen", "err", err, "version", s.version)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *SharedSessions) listenOnce(ctx context.Context) error {
	// Dedicated connection, not a pool slot. The storage pool is small, and
	// LISTEN has to stay open for the life of the process.
	conn, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig)
	if err != nil {
		return err
	}
	defer func() {
		s.mu.Lock()
		if s.listen == conn {
			s.listen = nil
		}
		s.mu.Unlock()
		_ = conn.Close(context.Background())
	}()
	s.sqlN.Add(1)
	if _, err = conn.Exec(ctx, "SET statement_timeout = 0"); err != nil {
		return err
	}
	s.sqlN.Add(1)
	if _, err = conn.Exec(ctx, "LISTEN "+sessionNotifyChannel); err != nil {
		return err
	}
	s.mu.Lock()
	s.listen = conn
	s.mu.Unlock()

	if err = s.catchUp(ctx, conn); err != nil {
		return err
	}
	if !s.ready.Load() {
		if err = s.upsertMember(ctx, true); err != nil {
			return err
		}
		s.ready.Store(true)
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		waitCtx, cancel := context.WithTimeout(ctx, sharedPoll)
		_, err = conn.WaitForNotification(waitCtx)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			ensureSessionHAMetrics()
			notifyReconnects.Inc()
			return err
		}
		if err = s.catchUp(ctx, conn); err != nil {
			ensureSessionHAMetrics()
			notifyReconnects.Inc()
			return err
		}
	}
}

func (s *SharedSessions) catchUp(ctx context.Context, conn *pgx.Conn) error {
	s.sqlN.Add(1)
	rows, err := conn.Query(ctx, sinceSessionsSQL, s.host, s.version, s.applied.Load())
	if err != nil {
		return err
	}
	defer rows.Close()
	got, maxSeq, err := scanSessionRows(rows, s.host, s.version)
	if err != nil {
		return err
	}
	prev := s.applied.Load()
	if len(got) > 0 && s.apply != nil {
		s.apply(got, nil)
	}
	if maxSeq > prev {
		ensureSessionHAMetrics()
		applyLagSeq.Set(float64(maxSeq - prev))
		s.applied.Store(maxSeq)
		return s.upsertMember(ctx, true)
	}
	return nil
}

func (s *SharedSessions) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(sharedHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !s.ready.Load() || s.closed.Load() {
				continue
			}
			_ = s.upsertMember(ctx, true)
		}
	}
}

func (s *SharedSessions) upsertMember(ctx context.Context, ready bool) error {
	if s.pool == nil {
		return nil
	}
	s.sqlN.Add(1)
	_, err := s.pool.Exec(ctx, `
INSERT INTO devshard_peer_rpc_members (
    instance_id, host_address, version, applied_seq, heartbeat_at, ready
) VALUES ($1, $2, $3, $4, now(), $5)
ON CONFLICT (instance_id) DO UPDATE SET
    host_address = EXCLUDED.host_address,
    version = EXCLUDED.version,
    applied_seq = GREATEST(devshard_peer_rpc_members.applied_seq, EXCLUDED.applied_seq),
    heartbeat_at = now(),
    ready = EXCLUDED.ready`,
		s.instanceID(), s.host, s.version, s.applied.Load(), ready)
	return err
}

// Commit writes one attach under the per-peer advisory lock, then NOTIFYs.
// The caller applies out.Rows locally and waits for the member barrier.
func (s *SharedSessions) Commit(ctx context.Context, in attachCommit) (attachOutcome, error) {
	var out attachOutcome
	if s == nil || s.pool == nil {
		return out, errors.New("peer rpc session store is not configured")
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		s.sqlN.Add(1)
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, s.lockKey(in.Peer)); err != nil {
			return err
		}
		renewed, err := s.renewIfPresent(ctx, tx, in, &out)
		if err != nil {
			return err
		}
		if !renewed {
			if err := s.rejectStale(ctx, tx, in); err != nil {
				return err
			}
			if err := s.evictIfNeeded(ctx, tx, in, &out); err != nil {
				return err
			}
			if err := s.replaceLive(ctx, tx, in, &out); err != nil {
				return err
			}
			if err := s.insertLive(ctx, tx, in, &out); err != nil {
				return err
			}
		}
		if out.Seq == 0 {
			return errors.New("peer rpc session commit produced no seq")
		}
		s.sqlN.Add(1)
		if _, err := tx.Exec(ctx, `
INSERT INTO devshard_peer_rpc_members (
    instance_id, host_address, version, applied_seq, heartbeat_at, ready
) VALUES ($1, $2, $3, $4, now(), true)
ON CONFLICT (instance_id) DO UPDATE SET
    applied_seq = GREATEST(devshard_peer_rpc_members.applied_seq, EXCLUDED.applied_seq),
    heartbeat_at = now(),
    ready = true`,
			s.instanceID(), s.host, s.version, out.Seq); err != nil {
			return err
		}
		s.sqlN.Add(1)
		_, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, sessionNotifyChannel, strconv.FormatInt(out.Seq, 10))
		return err
	})
	if err != nil {
		return attachOutcome{}, mapPGAttachErr(err)
	}
	if out.Seq > s.applied.Load() {
		s.applied.Store(out.Seq)
	}
	return out, nil
}

func (s *SharedSessions) lockKey(peer string) string {
	// Unit separator keeps the key valid UTF-8. A NUL byte is rejected by
	// Postgres text, which hashtextextended requires.
	return s.host + "\x1f" + s.version + "\x1f" + peer
}

func (s *SharedSessions) renewIfPresent(ctx context.Context, tx pgx.Tx, in attachCommit, out *attachOutcome) (bool, error) {
	var state, peer string
	var attached int64
	s.sqlN.Add(1)
	err := tx.QueryRow(ctx, `
SELECT state, peer, attached_unix
FROM devshard_peer_rpc_sessions
WHERE token_hash = $1`, in.Hash).Scan(&state, &peer, &attached)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state != sessionStateLive || peer != in.Peer {
		return false, errAttachNonceUsed
	}
	if in.AttachedUnix < attached {
		return false, errAttachStale
	}
	s.sqlN.Add(1)
	rows, err := tx.Query(ctx, `
UPDATE devshard_peer_rpc_sessions
SET expires_at = now() + ($2 * interval '1 microsecond'),
    seq = nextval('devshard_peer_rpc_session_seq'),
    updated_at = now(),
    state = 'live'
WHERE token_hash = $1
RETURNING token_hash, peer, attached_unix, expires_at, grace_until, state, seq, updated_at`,
		in.Hash, durationMicros(in.TTL))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	got, _, err := scanSessionRows(rows, s.host, s.version)
	if err != nil {
		return false, err
	}
	out.Rows = append(out.Rows, got...)
	noteLive(out)
	return true, nil
}

func (s *SharedSessions) rejectStale(ctx context.Context, tx pgx.Tx, in attachCommit) error {
	var attached int64
	s.sqlN.Add(1)
	err := tx.QueryRow(ctx, `
SELECT attached_unix
FROM devshard_peer_rpc_sessions
WHERE host_address = $1 AND version = $2 AND peer = $3 AND state = 'live'`,
		s.host, s.version, in.Peer).Scan(&attached)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if in.AttachedUnix < attached {
		return errAttachStale
	}
	return nil
}

func (s *SharedSessions) evictIfNeeded(ctx context.Context, tx pgx.Tx, in attachCommit, out *attachOutcome) error {
	var live int
	s.sqlN.Add(1)
	err := tx.QueryRow(ctx, `
SELECT COUNT(*)
FROM devshard_peer_rpc_sessions
WHERE host_address = $1 AND version = $2 AND peer = $3 AND state = 'live'`,
		s.host, s.version, in.Peer).Scan(&live)
	if err != nil {
		return err
	}
	if live > 0 {
		return nil
	}
	maxN := in.MaxSessions
	if maxN <= 0 {
		maxN = defaultMaxSessions
	}
	s.sqlN.Add(1)
	var n int
	if err := tx.QueryRow(ctx, `
SELECT COUNT(*)
FROM devshard_peer_rpc_sessions
WHERE host_address = $1 AND version = $2 AND state = 'live' AND expires_at > now()`,
		s.host, s.version).Scan(&n); err != nil {
		return err
	}
	if n < maxN {
		return nil
	}
	s.sqlN.Add(1)
	rows, err := tx.Query(ctx, `
UPDATE devshard_peer_rpc_sessions
SET state = 'evicted',
    grace_until = now() + ($3 * interval '1 microsecond'),
    seq = nextval('devshard_peer_rpc_session_seq'),
    updated_at = now()
WHERE token_hash = (
    SELECT token_hash
    FROM devshard_peer_rpc_sessions
    WHERE host_address = $1 AND version = $2 AND state = 'live' AND expires_at > now()
    ORDER BY attached_unix ASC
    LIMIT 1
)
RETURNING token_hash, peer, attached_unix, expires_at, grace_until, state, seq, updated_at`,
		s.host, s.version, durationMicros(in.Grace))
	if err != nil {
		return err
	}
	defer rows.Close()
	got, _, err := scanSessionRows(rows, s.host, s.version)
	if err != nil {
		return err
	}
	if len(got) == 0 {
		return errTooManySessions
	}
	out.Rows = append(out.Rows, got...)
	return nil
}

func (s *SharedSessions) replaceLive(ctx context.Context, tx pgx.Tx, in attachCommit, out *attachOutcome) error {
	s.sqlN.Add(1)
	rows, err := tx.Query(ctx, `
UPDATE devshard_peer_rpc_sessions
SET state = 'replaced',
    grace_until = now() + ($4 * interval '1 microsecond'),
    seq = nextval('devshard_peer_rpc_session_seq'),
    updated_at = now()
WHERE host_address = $1 AND version = $2 AND peer = $3 AND state = 'live'
RETURNING token_hash, peer, attached_unix, expires_at, grace_until, state, seq, updated_at`,
		s.host, s.version, in.Peer, durationMicros(in.Grace))
	if err != nil {
		return err
	}
	defer rows.Close()
	got, _, err := scanSessionRows(rows, s.host, s.version)
	if err != nil {
		return err
	}
	out.Rows = append(out.Rows, got...)
	return nil
}

func (s *SharedSessions) insertLive(ctx context.Context, tx pgx.Tx, in attachCommit, out *attachOutcome) error {
	s.sqlN.Add(1)
	rows, err := tx.Query(ctx, `
INSERT INTO devshard_peer_rpc_sessions (
    token_hash, host_address, version, peer, attached_unix, expires_at, grace_until, state, seq, origin, updated_at
) VALUES (
    $1, $2, $3, $4, $5,
    now() + ($6 * interval '1 microsecond'),
    NULL,
    'live',
    nextval('devshard_peer_rpc_session_seq'),
    $7,
    now()
)
RETURNING token_hash, peer, attached_unix, expires_at, grace_until, state, seq, updated_at`,
		in.Hash, s.host, s.version, in.Peer, in.AttachedUnix, durationMicros(in.TTL), s.instanceID())
	if err != nil {
		return err
	}
	defer rows.Close()
	got, _, err := scanSessionRows(rows, s.host, s.version)
	if err != nil {
		return err
	}
	out.Rows = append(out.Rows, got...)
	noteLive(out)
	return nil
}

func noteLive(out *attachOutcome) {
	for _, row := range out.Rows {
		if row.Seq > out.Seq {
			out.Seq = row.Seq
		}
		if row.State == sessionStateLive {
			out.Expires = row.AdmitUntil
			if row.Seq > out.Seq {
				out.Seq = row.Seq
			}
		}
	}
}

// WaitApplied polls ready members of this host version until each has
// applied seq, or until the 1s cap. The token is returned either way.
func (s *SharedSessions) WaitApplied(ctx context.Context, seq int64) bool {
	ensureSessionHAMetrics()
	start := time.Now()
	defer func() {
		barrierWait.Observe(time.Since(start).Seconds())
	}()
	if s == nil || seq <= 0 {
		return false
	}
	deadline := time.Now().Add(sharedBarrierWait)
	var missing []string
	for {
		behind, miss, err := s.membersBehind(ctx, seq)
		if err == nil {
			missing = miss
			if behind == 0 {
				return false
			}
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			s.noteAbsent(missing)
			barrierTimeouts.Inc()
			return true
		}
		timer := time.NewTimer(sharedBarrierPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			s.noteAbsent(missing)
			barrierTimeouts.Inc()
			return true
		case <-timer.C:
		}
	}
}

func (s *SharedSessions) noteAbsent(ids []string) {
	if s == nil || len(ids) == 0 {
		return
	}
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if !s.usePublished {
		return
	}
	if s.absent == nil {
		s.absent = map[string]struct{}{}
	}
	for _, id := range ids {
		s.absent[id] = struct{}{}
	}
}

func (s *SharedSessions) membersBehind(ctx context.Context, seq int64) (int, []string, error) {
	if on, ids, absent := s.publishedOn(); on {
		return s.membersBehindPublished(ctx, seq, ids, absent)
	}
	n, err := s.membersBehindTable(ctx, seq)
	return n, nil, err
}

func (s *SharedSessions) membersBehindTable(ctx context.Context, seq int64) (int, error) {
	s.sqlN.Add(1)
	var n int
	err := s.pool.QueryRow(ctx, `
SELECT COUNT(*)
FROM devshard_peer_rpc_members
WHERE host_address = $1 AND version = $2 AND ready
  AND heartbeat_at > now() - ($5 * interval '1 second')
  AND applied_seq < $3
  AND instance_id <> $4`,
		s.host, s.version, seq, s.instanceID(), int64(sharedHeartbeatTTL/time.Second)).Scan(&n)
	return n, err
}

type memberApply struct {
	ready   bool
	applied int64
}

func (s *SharedSessions) membersBehindPublished(ctx context.Context, seq int64, ids []string, absent map[string]struct{}) (int, []string, error) {
	s.sqlN.Add(1)
	rows, err := s.pool.Query(ctx, `
SELECT instance_id, ready, applied_seq
FROM devshard_peer_rpc_members
WHERE host_address = $1 AND version = $2`, s.host, s.version)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	found := map[string]memberApply{}
	for rows.Next() {
		var id string
		var row memberApply
		if err := rows.Scan(&id, &row.ready, &row.applied); err != nil {
			return 0, nil, err
		}
		found[id] = row
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	self := s.instanceID()
	var missing []string
	var seen []string
	behind := 0
	for _, id := range ids {
		if id == "" || id == self {
			continue
		}
		row, ok := found[id]
		if !ok {
			if _, skip := absent[id]; skip {
				continue
			}
			missing = append(missing, id)
			continue
		}
		seen = append(seen, id)
		if !row.ready || row.applied < seq {
			behind++
		}
	}
	if len(seen) > 0 {
		s.pubMu.Lock()
		for _, id := range seen {
			delete(s.absent, id)
		}
		s.pubMu.Unlock()
	}
	return behind + len(missing), missing, nil
}

// SweepExpired turns expired live rows into replay tombstones and deletes
// tombstones older than retiredNonceTTL. Shutdown does not call this to
// drop live rows.
func (s *SharedSessions) SweepExpired(ctx context.Context) {
	if s == nil || s.pool == nil || !s.ready.Load() {
		return
	}
	s.sqlN.Add(1)
	if _, err := s.pool.Exec(ctx, `
UPDATE devshard_peer_rpc_sessions
SET state = 'invalidated',
    seq = nextval('devshard_peer_rpc_session_seq'),
    updated_at = now()
WHERE host_address = $1 AND version = $2 AND state = 'live' AND expires_at <= now()`,
		s.host, s.version); err != nil {
		slog.Warn("devshard peer rpc session sweep", "err", err)
		return
	}
	s.sqlN.Add(1)
	if _, err := s.pool.Exec(ctx, `
DELETE FROM devshard_peer_rpc_sessions
WHERE host_address = $1 AND version = $2 AND state <> 'live'
  AND COALESCE(grace_until, expires_at) < now() - ($3 * interval '1 microsecond')`,
		s.host, s.version, durationMicros(retiredNonceTTL)); err != nil {
		slog.Warn("devshard peer rpc session sweep", "err", err)
	}
}

func mapPGAttachErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errAttachNonceUsed) || errors.Is(err, errAttachStale) || errors.Is(err, errTooManySessions) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return errAttachNonceUsed
	}
	return err
}

func scanSessionRows(rows pgx.Rows, host, version string) ([]sessionRow, int64, error) {
	var out []sessionRow
	var maxSeq int64
	for rows.Next() {
		var hash []byte
		var peer, state string
		var attached, seq int64
		var expires, updated time.Time
		var grace *time.Time
		if err := rows.Scan(&hash, &peer, &attached, &expires, &grace, &state, &seq, &updated); err != nil {
			return nil, 0, err
		}
		if seq > maxSeq {
			maxSeq = seq
		}
		out = append(out, sessionRow{
			Hash:         append([]byte(nil), hash...),
			HashHex:      hex.EncodeToString(hash),
			Host:         host,
			Version:      version,
			Peer:         peer,
			AttachedUnix: attached,
			AdmitUntil:   admitUntil(state, expires, grace),
			State:        state,
			Seq:          seq,
			UpdatedAt:    updated,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, maxSeq, nil
}
