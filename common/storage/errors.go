package storage

import (
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("storage: not found")

func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
