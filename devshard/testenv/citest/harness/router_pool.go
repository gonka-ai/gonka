package harness

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// Router pool states as reported by gonka-drain.
const (
	RouterSlotUp    = "UP"
	RouterSlotDown  = "DOWN"
	RouterSlotDrain = "DRAIN"
)

// RouterSlot is one server slot in the router's HA backend.
type RouterSlot struct {
	Name    string
	Address string
	State   string
}

// RouterPool asks the router what it currently believes about the pool. This is
// the whole control surface now: membership arrives over DNS and health over
// active /readyz checks, so the router's own view is the only authority.
func RouterPool(t *testing.T, stack *Stack) []RouterSlot {
	t.Helper()
	slots, err := routerPool(stack)
	require.NoError(t, err)
	return slots
}

func routerPool(stack *Stack) ([]RouterSlot, error) {
	out, err := stack.ComposeExecOutput("versiond-router", "gonka-drain", "status")
	if err != nil {
		return nil, fmt.Errorf("gonka-drain status: %w: %s", err, out)
	}
	return parseRouterPool(out), nil
}

func parseRouterPool(out string) []RouterSlot {
	var slots []RouterSlot
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] == "SLOT" {
			continue
		}
		slots = append(slots, RouterSlot{Name: fields[0], Address: fields[1], State: fields[2]})
	}
	return slots
}

// RouterPoolHostState maps each versiond host to the state the router sees.
// Hosts the router cannot resolve at all are simply absent.
func RouterPoolHostState(t *testing.T, stack *Stack, cfg *config.File) map[string]string {
	t.Helper()
	byHost := make(map[string]string)
	for _, slot := range RouterPool(t, stack) {
		if host := HostIDForUpstream(cfg, slot.Address); host != "" {
			byHost[host] = slot.State
		}
	}
	return byHost
}

// WaitRouterPoolState blocks until the router reports the host in wantState,
// where the empty string means "not in the pool at all". A router that is itself
// restarting is a normal transient here, so an unreachable router is retried
// rather than failed.
func WaitRouterPoolState(
	t *testing.T,
	stack *Stack,
	cfg *config.File,
	host, wantState string,
	timeout time.Duration,
) {
	t.Helper()
	var last string
	ok := AssertEventually(t, timeout, 250*time.Millisecond, func() bool {
		slots, err := routerPool(stack)
		if err != nil {
			last = "unreachable"
			return false
		}
		last = ""
		for _, slot := range slots {
			if HostIDForUpstream(cfg, slot.Address) == host {
				last = slot.State
			}
		}
		return last == wantState
	})
	require.True(t, ok, "router never saw %s as %q (last %q)", host, wantState, last)
}

// RouterDrain takes a host out of rotation without stopping it, addressing it by
// compose service name the way an operator would.
func RouterDrain(t *testing.T, stack *Stack, host string) {
	t.Helper()
	out, err := stack.ComposeExecOutput("versiond-router", "gonka-drain", "out", host)
	require.NoError(t, err, "gonka-drain out %s: %s", host, out)
}

// RouterUndrain puts a drained host back into rotation.
func RouterUndrain(t *testing.T, stack *Stack, host string) {
	t.Helper()
	out, err := stack.ComposeExecOutput("versiond-router", "gonka-drain", "in", host)
	require.NoError(t, err, "gonka-drain in %s: %s", host, out)
}

// RequireRouterRefusesToEmptyPool asserts the last serving host cannot be
// drained. The guard is what keeps a host-by-host drain from taking the whole
// deployment down.
func RequireRouterRefusesToEmptyPool(t *testing.T, stack *Stack, host string) {
	t.Helper()
	out, err := stack.ComposeExecOutput("versiond-router", "gonka-drain", "out", host)
	require.Error(t, err, "draining the last serving host should be refused: %s", out)
	require.Contains(t, out, "last server taking traffic")
}

// RouterServingHosts lists the hosts currently taking new traffic.
func RouterServingHosts(t *testing.T, stack *Stack, cfg *config.File) []string {
	t.Helper()
	var serving []string
	for host, state := range RouterPoolHostState(t, stack, cfg) {
		if state == RouterSlotUp {
			serving = append(serving, host)
		}
	}
	return serving
}

// DescribeRouterPool renders the pool for failure messages.
func DescribeRouterPool(t *testing.T, stack *Stack, cfg *config.File) string {
	t.Helper()
	var b strings.Builder
	for _, slot := range RouterPool(t, stack) {
		host := HostIDForUpstream(cfg, slot.Address)
		if host == "" {
			host = "?"
		}
		fmt.Fprintf(&b, "%s(%s)=%s ", host, slot.Address, slot.State)
	}
	return strings.TrimSpace(b.String())
}
