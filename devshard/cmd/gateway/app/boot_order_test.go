package app

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/store"
)

func bootStepNames(steps []bootStep) []string {
	names := make([]string, 0, len(steps))
	for _, step := range steps {
		names = append(names, step.name)
	}
	return names
}

// Test flow:
//  1. Build the boot order from an empty gateway and config.
//  2. Collect the resulting step names.
//  3. Assert they match the full boot sequence, chain observer first and http listener last.
func TestBootStartsTheChainObserverFirstAndTheListenerLast(t *testing.T) {
	steps := (&gateway{}).bootOrder(context.Background(), context.Background(), &config.Config{}, &bootState{})

	want := []string{
		"chain observer", "warmup prober", "host pings", "seed devshards", "publish escrows",
		"nonce ledger", "escrow lifecycle", "devshard write republish", "http listener",
	}
	assertSame(t, "boot sequence", bootStepNames(steps), want)
}

// Test flow:
//  1. Build a boot step list where "seed devshards" fails with an error.
//  2. Run the steps through startAll.
//  3. Assert startAll returns an error naming "seed devshards".
//  4. Assert only the step before the failing one ran.
func TestBootStopsAtTheFirstStepThatFails(t *testing.T) {
	var sequence []string
	record := func(name string) bootStep {
		return bootStep{name: name, start: func() error { sequence = append(sequence, name); return nil }}
	}
	steps := []bootStep{
		record("chain observer"),
		{name: "seed devshards", start: func() error { return errors.New("escrow names no key variable") }},
		record("http listener"),
	}

	err := startAll(steps)

	if err == nil || !strings.Contains(err.Error(), "seed devshards") {
		t.Fatalf("startAll() = %v, want the failing step named", err)
	}
	assertSame(t, "boot sequence", sequence, []string{"chain observer"})
}

// Test flow:
//  1. Derive a background context and hand its cancel and a recording shutdown to stopThenShutDown.
//  2. Assert the shutdown saw the background context already cancelled.
//  3. Assert the shutdown got the full grace period.
func TestTheShutdownStartsAfterTheBackgroundWorkIsCancelled(t *testing.T) {
	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()
	var cancelledFirst bool
	var grace time.Duration

	err := stopThenShutDown(stopBackground, func(given time.Duration) error {
		cancelledFirst = backgroundCtx.Err() != nil
		grace = given
		return nil
	})

	if err != nil {
		t.Fatalf("stopThenShutDown() = %v, want nil", err)
	}
	if !cancelledFirst {
		t.Error("the shutdown ran with the background context still live")
	}
	if grace != shutdownGracePeriod {
		t.Errorf("shutdown grace = %v, want %v", grace, shutdownGracePeriod)
	}
}

func servableGateway(t *testing.T, port int) *gateway {
	t.Helper()
	gatewayEnvironment(t)
	t.Setenv("DEVSHARD_PORT", strconv.Itoa(port))
	values, err := env.Load()
	if err != nil {
		t.Fatalf("env.Load() = %v, want nil", err)
	}
	storageDir, err := resolveStorageDir(values.StorageDir)
	if err != nil {
		t.Fatalf("resolveStorageDir() = %v, want nil", err)
	}
	gatewayStore, err := store.Open(storageDir)
	if err != nil {
		t.Fatalf("store.Open() = %v, want nil", err)
	}
	composed, err := compose(t.Context(), values, storageDir, gatewayStore, sessionsReading(gatewayStore, storageDir, chainWithoutADial{}))
	if err != nil {
		gatewayStore.Close()
		t.Fatalf("compose() = %v, want nil", err)
	}
	return composed
}

func backgroundStateAtShutdown(t *testing.T, composed *gateway) <-chan error {
	t.Helper()
	var backgroundErr func() error
	composedBoot := composed.bootSteps
	composed.bootSteps = func(g *gateway, ctx, background context.Context, settings *config.Config, started *bootState) []bootStep {
		backgroundErr = background.Err
		return composedBoot(g, ctx, background, settings, started)
	}
	shutdown := make(chan error, 1)
	composed.server.RegisterOnShutdown(func() { shutdown <- backgroundErr() })
	return shutdown
}

func assertBackgroundCancelled(t *testing.T, seen <-chan error) {
	t.Helper()
	select {
	case err := <-seen:
		if err == nil {
			t.Fatal("the shutdown's first step ran with the background context still live")
		}
	case <-time.After(shutdownGracePeriod):
		t.Fatal("the shutdown never stopped the http server")
	}
}

// Test flow:
//  1. Compose a gateway whose DEVSHARDS_JSON seed names a non-canonical escrow id, and record the background context serve hands its boot steps.
//  2. Serve it, so the boot fails at "seed devshards".
//  3. Assert the background context was already cancelled when the shutdown's first step stopped the http server.
func TestAFailedBootCancelsTheBackgroundWorkBeforeTheShutdownSteps(t *testing.T) {
	t.Setenv("DEVSHARDS_JSON", `[{"id":"042","model":"model-a","private_key_env":"DEVSHARD_IMPORT_KEY"}]`)
	composed := servableGateway(t, freePort(t))
	seen := backgroundStateAtShutdown(t, composed)

	err := composed.serve(t.Context())

	if !errors.Is(err, api.ErrInvalidEscrowID) {
		t.Fatalf("serve() = %v, want %v", err, api.ErrInvalidEscrowID)
	}
	assertBackgroundCancelled(t, seen)
}

// Test flow:
//  1. Hold a port, compose a gateway listening on it, and record the background context serve hands its boot steps.
//  2. Serve it with a live context, so every boot step runs and the http listener fails.
//  3. Assert the background context was already cancelled when the shutdown's first step stopped the http server.
//  4. Assert serve returns the listen error.
func TestAServeWhoseListenerFailsCancelsTheBackgroundWorkBeforeTheShutdownSteps(t *testing.T) {
	taken, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("reserving port: %v", err)
	}
	defer taken.Close()
	composed := servableGateway(t, taken.Addr().(*net.TCPAddr).Port)
	seen := backgroundStateAtShutdown(t, composed)

	served := make(chan error, 1)
	go func() { served <- composed.serve(t.Context()) }()

	assertBackgroundCancelled(t, seen)
	if err = <-served; err == nil || !strings.Contains(err.Error(), "http server") {
		t.Fatalf("serve() = %v, want the http server's listen error", err)
	}
}
