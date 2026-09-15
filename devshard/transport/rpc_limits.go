package transport

import (
	"math"
	"os"
	"strconv"
	"strings"

	"devshard/logging"
)

const (
	// DefaultRPCMessagesPerMin is the per-peer weight budget advertised and
	// enforced after Attach.
	DefaultRPCMessagesPerMin uint32 = 6000
	// DefaultRPCMessagesBurst is the peer-bucket pulse (10% of the minute).
	DefaultRPCMessagesBurst = DefaultRPCMessagesPerMin / 10
	// DefaultRPCMaxStreams is the per-peer concurrent Watch/Chat cap.
	DefaultRPCMaxStreams uint32 = 256
	// DefaultRPCAttachFloorPerMin is the process-wide Attach floor (before ECDSA).
	DefaultRPCAttachFloorPerMin = 10_000
	// DefaultRPCLimiterMaxEntries is the per-map cap on distinct peer keys.
	// Idle buckets older than a minute are evicted first. Peer maps then
	// refuse a new key (finding 18: advertised rate stays the named bucket).
	// Stream maps refuse new peers instead of clearing.
	DefaultRPCLimiterMaxEntries = 100_000
)

const (
	envRPCLimitsOff         = "DEVSHARD_RPC_LIMITS"
	envRPCMsgsPerMin        = "DEVSHARD_RPC_MSGS_PER_MIN"
	envRPCMsgsBurst         = "DEVSHARD_RPC_MSGS_BURST"
	envRPCMaxStreams        = "DEVSHARD_RPC_MAX_STREAMS_PER_PEER"
	envRPCAttachPerMinTotal = "DEVSHARD_RPC_ATTACH_PER_MIN_TOTAL"
)

// UnlimitedRPCLimit is the sentinel for "do not enforce this cap".
const UnlimitedRPCLimit uint32 = math.MaxUint32

// ChannelLimitConfig is the env-resolved channel budget advertised on Attach
// and enforced by the Connect interceptor. Zero fields mean defaults.
type ChannelLimitConfig struct {
	Disabled bool
	// MessagesPerMin is the per-peer weight budget. UnlimitedRPCLimit disables it.
	MessagesPerMin uint32
	// MessagesBurst is tokens available at rest. Zero means 10% of MessagesPerMin.
	MessagesBurst uint32
	MaxStreams    uint32
	// AttachFloorPerMin is process-wide. Zero means DefaultRPCAttachFloorPerMin.
	AttachFloorPerMin int
	// MaxEntries caps distinct keys per limiter map. Zero means
	// DefaultRPCLimiterMaxEntries. Tests lower it; production does not
	// advertise it. Peer maps refuse a new key at cap.
	MaxEntries int
}

// LoadChannelLimitConfig reads DEVSHARD_RPC_* limit env vars. Unset →
// default (no log). "0", invalid, or the numeric UnlimitedRPCLimit sentinel
// (4294967295) → default and a warn. "-1" → unlimited for that cap.
// DEVSHARD_RPC_LIMITS=off disables all interceptor buckets and the Attach
// process floor. Per-IP Attach is not configured here; it belongs on
// versiond / Phase 6 proxy (`src`).
func LoadChannelLimitConfig() ChannelLimitConfig {
	cfg := ChannelLimitConfig{
		Disabled:          strings.EqualFold(strings.TrimSpace(os.Getenv(envRPCLimitsOff)), "off"),
		MessagesPerMin:    parseRPCLimit(envRPCMsgsPerMin, DefaultRPCMessagesPerMin),
		MessagesBurst:     parseRPCLimit(envRPCMsgsBurst, 0),
		MaxStreams:        parseRPCLimit(envRPCMaxStreams, DefaultRPCMaxStreams),
		AttachFloorPerMin: parseRPCLimitInt(envRPCAttachPerMinTotal, DefaultRPCAttachFloorPerMin),
	}
	return cfg.WithDefaults()
}

// WithDefaults fills zeros. Disabled configs skip per-cap defaults and set
// the Attach process floor to unlimited.
func (c ChannelLimitConfig) WithDefaults() ChannelLimitConfig {
	if c.Disabled {
		c.AttachFloorPerMin = math.MaxInt
		return c
	}
	if c.MessagesPerMin == 0 {
		c.MessagesPerMin = DefaultRPCMessagesPerMin
	}
	if IsUnlimitedRPCLimit(c.MessagesPerMin) {
		c.MessagesBurst = UnlimitedRPCLimit
	} else if c.MessagesBurst == 0 {
		c.MessagesBurst = c.MessagesPerMin / 10
		if c.MessagesBurst == 0 {
			c.MessagesBurst = 1
		}
	}
	if c.MaxStreams == 0 {
		c.MaxStreams = DefaultRPCMaxStreams
	}
	if c.AttachFloorPerMin <= 0 {
		c.AttachFloorPerMin = DefaultRPCAttachFloorPerMin
	}
	if c.MaxEntries <= 0 {
		c.MaxEntries = DefaultRPCLimiterMaxEntries
	}
	return c
}

// IsUnlimitedRPCLimit reports a cap that the interceptor must not enforce.
func IsUnlimitedRPCLimit(n uint32) bool {
	return n == UnlimitedRPCLimit
}

func parseRPCLimit(env string, def uint32) uint32 {
	raw := strings.TrimSpace(os.Getenv(env))
	if raw == "" {
		return def
	}
	if raw == "-1" {
		return UnlimitedRPCLimit
	}
	n, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		warnIgnoredRPCLimit(env, raw, def, "invalid")
		return def
	}
	if n == 0 {
		warnIgnoredRPCLimit(env, raw, def, "zero")
		return def
	}
	if uint32(n) == UnlimitedRPCLimit {
		warnIgnoredRPCLimit(env, raw, def, "unlimited_sentinel")
		return def
	}
	return uint32(n)
}

func parseRPCLimitInt(env string, def int) int {
	raw := strings.TrimSpace(os.Getenv(env))
	if raw == "" {
		return def
	}
	if raw == "-1" {
		return math.MaxInt
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		warnIgnoredRPCLimit(env, raw, def, "invalid")
		return def
	}
	if n <= 0 {
		reason := "zero"
		if n < 0 {
			reason = "non_positive"
		}
		warnIgnoredRPCLimit(env, raw, def, reason)
		return def
	}
	return n
}

func warnIgnoredRPCLimit(env, raw string, def any, reason string) {
	logging.Warn("DEVSHARD_RPC limit env ignored; using default",
		"subsystem", "transport",
		"env", env,
		"value", raw,
		"default", def,
		"reason", reason,
	)
}
