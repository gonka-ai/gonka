package hostctl

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDockerRuntimeStateClassifiesContainerAbsence(t *testing.T) {
	runtime := dockerRuntime{
		service: "versiond-2",
		run: func(context.Context, ...string) (string, error) {
			return "", errors.New(
				"docker: exit status 1: Error: No such object: versiond-2",
			)
		},
	}

	state, err := runtime.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state != serviceAbsent {
		t.Fatalf("Docker service state = %s, want %s", state, serviceAbsent)
	}
}

func TestDockerRuntimeStateKeepsOtherInspectErrors(t *testing.T) {
	runtime := dockerRuntime{
		service: "versiond-2",
		run: func(context.Context, ...string) (string, error) {
			return "", errors.New("docker: permission denied")
		},
	}

	if _, err := runtime.State(context.Background()); err == nil {
		t.Fatal("Docker inspect permission error was treated as service absence")
	}
}

func TestSystemdRuntimeStateDistinguishesAbsentUnit(t *testing.T) {
	runtime := systemdRuntime{
		service: "versiond-2.service",
		run: func(_ context.Context, args ...string) (string, error) {
			want := "systemctl show versiond-2.service --property=LoadState --property=ActiveState"
			if got := strings.Join(args, " "); got != want {
				t.Fatalf("systemd state command = %q, want %q", got, want)
			}
			return "LoadState=not-found\nActiveState=inactive\n",
				errors.New("systemctl: exit status 1")
		},
	}

	state, err := runtime.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state != serviceAbsent {
		t.Fatalf("systemd service state = %s, want %s", state, serviceAbsent)
	}
}

func TestSystemdRuntimeStateKeepsLoadedInactiveUnitStopped(t *testing.T) {
	runtime := systemdRuntime{
		service: "versiond-2.service",
		run: func(context.Context, ...string) (string, error) {
			return "LoadState=loaded\nActiveState=inactive\n", nil
		},
	}

	state, err := runtime.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state != serviceStopped {
		t.Fatalf("systemd service state = %s, want %s", state, serviceStopped)
	}
}
