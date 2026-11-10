// Package pgpool applies the shared Postgres pool cap.
//
// pgx otherwise sizes a pool to max(4, runtime.NumCPU). Each HA devshard
// version opens its own pool, so a CPU-sized payload pool exhausts
// max_connections before the workload needs that many backends.
package pgpool

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultMaxConns is the pool size used when PG_POOL_MAX_CONNS is unset.
// Session storage and payload storage share this one cap. Split them only
// when payload reads are waiting on it.
const DefaultMaxConns int32 = 4

// ConfigureMaxConns sets cfg.MaxConns from PG_POOL_MAX_CONNS, or DefaultMaxConns
// when that variable is unset. A non-positive or non-integer value is an error.
func ConfigureMaxConns(cfg *pgxpool.Config) error {
	raw := strings.TrimSpace(os.Getenv("PG_POOL_MAX_CONNS"))
	if raw == "" {
		cfg.MaxConns = DefaultMaxConns
		return nil
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value <= 0 {
		return fmt.Errorf("PG_POOL_MAX_CONNS must be a positive integer, got %q", raw)
	}
	cfg.MaxConns = int32(value)
	return nil
}
