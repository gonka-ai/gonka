package store

import (
	"errors"
	"sync"
	"time"
)

type composed struct {
	Backend

	mu        sync.Mutex
	ledger    *Ledger
	owedVotes *OwedVotes
}

// New wraps a backend with the ledger and owed-vote writers; Close drains both before the backend closes.
func New(backend Backend) Store {
	return &composed{Backend: backend}
}

// NewLedger registers the writer, whose drain Close waits for before the database handle goes.
func (s *composed) NewLedger(retention Retention, now func() time.Time) (*Ledger, error) {
	if err := validateLedger(retention, now); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ledger != nil && !s.ledger.isClosed() {
		return nil, errors.New("accounting ledger: already open")
	}
	ledger := newLedger(s.Backend, retention, now)
	s.ledger = ledger
	go ledger.run()
	return ledger, nil
}

// NewOwedVotes registers the writer, whose drain Close waits for before the database handle goes.
func (s *composed) NewOwedVotes() (*OwedVotes, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owedVotes != nil && !s.owedVotes.isClosed() {
		return nil, errors.New("owed votes: already open")
	}
	owedVotes := newOwedVotes(s.Backend)
	s.owedVotes = owedVotes
	go owedVotes.run()
	return owedVotes, nil
}

// Close drains the accounting ledger and the owed votes first, so no queued row outlives the connection it needs.
func (s *composed) Close() error {
	s.mu.Lock()
	ledger, owedVotes := s.ledger, s.owedVotes
	s.mu.Unlock()
	var ledgerErr error
	if ledger != nil {
		ledgerErr = ledger.Close()
	}
	if owedVotes != nil {
		owedVotes.drain()
	}
	return errors.Join(ledgerErr, s.Backend.Close())
}
