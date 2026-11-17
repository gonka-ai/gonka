package hostevents

import (
	"errors"
	"log/slog"
	"time"

	"common/nodemanager/gen"

	"devshard/bridge"
)

// Sink receives escrow and warm-key host-events from the long-poll loop.
type Sink interface {
	WarmEscrow(escrowID string) error
	OnEscrowSettled(escrowID string) error
	// OnWarmKeyEvent receives one warm-key log entry as the chain emitted
	// it: a grant, or an end of one. See docs/warm-keys-v7.md.
	OnWarmKeyEvent(entry bridge.WarmKeyEntry) error
	// RehydrateOpenEscrows runs on needs_reset: events may have been lost,
	// so sessions and the warm-key log copy are re-derived from the chain.
	RehydrateOpenEscrows()
}

// Clock abstracts time for tests.
type Clock interface {
	Now() time.Time
	Since(time.Time) time.Duration
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) Since(t time.Time) time.Duration        { return time.Since(t) }
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
	// MaxDispatchAttempts bounds how many times a single event is redelivered
	// after its sink call fails. Past the bound the loop skips it so one wedged
	// escrow cannot stall the whole stream.
	MaxDispatchAttempts int
	// LoadMap, when set and the subscribe set includes escrow kinds, is replaced
	// on every non-error GetHostEvents response, including an unchanged timeout,
	// with that response's escrow_load snapshot.
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
	if c.MaxDispatchAttempts <= 0 {
		c.MaxDispatchAttempts = 10
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
