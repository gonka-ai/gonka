package sqlite

import (
	"errors"

	modernc "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func isLockedError(err error) bool {
	var sqliteErr *modernc.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code() & retryPrimaryCodeMask
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
}
