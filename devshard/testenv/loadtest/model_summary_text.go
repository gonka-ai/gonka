package loadtest

import (
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"
)

// RenderModelBreakdownText prints the same model statistics as report.md using
// aligned plain-text columns. Full IDs are mapped to unique compact headings.
func RenderModelBreakdownText(b *ModelBreakdown) string {
	if b == nil {
		return ""
	}
	var out, table strings.Builder
	out.WriteString("\nMODEL SUMMARY\n\n")
	labels := ModelColumnLabels(b.Models)
	mapping := false
	for i, model := range b.Models {
		if labels[i] != model.Model {
			mapping = true
			fmt.Fprintf(&out, "%s = %s\n", labels[i], consoleCell(model.Model))
		}
	}
	if mapping {
		out.WriteByte('\n')
	}
	writer := tabwriter.NewWriter(&table, 0, 4, 3, ' ', 0)
	write := func(cells []string) {
		for i := range cells {
			cells[i] = consoleCell(cells[i])
		}
		fmt.Fprintln(writer, strings.Join(cells, "\t"))
	}
	write(append(append([]string{"Metric"}, labels...), "Total"))
	rows := append(append([]ModelSummary(nil), b.Models...), b.Total)
	row := func(label string, value func(ModelSummary) string) {
		cells := []string{label}
		for _, model := range rows {
			cells = append(cells, value(model))
		}
		write(cells)
	}
	number := func(label string, value func(ModelSummary) int) {
		row(label, func(r ModelSummary) string { return strconv.Itoa(value(r)) })
	}
	section := func(label string) {
		// Keep hard tab stops across visually blank separator rows.
		write(make([]string, len(rows)+1))
		write(append([]string{label}, make([]string, len(rows))...))
	}
	row("Client result", func(r ModelSummary) string { return r.ClientResult })
	number("Planned", func(r ModelSummary) int { return r.Planned })
	number("Sent", func(r ModelSummary) int { return r.Sent })
	number("Dropped", func(r ModelSummary) int { return r.Dropped })
	if b.Total.ConcurrencyDrops > 0 {
		number("  Concurrency limit", func(r ModelSummary) int { return r.ConcurrencyDrops })
	}
	if b.Total.BehindDrops > 0 {
		number("  Generator behind", func(r ModelSummary) int { return r.BehindDrops })
	}
	number("Successful", func(r ModelSummary) int { return r.Completed })
	row("Success rate (sent)", func(r ModelSummary) string {
		if r.Sent == 0 {
			return "-"
		}
		return fmt.Sprintf("%.2f%%", 100*r.SuccessRate)
	})
	row("Completion rate (planned)", func(r ModelSummary) string {
		if r.Planned == 0 {
			return "-"
		}
		return fmt.Sprintf("%.2f%%", 100*r.CompletionRate)
	})
	number("Errors total", func(r ModelSummary) int { return r.Errors })
	for _, category := range nonzeroModelErrorCategories(b) {
		number("  "+errorCategoryLabel(category), func(r ModelSummary) int { return r.ErrorCategories[category] })
	}
	section("Successful latency")
	number("  Samples", func(r ModelSummary) int { return r.LatencySamples })
	for _, metric := range []struct {
		label string
		value func(ModelSummary) time.Duration
	}{
		{"  P50", func(r ModelSummary) time.Duration { return r.P50 }},
		{"  P95", func(r ModelSummary) time.Duration { return r.P95 }},
		{"  P99", func(r ModelSummary) time.Duration { return r.P99 }},
		{"  Max", func(r ModelSummary) time.Duration { return r.Max }},
	} {
		row(metric.label, func(r ModelSummary) string {
			if r.LatencySamples == 0 {
				return "-"
			}
			return compactLatency(metric.value(r))
		})
	}
	section("Throughput")
	row("Successful requests/s", func(r ModelSummary) string { return fmt.Sprintf("%.2f", r.SuccessfulRPS) })
	_ = writer.Flush() // strings.Builder writes cannot fail.
	lines := strings.Split(strings.TrimRight(table.String(), "\n"), "\n")
	width := 0
	for _, line := range lines {
		if len(strings.TrimRight(line, " ")) > width {
			width = len(strings.TrimRight(line, " "))
		}
	}
	for i, line := range lines {
		out.WriteString(strings.TrimRight(line, " "))
		out.WriteByte('\n')
		if i == 0 {
			out.WriteString(strings.Repeat("-", width))
			out.WriteByte('\n')
		}
	}
	out.WriteString("\nLatency: full successful responses; Total percentiles: pooled samples.\nRates use sent or planned requests as labeled; throughput uses workload duration.\nClient results use scenario error/drop thresholds; server checks are separate.\nP99 is unstable with small samples.\n")
	for _, model := range b.Models {
		if model.Model == "Unattributed" {
			out.WriteString("Unattributed: legacy records without a model ID.\n")
			break
		}
	}
	return out.String()
}

func consoleCell(text string) string {
	return strings.NewReplacer("\t", " ", "\r", " ", "\n", " ", "\x1b", "?").Replace(text)
}

// ModelColumnLabels supplies unique compact terminal headings and preserves model order.
func ModelColumnLabels(models []ModelSummary) []string {
	labels := make([]string, len(models))
	counts := map[string]int{"Total": 1}
	for i, model := range models {
		label := model.Model
		if slash := strings.LastIndex(label, "/"); slash >= 0 {
			label = label[slash+1:]
		}
		if suffix := strings.Index(label, "-Instruct"); suffix >= 0 {
			label = strings.ReplaceAll(label[:suffix], "-", " ")
		}
		label = consoleCell(label)
		labels[i] = label
		counts[label]++
	}
	used := map[string]bool{"Total": true}
	for i, label := range labels {
		if label == "" || utf8.RuneCountInString(label) > 18 || counts[label] > 1 || used[label] {
			label = fmt.Sprintf("Model %d", i+1)
			for used[label] || counts[label] > 0 {
				label += "*"
			}
		}
		labels[i] = label
		used[label] = true
	}
	return labels
}

func compactLatency(d time.Duration) string {
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	case d >= time.Microsecond:
		return fmt.Sprintf("%.2fus", float64(d)/float64(time.Microsecond))
	default:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	}
}
