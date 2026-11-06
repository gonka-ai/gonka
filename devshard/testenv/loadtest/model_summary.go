package loadtest

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ModelBreakdown separates failed requests from planned arrivals never sent.
// Its latency samples contain successful responses only, including their full bodies.
type ModelBreakdown struct {
	Models []ModelSummary `json:"models"`
	Total  ModelSummary   `json:"total"`
}

type ModelSummary struct {
	Model            string         `json:"model"`
	Planned          int            `json:"planned"`
	Sent             int            `json:"sent"`
	Completed        int            `json:"completed"`
	Dropped          int            `json:"dropped"`
	ConcurrencyDrops int            `json:"concurrency_drops"`
	BehindDrops      int            `json:"generator_behind_drops"`
	Errors           int            `json:"errors"`
	ErrorCategories  map[string]int `json:"error_categories"`
	SuccessRate      float64        `json:"success_rate"`
	CompletionRate   float64        `json:"completion_rate"`
	SuccessfulRPS    float64        `json:"successful_rps"`
	LatencySamples   int            `json:"latency_samples"`
	P50              time.Duration  `json:"p50_latency"`
	P95              time.Duration  `json:"p95_latency"`
	P99              time.Duration  `json:"p99_latency"`
	Max              time.Duration  `json:"max_latency"`
	ClientResult     string         `json:"client_result"`
}

type modelAccumulator struct {
	ModelSummary
	latencies []time.Duration
}

func newModelAccumulator(model string) *modelAccumulator {
	return &modelAccumulator{ModelSummary: ModelSummary{Model: model, ErrorCategories: map[string]int{}}}
}

// BuildModelBreakdown preserves YAML model order and pools all successful
// latency samples for Total; it never averages model percentiles.
func BuildModelBreakdown(scenario Scenario, results []RequestResult) *ModelBreakdown {
	groups := map[string]*modelAccumulator{}
	order := []string{}
	add := func(model string) *modelAccumulator {
		if groups[model] == nil {
			groups[model] = newModelAccumulator(model)
			order = append(order, model)
		}
		return groups[model]
	}
	for _, model := range scenario.RequestModels() {
		add(model)
	}
	total := newModelAccumulator("Total")
	for _, result := range results {
		group := add(result.Model)
		group.observe(scenario, result)
		total.observe(scenario, result)
	}
	breakdown := &ModelBreakdown{}
	for _, model := range order {
		groups[model].finish(scenario)
		row := groups[model].ModelSummary
		if row.Model == "" {
			row.Model = "Unattributed"
		}
		breakdown.Models = append(breakdown.Models, row)
	}
	total.finish(scenario)
	breakdown.Total = total.ModelSummary
	return breakdown
}

func (a *modelAccumulator) observe(scenario Scenario, r RequestResult) {
	a.Planned++
	if r.Outcome == "dropped_by_generator" {
		a.Dropped++
		if r.Error == generatorBehind {
			a.BehindDrops++
		} else {
			a.ConcurrencyDrops++
		}
		return
	}
	a.Sent++
	if r.Outcome == scenario.Assertions.Requests.TerminalOutcome {
		a.Completed++
		a.latencies = append(a.latencies, r.Duration)
		return
	}
	a.Errors++
	a.ErrorCategories[resultErrorCategory(scenario, r)]++
}

func resultErrorCategory(scenario Scenario, r RequestResult) string {
	if r.ErrorCategory != "" {
		return r.ErrorCategory
	}
	// Legacy artifacts did not record typed error categories.
	if r.Status > 0 && r.Status != scenario.Assertions.Requests.HTTPStatus {
		return fmt.Sprintf("http_%d", r.Status)
	}
	lower := strings.ToLower(r.Error)
	if strings.Contains(lower, "deadline exceeded") || strings.Contains(lower, "timeout") {
		return "timeout"
	}
	if strings.Contains(lower, "context canceled") {
		return "cancelled"
	}
	if r.StreamOutcome != "" && r.StreamOutcome != streamCompleted {
		return "stream_" + r.StreamOutcome
	}
	if r.Status == 0 {
		return "network"
	}
	return "invalid_response"
}

func (a *modelAccumulator) finish(scenario Scenario) {
	if a.Sent > 0 {
		a.SuccessRate = float64(a.Completed) / float64(a.Sent)
	}
	if a.Planned > 0 {
		a.CompletionRate = float64(a.Completed) / float64(a.Planned)
	}
	if duration := scenario.Duration(); duration > 0 {
		a.SuccessfulRPS = float64(a.Completed) / duration.Seconds()
	}
	sort.Slice(a.latencies, func(i, j int) bool { return a.latencies[i] < a.latencies[j] })
	a.LatencySamples = len(a.latencies)
	a.P50 = percentile(a.latencies, 0.5)
	a.P95 = percentile(a.latencies, 0.95)
	a.P99 = percentile(a.latencies, 0.99)
	if len(a.latencies) > 0 {
		a.Max = a.latencies[len(a.latencies)-1]
	}
	a.ClientResult = "NO REQUESTS"
	if a.Planned == 0 {
		return
	}
	dropBound := scenario.Assertions.Requests.ErrorRate
	if value := scenario.Assertions.Requests.DroppedRate; value != nil {
		dropBound = *value
	}
	a.ClientResult = "FAIL"
	if a.Sent > 0 && a.Completed > 0 && float64(a.Errors)/float64(a.Sent) <= scenario.Assertions.Requests.ErrorRate && float64(a.Dropped)/float64(a.Planned) <= dropBound {
		a.ClientResult = "PASS"
	}
}

