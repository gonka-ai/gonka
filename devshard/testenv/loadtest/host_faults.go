package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"devshard/testenv/config"
)

func validateHostFaults(topology Topology, faults []HostFault, workload time.Duration) error {
	ends := map[string]time.Duration{}
	for i, fault := range faults {
		validHost := false
		for h := 0; h < topology.Participants+1; h++ {
			validHost = validHost || fault.Host == fmt.Sprintf("versiond-%d", h)
		}
		if topology.Participants == 0 || !validHost {
			return fmt.Errorf("workload.host_faults[%d]: host must belong to an explicit topology.participants", i)
		}
		start, e1 := time.ParseDuration(fault.StartAfter)
		duration, e2 := time.ParseDuration(fault.Duration)
		if fault.Mode != "pause" || e1 != nil || e2 != nil || start < 0 || duration <= 0 || start+duration > workload || duration > workload-start {
			return fmt.Errorf("workload.host_faults[%d]: requires mode pause, non-negative start_after, positive duration, and pause within workload", i)
		}
		if end, ok := ends[fault.Host]; ok && start < end {
			return fmt.Errorf("workload.host_faults[%d]: windows for %s must be ordered and not overlap", i, fault.Host)
		}
		ends[fault.Host] = start + duration
	}
	return nil
}

func validTopologyHost(host string, participants int) bool {
	for index := 0; index <= participants; index++ {
		if host == fmt.Sprintf("versiond-%d", index) {
			return true
		}
	}
	return false
}

type HostFaultResult struct {
	HostFault
	PausedAt  time.Time `json:"paused_at,omitempty"`
	ResumedAt time.Time `json:"resumed_at,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Dependencies keep lifecycle tests independent of Docker.
type hostFaultOps struct {
	command func(context.Context, string, string) error
}

func executeHostFault(ctx context.Context, started time.Time, fault HostFault, ops hostFaultOps) (result HostFaultResult) {
	result.HostFault = fault
	var runErr error
	defer func() {
		if runErr != nil {
			result.Error = runErr.Error()
		}
	}()
	after, _ := time.ParseDuration(fault.StartAfter)
	duration, _ := time.ParseDuration(fault.Duration)
	if !waitForArrival(ctx, started.Add(after)) {
		runErr = ctx.Err()
		return
	}

	// Always attempt unpause even when pause returns an ambiguous failure.
	paused := true
	defer func() {
		if paused {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cleanupErr := ops.command(cleanup, "unpause", fault.Host)
			if cleanupErr == nil && !result.PausedAt.IsZero() {
				result.ResumedAt = time.Now().UTC()
			}
			runErr = errors.Join(runErr, cleanupErr)
		}
	}()
	runErr = ops.command(ctx, "pause", fault.Host)
	if runErr != nil {
		return
	}
	result.PausedAt = time.Now().UTC()
	log.Printf("loadtest: host=%s stage=paused duration=%s", fault.Host, fault.Duration)
	if !waitForArrival(ctx, result.PausedAt.Add(duration)) {
		runErr = ctx.Err()
		return
	}

	runErr = ops.command(ctx, "unpause", fault.Host)
	if runErr != nil {
		return
	}
	paused = false
	result.ResumedAt = time.Now().UTC()
	log.Printf("loadtest: host=%s stage=resumed", fault.Host)
	return
}

type hostFaultRunner struct {
	cancel    context.CancelFunc
	done      chan struct{}
	results   []HostFaultResult
	outputDir string
	once      sync.Once
	err       error
}

func startHostFaults(ctx context.Context, opts RunnerConfig, cfg *config.File, project, compose string, faults []HostFault) (*hostFaultRunner, error) {
	if len(faults) == 0 {
		return nil, nil
	}
	hosts := make(map[string]bool)
	for _, host := range cfg.Hosts {
		hosts[host.ID] = true
	}
	for _, fault := range faults {
		if !hosts[fault.Host] {
			return nil, fmt.Errorf("host fault target %s absent from generated topology", fault.Host)
		}
	}
	ops := hostFaultOps{
		command: func(ctx context.Context, action, host string) error {
			commandCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			return runCommand(commandCtx, opts.TestenvDir, "docker", "compose", "-p", project, "-f", compose, action, host)
		},
	}

	child, cancel := context.WithCancel(ctx)
	runner := &hostFaultRunner{cancel: cancel, done: make(chan struct{}), results: make([]HostFaultResult, len(faults)), outputDir: opts.OutputDir}
	started := time.Now()
	go func() {
		defer close(runner.done)
		var wg sync.WaitGroup
		for host := range hosts {
			wg.Add(1)
			go func(host string) {
				defer wg.Done()
				for i, fault := range faults {
					if fault.Host == host {
						runner.results[i] = executeHostFault(child, started, fault, ops)
					}
				}
			}(host)
		}
		wg.Wait()
	}()
	return runner, nil
}

func (r *hostFaultRunner) finish(cancel bool) ([]HostFaultResult, error) {
	if r == nil {
		return nil, nil
	}
	r.once.Do(func() {
		if cancel {
			r.cancel()
		}
		<-r.done
		r.cancel()
		for _, result := range r.results {
			if result.Error != "" {
				r.err = errors.Join(r.err, fmt.Errorf("host fault %s: %s", result.Host, result.Error))
			}
		}
		body, err := json.MarshalIndent(r.results, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(r.outputDir, "host-faults.json"), append(body, '\n'), 0o644)
		}
		r.err = errors.Join(r.err, err)
	})
	return r.results, r.err
}
