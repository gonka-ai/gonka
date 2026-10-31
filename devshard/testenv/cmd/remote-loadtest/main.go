// remote-loadtest runs the existing client workload against an external gateway.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
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
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	scenarioPath := flag.String("scenario", "loadtest/scenarios/normal-load.yaml", "scenario YAML (client workload and request assertions only)")
	loadDataset := flag.String("load-dataset", "", "JSONL dataset with captured client requests (fixture responses ignored)")
	base := flag.String("base", os.Getenv("GONKA_BASE"), "gateway base URL, including /gateway if needed; defaults to GONKA_BASE")
	model := flag.String("model", os.Getenv("GONKA_MODEL"), "model override; defaults to GONKA_MODEL")
	output := flag.String("output", "", "new artifact directory")
	duration := flag.Duration("duration", 0, "workload duration override")
	concurrency := flag.Int("concurrency", 0, "max in flight override")
	stream := flag.Bool("stream", false, "override request streaming")
	requestTimeout := flag.Duration("request-timeout", 30*time.Second, "override scenario workload.request_timeout (default 30s)")
	reportInterval := flag.Duration("report-interval", 5*time.Second, "override scenario workload.report_interval (default 5s)")
	flag.Parse()
	apiKey := os.Getenv("GONKA_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("set GONKA_API_KEY")
	}
	*base = strings.TrimRight(*base, "/")
	u, err := url.Parse(*base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base must be an http(s) gateway URL without credentials, query or fragment")
	}
	if *duration < 0 || *concurrency < 0 || *requestTimeout <= 0 || *reportInterval <= 0 {
		return fmt.Errorf("duration/concurrency overrides must be nonnegative; request-timeout and report-interval must be positive")
	}
	scenario, err := loadtest.LoadScenario(*scenarioPath)
	if err != nil {
		return err
	}
	if len(scenario.Workload.HostFaults) > 0 {
		return fmt.Errorf("remote runner cannot apply host faults")
	}
	if *model != "" {
		scenario.Workload.Request.Model = *model
	}
	if *duration > 0 {
		scenario.Workload.Duration = duration.String()
	}
	if *concurrency > 0 {
		scenario.Workload.MaxInFlight = *concurrency
	}
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "stream" {
			scenario.Workload.Request.Stream = *stream
		}
		if f.Name == "request-timeout" {
			scenario.Workload.RequestTimeout = requestTimeout.String()
		}
		if f.Name == "report-interval" {
			scenario.Workload.ReportInterval = reportInterval.String()
		}
	})
	if scenario.Workload.ReportInterval == "" {
		scenario.Workload.ReportInterval = min(5*time.Second, scenario.Duration()).String()
	}
	if err := scenario.Validate(); err != nil {
		return err
	}
	*requestTimeout = scenario.RequestTimeout()
	scenario.Workload.RequestTimeout = requestTimeout.String()
	var selector *replay.Selector
	if *loadDataset != "" {
		*loadDataset, err = filepath.Abs(*loadDataset)
		if err != nil {
			return err
		}
		dataset, err := replay.LoadRequestsFile(*loadDataset)
		if err != nil {
			return err
		}
		selector = replay.NewSelector(dataset, scenario.Seed)
		fmt.Printf("Dataset: %s; samples: %d; captured models: %v; target model: %s\n", *loadDataset, dataset.Len(), dataset.Models(), scenario.Workload.Request.Model)
		fmt.Println("Sending fixture requests to the target model; checking HTTP status only.")
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
	body, err := yaml.Marshal(scenario)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*output, "run.yaml"), body, 0o644); err != nil {
		return err
	}
	streamMode := fmt.Sprint(scenario.Workload.Request.Stream)
	if selector != nil {
		streamMode = "per dataset request"
	}
	fmt.Printf("Remote client workload: %s\nModel: %s; duration: %s; max in flight: %d; stream: %s\nArtifacts: %s\nValidation: HTTP status only.\n", *base, scenario.Workload.Request.Model, scenario.Workload.Duration, scenario.Workload.MaxInFlight, streamMode, *output)
	fmt.Printf("Request timeout: %s (includes response body); report interval: %s\n", *requestTimeout, scenario.Workload.ReportInterval)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, scenario.Duration()+*requestTimeout+5*time.Second)
	defer cancel()
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return fmt.Errorf("gateway connectivity check failed before load: %w", err)
	}
	_ = conn.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = scenario.Workload.MaxInFlight
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: *requestTimeout, Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	fmt.Println("Collecting Gateway metrics at 0%, 10%, ..., 100% of workload, then after final responses.")
	metrics, err := startRemoteMetrics(ctx, client, *base, apiKey, scenario.Workload.Request.Model, *output, scenario.Duration())
	if err != nil {
		return err
	}
	summary, err := loadtest.RunGenerator(ctx, loadtest.GeneratorConfig{
		GatewayURL: *base, APIKey: apiKey, Scenario: scenario, OutputDir: *output, Client: client,
		Replay: selector, ReplayModel: scenario.Workload.Request.Model, HTTPStatusOnly: true,
	})
	metricsSummary := metrics.stop()
	if err != nil {
		return err
	}
	passed := ctx.Err() == nil && summary.Requests > 0 && summary.Completed > 0 && summary.ErrorRate <= scenario.Assertions.Requests.ErrorRate
	report := struct {
		Passed             bool     `json:"passed"`
		MaxErrorRate       float64  `json:"max_error_rate"`
		ActualErrorRate    float64  `json:"actual_error_rate"`
		ExpectedHTTPStatus int      `json:"expected_http_status"`
		ExpectedOutcome    string   `json:"expected_outcome"`
		Skipped            []string `json:"skipped"`
	}{passed, scenario.Assertions.Requests.ErrorRate, summary.ErrorRate, scenario.Assertions.Requests.HTTPStatus, scenario.Assertions.Requests.TerminalOutcome, []string{"devshard", "mock_ml", "escrow_rotation", "accounting", "host process metrics and profiles"}}
	body, err = json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*output, "assertions.json"), append(body, '\n'), 0o644); err != nil {
		return err
	}
	var performance bytes.Buffer
	printPerformanceReport(&performance, summary, scenario, passed, *output, *loadDataset)
	printRemoteMetrics(&performance, metricsSummary)
	if err := os.WriteFile(filepath.Join(*output, "report.txt"), performance.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Print(performance.String())
	if err := ctx.Err(); err != nil {
		return err
	}
	if !passed {
		return fmt.Errorf("remote client workload failed; inspect %s", *output)
	}
	return nil
}
