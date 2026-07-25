package client

import (
	"errors"
	"log/slog"
	"time"

	"common/nodemanager/gen"
)

const defaultLogprobsMode = "processed"

// Config configures the gRPC long-poll runtime config provider.
type Config struct {
	Client gen.NodeManagerClient

	ServerMaxWait        time.Duration
	ClientDeadlineSlack  time.Duration
	ErrorBackoffMin      time.Duration
	ErrorBackoffMax      time.Duration
	UnchangedRetryFloor  *time.Duration
	Defaults             Snapshot
	Availability         AvailabilitySink
	Log                  *slog.Logger
	Clock                Clock
}

func (c *Config) applyDefaults() error {
	if c.Client == nil {
		return errors.New("runtimeconfig: NodeManagerClient is required")
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
	if c.UnchangedRetryFloor == nil {
		floor := 30 * time.Second
		c.UnchangedRetryFloor = &floor
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Clock == nil {
		c.Clock = realClock{}
	}
	if c.Defaults.LogprobsMode == "" {
		c.Defaults.LogprobsMode = defaultLogprobsMode
	}
	return nil
}

func (c *Config) clientCallDeadline() time.Duration {
	return c.ServerMaxWait + c.ClientDeadlineSlack
}

func (c *Config) unchangedRetryFloor() time.Duration {
	return *c.UnchangedRetryFloor
}
