package devshardobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionVersionLookup resolves which protocol version owns a bound escrow.
// ok=false means no active session row in Postgres (may still exist on SQLite).
type SessionVersionLookup interface {
	LookupSessionVersion(ctx context.Context, escrowID string) (version string, ok bool, err error)
}

const (
	sessionDBConnectTimeout = 5 * time.Second
	// How long obs skips the session DB after a failure. Keeps a down database
	// from adding a dial attempt to every session-scoped request, and retries
	// often enough that routing precision returns shortly after recovery.
	sessionDBRetryInterval = 15 * time.Second
)

// errSessionDBUnavailable marks a lookup skipped because the session DB was
// unreachable recently. The handler treats it like any lookup failure: fan out.
var errSessionDBUnavailable = errors.New("session DB unavailable; retrying later")

// Lookup resolves escrow → bound protocol version from shared Postgres session
// metadata (devshard_session_index + devshard_sessions).
//
// A Lookup stays usable while the session DB is down: failures put it on a
// cooldown during which every call reports unavailability so obs fans out, and
// the first call after the cooldown probes Postgres again.
type Lookup struct {
	pool *pgxpool.Pool

	mu              sync.Mutex
	unavailableTill time.Time
}

// OpenLookupFromEnv connects to the devshard session database for obs routing.
//
// Configured by dedicated session-DB env only (never dapi payload PG*), either:
//  1. DEVSHARD_OBS_DATABASE_URL (fields it omits come from DEVSHARD_OBS_PG*), or
//  2. DEVSHARD_OBS_PGHOST + DEVSHARD_OBS_PGPORT + DEVSHARD_OBS_PGDATABASE +
//     DEVSHARD_OBS_PGUSER (+ DEVSHARD_OBS_PGPASSWORD)
//
// There are no built-in host/port/database/user values: compose owns those, and
// an incomplete session config is reported instead of guessed. Join compose
// defaults PGDATABASE=payloads with empty PGHOST on dapi; local-test-net often
// sets PGHOST to the payload DB. Neither is the session index database, so
// libpq PG* must not enable or fill in the lookup.
//
// Returns (nil, nil) when Postgres is not configured or lookup is disabled, so
// an absent DEVSHARD_OBS_PGHOST simply means fan-out. An unreachable session DB
// yields a Lookup on cooldown, which also fans out but recovers by itself.
// Only unusable config returns an error, which OpenFromEnv degrades to fan-out.
func OpenLookupFromEnv(ctx context.Context) (*Lookup, error) {
	if lookupDisabled() {
		slog.Info("devshardobs: session version lookup disabled")
		return nil, nil
	}
	if !postgresConfigured() {
		slog.Info("devshardobs: session version lookup disabled (no DEVSHARD_OBS_PGHOST/DEVSHARD_OBS_DATABASE_URL); fan-out only")
		return nil, nil
	}

	cfg, err := lookupPoolConfig()
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 4
	cfg.MinConns = 0
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.ConnConfig.ConnectTimeout = sessionDBConnectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}

	lookup := &Lookup{pool: pool}
	pingCtx, cancel := context.WithTimeout(ctx, sessionDBConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		// Keep the pool: obs serves fan-out meanwhile and picks the lookup back
		// up on its own once the session DB accepts connections again.
		lookup.noteUnavailable()
		slog.Warn("devshardobs: session DB unreachable; serving fan-out until it recovers",
			"host", cfg.ConnConfig.Host,
			"database", cfg.ConnConfig.Database,
			"retry_in", sessionDBRetryInterval.String(),
			"error", err,
		)
		return lookup, nil
	}
	slog.Info("devshardobs: session version lookup enabled (postgres)")
	return lookup, nil
}

func lookupDisabled() bool {
	v := os.Getenv("DEVSHARD_OBS_DISABLE_SESSION_LOOKUP")
	if v == "" {
		v = os.Getenv("VERSIOND_DISABLE_SESSION_LOOKUP")
	}
	return strings.EqualFold(v, "true") || v == "1"
}

// postgresConfigured reports whether the dedicated session-DB env is present.
// Payload-oriented PGHOST/PGDATABASE/DATABASE_URL do not count.
func postgresConfigured() bool {
	return strings.TrimSpace(os.Getenv("DEVSHARD_OBS_DATABASE_URL")) != "" ||
		strings.TrimSpace(os.Getenv("DEVSHARD_OBS_PGHOST")) != ""
}

