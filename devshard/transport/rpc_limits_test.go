package transport

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/logging"
	"devshard/transport/rpcpb/rpcpbconnect"
)

func TestLoadChannelLimitConfig_Defaults(t *testing.T) {
	t.Setenv(envRPCLimitsOff, "")
	t.Setenv(envRPCMsgsPerMin, "")
	t.Setenv(envRPCMsgsBurst, "")
	t.Setenv(envRPCMaxStreams, "")
	t.Setenv(envRPCMaxConnsPerPeer, "")
	t.Setenv(envRPCAttachPerMinTotal, "")

	cfg := LoadChannelLimitConfig()
	require.False(t, cfg.Disabled)
	require.Equal(t, DefaultRPCMessagesPerMin, cfg.MessagesPerMin)
	require.Equal(t, DefaultRPCMessagesBurst, cfg.MessagesBurst)
	require.Equal(t, DefaultRPCMaxStreams, cfg.MaxStreams)
	require.Equal(t, DefaultRPCMaxConnsPerPeer, cfg.MaxConns)
	require.Equal(t, int(DefaultRPCMaxStreams), DefaultRPCMaxConnsPerPeer)
	require.Equal(t, DefaultRPCAttachFloorPerMin, cfg.AttachFloorPerMin)
	require.Equal(t, 10*DefaultRPCAttachFloorPerMin, MaxRPCAttachFloorPerMin)
	require.Equal(t, DefaultRPCLimiterMaxEntries, cfg.MaxEntries)
	require.Equal(t, uint32(4096), DefaultH2MaxConcurrentStreams)
	require.NotEqual(t, DefaultRPCMaxStreams, DefaultH2MaxConcurrentStreams)
}

func TestLoadChannelLimitConfig_EnvAndOff(t *testing.T) {
	t.Setenv(envRPCMsgsPerMin, "120")
	t.Setenv(envRPCMsgsBurst, "20")
	t.Setenv(envRPCMaxStreams, "8")
	t.Setenv(envRPCMaxConnsPerPeer, "16")
	t.Setenv(envRPCAttachPerMinTotal, "50")
	cfg := LoadChannelLimitConfig()
	require.Equal(t, uint32(120), cfg.MessagesPerMin)
	require.Equal(t, uint32(20), cfg.MessagesBurst)
	require.Equal(t, uint32(8), cfg.MaxStreams)
	require.Equal(t, 16, cfg.MaxConns)
	require.Equal(t, uint32(8), cfg.EffectiveMaxStreams())
	require.Equal(t, 50, cfg.AttachFloorPerMin)

	t.Setenv(envRPCMsgsBurst, "-1")
	cfg = LoadChannelLimitConfig()
	require.Equal(t, uint32(120), cfg.MessagesPerMin)
	require.True(t, IsUnlimitedRPCLimit(cfg.MessagesBurst))

	t.Setenv(envRPCMsgsPerMin, "-1")
	cfg = LoadChannelLimitConfig()
	require.True(t, IsUnlimitedRPCLimit(cfg.MessagesPerMin))
	require.True(t, IsUnlimitedRPCLimit(cfg.MessagesBurst))
	require.Equal(t, UnlimitedRPCLimit, uint32(math.MaxUint32))

	t.Setenv(envRPCLimitsOff, "off")
	cfg = LoadChannelLimitConfig()
	require.True(t, cfg.Disabled)
	require.Equal(t, math.MaxInt, cfg.AttachFloorPerMin)
}

func TestChannelLimitConfig_ZeroMeansDefault(t *testing.T) {
	cfg := ChannelLimitConfig{}.WithDefaults()
	require.Equal(t, DefaultRPCMessagesPerMin, cfg.MessagesPerMin)
	require.Equal(t, DefaultRPCMessagesBurst, cfg.MessagesBurst)
	require.Equal(t, DefaultRPCLimiterMaxEntries, cfg.MaxEntries)
	require.Equal(t, DefaultRPCMaxConnsPerPeer, cfg.MaxConns)
	require.Equal(t, DefaultRPCAttachFloorPerMin, cfg.AttachFloorPerMin)
}

func TestChannelLimitConfig_AttachFloorClamps(t *testing.T) {
	cfg := ChannelLimitConfig{AttachFloorPerMin: 50_000_000}.WithDefaults()
	require.Equal(t, MaxRPCAttachFloorPerMin, cfg.AttachFloorPerMin)
	require.Equal(t, math.MaxInt, ClampAttachFloorPerMin(math.MaxInt))
	require.Equal(t, 50, ClampAttachFloorPerMin(50))
	require.Equal(t, 0, ClampAttachFloorPerMin(0))
}

func TestEffectiveMaxStreams_MinOfStreamsAndPool(t *testing.T) {
	require.Equal(t, uint32(16), ChannelLimitConfig{MaxStreams: 256, MaxConns: 16}.EffectiveMaxStreams())
	require.Equal(t, uint32(8), ChannelLimitConfig{MaxStreams: 8, MaxConns: 256}.EffectiveMaxStreams())
	require.Equal(t, DefaultRPCMaxStreams, ChannelLimitConfig{}.EffectiveMaxStreams())
	require.True(t, IsUnlimitedRPCLimit(ChannelLimitConfig{MaxStreams: UnlimitedRPCLimit, MaxConns: 16}.EffectiveMaxStreams()))
}

func TestChannelLimitConfig_UnlimitedMessagesForcesUnlimitedBurst(t *testing.T) {
	cfg := ChannelLimitConfig{MessagesPerMin: UnlimitedRPCLimit, MessagesBurst: 5}.WithDefaults()
	require.True(t, IsUnlimitedRPCLimit(cfg.MessagesBurst))
}

