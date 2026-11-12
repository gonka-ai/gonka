package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"devshard/testenv/config"
)

func validSnapshotHost(target string, participants int) bool {
	n, err := strconv.Atoi(strings.TrimPrefix(target, "versiond-"))
	return err == nil && target == fmt.Sprintf("versiond-%d", n) && n >= 0 && (participants == 0 || n <= participants)
}

// Reuse private admin discovery, supplying a fixed endpoint as the third argument.
var hostSnapshotScript = strings.Replace(hostCPUProfileScript,
	`http://$addr/debug/pprof/profile?seconds=$1`, `http://$addr/debug/pprof/$3`, 1)

type ProfileSnapshotResult struct {
	Kind       string    `json:"kind"`
	Target     string    `json:"target"`
	Snapshot   int       `json:"snapshot"`
	StartAfter string    `json:"start_after"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	File       string    `json:"file,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type snapshotProfiler struct {
	cancel    context.CancelFunc
	done      chan struct{}
	once      sync.Once
	results   []ProfileSnapshotResult
	outputDir string
}

func startSnapshotProfiler(ctx context.Context, opts RunnerConfig, cfg *config.File, project, compose, gatewayURL, apiKey string, diagnostics Diagnostics) (*snapshotProfiler, error) {
	type job struct {
		kind     string
		index    int
		snapshot ProfileSnapshot
		target   string
	}
	var jobs []job
	available := map[string]bool{"gateway": true}
	all := []string{"gateway"}
	for _, host := range cfg.Hosts {
		available[host.ID] = true
		all = append(all, host.ID)
	}
	for kind, snapshots := range map[string][]ProfileSnapshot{"heap": diagnostics.HeapProfiles, "goroutine": diagnostics.GoroutineProfiles} {
		for index, snapshot := range snapshots {
			targets := snapshot.Targets
			if len(targets) == 0 {
				targets = all
			}
			for _, target := range targets {
				if !available[target] {
					return nil, fmt.Errorf("%s snapshot target %q is not configured", kind, target)
				}
				jobs = append(jobs, job{kind, index + 1, snapshot, target})
			}
		}
	}
	if len(jobs) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Join(opts.OutputDir, "profiles"), 0755); err != nil {
		return nil, err
	}
	child, cancel := context.WithCancel(ctx)
	p := &snapshotProfiler{cancel: cancel, done: make(chan struct{}), outputDir: opts.OutputDir, results: make([]ProfileSnapshotResult, len(jobs))}
	started := time.Now()
	var wg sync.WaitGroup
	for index, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			after, _ := time.ParseDuration(j.snapshot.StartAfter)
			if !waitForArrival(child, started.Add(after)) {
				return
			}
			result := ProfileSnapshotResult{Kind: j.kind, Target: j.target, Snapshot: j.index, StartAfter: j.snapshot.StartAfter, StartedAt: time.Now().UTC()}
			// The gateway heap took longer than 15 s at 32 rps; a snapshot is not urgent.
			sampleCtx, sampleCancel := context.WithTimeout(child, 60*time.Second)
			defer sampleCancel()
			endpoint, suffix := "heap", "heap.pprof"
			if j.kind == "goroutine" {
				endpoint, suffix = "goroutine?debug=2", "goroutines.txt"
			}
			log.Printf("loadtest: %s snapshot start target=%s snapshot=%d", j.kind, j.target, j.index)
			var body []byte
			var err error
			if j.target == "gateway" {
				body, err = fetchCPUProfile(sampleCtx, strings.TrimRight(gatewayURL, "/")+"/debug/pprof/"+endpoint, apiKey)
			} else {
				cmd := exec.CommandContext(sampleCtx, "docker", "compose", "-p", project, "-f", compose, "exec", "-T", j.target, "sh", "-c", hostSnapshotScript, "snapshot", "0", "/proc", endpoint)
				cmd.Dir = opts.TestenvDir
				body, err = cmd.Output()
				if exitErr, ok := err.(*exec.ExitError); ok {
					err = fmt.Errorf("%w: %s", err, exitErr.Stderr)
				}
			}
			if err == nil && (len(body) == 0 || len(body) > 32<<20) {
				err = fmt.Errorf("empty or oversized %s snapshot", j.kind)
			}
			if err == nil {
				filename := filepath.Join("profiles", fmt.Sprintf("%s-snapshot-%02d.%s", j.target, j.index, suffix))
				err = os.WriteFile(filepath.Join(opts.OutputDir, filename), body, 0644)
				if err == nil {
					result.File = filename
				}
			}
			result.FinishedAt = time.Now().UTC()
			if err != nil {
				result.Error = err.Error()
			}
			log.Printf("loadtest: %s snapshot target=%s file=%s error=%q", j.kind, j.target, result.File, result.Error)
			p.results[i] = result
		}(index, j)
	}
	go func() { wg.Wait(); close(p.done) }()
	return p, nil
}

func (p *snapshotProfiler) stop() []ProfileSnapshotResult {
	if p == nil {
		return nil
	}
	p.once.Do(func() {
		p.cancel()
		<-p.done
		captured := p.results[:0]
		for _, r := range p.results {
			if !r.StartedAt.IsZero() {
				captured = append(captured, r)
			}
		}
		p.results = captured
		sort.Slice(p.results, func(i, j int) bool {
			a, b := p.results[i], p.results[j]
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			if a.Snapshot != b.Snapshot {
				return a.Snapshot < b.Snapshot
			}
			return a.Target < b.Target
		})
		body, err := json.MarshalIndent(p.results, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(p.outputDir, "profile-snapshots.json"), append(body, '\n'), 0644)
		}
		if err != nil {
			p.results = append(p.results, ProfileSnapshotResult{Kind: "manifest", Error: err.Error()})
		}
	})
	return p.results
}
