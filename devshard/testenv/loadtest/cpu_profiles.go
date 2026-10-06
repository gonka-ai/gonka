package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"devshard/testenv/config"
)

type CPUProfileResult struct {
	Target     string    `json:"target"`
	Window     int       `json:"window"`
	StartAfter string    `json:"start_after"`
	Duration   string    `json:"duration"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	File       string    `json:"file,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type cpuProfiler struct {
	cancel    context.CancelFunc
	done      chan struct{}
	results   []CPUProfileResult
	outputDir string
	once      sync.Once
}

// pprof is on the child's private admin listener, not the versiond session
// proxy. Read only its admin address; never emit the remaining environment.
// The testenv runs one devshardd child per host; reject ambiguous processes.
const hostCPUProfileScript = `found=0
addr=
for p in "$2"/[0-9]*; do
  read -r name 2>/dev/null < "$p/comm" || continue
  [ "$name" = devshardd ] || continue
  found=$((found+1))
  addr=$(tr '\000' '\n' < "$p/environ" | sed -n 's/^DEVSHARD_ADMIN_ADDR=//p')
done
[ "$found" -eq 1 ] || { echo 'expected exactly one devshardd child' >&2; exit 1; }
case "$addr" in
  127.0.0.1:[0-9]*|localhost:[0-9]*) ;;
  *) echo 'missing or unsupported child admin address' >&2; exit 1 ;;
esac
exec wget -q -T "$(( $1 + 10 ))" -O - "http://$addr/debug/pprof/profile?seconds=$1"`

func fetchCPUProfile(ctx context.Context, url, apiKey string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CPU profile returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || len(body) > 32<<20 {
		return nil, fmt.Errorf("empty or oversized CPU profile")
	}
	return body, nil
}

func startCPUProfiler(ctx context.Context, opts RunnerConfig, cfg *config.File, project, compose, gatewayURL, apiKey string, windows []CPUProfileWindow) (*cpuProfiler, error) {
	if len(windows) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Join(opts.OutputDir, "profiles"), 0o755); err != nil {
		return nil, err
	}
	child, cancel := context.WithCancel(ctx)
	p := &cpuProfiler{cancel: cancel, done: make(chan struct{}), outputDir: opts.OutputDir}
	started := time.Now()
	go func() {
		defer close(p.done)
		for index, window := range windows {
			after, _ := time.ParseDuration(window.StartAfter)
			duration, _ := time.ParseDuration(window.Duration)
			if !waitForArrival(child, started.Add(after)) {
				return
			}
			targets := []string{"gateway"}
			for _, host := range cfg.Hosts {
				targets = append(targets, host.ID)
			}
			results := make([]CPUProfileResult, len(targets))
			var wg sync.WaitGroup
			for targetIndex, target := range targets {
				wg.Add(1)
				go func(i int, target string) {
					defer wg.Done()
					result := CPUProfileResult{Target: target, Window: index + 1, StartAfter: window.StartAfter, Duration: window.Duration, StartedAt: time.Now().UTC()}
					log.Printf("loadtest: CPU profile start target=%s window=%d duration=%s", target, index+1, window.Duration)
					sampleCtx, sampleCancel := context.WithTimeout(child, duration+15*time.Second)
					defer sampleCancel()
					var body []byte
					var err error
					if target == "gateway" {
						body, err = fetchCPUProfile(sampleCtx, fmt.Sprintf("%s/debug/pprof/profile?seconds=%d", gatewayURL, int(duration/time.Second)), apiKey)
					} else {
						cmd := exec.CommandContext(sampleCtx, "docker", "compose", "-p", project, "-f", compose, "exec", "-T", target, "sh", "-c", hostCPUProfileScript, "profile", fmt.Sprint(int(duration/time.Second)), "/proc")
						cmd.Dir = opts.TestenvDir
						body, err = cmd.Output()
						if err != nil {
							if exitErr, ok := err.(*exec.ExitError); ok {
								err = fmt.Errorf("%w: %s", err, exitErr.Stderr)
							}
						}
					}
					if err == nil && len(body) == 0 {
						err = fmt.Errorf("empty CPU profile")
					}
					if err == nil {
						file := filepath.Join("profiles", fmt.Sprintf("%s-window-%02d.cpu.pprof", filepath.Base(target), index+1))
						err = os.WriteFile(filepath.Join(opts.OutputDir, file), body, 0o644)
						if err == nil {
							result.File = file
						}
					}
					result.FinishedAt = time.Now().UTC()
					if err != nil {
						result.Error = err.Error()
						log.Printf("loadtest: CPU profile failed target=%s window=%d: %v", target, index+1, err)
					} else {
						log.Printf("loadtest: CPU profile saved target=%s window=%d file=%s", target, index+1, result.File)
					}
					results[i] = result
				}(targetIndex, target)
			}
			wg.Wait()
			p.results = append(p.results, results...)
		}
	}()
	return p, nil
}

func (p *cpuProfiler) stop() []CPUProfileResult {
	if p == nil {
		return nil
	}
	p.once.Do(func() {
		p.cancel()
		<-p.done
		sort.Slice(p.results, func(i, j int) bool {
			if p.results[i].Window != p.results[j].Window {
				return p.results[i].Window < p.results[j].Window
			}
			return p.results[i].Target < p.results[j].Target
		})
		body, err := json.MarshalIndent(p.results, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(p.outputDir, "cpu-profiles.json"), append(body, '\n'), 0o644)
		}
		if err != nil {
			p.results = append(p.results, CPUProfileResult{Target: "manifest", Error: err.Error()})
		}
	})
	return p.results
}
