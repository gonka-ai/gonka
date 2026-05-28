package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrOutOfOrder is returned when migration step IDs are not strictly increasing.
var ErrOutOfOrder = errors.New("migrate: step IDs must be strictly increasing")

// Step is one forward-only schema change applied in order.
//
// Each entry in Statements is executed as exactly one SQL statement inside the
// step's transaction. Statements MUST NOT be split by semicolons by callers —
// list each statement on its own. This intentionally avoids any in-process SQL
// parser so the framework never has to reason about semicolons in string
// literals, comments, trigger/function bodies, or driver-specific multi-query
// semantics.
//
// Use IF NOT EXISTS / ADD COLUMN patterns so re-runs are safe when a step is
// retried after a failed commit. When SQLiteRun is set, it runs instead of
// Statements on SQLite (for conditional DDL).
type Step struct {
	ID         int
	Name       string
	Statements []string
	// SQLiteRun is optional SQLite-only logic executed inside the step transaction.
	// When set, Statements is ignored on SQLite.
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
