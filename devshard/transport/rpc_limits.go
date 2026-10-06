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
	// DefaultRPCMaxChatsPerPeer is concurrent Chat streams for one peer.
	// Watch has its own cap and does not spend this.
	DefaultRPCMaxChatsPerPeer uint32 = 10240
	// DefaultRPCMaxStreams is the per-peer non-Watch stream cap. Chat
	// uses at most max-1 when max>1, so this stays one above the chat
	// cap. This is the Connect interceptor, not HTTP/2 SETTINGS (per TCP).
	DefaultRPCMaxStreams uint32 = DefaultRPCMaxChatsPerPeer + 1
	// DefaultRPCMaxStreamsTotal is the child-wide non-Watch stream ceiling.
	// Watch does not spend it; Watch is capped at MaxSessions. It matches
	// the per-peer cap so one peer can fill its chat roster. SETTINGS
	// stays DefaultH2MaxConcurrentStreams so one mux can carry that roster.
	DefaultRPCMaxStreamsTotal uint32 = DefaultRPCMaxStreams
	// DefaultRPCMaxChatsTotal is the child-wide Chat ceiling. It matches
	// the per-peer chat cap. Watch does not spend this.
	DefaultRPCMaxChatsTotal uint32 = DefaultRPCMaxChatsPerPeer
	// DefaultH2MaxConcurrentStreams is SETTINGS_MAX_CONCURRENT_STREAMS on
	// the child h2c listen. Overlay muxes every peer's Watch onto one
	// (or a few) versiond→child TCP connections, so this must match
	// versiond's public listen and HAProxy tune.h2.max-concurrent-streams,
	// and it stays above DefaultRPCMaxStreamsTotal. versioned copies the
	// same value (versioned cannot import this module).
	DefaultH2MaxConcurrentStreams uint32 = 16384
	// DefaultRPCAttachFloorPerMin is the configured Attach ceiling. The
	// enforced anonymous bucket is smaller: 50 tokens, refilled at 50 per
	// second. A configured value below 50 is that burst and that refill.
	DefaultRPCAttachFloorPerMin = 10_000
	// MaxRPCAttachFloorPerMin is the highest DEVSHARD_RPC_ATTACH_PER_MIN_TOTAL
	// we honor (10× default). Above that, parse / WithDefaults warn (env) or
	// clamp. -1 stays unlimited. The enforced bucket does not grow with this.
	MaxRPCAttachFloorPerMin = 100_000
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
	envRPCMaxStreamsTotal   = "DEVSHARD_RPC_MAX_STREAMS_TOTAL"
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
	// MaxStreams is the configured Watch+Chat cap before min(MaxStreams, MaxConns).
	MaxStreams uint32
	// MaxStreamsTotal is the child-wide Watch+Chat cap. Zero means
	// DefaultRPCMaxStreamsTotal. UnlimitedRPCLimit (-1) disables it.
	// Not advertised: clients still pace max_streams per peer.
	MaxStreamsTotal uint32
	// MaxChatsTotal is the child-wide Chat cap. Zero means
	// min(MaxStreamsTotal, DefaultRPCMaxChatsTotal). Watch does not spend it.
	MaxChatsTotal uint32
	// MaxConns is this process's HTTP/1.1 PeerConn pool
	// (MaxIdleConnsPerHost / MaxConnsPerHost). Zero means
	// DefaultRPCMaxConnsPerPeer / DEVSHARD_RPC_MAX_CONNS_PER_PEER.
	// Advertised and enforced max_streams is min(MaxStreams, MaxConns).
	MaxConns int
	// AttachFloorPerMin sizes the anonymous Attach bucket. Zero means
	// DefaultRPCAttachFloorPerMin. Values above MaxRPCAttachFloorPerMin are
	// clamped. math.MaxInt is unlimited. A value of at least 50 enforces a
	// 50-token bucket refilled at 50 per second; a smaller value is both
	// the burst and the per-second refill.
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
		MaxStreamsTotal:   parseRPCLimit(envRPCMaxStreamsTotal, DefaultRPCMaxStreamsTotal),
		MaxConns:          RPCMaxConnsPerPeerFromEnv(),
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
	if c.MaxStreamsTotal == 0 {
		c.MaxStreamsTotal = DefaultRPCMaxStreamsTotal
	}
	if !IsUnlimitedRPCLimit(c.MaxStreamsTotal) && c.MaxChatsTotal == 0 {
		c.MaxChatsTotal = DefaultRPCMaxChatsTotal
		if c.MaxChatsTotal > c.MaxStreamsTotal {
			c.MaxChatsTotal = c.MaxStreamsTotal
		}
	}
	if c.MaxConns <= 0 {
		c.MaxConns = DefaultRPCMaxConnsPerPeer
	}
	if c.AttachFloorPerMin <= 0 {
		c.AttachFloorPerMin = DefaultRPCAttachFloorPerMin
	} else {
		c.AttachFloorPerMin = ClampAttachFloorPerMin(c.AttachFloorPerMin)
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

// ProcessStreamCaps is the child-wide Chat stream ceiling and the Chat
// slice of it (one MLNode). Watch is not part of this ceiling. unlimited
// is true when the process ceiling is off (DEVSHARD_RPC_LIMITS=off or
// MAX_STREAMS_TOTAL=-1). SETTINGS stays DefaultH2MaxConcurrentStreams; this is the interceptor,
// not the mux.
func (c ChannelLimitConfig) ProcessStreamCaps() (streams, chats uint32, unlimited bool) {
	if c.Disabled {
		return UnlimitedRPCLimit, UnlimitedRPCLimit, true
	}
	c = c.WithDefaults()
	if IsUnlimitedRPCLimit(c.MaxStreamsTotal) {
		return UnlimitedRPCLimit, UnlimitedRPCLimit, true
	}
	streams = c.MaxStreamsTotal
	chats = c.MaxChatsTotal
	if chats == 0 || chats > streams {
		chats = streams
	}
	return streams, chats, false
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

// EffectiveMaxStreams is the Watch+Chat cap this process advertises and
// enforces: min(MaxStreams, MaxConns) so Attach max_streams is not above
// the HTTP/1.1 PeerConn pool. Unlimited MaxStreams is unchanged. Disabled
// configs keep MaxStreams as WithDefaults left it (advertise path uses
// UnlimitedRPCLimit).
func (c ChannelLimitConfig) EffectiveMaxStreams() uint32 {
	c = c.WithDefaults()
	if c.Disabled || IsUnlimitedRPCLimit(c.MaxStreams) {
		return c.MaxStreams
	}
	return minUint32Cap(c.MaxStreams, c.MaxConns)
}

func minUint32Cap(streams uint32, maxConns int) uint32 {
	if maxConns <= 0 {
		return streams
	}
	if uint64(maxConns) >= uint64(streams) {
		return streams
	}
	return uint32(maxConns)
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
	if n > MaxRPCAttachFloorPerMin {
		warnIgnoredRPCLimit(env, raw, MaxRPCAttachFloorPerMin, "too_large")
		return MaxRPCAttachFloorPerMin
	}
	return n
}

// ClampAttachFloorPerMin caps a configured Attach floor. math.MaxInt
// (unlimited / Disabled) is unchanged. Zero and negative are left for
// WithDefaults to replace with DefaultRPCAttachFloorPerMin.
func ClampAttachFloorPerMin(n int) int {
	if n <= 0 || n == math.MaxInt {
		return n
	}
	if n > MaxRPCAttachFloorPerMin {
		return MaxRPCAttachFloorPerMin
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