func TestRPCProcedureWeight_Table(t *testing.T) {
	require.Equal(t, RPCWeightGetDiffs, RPCProcedureWeight(rpcpbconnect.SessionServiceGetDiffsProcedure))
	require.Equal(t, RPCWeightGetMempool, RPCProcedureWeight(rpcpbconnect.SessionServiceGetMempoolProcedure))
	require.Equal(t, RPCWeightGetPayload, RPCProcedureWeight(rpcpbconnect.PayloadServiceGetPayloadProcedure))
	require.Equal(t, RPCWeightChat, RPCProcedureWeight(rpcpbconnect.SessionServiceChatProcedure))
	require.Equal(t, RPCWeightChallenge, RPCProcedureWeight(rpcpbconnect.SessionServiceChallengeReceiptProcedure))
	require.Equal(t, RPCWeightGossipTxs, RPCProcedureWeight(rpcpbconnect.GossipServiceTxsProcedure))
	require.Equal(t, RPCWeightGetSignatures, RPCProcedureWeight(rpcpbconnect.GossipServiceNonceProcedure))
	require.Equal(t, RPCWeightGetSignatures, RPCProcedureWeight(rpcpbconnect.SessionServiceGetSignaturesProcedure))
	require.Equal(t, 0, RPCProcedureWeight(rpcpbconnect.PeerAuthServiceAttachProcedure))
	require.Equal(t, 0, RPCProcedureWeight(rpcpbconnect.PeerAuthServiceWatchProcedure))
}

func TestParseRPCLimit_IgnoredValuesUseDefault(t *testing.T) {
	capLog := &limitWarnLogger{}
	logging.SetLogger(capLog)
	t.Cleanup(func() { logging.SetLogger(logging.NewSlogAdapter()) })

	t.Setenv(envRPCLimitsOff, "")
	t.Setenv(envRPCMsgsBurst, "")
	t.Setenv(envRPCMaxStreams, "")
	t.Setenv(envRPCAttachPerMinTotal, "")

	t.Setenv(envRPCMsgsPerMin, "")
	before := len(capLog.warns)
	cfg := LoadChannelLimitConfig()
	require.Equal(t, DefaultRPCMessagesPerMin, cfg.MessagesPerMin)
	require.Equal(t, before, len(capLog.warns), "unset env must not warn")

	t.Setenv(envRPCMsgsPerMin, "6O00")
	cfg = LoadChannelLimitConfig()
	require.Equal(t, DefaultRPCMessagesPerMin, cfg.MessagesPerMin)
	require.True(t, capLog.has(envRPCMsgsPerMin, "6O00", "invalid"))

	t.Setenv(envRPCMsgsPerMin, "0")
	cfg = LoadChannelLimitConfig()
	require.Equal(t, DefaultRPCMessagesPerMin, cfg.MessagesPerMin)
	require.True(t, capLog.has(envRPCMsgsPerMin, "0", "zero"))

	t.Setenv(envRPCMsgsPerMin, "4294967295")
	cfg = LoadChannelLimitConfig()
	require.Equal(t, DefaultRPCMessagesPerMin, cfg.MessagesPerMin)
	require.False(t, IsUnlimitedRPCLimit(cfg.MessagesPerMin))
	require.True(t, capLog.has(envRPCMsgsPerMin, "4294967295", "unlimited_sentinel"))

	t.Setenv(envRPCMsgsPerMin, "-1")
	cfg = LoadChannelLimitConfig()
	require.True(t, IsUnlimitedRPCLimit(cfg.MessagesPerMin))

	t.Setenv(envRPCMsgsPerMin, "120")
	cfg = LoadChannelLimitConfig()
	require.Equal(t, uint32(120), cfg.MessagesPerMin)

	t.Setenv(envRPCMsgsPerMin, "")
	t.Setenv(envRPCAttachPerMinTotal, "0")
	cfg = LoadChannelLimitConfig()
	require.Equal(t, DefaultRPCAttachFloorPerMin, cfg.AttachFloorPerMin)
	require.True(t, capLog.has(envRPCAttachPerMinTotal, "0", "zero"))

	t.Setenv(envRPCAttachPerMinTotal, "50000000")
	cfg = LoadChannelLimitConfig()
	require.Equal(t, MaxRPCAttachFloorPerMin, cfg.AttachFloorPerMin)
	require.True(t, capLog.has(envRPCAttachPerMinTotal, "50000000", "too_large"))

	t.Setenv(envRPCAttachPerMinTotal, "-1")
	cfg = LoadChannelLimitConfig()
	require.Equal(t, math.MaxInt, cfg.AttachFloorPerMin)
}

type limitWarnLogger struct {
	warns []string
}

func (l *limitWarnLogger) Info(string, ...any)  {}
func (l *limitWarnLogger) Error(string, ...any) {}
func (l *limitWarnLogger) Debug(string, ...any) {}
func (l *limitWarnLogger) Warn(msg string, kv ...any) {
	line := msg
	for i := 0; i+1 < len(kv); i += 2 {
		line += fmt.Sprintf(" %v=%v", kv[i], kv[i+1])
	}
	l.warns = append(l.warns, line)
}

func (l *limitWarnLogger) has(env, value, reason string) bool {
	for _, w := range l.warns {
		if strings.Contains(w, "env="+env) &&
			strings.Contains(w, "value="+value) &&
			strings.Contains(w, "reason="+reason) {
			return true
		}
	}
	return false
}
