package loadtest

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHostFaultValidation(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "catch-up.yaml"))
	require.NoError(t, err)
	require.Len(t, scenario.Workload.HostFaults, 1)
	valid := scenario.Workload.HostFaults[0]
	t.Run("adjacent pauses", func(t *testing.T) {
		s := scenario
		next := valid
		next.StartAfter = "150s"
		s.Workload.HostFaults = []HostFault{valid, next}
		require.NoError(t, s.Validate())
	})
	for _, tc := range []struct {
		name   string
		change func(*Scenario)
	}{
		{"host", func(s *Scenario) { s.Workload.HostFaults[0].Host = "mock-chain" }},
		{"outside topology", func(s *Scenario) { s.Workload.HostFaults[0].Host = "versiond-4" }},
		{"implicit topology", func(s *Scenario) { s.Topology.Participants = 0 }},
		{"mode", func(s *Scenario) { s.Workload.HostFaults[0].Mode = "stop" }},
		{"negative start", func(s *Scenario) { s.Workload.HostFaults[0].StartAfter = "-1s" }},
		{"duration", func(s *Scenario) { s.Workload.HostFaults[0].Duration = "0s" }},
		{"invalid duration", func(s *Scenario) { s.Workload.HostFaults[0].Duration = "oops" }},
		{"outside workload", func(s *Scenario) { s.Workload.HostFaults[0].StartAfter = "5m" }},
		{"overlap", func(s *Scenario) { s.Workload.HostFaults = append(s.Workload.HostFaults, valid) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := scenario
			s.Workload.HostFaults = []HostFault{valid}
			tc.change(&s)
			require.Error(t, s.Validate())
		})
	}
}

func TestExecuteHostFaultLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "cancel", "pause error", "unpause error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var commands []string
			fault := HostFault{Host: "versiond-2", Mode: "pause", StartAfter: "0s", Duration: "1ms"}
			ops := hostFaultOps{
				command: func(commandCtx context.Context, action, host string) error {
					require.NoError(t, commandCtx.Err())
					require.Equal(t, fault.Host, host)
					commands = append(commands, action)
					if action == "pause" && mode == "cancel" {
						cancel()
					}
					if action == "pause" && mode == "pause error" {
						return errors.New("pause failed")
					}
					if action == "unpause" && mode == "unpause error" && len(commands) == 2 {
						return errors.New("unpause failed")
					}
					return nil
				},
			}

			result := executeHostFault(ctx, time.Now(), fault, ops)
			require.Equal(t, "pause", commands[0])
			require.Equal(t, "unpause", commands[len(commands)-1])
			if mode == "success" {
				require.Empty(t, result.Error)
				require.False(t, result.PausedAt.IsZero())
				require.False(t, result.ResumedAt.IsZero())
			} else {
				require.NotEmpty(t, result.Error)
			}
			if mode == "unpause error" {
				require.Equal(t, []string{"pause", "unpause", "unpause"}, commands)
			}
		})
	}
}

func TestHostFaultManifestOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &hostFaultRunner{cancel: cancel, done: make(chan struct{}), outputDir: t.TempDir()}
	go func() {
		<-ctx.Done()
		runner.results = []HostFaultResult{{HostFault: HostFault{Host: "versiond-2"}, Error: ctx.Err().Error()}}
		close(runner.done)
	}()
	results, err := runner.finish(true)
	require.Error(t, err)
	require.Len(t, results, 1)
	require.FileExists(t, filepath.Join(runner.outputDir, "host-faults.json"))
}
