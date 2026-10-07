package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ValidationCreditStore is the shared balance of validation credits for one
// participant. HA replicas of the same key spend from this table, so a credit
// earned by the replica that executed survives that process stopping.
// Reserve takes the oldest live credit; a second replica cannot take the same
// one.
type ValidationCreditStore interface {
	EarnValidationCredit(ctx context.Context, participant, model string, ttl time.Duration) error
	ValidationCreditAvailable(ctx context.Context, participant, model string) (bool, error)
	ReserveValidationCredit(ctx context.Context, participant, model string) (expiresAt time.Time, ok bool, err error)
	RefundValidationCredit(ctx context.Context, participant, model string, expiresAt time.Time) error
}

// AsValidationCreditStore finds the Postgres credit table behind a storage
// wrapper. SQLite has no shared table; callers keep the process-local budget.
func AsValidationCreditStore(s Storage) (ValidationCreditStore, bool) {
	if s == nil {
		return nil, false
	}
	if c, ok := s.(ValidationCreditStore); ok {
		return c, true
	}
	switch v := s.(type) {
	case *ManagedStorage:
		return AsValidationCreditStore(v.inner)
	case *HybridStorage:
		v.mu.RLock()
		pg := v.pg
		v.mu.RUnlock()
		return AsValidationCreditStore(pg)
	default:
		return nil, false
	}
}

func (s *Postgres) EarnValidationCredit(ctx context.Context, participant, model string, ttl time.Duration) error {
	if err := s.WaitReady(ctx); err != nil {
		return err
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM devshard_validation_credits
		 WHERE participant = $1 AND model = $2 AND expires_at <= now()`,
		participant, model,
	); err != nil {
		return fmt.Errorf("validation credits: drop expired %s/%s: %w", participant, model, err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO devshard_validation_credits (participant, model, expires_at)
		 VALUES ($1, $2, now() + make_interval(secs => $3))`,
		participant, model, ttl.Seconds(),
	); err != nil {
		return fmt.Errorf("validation credits: earn %s/%s: %w", participant, model, err)
	}
	return nil
}

func (s *Postgres) ValidationCreditAvailable(ctx context.Context, participant, model string) (bool, error) {
	if err := s.WaitReady(ctx); err != nil {
		return false, err
	}
	var ok bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (
		    SELECT 1 FROM devshard_validation_credits
		    WHERE participant = $1 AND model = $2 AND expires_at > now()
		 )`,
		participant, model,
	).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("validation credits: available %s/%s: %w", participant, model, err)
	}
	return ok, nil
}

func (s *Postgres) ReserveValidationCredit(ctx context.Context, participant, model string) (time.Time, bool, error) {
	if err := s.WaitReady(ctx); err != nil {
		return time.Time{}, false, err
	}
	var expiresAt time.Time
	err := s.pool.QueryRow(ctx,
		`DELETE FROM devshard_validation_credits
		 WHERE id = (
		     SELECT id FROM devshard_validation_credits
		     WHERE participant = $1 AND model = $2 AND expires_at > now()
		     ORDER BY expires_at, id
		     FOR UPDATE SKIP LOCKED
		     LIMIT 1
		 )
		 RETURNING expires_at`,
		participant, model,
	).Scan(&expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("validation credits: reserve %s/%s: %w", participant, model, err)
	}
	return expiresAt, true, nil
}

func (s *Postgres) RefundValidationCredit(ctx context.Context, participant, model string, expiresAt time.Time) error {
	if err := s.WaitReady(ctx); err != nil {
		return err
	}
	if !expiresAt.After(time.Now()) {
		return nil
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO devshard_validation_credits (participant, model, expires_at)
		 VALUES ($1, $2, $3)`,
		participant, model, expiresAt,
	); err != nil {
		return fmt.Errorf("validation credits: refund %s/%s: %w", participant, model, err)
	}
	return nil
}
