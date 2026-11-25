package accounting

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"

	_ "modernc.org/sqlite"
)

// Pragmas ride the connection string; one connection. See README.md, "Storage".
const connectionPragmas = "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"

type Store struct {
	db      *sql.DB
	writing sync.Mutex
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+connectionPragmas)
	if err != nil {
		return nil, fmt.Errorf("opening accounting store %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := discardOutdatedSchema(db); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, errors.Join(fmt.Errorf("creating accounting schema: %w", err), db.Close())
	}
	if err := addMissingColumns(db); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := discardUntrustedFirstContent(db); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
