package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == serializationFailure || pgErr.Code == deadlockDetected
	}
	return pgconn.SafeToRetry(err)
}
