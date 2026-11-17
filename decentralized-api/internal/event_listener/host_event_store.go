package event_listener

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"common/nodemanager/gen"
	"decentralized-api/apiconfig"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/protobuf/proto"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// ErrWriterLocked means another process holds this writer's feed lock.
var ErrWriterLocked = errors.New("host event writer lock is held")

// ErrFeedDiverged means the stored generation, next sequence, or emitted height
// does not match the ring. The height was not written.
var ErrFeedDiverged = errors.New("host event feed diverged")

// AppendExpect is the ring's position for the height about to be stored.
// NextSeq is the ring head plus one. PrevHeight is the height before it.
type AppendExpect struct {
	Generation uint64
	NextSeq    uint64
	PrevHeight int64
}

// errInjectedFeedUpdate fails the feed-meta write so the height transaction rolls back.
var errInjectedFeedUpdate = errors.New("injected host event feed update failure")

// hotTailRows is the in-memory ring capacity the store reloads on boot.
const hotTailRows = apiconfig.DefaultHostEventRingCapacity

// sqliteReaderConns is the SQLite pool for store reads. The writer holds one
// extra sql.Conn, so MaxOpenConns is this plus one.
const sqliteReaderConns = 4

// HostEventSnapshot is the saved feed cursor and the hot tail for one writer.
type HostEventSnapshot struct {
	Generation    uint64
	NextSeq       uint64
	EmittedHeight int64
	OldestSeq     uint64
	Tail          []apiconfig.HostEvent
	Empty         bool
}

// StoreHead is the last committed row for one writer.
type StoreHead struct {
	Seq        uint64
	Height     int64
	Generation uint64
	Empty      bool
}

// HostEventStore is the catch-up window behind the memory ring.
// Every operation is scoped to the writer the store was opened for.
type HostEventStore interface {
	Writer() string
	// HoldWriter takes this process's writer lock when it does not hold one.
	// ErrWriterLocked means another process has it. SQLite always holds it.
	HoldWriter(ctx context.Context) error
	AppendHeight(ctx context.Context, height int64, epoch uint64, events []apiconfig.HostEvent, expect AppendExpect) ([]apiconfig.HostEvent, error)
	Read(ctx context.Context, afterSeq uint64, kinds []apiconfig.HostEventKind, limit int) (rows []apiconfig.HostEvent, generation uint64, err error)
	// Oldest is the smallest retained sequence. empty is true when this writer has no rows.
	Oldest(ctx context.Context) (seq uint64, empty bool, generation uint64, err error)
	ResolveBlock(ctx context.Context, height int64) (seq uint64, found bool, generation uint64, err error)
	Prune(ctx context.Context, currentEpoch uint64) error
	Load(ctx context.Context) (HostEventSnapshot, error)
	Head(ctx context.Context) (StoreHead, error)
	// FeedHead is Head for readers that only need the sequence and generation.
	// empty is true when this writer has no rows.
	FeedHead(ctx context.Context) (seq, generation uint64, empty bool, err error)
	Reset(ctx context.Context, generation uint64) error
	Close() error
}

type dbStore struct {
	dialect   string
	writer    string
	db        *sql.DB
	lockKey   int64
	mu        sync.Mutex
	conn      *sql.Conn
	reconnect bool
	reloads   int

	generation     uint64
	pruneCalls     int
	insertStmts    int
	failFeedUpdate bool
	failOnce       bool
	headErr        error
	resetErr       error
	loadErr        error
	stmtErr        error
	waitCtx        bool
	inTx           func(ctx context.Context, tx *sql.Tx) error
	onPrune        func()
	// betweenRead runs after the feed-row SELECT in Load and Head so a test
	// can commit another height before the tail SELECT.
	betweenRead func()
}

func (s *dbStore) Writer() string { return s.writer }

func (s *dbStore) HoldWriter(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureConn(ctx)
}

func sqliteDSN(path string) string {
	p := strings.ReplaceAll(path, " ", "%20")
	return "file:" + p + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)"
}

