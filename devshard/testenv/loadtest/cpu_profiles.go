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
	"strings"
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

type ContentionProfileResult struct {
	Target            string    `json:"target"`
	Window            int       `json:"window"`
	Kind              string    `json:"kind"`
	StartAfter        string    `json:"start_after"`
	CPUWindowDuration string    `json:"cpu_window_duration"`
	StartedAt         time.Time `json:"started_at"`
	FinishedAt        time.Time `json:"finished_at"`
	File              string    `json:"file,omitempty"`
	Error             string    `json:"error,omitempty"`
}

type cpuProfiler struct {
	cancel            context.CancelFunc
	done              chan struct{}
	results           []CPUProfileResult
	contentionResults []ContentionProfileResult
	contentionEnabled bool
	outputDir         string
	once              sync.Once
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
[ "$found" -eq 1 ] || { echo "expected exactly one devshardd child, found $found" >&2; exit 1; }
case "$addr" in
  127.0.0.1:[0-9]*|localhost:[0-9]*) ;;
  *) echo 'missing or unsupported child admin address' >&2; exit 1 ;;
esac
exec wget -q -T "$(( $1 + 10 ))" -O - "http://$addr/debug/pprof/profile?seconds=$1"`

func fetchPprofProfile(ctx context.Context, url, apiKey, name string) ([]byte, error) {
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
		return nil, fmt.Errorf("%s returned %s", name, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || len(body) > 32<<20 {
		return nil, fmt.Errorf("empty or oversized %s", name)
	}
	return body, nil
}

func fetchCPUProfile(ctx context.Context, url, apiKey string) ([]byte, error) {
	return fetchPprofProfile(ctx, url, apiKey, "CPU profile")
}

type contentionProfileSpec struct {
	kind     string
	endpoint string
	suffix   string
}

var contentionProfileSpecs = []contentionProfileSpec{
	{kind: "goroutine", endpoint: "goroutine?debug=1", suffix: "goroutines.txt"},
	{kind: "mutex", endpoint: "mutex", suffix: "mutex.pprof"},
	{kind: "block", endpoint: "block", suffix: "block.pprof"},
}

func startCPUProfiler(ctx context.Context, opts RunnerConfig, cfg *config.File, project, compose, gatewayURL, apiKey string, windows []CPUProfileWindow, contentionEnabled bool) (*cpuProfiler, error) {
	if len(windows) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Join(opts.OutputDir, "profiles"), 0o755); err != nil {
		return nil, err
	}
	child, cancel := context.WithCancel(ctx)
	p := &cpuProfiler{cancel: cancel, done: make(chan struct{}), outputDir: opts.OutputDir, contentionEnabled: contentionEnabled}
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
			if p.contentionEnabled {
				p.collectContentionWindow(child, opts, project, compose, gatewayURL, apiKey, targets, index+1, window)
			}
		}
	}()
	return p, nil
}

func (p *cpuProfiler) collectContentionWindow(ctx context.Context, opts RunnerConfig, project, compose, gatewayURL, apiKey string, targets []string, windowIndex int, window CPUProfileWindow) {
	results := make([]ContentionProfileResult, len(targets)*len(contentionProfileSpecs))
	var wg sync.WaitGroup
	for targetIndex, target := range targets {
		for specIndex, spec := range contentionProfileSpecs {
			resultIndex := targetIndex*len(contentionProfileSpecs) + specIndex
			wg.Add(1)
			go func(i int, target string, spec contentionProfileSpec) {
				defer wg.Done()
				result := ContentionProfileResult{
					Target: target, Window: windowIndex, Kind: spec.kind,
					StartAfter: window.StartAfter, CPUWindowDuration: window.Duration,
					StartedAt: time.Now().UTC(),
				}
				log.Printf("loadtest: contention profile start target=%s kind=%s window=%d", target, spec.kind, windowIndex)
				sampleCtx, sampleCancel := context.WithTimeout(ctx, 15*time.Second)
				defer sampleCancel()
				var body []byte
				var err error
				if target == "gateway" {
					body, err = fetchPprofProfile(sampleCtx, strings.TrimRight(gatewayURL, "/")+"/debug/pprof/"+spec.endpoint, apiKey, spec.kind+" profile")
				} else {
					cmd := exec.CommandContext(sampleCtx, "docker", "compose", "-p", project, "-f", compose, "exec", "-T", target, "sh", "-c", hostSnapshotScript, "snapshot", "0", "/proc", spec.endpoint)
					cmd.Dir = opts.TestenvDir
					body, err = cmd.Output()
					if exitErr, ok := err.(*exec.ExitError); ok {
						err = fmt.Errorf("%w: %s", err, exitErr.Stderr)
					}
				}
				if err == nil && (len(body) == 0 || len(body) > 32<<20) {
					err = fmt.Errorf("empty or oversized %s profile", spec.kind)
				}
				if err == nil {
					file := filepath.Join("profiles", fmt.Sprintf("%s-window-%02d.%s", filepath.Base(target), windowIndex, spec.suffix))
					err = os.WriteFile(filepath.Join(p.outputDir, file), body, 0o644)
					if err == nil {
						result.File = file
					}
				}
				result.FinishedAt = time.Now().UTC()
				if err != nil {
					result.Error = err.Error()
					log.Printf("loadtest: contention profile failed target=%s kind=%s window=%d: %v", target, spec.kind, windowIndex, err)
				} else {
					log.Printf("loadtest: contention profile saved target=%s kind=%s window=%d file=%s", target, spec.kind, windowIndex, result.File)
				}
				results[i] = result
			}(resultIndex, target, spec)
		}
	}
	wg.Wait()
	p.contentionResults = append(p.contentionResults, results...)
}

func (p *cpuProfiler) stop() ([]CPUProfileResult, []ContentionProfileResult) {
	if p == nil {
		return nil, nil
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
		if p.contentionEnabled {
			sort.Slice(p.contentionResults, func(i, j int) bool {
				a, b := p.contentionResults[i], p.contentionResults[j]
				if a.Window != b.Window {
					return a.Window < b.Window
				}
				if a.Target != b.Target {
					return a.Target < b.Target
				}
				return a.Kind < b.Kind
			})
			contentionBody, contentionErr := json.MarshalIndent(p.contentionResults, "", "  ")
			if contentionErr == nil {
				contentionErr = os.WriteFile(filepath.Join(p.outputDir, "contention-profiles.json"), append(contentionBody, '\n'), 0o644)
			}
			if contentionErr != nil {
				p.contentionResults = append(p.contentionResults, ContentionProfileResult{Target: "manifest", Error: contentionErr.Error()})
			}
		}
	})
	return p.results, p.contentionResults
}