func lookupPoolConfig() (*pgxpool.Config, error) {
	if raw := strings.TrimSpace(os.Getenv("DEVSHARD_OBS_DATABASE_URL")); raw != "" {
		cfg, err := pgxpool.ParseConfig(raw)
		if err != nil {
			return nil, fmt.Errorf("parse DEVSHARD_OBS_DATABASE_URL: %w", err)
		}
		if err := pinSessionIdentity(cfg, dsnHasField(raw, "user"), dsnHasField(raw, "password"), dsnHasField(raw, "dbname")); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	host := strings.TrimSpace(os.Getenv("DEVSHARD_OBS_PGHOST"))
	if host == "" {
		return nil, fmt.Errorf("DEVSHARD_OBS_PGHOST unset")
	}
	database, err := requireEnv("DEVSHARD_OBS_PGDATABASE")
	if err != nil {
		return nil, err
	}
	user, err := requireEnv("DEVSHARD_OBS_PGUSER")
	if err != nil {
		return nil, err
	}
	port, err := requireEnv("DEVSHARD_OBS_PGPORT")
	if err != nil {
		return nil, err
	}
	dsn := buildLibpqDSN(host, port, database, user, os.Getenv("DEVSHARD_OBS_PGPASSWORD"))
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DEVSHARD_OBS_PG* config: %w", err)
	}
	// An empty DEVSHARD_OBS_PGPASSWORD leaves the DSN without one, which pgx
	// would take from payload PGPASSWORD; pin it to the session env instead.
	return cfg, pinSessionIdentity(cfg, true, false, true)
}

// pinSessionIdentity fills the fields the DSN left unset from the session env,
// because pgx otherwise takes them from libpq PG* — that is, dapi's payload
// database and credentials. The session index is a different database and must
// never be reached with borrowed payload settings, so an unset field is an
// error rather than a guess.
func pinSessionIdentity(cfg *pgxpool.Config, hasUser, hasPassword, hasDatabase bool) error {
	if !hasUser {
		user, err := requireEnv("DEVSHARD_OBS_PGUSER")
		if err != nil {
			return err
		}
		cfg.ConnConfig.User = user
	}
	if !hasPassword {
		cfg.ConnConfig.Password = os.Getenv("DEVSHARD_OBS_PGPASSWORD")
	}
	if !hasDatabase {
		database, err := requireEnv("DEVSHARD_OBS_PGDATABASE")
		if err != nil {
			return err
		}
		cfg.ConnConfig.Database = database
	}
	return nil
}

// requireEnv reads a session-DB setting that has no safe default: guessing one
// risks pointing the lookup at the payload database.
func requireEnv(key string) (string, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return "", fmt.Errorf("%s unset (session DB config is env-only; set it or unset DEVSHARD_OBS_PGHOST to run fan-out only)", key)
	}
	return v, nil
}

// dsnHasField reports whether raw itself carries the given libpq field, so the
// caller knows not to overwrite it. Handles URL and keyword/value DSNs.
func dsnHasField(raw, field string) bool {
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		switch field {
		case "user":
			return u.User != nil && u.User.Username() != ""
		case "password":
			if u.User == nil {
				return false
			}
			_, ok := u.User.Password()
			return ok
		case "dbname":
			return strings.Trim(u.Path, "/") != ""
		}
	}
	return keywordValueDSNField(raw, field) != ""
}

// keywordValueDSNField reads a key from a libpq keyword/value DSN
// ("host=… dbname=…"). Quoted values are not supported; absent → "".
func keywordValueDSNField(raw, key string) string {
	for _, field := range strings.Fields(raw) {
		k, v, ok := strings.Cut(field, "=")
		if ok && k == key {
			return v
		}
	}
	return ""
}

func buildLibpqDSN(host, port, database, user, password string) string {
	u := &url.URL{
		Scheme: "postgres",
		Host:   host,
		Path:   "/" + strings.TrimPrefix(database, "/"),
	}
	if port != "" && !strings.Contains(host, ":") {
		u.Host = host + ":" + port
	}
	switch {
	case user != "" && password != "":
		u.User = url.UserPassword(user, password)
	case user != "":
		u.User = url.User(user)
	}
	return u.String()
}

// Close releases the pool.
func (l *Lookup) Close() {
	if l != nil && l.pool != nil {
		l.pool.Close()
	}
}

// noteUnavailable starts a cooldown during which lookups are skipped.
func (l *Lookup) noteUnavailable() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.unavailableTill = time.Now().Add(sessionDBRetryInterval)
}

// noteAvailable ends the cooldown after a successful query.
func (l *Lookup) noteAvailable() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.unavailableTill = time.Time{}
}

func (l *Lookup) inCooldown() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return time.Now().Before(l.unavailableTill)
}

// LookupSessionVersion implements SessionVersionLookup.
func (l *Lookup) LookupSessionVersion(ctx context.Context, escrowID string) (string, bool, error) {
	if l == nil || l.pool == nil {
		return "", false, nil
	}
	if escrowID == "" {
		return "", false, nil
	}
	if l.inCooldown() {
		return "", false, errSessionDBUnavailable
	}
	qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var version *string
	err := l.pool.QueryRow(qctx, `
		SELECT s.version
		FROM devshard_session_index i
		JOIN devshard_sessions s
		  ON s.epoch_id = i.epoch_id AND s.escrow_id = i.escrow_id
		WHERE i.escrow_id = $1
		  AND s.status = 'active'
		LIMIT 1`, escrowID).Scan(&version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			l.noteAvailable()
			return "", false, nil
		}
		l.noteUnavailable()
		return "", false, err
	}
	l.noteAvailable()
	if version == nil || *version == "" {
		return "", false, nil
	}
	return *version, true, nil
}
