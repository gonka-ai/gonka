package hostevents

import (
	"errors"
	"log/slog"
	"time"

	"devshard/nodemanager/gen"
)

// Sink receives escrow host-events from the long-poll loop.
type Sink interface {
	WarmEscrow(escrowID string) error
	OnEscrowSettled(escrowID string) error
	RehydrateOpenEscrows()
}

// Clock abstracts time for tests.
type Clock interface {
	Now() time.Time
	Since(time.Time) time.Duration
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                          { return time.Now() }
func (realClock) Since(t time.Time) time.Duration         { return time.Since(t) }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Config configures the GetHostEvents long-poll consumer.
type Config struct {
	Client              gen.NodeManagerClient
	ServerMaxWait       time.Duration
	ClientDeadlineSlack time.Duration
	ErrorBackoffMin     time.Duration
	ErrorBackoffMax     time.Duration
	Log                 *slog.Logger
	Clock               Clock
	// LoadMap, when set, is updated on every non-error GetHostEvents response
	// (including unchanged / timeout) with the response's escrow_load snapshot.
	LoadMap *LoadMap
}

func (c *Config) applyDefaults() error {
	if c.Client == nil {
		return errors.New("hostevents: NodeManagerClient is required")
	}
	if c.ServerMaxWait <= 0 {
		c.ServerMaxWait = 60 * time.Second
	}
	if c.ClientDeadlineSlack <= 0 {
		c.ClientDeadlineSlack = 5 * time.Second
	}
	if c.ErrorBackoffMin <= 0 {
		c.ErrorBackoffMin = time.Second
	}
	if c.ErrorBackoffMax <= 0 {
		c.ErrorBackoffMax = 10 * time.Second
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Clock == nil {
		c.Clock = realClock{}
	}
	return nil
}

func (c *Config) clientCallDeadline() time.Duration {
	return c.ServerMaxWait + c.ClientDeadlineSlack
}

func nextBackoff(cur, min, max time.Duration) time.Duration {
	if cur <= 0 {
		return min
	}
	next := cur * 2
	if next > max {
		return max
	}
	return next
}
