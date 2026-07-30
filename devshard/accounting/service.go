package accounting

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

const DefaultSnapshotInterval = 5 * time.Minute

type Service struct {
	Book  *Book
	Store *Store

	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func OpenService(path string, retentionEpochs uint64, interval time.Duration) (*Service, error) {
	store, err := OpenStore(path, retentionEpochs)
	if err != nil {
		return nil, err
	}
	book, err := store.Load(context.Background())
	if err != nil {
		store.Close()
		return nil, err
	}
	if interval <= 0 {
		interval = DefaultSnapshotInterval
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		Book:   book,
		Store:  store,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go service.run(ctx, interval)
	return service, nil
}

func (s *Service) run(ctx context.Context, interval time.Duration) {
	defer close(s.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := s.Flush(ctx); err != nil {
				log.Printf("accounting snapshot: %v", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (s *Service) Flush(ctx context.Context) error {
	if s == nil || s.Store == nil || s.Book == nil {
		return errors.New("accounting service is unavailable")
	}
	return s.Store.Snapshot(ctx, s.Book)
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	var result error
	s.once.Do(func() {
		s.cancel()
		<-s.done
		if err := s.Flush(context.Background()); err != nil {
			result = err
		}
		if err := s.Store.Close(); err != nil && result == nil {
			result = err
		}
	})
	return result
}
