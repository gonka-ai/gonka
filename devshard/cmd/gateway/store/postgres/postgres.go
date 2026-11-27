// Package postgres is the gateway store in a dedicated Postgres database, reached through the libpq PG* variables. See ../README.md, "Postgres".
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"time"

	"common/storage/pgpool"
	"common/storage/pgtimeouts"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/store"
	"devshard/cmd/gateway/store/internal/rows"
	"devshard/logging"
)

var _ store.Backend = (*Backend)(nil)

// Options tunes one Open: ConnectionString is a libpq URL or keyword string (empty reads the PG* variables).
type Options struct {
	ConnectionString string
}

// Backend is the Postgres backend; Open wraps it with the ledger and owed-vote writers.
type Backend struct {
	database database
	retrier  rows.Retrier
}

// Open connects, checks the database is the gateway's own and applies the migrations; it fails closed, with no SQLite fallback.
func Open(ctx context.Context, options Options) (store.Store, error) {
	backend, err := openBackend(ctx, options)
	if err != nil {
		return nil, err
	}
	return store.New(backend), nil
}

func openBackend(ctx context.Context, options Options) (*Backend, error) {
	cfg, err := connectionConfig(options.ConnectionString)
	if err != nil {
		return nil, err
	}
	pool := openPool(cfg)
	bootCtx, cancel := context.WithTimeout(ctx, bootTimeout)
	defer cancel()
	if err := pool.PingContext(bootCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to the gateway Postgres database: %w", err)
	}
	if err := boot(bootCtx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Backend{
		database: database{pool: pool, operationTimeout: pgtimeouts.OperationTimeout()},
		retrier:  rows.Retrier{Retryable: isRetryable, Backoff: rows.DefaultRetryBackoff},
	}, nil
}

func openPool(cfg *pgxpool.Config) *sql.DB {
	pool := stdlib.OpenDB(*cfg.ConnConfig)
	pool.SetMaxOpenConns(int(cfg.MaxConns))
	pool.SetMaxIdleConns(int(cfg.MaxConns))
	return pool
}

func connectionConfig(connectionString string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, fmt.Errorf("reading Postgres settings: %w", err)
	}
	pgtimeouts.ApplyConnConfig(cfg.ConnConfig)
	fitHostsInOperation(cfg.ConnConfig, pgtimeouts.OperationTimeout())
	cfg.ConnConfig.RuntimeParams[synchronousCommitParam] = synchronousCommitOn
	cfg.ConnConfig.ValidateConnect = pgconn.ValidateConnectTargetSessionAttrsReadWrite
	if err := pgpool.ConfigureMaxConns(cfg); err != nil {
		return nil, err
	}
	if err := requireTLSForRemoteHosts(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// fitHostsInOperation shrinks the per-host connect timeout so that a host that never answers leaves the next host and the statement a share of the operation deadline, which the dial runs under. It counts one address per host name, as pgx gives each address its own timeout.
func fitHostsInOperation(config *pgx.ConnConfig, operation time.Duration) {
	hosts, last := 1, hostAddress(config.Host, config.Port)
	for _, fallback := range config.Fallbacks {
		if address := hostAddress(fallback.Host, fallback.Port); address != last {
			hosts, last = hosts+1, address
		}
	}
	if hosts > 1 && operation > 0 {
		config.ConnectTimeout = min(config.ConnectTimeout, operation/time.Duration(hosts+1))
	}
}

func hostAddress(host string, port uint16) string {
	return net.JoinHostPort(host, strconv.Itoa(int(port)))
}

// WithRetry retries a serialization failure, a deadlock or an error raised before anything reached the server. See ../README.md, "Writing under a lock".
func (s *Backend) WithRetry(ctx context.Context, operation func() error) error {
	return s.retrier.Do(ctx, operation)
}

// Close closes the connection pool.
func (s *Backend) Close() error {
	if err := s.database.pool.Close(); err != nil {
		return fmt.Errorf("closing gateway store: %w", err)
	}
	return nil
}

type database struct {
	pool             *sql.DB
	operationTimeout time.Duration
}

func (d database) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if d.operationTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d.operationTimeout)
}

func (d database) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	ctx, cancel := d.bound(ctx)
	defer cancel()
	return d.pool.ExecContext(ctx, query, args...)
}

func (d database) scanRow(ctx context.Context, query string, args []any, destinations ...any) error {
	ctx, cancel := d.bound(ctx)
	defer cancel()
	return d.pool.QueryRowContext(ctx, query, args...).Scan(destinations...)
}

func (d database) forEachRow(ctx context.Context, query string, args []any, scan func(*sql.Rows) error) error {
	ctx, cancel := d.bound(ctx)
	defer cancel()
	result, err := d.pool.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer result.Close()
	for result.Next() {
		if err := scan(result); err != nil {
			return err
		}
	}
	return result.Err()
}

func logNewerSchema(found, supported int) {
	if found > supported {
		logging.Warn("the Postgres schema is newer than this build knows; an older binary is running on it",
			logkey.Subsystem, "gateway", logkey.SchemaVersion, found, logkey.SupportedVersion, supported)
	}
}