// RenderModelBreakdown renders the Markdown table for local/remote report.md.
// Only error categories with at least one failure anywhere are rendered.
func RenderModelBreakdown(b *ModelBreakdown) string {
	if b == nil {
		return ""
	}
	var out strings.Builder
	out.WriteString("\n## Model summary\n\n")
	header := []string{"Metric"}
	rows := append(append([]ModelSummary(nil), b.Models...), b.Total)
	for _, r := range rows {
		header = append(header, r.Model)
	}
	writeReportRow(&out, header...)
	separator := []string{"---"}
	for range rows {
		separator = append(separator, "---:")
	}
	writeReportRow(&out, separator...)
	row := func(label string, value func(ModelSummary) string) {
		cells := []string{label}
		for _, r := range rows {
			cells = append(cells, value(r))
		}
		writeReportRow(&out, cells...)
	}
	number := func(label string, value func(ModelSummary) int) {
		row(label, func(r ModelSummary) string { return strconv.Itoa(value(r)) })
	}
	section := func(label string) { row("**"+label+"**", func(ModelSummary) string { return "" }) }
	row("Client result", func(r ModelSummary) string { return r.ClientResult })
	section("Requests")
	number("Planned", func(r ModelSummary) int { return r.Planned })
	number("Sent", func(r ModelSummary) int { return r.Sent })
	number("Dropped before sending", func(r ModelSummary) int { return r.Dropped })
	if b.Total.ConcurrencyDrops > 0 {
		number("↳ Concurrency limit", func(r ModelSummary) int { return r.ConcurrencyDrops })
	}
	if b.Total.BehindDrops > 0 {
		number("↳ Generator behind", func(r ModelSummary) int { return r.BehindDrops })
	}
	number("Successful", func(r ModelSummary) int { return r.Completed })
	row("Success rate of sent requests", func(r ModelSummary) string {
		if r.Sent == 0 {
			return reportMissing
		}
		return fmt.Sprintf("%.2f%%", 100*r.SuccessRate)
	})
	row("Successful share of planned requests", func(r ModelSummary) string {
		if r.Planned == 0 {
			return reportMissing
		}
		return fmt.Sprintf("%.2f%%", 100*r.CompletionRate)
	})
	number("**Errors total**", func(r ModelSummary) int { return r.Errors })
	for _, category := range nonzeroModelErrorCategories(b) {
		number("↳ "+errorCategoryLabel(category), func(r ModelSummary) int { return r.ErrorCategories[category] })
	}
	section("Successful request latency")
	number("Samples", func(r ModelSummary) int { return r.LatencySamples })
	for _, metric := range []struct {
		label string
		value func(ModelSummary) time.Duration
	}{
		{"P50", func(r ModelSummary) time.Duration { return r.P50 }},
		{"P95", func(r ModelSummary) time.Duration { return r.P95 }},
		{"P99", func(r ModelSummary) time.Duration { return r.P99 }},
		{"Max", func(r ModelSummary) time.Duration { return r.Max }},
	} {
		row(metric.label, func(r ModelSummary) string {
			if r.LatencySamples == 0 {
				return reportMissing
			}
			return metric.value(r).Round(time.Microsecond).String()
		})
	}
	section("Throughput")
	row("Successful requests/s", func(r ModelSummary) string { return fmt.Sprintf("%.2f", r.SuccessfulRPS) })
	out.WriteString("\nSuccess rate uses sent requests; drops are counted separately. Latency includes the full successful response body. Total percentiles use pooled samples, not averages of model percentiles. Throughput uses the configured workload duration. Client results use the scenario's error and drop thresholds; server state checks are separate. P99 is descriptive and unstable with small sample counts.\n")
	for _, r := range b.Models {
		if r.Model == "Unattributed" {
			out.WriteString("\nUnattributed contains legacy request records without a model ID.\n")
			break
		}
	}
	return out.String()
}

func nonzeroModelErrorCategories(b *ModelBreakdown) []string {
	categories := []string{}
	for category, count := range b.Total.ErrorCategories {
		if count > 0 {
			categories = append(categories, category)
		}
	}
	sort.Slice(categories, func(i, j int) bool {
		left, right := strings.HasPrefix(categories[i], "http_"), strings.HasPrefix(categories[j], "http_")
		if left != right {
			return left
		}
		return categories[i] < categories[j]
	})
	return categories
}

func errorCategoryLabel(category string) string {
	if strings.HasPrefix(category, "http_") {
		return "HTTP " + strings.TrimPrefix(category, "http_")
	}
	switch category {
	case "timeout":
		return "Timeout"
	case "cancelled":
		return "Cancelled"
	case "network":
		return "Network error"
	case "invalid_response":
		return "Invalid JSON response"
	case "stream_empty_stream":
		return "Empty SSE stream"
	case "stream_partial":
		return "Incomplete or malformed SSE stream"
	case "stream_error_envelope":
		return "SSE error envelope"
	case "request_error":
		return "Request preparation error"
	default:
		return category
	}
}
