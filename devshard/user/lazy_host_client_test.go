package user

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/transport"
)

// fakeBuiltHost is a built host client that records sends, admission strips, and closes.
type fakeBuiltHost struct {
	stripped bool
	sends    *atomic.Int32
	strips   *atomic.Int32
	closes   *atomic.Int32
	wait     time.Duration
}

func newFakeBuiltHost() *fakeBuiltHost {
	return &fakeBuiltHost{sends: new(atomic.Int32), strips: new(atomic.Int32), closes: new(atomic.Int32)}
}

func (f *fakeBuiltHost) Send(context.Context, host.HostRequest, io.Writer, func(*host.HostResponse)) (*host.HostResponse, error) {
	f.sends.Add(1)
	return &host.HostResponse{}, nil
}

func (f *fakeBuiltHost) WithoutAdmission() any {
	f.strips.Add(1)
	cp := *f
	cp.stripped = true
	return &cp
}

func (f *fakeBuiltHost) Close()                        { f.closes.Add(1) }
func (f *fakeBuiltHost) ChatBudgetWait() time.Duration { return f.wait }
func (f *fakeBuiltHost) BaseURL() string               { return "http://built" }

// Tests that an unresolved host refuses calls and reports itself spent to the chat picker.
//  1. Wrap a lookup that fails.
//  2. Send, and read ChatBudgetWait and the accessors.
//  3. Expect ErrHostUnresolved carrying the cause, a spent budget, and empty accessors.
func TestLazyHostClient_UnresolvedRefusesAndReportsSpent(t *testing.T) {
	cause := errors.New("chain down")
	c := newLazyHostClient(transport.NewLazy(func() (HostClient, error) { return nil, cause }, releaseHostClient, 0, time.Hour))

	_, err := c.Send(context.Background(), host.HostRequest{}, nil, nil)
	require.ErrorIs(t, err, ErrHostUnresolved)
	require.ErrorIs(t, err, cause)
	_, err = c.GetSignatures(context.Background(), 1)
	require.ErrorIs(t, err, ErrHostUnresolved)
	_, err = c.SeedHeightSync(context.Background())
	require.ErrorIs(t, err, ErrHostUnresolved)

	require.GreaterOrEqual(t, c.ChatBudgetWait(), unresolvedHostChatWait)
	require.Empty(t, c.BaseURL())
	require.Empty(t, c.CatalogHealthzURL())
	_, ok := c.ObservedHeightNow()
	require.False(t, ok)
}

// Tests that the chat picker's budget poll retries a failed lookup once the retry window ends.
//  1. Fail the first lookup under a short retry window.
//  2. Heal the lookup, wait out the window, and poll ChatBudgetWait.
//  3. Expect the poll to start a lookup and, once built, to forward the client's budget and accessors.
func TestLazyHostClient_BudgetPollRetriesAfterWindow(t *testing.T) {
	built := newFakeBuiltHost()
	var healed atomic.Bool
	var calls atomic.Int32
	c := newLazyHostClient(transport.NewLazy(func() (HostClient, error) {
		calls.Add(1)
		if !healed.Load() {
			return nil, errors.New("chain down")
		}
		return built, nil
	}, releaseHostClient, 0, 20*time.Millisecond))

	_, err := c.Send(context.Background(), host.HostRequest{}, nil, nil)
	require.ErrorIs(t, err, ErrHostUnresolved)
	require.GreaterOrEqual(t, c.ChatBudgetWait(), unresolvedHostChatWait)

	healed.Store(true)
	require.Eventually(t, func() bool { return c.ChatBudgetWait() == 0 }, 2*time.Second, 5*time.Millisecond)
	require.GreaterOrEqual(t, calls.Load(), int32(2), "only a fresh lookup after the window can build the client")
	require.Equal(t, "http://built", c.BaseURL())

	built.wait = 3 * time.Second
	require.Equal(t, 3*time.Second, c.ChatBudgetWait())
	_, err = c.Send(context.Background(), host.HostRequest{}, nil, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, built.sends.Load())
}

// Tests that a finalize view taken before the host resolved strips admission per call and owns nothing.
//  1. Take WithoutAdmission while the lookup is still blocked, then let it build.
//  2. Send through the view and close the view.
//  3. Expect the send on an admission-stripped copy that is released after the call, and the shared client left open until the owner closes.
func TestLazyHostClient_AdmissionViewBeforeResolve(t *testing.T) {
	built := newFakeBuiltHost()
	gate := make(chan struct{})
	c := newLazyHostClient(transport.NewLazy(func() (HostClient, error) {
		<-gate
		return built, nil
	}, releaseHostClient, 0, 0))

	view, ok := c.WithoutAdmission().(HostClient)
	require.True(t, ok)
	require.NotSame(t, c, view)
	close(gate)

	_, err := view.Send(context.Background(), host.HostRequest{}, nil, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, built.strips.Load())
	require.EqualValues(t, 1, built.sends.Load())
	require.EqualValues(t, 1, built.closes.Load(), "the per-call stripped copy is released")

	view.(interface{ Close() }).Close()
	require.EqualValues(t, 1, built.closes.Load(), "closing the view leaves the shared client open")
	c.Close()
	require.EqualValues(t, 2, built.closes.Load())

	strippedAfter := c.WithoutAdmission()
	require.IsType(t, &lazyHostClient{}, strippedAfter, "a closed owner no longer hands out its client")
}

// Tests that the wrapper answers every optional interface a production host client answers.
//  1. List the methods the session and gateway type-assert on host clients.
//  2. Expect each one that *transport.RPCClient implements to be implemented by lazyHostClient too.
func TestLazyHostClient_MatchesRPCClientMethodSet(t *testing.T) {
	probed := []string{
		"Send", "GetSignatures", "VerifyTimeout", "VerifyErrorMiss", "SeedHeightSync",
		"WithoutAdmission", "ChatBudgetWait", "CatalogHealthzURL", "ObservedStampNow",
		"ObservedHeightNow", "HeightSyncPeerTips", "BaseURL", "RoutePrefix", "Close",
	}
	rpc := reflect.TypeOf((*transport.RPCClient)(nil))
	lazy := reflect.TypeOf((*lazyHostClient)(nil))
	for _, name := range probed {
		want, ok := rpc.MethodByName(name)
		require.True(t, ok, "RPCClient no longer has %s; drop it from the list", name)
		got, ok := lazy.MethodByName(name)
		require.True(t, ok, "lazyHostClient is missing %s", name)
		require.Equal(t, want.Type.NumIn(), got.Type.NumIn(), name)
		for i := 1; i < want.Type.NumIn(); i++ {
			require.Equal(t, want.Type.In(i), got.Type.In(i), "%s arg %d", name, i)
		}
		require.Equal(t, want.Type.NumOut(), got.Type.NumOut(), name)
		for i := 0; i < want.Type.NumOut(); i++ {
			require.Equal(t, want.Type.Out(i), got.Type.Out(i), "%s result %d", name, i)
		}
	}
}