func openSQLiteStore(path, writer string) (*dbStore, error) {
	s, err := openSQLiteFile(path, writer)
	if err == nil || !sqliteRecreate(err) {
		return s, err
	}
	if mvErr := moveCorruptSQLite(path); mvErr != nil {
		return nil, fmt.Errorf("%w (moving aside: %v)", err, mvErr)
	}
	return openSQLiteFile(path, writer)
}

// sqliteRecreate reports a database image SQLite itself rejected.
// Permission, disk, and lock errors are not in this set.
func sqliteRecreate(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() & 0xff {
	case sqlite3.SQLITE_NOTADB, sqlite3.SQLITE_CORRUPT:
		return true
	default:
		return false
	}
}

// moveCorruptSQLite renames the main file aside and removes the WAL sidecars.
// A new database must not be created next to the old -wal and -shm files.
func moveCorruptSQLite(path string) error {
	aside := fmt.Sprintf("%s.bad.%d", path, time.Now().UnixNano())
	if err := os.Rename(path, aside); err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func openSQLiteFile(path, writer string) (*dbStore, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(sqliteReaderConns + 1)
	db.SetMaxIdleConns(sqliteReaderConns)
	s := &dbStore{dialect: "sqlite", writer: writer, db: db}
	if err := s.pingAndSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureConn(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func openPostgresStore(ctx context.Context, writer string) (*dbStore, error) {
	cfg, err := pgx.ParseConfig("")
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}
	db := stdlib.OpenDB(*cfg)
	s := &dbStore{dialect: "postgres", writer: writer, db: db, lockKey: writerLockKey(writer)}
	if err := s.pingAndSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureConn(ctx); err != nil {
		if errors.Is(err, ErrWriterLocked) {
			return s, nil
		}
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func writerLockKey(writer string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(writer))
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

func (s *dbStore) pingAndSchema(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	payload := "BLOB"
	if s.dialect == "postgres" {
		payload = "BYTEA"
	}
	stmts := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS host_events (
			writer TEXT NOT NULL,
			seq BIGINT NOT NULL,
			height BIGINT NOT NULL,
			kind INTEGER NOT NULL,
			epoch_index BIGINT NOT NULL,
			observed_at_unix BIGINT NOT NULL,
			payload %s NOT NULL,
			PRIMARY KEY (writer, seq)
		)`, payload),
		`CREATE INDEX IF NOT EXISTS host_events_kind_seq ON host_events (writer, kind, seq)`,
		`CREATE INDEX IF NOT EXISTS host_events_epoch ON host_events (writer, epoch_index)`,
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS host_events_block_height ON host_events (writer, height) WHERE kind = %d`, apiconfig.HostEventKindBlock),
		`CREATE INDEX IF NOT EXISTS host_events_height ON host_events (writer, height)`,
		`CREATE TABLE IF NOT EXISTS host_event_feed (
			writer TEXT PRIMARY KEY,
			generation BIGINT NOT NULL,
			next_seq BIGINT NOT NULL,
			emitted_height BIGINT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("host event schema: %w", err)
		}
	}
	return nil
}

func (s *dbStore) ensureConn(ctx context.Context) error {
	if s.conn != nil {
		return nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	if s.dialect == "postgres" {
		var ok bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", s.lockKey).Scan(&ok); err != nil || !ok {
			_ = conn.Close()
			if err == nil {
				err = ErrWriterLocked
			}
			return err
		}
	}
	s.conn = conn
	if s.reconnect {
		s.reloads++
		s.reconnect = false
	}
	return nil
}

// dropConn closes the writer session instead of returning it to the pool.
// Raw's ErrBadConn makes database/sql call the driver Close. On Postgres that
// ends the session and releases its advisory lock.
func (s *dbStore) dropConn() {
	if s.conn == nil {
		return
	}
	conn := s.conn
	s.conn = nil
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

// beginRead opens a read-only transaction. Postgres uses REPEATABLE READ so
// Load and Head see one commit across their feed-row and tail SELECTs.
func (s *dbStore) beginRead(ctx context.Context) (*sql.Tx, error) {
	opts := &sql.TxOptions{ReadOnly: true}
	if s.dialect == "postgres" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	return s.db.BeginTx(ctx, opts)
}

func (s *dbStore) begin(ctx context.Context) (*sql.Tx, error) {
	if err := s.ensureConn(ctx); err != nil {
		return nil, err
	}
	if s.stmtErr != nil {
		err := s.stmtErr
		s.stmtErr = nil
		s.dropConn()
		s.reconnect = true
		return nil, err
	}
	var (
		tx  *sql.Tx
		err error
	)
	if s.conn != nil {
		tx, err = s.conn.BeginTx(ctx, nil)
	} else {
		tx, err = s.db.BeginTx(ctx, nil)
	}
	if err != nil {
		return nil, s.lost(err)
	}
	return tx, nil
}

// lost closes the lock session when err means that session is gone.
// A later write opens a new connection and takes the advisory lock again.
func (s *dbStore) lost(err error) error {
	if err == nil || !sessionLost(err) {
		return err
	}
	s.dropConn()
	s.reconnect = true
	return err
}

func sessionLost(err error) bool {
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		code := string(pgErr.Code)
		if strings.HasPrefix(code, "08") {
			return true
		}
		switch code {
		case "57P01", "57P02", "57P03":
			return true
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "conn closed") || strings.Contains(msg, "broken pipe")
}

func (s *dbStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		if s.dialect == "postgres" {
			_, _ = s.conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", s.lockKey)
		}
		_ = s.conn.Close()
		s.conn = nil
	}
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *dbStore) AppendHeight(ctx context.Context, height int64, epoch uint64, events []apiconfig.HostEvent, expect AppendExpect) (stored []apiconfig.HostEvent, err error) {
	if len(events) == 0 {
		return nil, errors.New("host event height has no events")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = tx.Rollback()
		if err != nil {
			err = s.lost(err)
		}
	}()
	if s.dialect == "postgres" {
		if _, err := tx.ExecContext(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
			return nil, err
		}
	}
	if s.inTx != nil {
		if err := s.inTx(ctx, tx); err != nil {
			return nil, err
		}
	}

	var (
		gen     uint64
		nextSeq uint64
		emitted int64
	)
	err = tx.QueryRowContext(ctx, s.q(`SELECT generation, next_seq, emitted_height FROM host_event_feed WHERE writer = ?`), s.writer).
		Scan(&gen, &nextSeq, &emitted)
	if errors.Is(err, sql.ErrNoRows) {
		gen = s.generation
		if gen == 0 {
			gen = 1
		}
		nextSeq = 1
		emitted = 0
	} else if err != nil {
		return nil, err
	}
	if nextSeq == 0 {
		nextSeq = 1
	}
	// A height already committed is returned as stored only when this is the
	// same generation and the first row is the ring's next sequence. That is
	// the crash between commit and marker write. Every other already-emitted
	// case, including no rows, is a new generation. A new height must continue
	// the ring. The first height of a generation is the tip, so an empty
	// cursor does not have to equal H-1.
	if emitted >= height {
		rows, err := s.eventsForHeight(ctx, tx, height)
		if err != nil {
			return nil, err
		}
		if alreadyEmittedMatch(gen, rows, expect) {
			return rows, nil
		}
		return nil, errFeedDiverged(gen, nextSeq, emitted, expect)
	}
	if feedDiverged(gen, nextSeq, emitted, expect) {
		return nil, errFeedDiverged(gen, nextSeq, emitted, expect)
	}

	now := time.Now().Unix()
	out := make([]apiconfig.HostEvent, len(events))
	copy(out, events)
	for i := range out {
		if out[i].ObservedAtUnix == 0 {
			out[i].ObservedAtUnix = now
		}
		out[i].Seq = nextSeq
		nextSeq++
	}
	if err := s.insertEvents(ctx, tx, height, epoch, out); err != nil {
		return nil, err
	}
	if s.failFeedUpdate || s.failOnce {
		s.failOnce = false
		return nil, errInjectedFeedUpdate
	}
	upsert := s.q(`INSERT INTO host_event_feed (writer, generation, next_seq, emitted_height) VALUES (?, ?, ?, ?)
		ON CONFLICT(writer) DO UPDATE SET next_seq = excluded.next_seq, emitted_height = excluded.emitted_height`)
	if _, err := tx.ExecContext(ctx, upsert, s.writer, gen, nextSeq, height); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// alreadyEmittedMatch is the crash between commit and marker write: the store
// has this height, the ring does not, and the cursor still names it.
func alreadyEmittedMatch(gen uint64, rows []apiconfig.HostEvent, expect AppendExpect) bool {
	return gen == expect.Generation && len(rows) > 0 && rows[0].Seq == expect.NextSeq
}

// feedDiverged reports that the stored cursor cannot continue the ring.
// An empty generation (next_seq 1, nothing emitted) accepts the tip as its first height.
func feedDiverged(gen, nextSeq uint64, emitted int64, expect AppendExpect) bool {
	if gen != expect.Generation || nextSeq != expect.NextSeq {
		return true
	}
	if nextSeq == 1 && emitted == 0 {
		return false
	}
	return emitted != expect.PrevHeight
}

func errFeedDiverged(gen, nextSeq uint64, emitted int64, expect AppendExpect) error {
	return fmt.Errorf("%w: store generation %d next_seq %d emitted_height %d, ring generation %d next_seq %d prev_height %d",
		ErrFeedDiverged, gen, nextSeq, emitted, expect.Generation, expect.NextSeq, expect.PrevHeight)
}

func (s *dbStore) eventsForHeight(ctx context.Context, tx *sql.Tx, height int64) ([]apiconfig.HostEvent, error) {
	rows, err := tx.QueryContext(ctx, s.q(`SELECT seq, height, kind, epoch_index, observed_at_unix, payload FROM host_events WHERE writer = ? AND height = ? ORDER BY seq`), s.writer, height)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func (s *dbStore) Read(ctx context.Context, afterSeq uint64, kinds []apiconfig.HostEventKind, limit int) ([]apiconfig.HostEvent, uint64, error) {
	tx, err := s.beginRead(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	generation, err := s.feedGeneration(ctx, tx)
	if err != nil {
		return nil, 0, err
	}
	if len(kinds) == 0 || limit <= 0 {
		return nil, generation, nil
	}
	args := make([]any, 0, len(kinds)+3)
	args = append(args, s.writer, afterSeq)
	holders := make([]string, len(kinds))
	for i, kind := range kinds {
		holders[i] = s.ph(len(args) + 1)
		args = append(args, int32(kind))
	}
	args = append(args, limit)
	q := fmt.Sprintf(`SELECT seq, height, kind, epoch_index, observed_at_unix, payload
		FROM host_events WHERE writer = %s AND seq > %s AND kind IN (%s) ORDER BY seq LIMIT %s`,
		s.ph(1), s.ph(2), strings.Join(holders, ", "), s.ph(len(args)))
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, generation, err
	}
	defer rows.Close()
	events, err := scanEvents(rows)
	if err != nil {
		return nil, generation, err
	}
	return events, generation, nil
}

func (s *dbStore) Oldest(ctx context.Context) (uint64, bool, uint64, error) {
	tx, err := s.beginRead(ctx)
	if err != nil {
		return 0, true, 0, err
	}
	defer tx.Rollback()
	generation, err := s.feedGeneration(ctx, tx)
	if err != nil {
		return 0, true, 0, err
	}
	var seq sql.NullInt64
	err = tx.QueryRowContext(ctx, s.q(`SELECT MIN(seq) FROM host_events WHERE writer = ?`), s.writer).Scan(&seq)
	if err != nil {
		return 0, true, generation, err
	}
	if !seq.Valid || seq.Int64 <= 0 {
		return 0, true, generation, nil
	}
	return uint64(seq.Int64), false, generation, nil
}

func (s *dbStore) ResolveBlock(ctx context.Context, height int64) (uint64, bool, uint64, error) {
	tx, err := s.beginRead(ctx)
	if err != nil {
		return 0, false, 0, err
	}
	defer tx.Rollback()
	generation, err := s.feedGeneration(ctx, tx)
	if err != nil {
		return 0, false, 0, err
	}
	var seq uint64
	err = tx.QueryRowContext(ctx, s.q(resolveBlockSelect()), s.writer, height).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, generation, nil
	}
	if err != nil {
		return 0, false, generation, err
	}
	return seq, true, generation, nil
}

func (s *dbStore) feedGeneration(ctx context.Context, tx *sql.Tx) (uint64, error) {
	var gen uint64
	err := tx.QueryRowContext(ctx, s.q(`SELECT generation FROM host_event_feed WHERE writer = ?`), s.writer).Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return gen, err
}

// resolveBlockSelect inlines kind = BLOCK so SQLite and Postgres can use
// host_events_block_height. A bound kind parameter does not match that partial index.
func resolveBlockSelect() string {
	return fmt.Sprintf(`SELECT seq FROM host_events WHERE writer = ? AND kind = %d AND height = ? LIMIT 1`, apiconfig.HostEventKindBlock)
}

func (s *dbStore) Prune(ctx context.Context, currentEpoch uint64) (err error) {
	if s.onPrune != nil {
		s.onPrune()
	}
	s.mu.Lock()
	s.pruneCalls++
	if currentEpoch < 4 {
		s.mu.Unlock()
		return nil
	}
	defer s.mu.Unlock()
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
		if err != nil {
			err = s.lost(err)
		}
	}()
	if s.dialect == "postgres" {
		if _, err := tx.ExecContext(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM host_events WHERE writer = ? AND epoch_index < ?`), s.writer, currentEpoch-4); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *dbStore) Load(ctx context.Context) (HostEventSnapshot, error) {
	if s.loadErr != nil {
		return HostEventSnapshot{}, s.loadErr
	}
	tx, err := s.beginRead(ctx)
	if err != nil {
		return HostEventSnapshot{}, err
	}
	defer tx.Rollback()
	snap := HostEventSnapshot{NextSeq: 1, Empty: true}
	err = tx.QueryRowContext(ctx, s.q(`SELECT generation, next_seq, emitted_height FROM host_event_feed WHERE writer = ?`), s.writer).
		Scan(&snap.Generation, &snap.NextSeq, &snap.EmittedHeight)
	if errors.Is(err, sql.ErrNoRows) {
		return snap, nil
	}
	if err != nil {
		return HostEventSnapshot{}, err
	}
	if s.betweenRead != nil {
		s.betweenRead()
	}
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COALESCE(MIN(seq), 0) FROM host_events WHERE writer = ?`), s.writer).Scan(&snap.OldestSeq); err != nil {
		return HostEventSnapshot{}, err
	}
	rows, err := tx.QueryContext(ctx, s.q(`SELECT seq, height, kind, epoch_index, observed_at_unix, payload FROM host_events WHERE writer = ? ORDER BY seq DESC LIMIT ?`), s.writer, hotTailRows)
	if err != nil {
		return HostEventSnapshot{}, err
	}
	tail, err := scanEvents(rows)
	_ = rows.Close()
	if err != nil {
		return HostEventSnapshot{}, err
	}
	for i, j := 0, len(tail)-1; i < j; i, j = i+1, j-1 {
		tail[i], tail[j] = tail[j], tail[i]
	}
	snap.Tail = tail
	snap.Empty = len(tail) == 0
	if snap.NextSeq == 0 {
		snap.NextSeq = 1
	}
	return snap, nil
}

func (s *dbStore) Head(ctx context.Context) (StoreHead, error) {
	if s.headErr != nil {
		return StoreHead{}, s.headErr
	}
	if s.waitCtx {
		<-ctx.Done()
		return StoreHead{}, ctx.Err()
	}
	tx, err := s.beginRead(ctx)
	if err != nil {
		return StoreHead{}, err
	}
	defer tx.Rollback()
	head := StoreHead{Empty: true}
	err = tx.QueryRowContext(ctx, s.q(`SELECT generation FROM host_event_feed WHERE writer = ?`), s.writer).Scan(&head.Generation)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return StoreHead{}, err
	}
	if s.betweenRead != nil {
		s.betweenRead()
	}
	err = tx.QueryRowContext(ctx, s.q(`SELECT seq, height FROM host_events WHERE writer = ? ORDER BY seq DESC LIMIT 1`), s.writer).Scan(&head.Seq, &head.Height)
	if errors.Is(err, sql.ErrNoRows) {
		return head, nil
	}
	if err != nil {
		return StoreHead{}, err
	}
	head.Empty = false
	return head, nil
}

// FeedHead is the last committed sequence and this writer's generation.
// empty is true when there are no rows. seq is then 0. generation can still
// be set after a reset that deleted the rows.
func (s *dbStore) FeedHead(ctx context.Context) (uint64, uint64, bool, error) {
	head, err := s.Head(ctx)
	if err != nil {
		return 0, 0, true, err
	}
	return head.Seq, head.Generation, head.Empty, nil
}

func (s *dbStore) Reset(ctx context.Context, generation uint64) (err error) {
	if generation == 0 {
		generation = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resetErr != nil {
		return s.resetErr
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
		if err != nil {
			err = s.lost(err)
		}
	}()
	if s.dialect == "postgres" {
		if _, err := tx.ExecContext(ctx, "SET LOCAL synchronous_commit = on"); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM host_events WHERE writer = ?`), s.writer); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO host_event_feed (writer, generation, next_seq, emitted_height) VALUES (?, ?, 1, 0)
		ON CONFLICT(writer) DO UPDATE SET generation = excluded.generation, next_seq = excluded.next_seq, emitted_height = excluded.emitted_height`), s.writer, generation); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.generation = generation
	return nil
}

func (s *dbStore) q(query string) string {
	if s.dialect != "postgres" {
		return query
	}
	n := 1
	var b strings.Builder
	for _, r := range query {
		if r == '?' {
			fmt.Fprintf(&b, "$%d", n)
			n++
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

const hostEventInsertBatch = 64

func (s *dbStore) insertEvents(ctx context.Context, tx *sql.Tx, height int64, epoch uint64, events []apiconfig.HostEvent) error {
	for start := 0; start < len(events); start += hostEventInsertBatch {
		end := start + hostEventInsertBatch
		if end > len(events) {
			end = len(events)
		}
		if err := s.insertEventBatch(ctx, tx, height, epoch, events[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (s *dbStore) insertEventBatch(ctx context.Context, tx *sql.Tx, height int64, epoch uint64, events []apiconfig.HostEvent) error {
	var b strings.Builder
	b.WriteString(`INSERT INTO host_events (writer, seq, height, kind, epoch_index, observed_at_unix, payload) VALUES `)
	args := make([]any, 0, len(events)*7)
	for i, ev := range events {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`(?, ?, ?, ?, ?, ?, ?)`)
		payload, err := marshalHostEvent(ev)
		if err != nil {
			return err
		}
		args = append(args, s.writer, ev.Seq, height, int32(ev.Kind), epoch, ev.ObservedAtUnix, payload)
	}
	s.insertStmts++
	_, err := tx.ExecContext(ctx, s.q(b.String()), args...)
	return err
}

func marshalHostEvent(ev apiconfig.HostEvent) ([]byte, error) {
	return proto.Marshal(hostEventProto(ev))
}

func unmarshalHostEvent(payload []byte, ev *apiconfig.HostEvent) error {
	msg := &gen.HostEvent{}
	if err := proto.Unmarshal(payload, msg); err != nil {
		return err
	}
	if msg.Escrow != nil {
		ev.Escrow = &apiconfig.EscrowPayload{
			EscrowID:    msg.Escrow.EscrowId,
			EpochIndex:  msg.Escrow.EpochIndex,
			ModelID:     msg.Escrow.ModelId,
			Creator:     msg.Escrow.Creator,
			Amount:      msg.Escrow.Amount,
			Settler:     msg.Escrow.Settler,
			TotalPayout: msg.Escrow.TotalPayout,
			Fees:        msg.Escrow.Fees,
			Remainder:   msg.Escrow.Remainder,
		}
	}
	if msg.Maintenance != nil {
		ev.Maintenance = &apiconfig.MaintenancePayload{
			ReservationID:  msg.Maintenance.ReservationId,
			Participant:    msg.Maintenance.Participant,
			StartHeight:    msg.Maintenance.StartHeight,
			DurationBlocks: msg.Maintenance.DurationBlocks,
			Reason:         msg.Maintenance.Reason,
		}
	}
	if msg.WarmKey != nil {
		ev.WarmKey = &apiconfig.WarmKeyPayload{
			Granter:       msg.WarmKey.Granter,
			Grantee:       msg.WarmKey.Grantee,
			GrantID:       msg.WarmKey.GrantId,
			Height:        msg.WarmKey.Height,
			ExpiresAtUnix: msg.WarmKey.ExpiresAtUnix,
			End:           msg.WarmKey.End,
		}
	}
	if msg.Block != nil {
		ev.Block = &apiconfig.BlockPayload{
			Height:           msg.Block.Height,
			Hash:             append([]byte(nil), msg.Block.Hash...),
			HeaderTimeUnixMs: msg.Block.HeaderTimeUnixMs,
			EpochIndex:       msg.Block.EpochIndex,
			Header:           append([]byte(nil), msg.Block.Header...),
		}
	}
	return nil
}

func hostEventProto(ev apiconfig.HostEvent) *gen.HostEvent {
	msg := &gen.HostEvent{
		Seq:            ev.Seq,
		Kind:           gen.HostEventKind(ev.Kind),
		ObservedAtUnix: ev.ObservedAtUnix,
	}
	if ev.Escrow != nil {
		msg.Escrow = &gen.EscrowPayload{
			EscrowId:    ev.Escrow.EscrowID,
			EpochIndex:  ev.Escrow.EpochIndex,
			ModelId:     ev.Escrow.ModelID,
			Creator:     ev.Escrow.Creator,
			Amount:      ev.Escrow.Amount,
			Settler:     ev.Escrow.Settler,
			TotalPayout: ev.Escrow.TotalPayout,
			Fees:        ev.Escrow.Fees,
			Remainder:   ev.Escrow.Remainder,
		}
	}
	if ev.Maintenance != nil {
		msg.Maintenance = &gen.MaintenancePayload{
			ReservationId:  ev.Maintenance.ReservationID,
			Participant:    ev.Maintenance.Participant,
			StartHeight:    ev.Maintenance.StartHeight,
			DurationBlocks: ev.Maintenance.DurationBlocks,
			Reason:         ev.Maintenance.Reason,
		}
	}
	if ev.WarmKey != nil {
		msg.WarmKey = &gen.WarmKeyPayload{
			Granter:       ev.WarmKey.Granter,
			Grantee:       ev.WarmKey.Grantee,
			GrantId:       ev.WarmKey.GrantID,
			Height:        ev.WarmKey.Height,
			ExpiresAtUnix: ev.WarmKey.ExpiresAtUnix,
			End:           ev.WarmKey.End,
		}
	}
	if ev.Block != nil {
		msg.Block = &gen.BlockPayload{
			Height:           ev.Block.Height,
			Hash:             append([]byte(nil), ev.Block.Hash...),
			HeaderTimeUnixMs: ev.Block.HeaderTimeUnixMs,
			EpochIndex:       ev.Block.EpochIndex,
			Header:           append([]byte(nil), ev.Block.Header...),
		}
	}
	return msg
}

func (s *dbStore) ph(n int) string {
	if s.dialect == "postgres" {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

func scanEvents(rows *sql.Rows) ([]apiconfig.HostEvent, error) {
	var out []apiconfig.HostEvent
	for rows.Next() {
		var (
			ev      apiconfig.HostEvent
			height  int64
			kind    int32
			epoch   int64
			payload []byte
		)
		if err := rows.Scan(&ev.Seq, &height, &kind, &epoch, &ev.ObservedAtUnix, &payload); err != nil {
			return nil, err
		}
		ev.Kind = apiconfig.HostEventKind(kind)
		if err := unmarshalHostEvent(payload, &ev); err != nil {
			return nil, err
		}
		_ = height
		_ = epoch
		out = append(out, ev)
	}
	return out, rows.Err()
}
