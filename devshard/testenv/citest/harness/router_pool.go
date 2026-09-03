package harness

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/testenv/config"
)

// Router pool states as reported by the router's pool-status diagnostic.
const (
	RouterSlotUp    = "UP"
	RouterSlotDown  = "DOWN"
	RouterSlotDrain = "DRAIN"
)

// RouterSlot is one server slot in one of the router's backends. Backend matters:
// the same host is a separate server in each, with its own health and therefore
// its own state.
type RouterSlot struct {
	Backend string
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
	out, err := stack.ComposeExecOutput("versiond-router", routerPoolStatusBin)
	if err != nil {
		return nil, fmt.Errorf("pool-status: %w: %s", err, out)
	}
	return parseRouterPool(out), nil
}

// routerPoolStatusBin is the router's read-only pool diagnostic. Off PATH on
// purpose — it is an internal formatter over the HAProxy Runtime API, and this
// harness is its primary consumer.
const routerPoolStatusBin = "/usr/local/lib/versiond-router/pool-status"

const routerVersionMapQuery = `
map=${VERSIOND_ROUTER_VERSIONS_MAP:-/etc/haproxy/versions.map}
sock=${HAPROXY_RECONCILER_SOCKET:-/var/run/haproxy/reconciler.sock}
printf 'show map %s\n' "$map" | socat stdio "UNIX-CONNECT:$sock"
`

// parseRouterPool reads pool-status output: each backend followed by its
// indented servers.
func parseRouterPool(out string) []RouterSlot {
	var slots []RouterSlot
	backend := ""
	for line := range strings.SplitSeq(out, "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			backend = strings.TrimSpace(line)
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		slots = append(slots, RouterSlot{
			Backend: backend,
			Name:    fields[0],
			Address: fields[1],
			State:   fields[2],
		})
	}
	return slots
}

// WaitRouterVersionBackend resolves the backend currently assigned to version
// through HAProxy's runtime map. Static bootstrap versions use versiond_pool_*
// names, while governance versions use versiond_dynamic_* slots, so callers
// must not derive the backend name from the version itself.
func WaitRouterVersionBackend(t *testing.T, stack *Stack, version string, timeout time.Duration) string {
	t.Helper()
	var backend string
	var lastErr error
	ok := AssertEventually(t, timeout, 250*time.Millisecond, func() bool {
		backend, lastErr = routerVersionBackend(stack, version)
		return lastErr == nil
	})
	require.True(t, ok, "router never mapped version %q to a backend: %v", version, lastErr)
	return backend
}

func routerVersionBackend(stack *Stack, version string) (string, error) {
	out, err := stack.ComposeExecOutput("versiond-router", "sh", "-c", routerVersionMapQuery)
	if err != nil {
		return "", fmt.Errorf("show versions map: %w: %s", err, out)
	}
	return parseRouterVersionBackend(out, version)
}

func parseRouterVersionBackend(out, version string) (string, error) {
	backend := ""
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.HasPrefix(fields[0], "#") || fields[1] != version {
			continue
		}
		if backend != "" && backend != fields[2] {
			return "", fmt.Errorf("version %q maps to multiple backends: %q and %q", version, backend, fields[2])
		}
		backend = fields[2]
	}
	if backend == "" {
		return "", fmt.Errorf("version %q is not present in the router versions map", version)
	}
	return backend, nil
}

// RouterPoolHostState maps each versiond host to the state the router sees in
// one backend. Hosts the router cannot resolve at all are simply absent.
func RouterPoolHostState(t *testing.T, stack *Stack, cfg *config.File, backend string) map[string]string {
	t.Helper()
	byHost := make(map[string]string)
	for _, slot := range RouterPool(t, stack) {
		if slot.Backend != backend {
			continue
		}
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
	backend, host, wantState string,
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
			if slot.Backend == backend && HostIDForUpstream(cfg, slot.Address) == host {
				last = slot.State
			}
		}
		return last == wantState
	})
	require.True(t, ok, "router never saw %s as %q in %s (last %q)", host, wantState, backend, last)
}

// RouterServingHosts lists the hosts currently taking new traffic in a backend.
func RouterServingHosts(t *testing.T, stack *Stack, cfg *config.File, backend string) []string {
	t.Helper()
	var serving []string
	for host, state := range RouterPoolHostState(t, stack, cfg, backend) {
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
		fmt.Fprintf(&b, "%s/%s(%s)=%s ", slot.Backend, host, slot.Address, slot.State)
	}
	return strings.TrimSpace(b.String())
}
