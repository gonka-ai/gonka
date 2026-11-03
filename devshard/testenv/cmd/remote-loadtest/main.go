// Binary remote-loadtest sends client traffic to an existing gateway without provisioning a stack.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"devshard/testenv/loadtest"
	"devshard/testenv/replay"
	"gopkg.in/yaml.v3"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(parent context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("remote-loadtest", flag.ContinueOnError)
	fs.SetOutput(out)
	scenarioPath := fs.String("scenario", "loadtest/scenarios/normal-load.yaml", "scenario YAML defining client traffic and models")
	datasetPath := fs.String("load-dataset", "", "JSONL requests grouped by model; fixture responses are not compared")
	base := fs.String("base", getenv("GONKA_BASE"), "remote gateway base URL including /gateway if needed")
	model := fs.String("model", "", "single-model override; incompatible with mix.models")
	output := fs.String("output", "", "new artifact directory")
	preflightOnly := fs.Bool("preflight-only", false, "inspect remote models, capacity, escrows and PoC without sending inference requests")
	duration := fs.Duration("duration", 0, "override workload duration")
	concurrency := fs.Int("concurrency", 0, "override max in flight")
	stream := fs.Bool("stream", false, "override common synthetic request streaming")
	requestTimeout := fs.Duration("request-timeout", 0, "override the entire HTTP request timeout")
	reportInterval := fs.Duration("report-interval", 0, "override workload report interval")
	httpStatusOnly := fs.Bool("http-status-only", false, "check only HTTP status, recording body read errors separately")
	preflightTimeout := fs.Duration("preflight-timeout", 5*time.Second, "timeout for each read-only environment probe")
	if err := fs.Parse(args); err != nil {
		return err
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if *duration < 0 || *concurrency < 0 || explicit["request-timeout"] && *requestTimeout <= 0 || explicit["report-interval"] && *reportInterval <= 0 {
		return fmt.Errorf("duration/concurrency must be nonnegative; explicit request-timeout/report-interval must be positive")
	}
	if *preflightTimeout <= 0 {
		return fmt.Errorf("preflight-timeout must be positive")
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	apiKey := getenv("GONKA_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("set GONKA_API_KEY")
	}
	*base = strings.TrimRight(*base, "/")
	u, err := url.Parse(*base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base must be an http(s) gateway URL without credentials, query or fragment")
	}
	loader := loadtest.LoadScenario
	if *datasetPath != "" {
		loader = loadtest.LoadDatasetScenario
	}
	scenario, err := loader(*scenarioPath)
	if err != nil {
		return err
	}
	if len(scenario.Workload.HostFaults) > 0 || len(scenario.Workload.Events) > 0 {
		return fmt.Errorf("remote runner cannot apply scenario host faults or events")
	}
	if mix := scenario.Workload.Request.Mix; mix != nil && len(mix.Models) > 0 {
		if *model != "" {
			return fmt.Errorf("-model cannot override mix.models; set model IDs and weights in YAML")
		}
		// GONKA_MODEL is intentionally ignored for a multi-model YAML scenario.
	} else {
		if *model == "" {
			*model = getenv("GONKA_MODEL")
		}
		if *model != "" {
			scenario.Workload.Request.Model = *model
		}
	}
	if *duration > 0 {
		scenario.Workload.Duration = duration.String()
	}
	if *concurrency > 0 {
		scenario.Workload.MaxInFlight = *concurrency
	}
	if explicit["stream"] {
		scenario.Workload.Request.Stream = *stream
	}
	if explicit["request-timeout"] {
		scenario.Workload.RequestTimeout = requestTimeout.String()
	}
	if explicit["report-interval"] {
		scenario.Workload.ReportInterval = reportInterval.String()
	}
	if scenario.Workload.ReportInterval == "" {
		scenario.Workload.ReportInterval = min(5*time.Second, scenario.Duration()).String()
	}
	if *datasetPath != "" {
		err = scenario.ValidateForDataset()
	} else {
		err = scenario.Validate()
	}
	if err != nil {
		return err
	}
	var selector *replay.Selector
	var dataset *replay.Dataset
	if *datasetPath != "" {
		dataset, err = replay.LoadRequestsFile(*datasetPath)
		if err != nil {
			return err
		}
		configured := map[string]bool{}
		for _, id := range scenario.RequestModels() {
			if !dataset.HasModel(id) {
				return fmt.Errorf("dataset has no samples for configured model %q", id)
			}
			configured[id] = true
		}
		for _, id := range dataset.Models() {
			if !configured[id] {
				fmt.Fprintf(out, "Ignoring unconfigured dataset model %q\n", id)
			}
		}
		selector = replay.NewSelector(dataset, scenario.Seed)
	}
	if *output == "" {
		*output = filepath.Join("loadtest", "results", "remote-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
	}
	*output, err = filepath.Abs(*output)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(*output, 0o755); err != nil {
		return fmt.Errorf("create fresh output directory: %w", err)
	}
	config, err := yaml.Marshal(scenario)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*output, "run.yaml"), config, 0o644); err != nil {
		return err
	}
	consoleSection(out, "PRE-FLIGHT CHECK")
	fmt.Fprintf(out, "Remote gateway: %s\nModels: %v\nArtifacts: %s\n\n", *base, scenario.RequestModels(), *output)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = scenario.Workload.MaxInFlight
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: scenario.RequestTimeout(), CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	environment := collectEnvironment(parent, client, *base, apiKey, scenario, dataset, *preflightTimeout)
	snapshot, err := json.MarshalIndent(environment, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*output, "environment.json"), append(snapshot, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Fprint(out, environmentText(environment))
	if *preflightOnly || !environment.Ready {
		result := "READY (preflight only; workload not started)"
		if !environment.Ready {
			result = "BLOCKED (preflight failed; workload not started)"
		}
		report := "# Remote environment preflight\n\nResult: " + result + "\n" + environmentMarkdown(environment)
		if err := os.WriteFile(filepath.Join(*output, "report.md"), []byte(report), 0o644); err != nil {
			return err
		}
		assertions, err := json.MarshalIndent(map[string]any{"passed": environment.Ready, "preflight_ready": environment.Ready, "workload_started": false, "scope": "remote environment preflight only"}, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*output, "assertions.json"), append(assertions, '\n'), 0o644); err != nil {
			return err
		}
		consoleSummary(out, result, nil)
		fmt.Fprintf(out, "Artifacts: %s\n", *output)
		if !environment.Ready {
			return fmt.Errorf("remote preflight blocked workload; inspect %s/environment.json", *output)
		}
		return nil
	}
	consoleSection(out, "LOAD TEST")
	fmt.Fprintf(out, "Duration: %s\nTraffic: %s\nMax in flight: %d\nScope: client requests only\n\n", scenario.Duration(), environment.Workload.Traffic, scenario.Workload.MaxInFlight)
	ctx, cancel := context.WithTimeout(parent, scenario.Duration()+scenario.RequestTimeout()+5*time.Second)
	defer cancel()
	metricModel := ""
	if models := scenario.RequestModels(); len(models) == 1 {
		metricModel = models[0]
	}
	metrics, err := startRemoteMetrics(ctx, client, *base, apiKey, metricModel, *output, scenario.Duration())
	if err != nil {
		return err
	}
	summary, err := loadtest.RunGenerator(ctx, loadtest.GeneratorConfig{
		GatewayURL: *base, APIKey: apiKey, Scenario: scenario, OutputDir: *output, Client: client, Replay: selector, HTTPStatusOnly: *httpStatusOnly,
	})
	metricsSummary := metrics.stop()
	if err != nil {
		consoleSummary(out, "ERROR", nil)
		fmt.Fprintf(out, "Reason: %s\nArtifacts: %s\n", err, *output)
		return err
	}
	dropBound := scenario.Assertions.Requests.ErrorRate
	if value := scenario.Assertions.Requests.DroppedRate; value != nil {
		dropBound = *value
	}
	passed := ctx.Err() == nil && summary.Requests > 0 && summary.Completed > 0 && summary.ErrorRate <= scenario.Assertions.Requests.ErrorRate && summary.DroppedRate <= dropBound
	verdict := "FAIL"
	if passed {
		verdict = "PASS"
	}
	checks, err := json.MarshalIndent(map[string]any{
		"preflight_ready": environment.Ready, "workload_started": true,
		"passed": passed, "error_rate": summary.ErrorRate, "max_error_rate": scenario.Assertions.Requests.ErrorRate,
		"dropped_rate": summary.DroppedRate, "max_dropped_rate": dropBound,
		"scope":            "client requests only; local topology and server state assertions are not evaluated",
		"http_status_only": *httpStatusOnly,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*output, "assertions.json"), append(checks, '\n'), 0o644); err != nil {
		return err
	}
	report := fmt.Sprintf("# Remote load test\n\nResult: %s\n\nClient request checks only. Server lifecycle, settlement, and host state were not evaluated.\n\nGateway: %s\n\nModels: %v\n\nPlanned: %d; sent: %d; completed: %d; errors: %d; dropped: %d\n", verdict, *base, scenario.RequestModels(), summary.Requests, summary.Requests-summary.Dropped, summary.Completed, summary.Requests-summary.Dropped-summary.Completed, summary.Dropped)
	report += environmentMarkdown(environment)
	report += loadtest.RenderModelBreakdown(summary.ModelBreakdown)
	var gatewayMetrics bytes.Buffer
	printRemoteMetrics(&gatewayMetrics, metricsSummary)
	report += "\n## Gateway metrics\n\nGateway observations may include traffic from other clients.\n\n```text\n" + gatewayMetrics.String() + "```\n"
	if *httpStatusOnly {
		report += "\nValidation: HTTP status only; response read errors are recorded separately.\n"
	}
	if err := os.WriteFile(filepath.Join(*output, "report.md"), []byte(report), 0o644); err != nil {
		return err
	}
	var terminal bytes.Buffer
	consoleSummary(&terminal, verdict, summary.ModelBreakdown)
	if *httpStatusOnly {
		fmt.Fprintln(&terminal, "Validation: HTTP status only; response read errors are recorded separately.")
	}
	fmt.Fprintln(&terminal, "Gateway observations may include traffic from other clients.")
	printRemoteMetrics(&terminal, metricsSummary)
	fmt.Fprintf(&terminal, "\nArtifacts: %s\n", *output)
	if err := os.WriteFile(filepath.Join(*output, "report.txt"), terminal.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Fprint(out, terminal.String())
	if err := ctx.Err(); err != nil {
		return err
	}
	if !passed {
		return fmt.Errorf("remote client workload failed; inspect %s", *output)
	}
	return nil
}
