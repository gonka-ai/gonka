package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrOutOfOrder is returned when migration step IDs are not strictly increasing.
var ErrOutOfOrder = errors.New("migrate: step IDs must be strictly increasing")

// Step is one forward-only schema change applied in order.
type Step struct {
	ID   int
	Name string
	// SQL is split on ';' and each non-empty statement is executed in order within
	// the step transaction. Use IF NOT EXISTS / ADD COLUMN patterns so re-runs
	// are safe when a step is retried after a failed commit.
	// When SQLiteRun is set, it runs instead of SQL on SQLite (for conditional DDL).
	SQL string
	// SQLiteRun is optional SQLite-only logic executed inside the step transaction.
	SQLiteRun func(ctx context.Context, tx *sql.Tx) error
}

func validateSteps(steps []Step) error {
	if len(steps) == 0 {
		return nil
	}
	prev := 0
	for _, s := range steps {
		if s.ID <= 0 {
			return fmt.Errorf("migrate: step %q has invalid ID %d", s.Name, s.ID)
		}
		if s.ID <= prev {
			return ErrOutOfOrder
		}
		prev = s.ID
	}
	return nil
}

func splitSQL(sql string) []string {
	parts := strings.Split(sql, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
