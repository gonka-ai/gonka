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
//
// Reserve puts a hold on the oldest live credit; a second replica cannot take
// a held credit. The holder renews the hold while its ML call runs and then
// spends or refunds it. A hold that is not renewed lapses, so a credit held by
// a replica that died becomes spendable again. Renew, Spend and Refund act only
// while the caller's hold token is still on the row.
type ValidationCreditStore interface {
	EarnValidationCredit(ctx context.Context, participant, model string, ttl time.Duration) error
	ValidationCreditAvailable(ctx context.Context, participant, model string) (bool, error)
	ReserveValidationCredit(ctx context.Context, participant, model string, hold time.Duration) (ValidationCreditHold, bool, error)
	RenewValidationCredit(ctx context.Context, h ValidationCreditHold, hold time.Duration) (bool, error)
	SpendValidationCredit(ctx context.Context, h ValidationCreditHold) error
	RefundValidationCredit(ctx context.Context, h ValidationCreditHold) error
}

// ValidationCreditHold identifies one reserved credit row and the hold on it.
type ValidationCreditHold struct {
	ID    int64
	Token string
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
		      AND (reserved_until IS NULL OR reserved_until <= now())
		 )`,
		participant, model,
	).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("validation credits: available %s/%s: %w", participant, model, err)
	}
	return ok, nil
}

func (s *Postgres) ReserveValidationCredit(ctx context.Context, participant, model string, hold time.Duration) (ValidationCreditHold, bool, error) {
	if err := s.WaitReady(ctx); err != nil {
		return ValidationCreditHold{}, false, err
	}
	var h ValidationCreditHold
	err := s.pool.QueryRow(ctx,
		`UPDATE devshard_validation_credits
		 SET hold_token = gen_random_uuid(),
		     reserved_until = now() + make_interval(secs => $3)
		 WHERE id = (
		     SELECT id FROM devshard_validation_credits
		     WHERE participant = $1 AND model = $2 AND expires_at > now()
		       AND (reserved_until IS NULL OR reserved_until <= now())
		     ORDER BY expires_at, id
		     FOR UPDATE SKIP LOCKED
		     LIMIT 1
		 )
		 RETURNING id, hold_token::text`,
		participant, model, hold.Seconds(),
	).Scan(&h.ID, &h.Token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ValidationCreditHold{}, false, nil
		}
		return ValidationCreditHold{}, false, fmt.Errorf("validation credits: reserve %s/%s: %w", participant, model, err)
	}
	return h, true, nil
}

// RenewValidationCredit extends the hold. It reports false once the hold is
// gone: the credit was spent, refunded, or taken by another replica after the
// hold lapsed.
func (s *Postgres) RenewValidationCredit(ctx context.Context, h ValidationCreditHold, hold time.Duration) (bool, error) {
	if err := s.WaitReady(ctx); err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE devshard_validation_credits
		 SET reserved_until = now() + make_interval(secs => $3)
		 WHERE id = $1 AND hold_token = $2::uuid`,
		h.ID, h.Token, hold.Seconds(),
	)
	if err != nil {
		return false, fmt.Errorf("validation credits: renew %d: %w", h.ID, err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Postgres) SpendValidationCredit(ctx context.Context, h ValidationCreditHold) error {
	if err := s.WaitReady(ctx); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM devshard_validation_credits
		 WHERE id = $1 AND hold_token = $2::uuid`,
		h.ID, h.Token,
	); err != nil {
		return fmt.Errorf("validation credits: spend %d: %w", h.ID, err)
	}
	return nil
}

func (s *Postgres) RefundValidationCredit(ctx context.Context, h ValidationCreditHold) error {
	if err := s.WaitReady(ctx); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE devshard_validation_credits
		 SET hold_token = NULL, reserved_until = NULL
		 WHERE id = $1 AND hold_token = $2::uuid`,
		h.ID, h.Token,
	); err != nil {
		return fmt.Errorf("validation credits: refund %d: %w", h.ID, err)
	}
	return nil
}
