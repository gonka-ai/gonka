package validationpool

import (
	"os"
	"strconv"
	"time"
)

const (
	DefaultSoftMaxOut   = 128
	DefaultHAMultiplier = 1
	DefaultPendingHWM   = 4096
	DefaultDispatchers  = 2
	DefaultDeferDelay   = 2 * time.Second
)

// Config holds process-wide scheduler knobs. Zero values are replaced by defaults in New.
type Config struct {
	SoftMaxOut   int           // DEVSHARD_VALIDATION_SOFT_MAX_OUT
	HAMultiplier int           // DEVSHARD_VALIDATION_HA_MULTIPLIER
	PendingHWM   int           // DEVSHARD_VALIDATION_PENDING_HWM
	Dispatchers  int           // DEVSHARD_VALIDATION_DISPATCHERS
	DeferDelay   time.Duration // DEVSHARD_VALIDATION_DEFER_DELAY
	DeferJitter  time.Duration // DEVSHARD_VALIDATION_DEFER_JITTER; 0 means = DeferDelay; <0 disables jitter
}

// EffectiveSoftMaxOut is the per-process in-flight spawn cap.
func (c Config) EffectiveSoftMaxOut() int {
	soft := c.SoftMaxOut
	if soft <= 0 {
		soft = DefaultSoftMaxOut
	}
	ha := c.HAMultiplier
	if ha <= 0 {
		ha = DefaultHAMultiplier
	}
	if ha > 64 {
		ha = 64
	}
	n := soft / ha
	if n < 1 {
		return 1
	}
	return n
}

func (c Config) withDefaults() Config {
	if c.SoftMaxOut <= 0 {
		c.SoftMaxOut = DefaultSoftMaxOut
	}
	if c.HAMultiplier <= 0 {
		c.HAMultiplier = DefaultHAMultiplier
	}
	if c.HAMultiplier > 64 {
		c.HAMultiplier = 64
	}
	if c.PendingHWM <= 0 {
		c.PendingHWM = DefaultPendingHWM
	}
	if c.Dispatchers <= 0 {
		c.Dispatchers = DefaultDispatchers
	}
	if c.Dispatchers > 4 {
		c.Dispatchers = 4
	}
	if c.DeferDelay <= 0 {
		c.DeferDelay = DefaultDeferDelay
	}
	switch {
	case c.DeferJitter < 0:
		c.DeferJitter = 0 // explicit disable (tests / env "-1ns")
	case c.DeferJitter == 0:
		c.DeferJitter = c.DeferDelay
	}
	return c
}

// ConfigFromEnv loads Config from environment variables (missing → defaults).
func ConfigFromEnv() Config {
	c := Config{
		SoftMaxOut:   envInt("DEVSHARD_VALIDATION_SOFT_MAX_OUT", DefaultSoftMaxOut),
		HAMultiplier: envInt("DEVSHARD_VALIDATION_HA_MULTIPLIER", DefaultHAMultiplier),
		PendingHWM:   envInt("DEVSHARD_VALIDATION_PENDING_HWM", DefaultPendingHWM),
		Dispatchers:  envInt("DEVSHARD_VALIDATION_DISPATCHERS", DefaultDispatchers),
		DeferDelay:   envDuration("DEVSHARD_VALIDATION_DEFER_DELAY", DefaultDeferDelay),
	}
	if v := os.Getenv("DEVSHARD_VALIDATION_DEFER_JITTER"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.DeferJitter = d
		}
	}
	return c.withDefaults()
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
