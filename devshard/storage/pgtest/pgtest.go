// Package pgtest starts an isolated Postgres testcontainer.
//
// go test ./... runs packages in parallel processes, and each storage test
// used to call postgres.Run at once. Docker then timed out inspecting the
// container (empty mapped port, docker.sock deadline) — the migration
// assertions never ran. A process-wide flock serializes starts; a few retries
// cover a busy daemon. Failed attempts terminate the container they created;
// a missing Docker host is an error (and a skip via MustStart), not a panic.
package pgtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	image          = "postgres:18.1-bookworm"
	startAttempts  = 3
	startupTimeout = 2 * time.Minute
	lockWait       = 3 * time.Minute
	terminateWait  = 15 * time.Second
)

// ErrDockerUnavailable is returned when testcontainers panics because it
// cannot reach a Docker host (missing socket, permission denied).
var ErrDockerUnavailable = errors.New("docker host unavailable")

func waitStrategy() wait.Strategy {
	return wait.ForAll(
		wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2),
		wait.ForListeningPort("5432/tcp"),
	).WithStartupTimeout(startupTimeout)
}

// MustStart is Run, skipping the test when Docker is not reachable.
func MustStart(t testing.TB, ctx context.Context) *postgres.PostgresContainer {
	t.Helper()
	container, err := Run(ctx)
	if errors.Is(err, ErrDockerUnavailable) {
		t.Skip(err.Error())
	}
	if err != nil {
		t.Fatalf("pgtest.Run: %v", err)
	}
	return container
}

// Run starts postgres:18.1-bookworm with testdb/testuser/testpass.
func Run(ctx context.Context) (*postgres.PostgresContainer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	unlock, err := lockStart(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	var last error
	for attempt := 1; attempt <= startAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		container, err := runOnce(ctx)
		if err == nil {
			return container, nil
		}
		terminate(container)
		last = err
		if errors.Is(err, ErrDockerUnavailable) {
			return nil, err
		}
		if attempt == startAttempts {
			break
		}
		if sleepErr := sleepContext(ctx, time.Duration(attempt)*2*time.Second); sleepErr != nil {
			return nil, fmt.Errorf("run postgres: %w (last: %v)", sleepErr, last)
		}
	}
	return nil, fmt.Errorf("run postgres after %d attempts: %w", startAttempts, last)
}

func runOnce(ctx context.Context) (container *postgres.PostgresContainer, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			terminate(container)
			container = nil
			err = fmt.Errorf("%w: %v", ErrDockerUnavailable, rec)
		}
	}()
	return postgres.Run(ctx,
		image,
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(waitStrategy()),
	)
}

func terminate(c *postgres.PostgresContainer) {
	if c == nil {
		return
	}
	termCtx, cancel := context.WithTimeout(context.Background(), terminateWait)
	defer cancel()
	_ = c.Terminate(termCtx)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func lockStart(ctx context.Context) (unlock func(), err error) {
	path := filepath.Join(os.TempDir(), "devshard-testcontainers-pg.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open postgres testcontainer lock: %w", err)
	}

	deadline := time.Now().Add(lockWait)
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("wait for postgres testcontainer lock: %w", err)
		}
		lockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if lockErr == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("wait for postgres testcontainer lock: %w", lockErr)
		}
		if err := sleepContext(ctx, 200*time.Millisecond); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("wait for postgres testcontainer lock: %w", err)
		}
	}
}
