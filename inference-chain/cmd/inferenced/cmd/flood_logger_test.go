package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"cosmossdk.io/log"
	cmtconfig "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/p2p"
	ssproto "github.com/cometbft/cometbft/proto/tendermint/statesync"
	"github.com/cometbft/cometbft/statesync"
	serverlog "github.com/cosmos/cosmos-sdk/server/log"
	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newTestFloodLogger(buf *bytes.Buffer, clock *fakeClock, burst int) log.Logger {
	inner := log.NewLogger(buf, log.ColorOption(false))
	return &floodLogger{
		Logger:  inner,
		limiter: newFloodLimiter(clock.now, time.Minute, burst, floodLogPrefixes),
	}
}

func countLines(buf *bytes.Buffer, substr string) int {
	n := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

func TestFloodLoggerCapsKnownFloodLines(t *testing.T) {
	var buf bytes.Buffer
	clock := &fakeClock{t: time.Unix(0, 0)}
	logger := newTestFloodLogger(&buf, clock, 3)

	consensus := logger.With("module", "consensus")
	for i := 0; i < 100; i++ {
		consensus.Info("failed attempting to add vote", "err", "invalid validator index")
		consensus.Error("failed to process message", "peer", "abc")
	}

	require.Equal(t, 3, countLines(&buf, "failed attempting to add vote"))
	require.Equal(t, 3, countLines(&buf, "failed to process message"))
}

func TestFloodLoggerPassesOtherLines(t *testing.T) {
	var buf bytes.Buffer
	clock := &fakeClock{t: time.Unix(0, 0)}
	logger := newTestFloodLogger(&buf, clock, 1)

	for i := 0; i < 50; i++ {
		logger.Info("finalized block", "height", i)
		logger.Debug("failed attempting to add vote")
	}

	require.Equal(t, 50, countLines(&buf, "finalized block"))
}

func TestFloodLoggerMatchesUnknownMessageTypePrefix(t *testing.T) {
	var buf bytes.Buffer
	clock := &fakeClock{t: time.Unix(0, 0)}
	logger := newTestFloodLogger(&buf, clock, 2)

	for i := 0; i < 10; i++ {
		logger.Error("Unknown message type *consensus.HasVoteMessage")
		logger.Error("Unknown message type *consensus.VoteMessage")
	}

	require.Equal(t, 2, countLines(&buf, "Unknown message type"))
}

func TestFloodLoggerReportsSuppressedCountAndResets(t *testing.T) {
	var buf bytes.Buffer
	clock := &fakeClock{t: time.Unix(0, 0)}
	logger := newTestFloodLogger(&buf, clock, 2)

	for i := 0; i < 12; i++ {
		logger.Error("received empty txs from peer", "src", "abc")
	}
	require.Equal(t, 0, countLines(&buf, "suppressed repeated log lines"))

	clock.t = clock.t.Add(time.Minute)
	logger.Info("finalized block")

	require.Equal(t, 1, countLines(&buf, "suppressed repeated log lines"))
	require.Contains(t, buf.String(), "suppressed=10")

	buf.Reset()
	for i := 0; i < 5; i++ {
		logger.Error("received empty txs from peer", "src", "abc")
	}
	require.Equal(t, 2, countLines(&buf, "received empty txs from peer"))
}

func TestFloodLoggerSharesBudgetAcrossWith(t *testing.T) {
	var buf bytes.Buffer
	clock := &fakeClock{t: time.Unix(0, 0)}
	logger := newTestFloodLogger(&buf, clock, 4)

	a := logger.With("module", "consensus")
	b := a.With("peer", "x")
	for i := 0; i < 10; i++ {
		a.Info("Peer asking for a block we don't have")
		b.Info("Peer asking for a block we don't have")
	}

	require.Equal(t, 4, countLines(&buf, "Peer asking for a block we don't have"))
}

func TestFloodLoggerKeepsVerboseMode(t *testing.T) {
	_, ok := newFloodLogger(log.NewNopLogger()).(log.VerboseModeLogger)
	require.True(t, ok)
}

func TestFloodLoggerKeepsSeparateBudgetPerMessageType(t *testing.T) {
	var buf bytes.Buffer
	clock := &fakeClock{t: time.Unix(0, 0)}
	logger := newTestFloodLogger(&buf, clock, 3).With("module", "consensus")

	for i := 0; i < 50; i++ {
		logger.Error("failed to process message", "msg_type", "*consensus.VoteMessage", "err", "error adding vote")
	}
	logger.Error("failed to process message", "msg_type", "*consensus.ProposalMessage", "err", "invalid proposal signature")

	require.Equal(t, 4, countLines(&buf, "failed to process message"))
	require.Contains(t, buf.String(), "invalid proposal signature")
}

func TestFloodLoggerCapsSyncTimeMessages(t *testing.T) {
	var buf bytes.Buffer
	clock := &fakeClock{t: time.Unix(0, 0)}
	logger := newTestFloodLogger(&buf, clock, 2)

	for i := 0; i < 100; i++ {
		logger.Info("Ignoring message received during sync", "msg", "Vote{1000:000000000000}")
	}

	require.Equal(t, 2, countLines(&buf, "Ignoring message received during sync"))
}

func TestFloodLoggerSummaryUsesOriginatingLogger(t *testing.T) {
	var buf bytes.Buffer
	clock := &fakeClock{t: time.Unix(0, 0)}
	logger := newTestFloodLogger(&buf, clock, 1)
	consensus := logger.With("module", "consensus")
	inference := logger.With("module", "x/inference")

	for i := 0; i < 5; i++ {
		consensus.Error("failed to process message", "msg_type", "*consensus.VoteMessage", "err", "error adding vote")
	}
	clock.t = clock.t.Add(time.Minute)
	inference.Info("epoch started")

	var summary string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "suppressed repeated log lines") {
			summary = line
		}
	}
	require.Contains(t, summary, "ERR")
	require.Contains(t, summary, "module=consensus")
	require.Contains(t, summary, "suppressed=4")
	require.Contains(t, summary, "msg_type=*consensus.VoteMessage")
	require.Contains(t, summary, "last_err=")
}

func TestFloodLoggerSummaryVisibleAtErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	clock := &fakeClock{t: time.Unix(0, 0)}
	filter, err := log.ParseLogLevel("*:error")
	require.NoError(t, err)
	inner := log.NewLogger(&buf, log.ColorOption(false), log.FilterOption(filter))
	logger := &floodLogger{
		Logger:  inner,
		limiter: newFloodLimiter(clock.now, time.Minute, 2, floodLogPrefixes),
	}

	for i := 0; i < 80; i++ {
		logger.Error("received empty txs from peer", "src", "abc")
	}
	clock.t = clock.t.Add(time.Minute)
	logger.Info("finalized block")

	require.Equal(t, 1, countLines(&buf, "suppressed repeated log lines"))
	require.Contains(t, buf.String(), "suppressed=78")
}

func TestFloodLoggerCapsStateSyncWrongChannelMessages(t *testing.T) {
	var buf bytes.Buffer
	clock := &fakeClock{t: time.Unix(0, 0)}
	logger := newTestFloodLogger(&buf, clock, 2)
	reactor := statesync.NewReactor(*cmtconfig.DefaultStateSyncConfig(), nil, nil, statesync.NopMetrics())
	reactor.SetLogger(serverlog.CometLoggerWrapper{Logger: logger.With("module", "statesync")})
	require.NoError(t, reactor.Start())
	t.Cleanup(func() { require.NoError(t, reactor.Stop()) })

	for _, input := range []struct {
		channel byte
		message p2p.Wrapper
	}{
		{statesync.ChunkChannel, &ssproto.SnapshotsRequest{}},
		{statesync.SnapshotChannel, &ssproto.ChunkRequest{Height: 1}},
	} {
		wire, err := proto.Marshal(input.message.Wrap())
		require.NoError(t, err)
		var decoded ssproto.Message
		require.NoError(t, proto.Unmarshal(wire, &decoded))
		message, err := decoded.Unwrap()
		require.NoError(t, err)
		for i := 0; i < 100; i++ {
			reactor.Receive(p2p.Envelope{ChannelID: input.channel, Message: message})
		}
	}

	require.Equal(t, 2, countLines(&buf, "Received unknown message"))
	require.True(t, reactor.IsRunning())
	clock.t = clock.t.Add(time.Minute)
	logger.Info("finalized block")
	require.Contains(t, buf.String(), "suppressed=198")
	require.Contains(t, buf.String(), "module=statesync")
}
