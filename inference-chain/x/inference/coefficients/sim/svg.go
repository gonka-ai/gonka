package main

import (
	"fmt"
	"html"
	"os"
	"sort"
	"strings"
)

type band struct {
	low, high float64
	color     string
}

type guide struct {
	value float64
	label string
}

type endpointLabel struct {
	name  string
	value float64
	y     float64
	color string
}

func writeSVG(path string, cfg config, modelIDs []string, epochs []epoch) error {
	const (
		width  = 1600
		top    = 95
		rowH   = 245
		gap    = 70
		leftX  = 55
		leftW  = 455
		rightX = 585
		rightW = 955
	)
	nonBase := make([]string, 0, len(modelIDs)-1)
	for _, modelID := range modelIDs {
		if modelID != cfg.BaseModel {
			nonBase = append(nonBase, modelID)
		}
	}
	rows := maxInt(4, 2+len(nonBase))
	height := top + rows*(rowH+gap) + 30
	colors := seriesColors(append(modelIDs, sortedKeys(cfg.GPUCounts)...))

	var svg strings.Builder
	fmt.Fprintf(&svg, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d">`, width, height)
	svg.WriteString(`<rect width="100%" height="100%" fill="white"/>`)
	svg.WriteString(`<text x="55" y="42" font-family="sans-serif" font-size="25" font-weight="bold">Dynamic coefficients - production Go algorithm</text>`)
	fmt.Fprintf(&svg, `<text x="55" y="68" font-family="sans-serif" font-size="13">%s  hosts=%d nodes=%d seed=%d epochs=%d</text>`,
		html.EscapeString(cfg.Name), cfg.Hosts, totalNodes(cfg.GPUCounts), cfg.Seed, cfg.Epochs)

	drawTextPanel(&svg, leftX, top, leftW, rowH, "Simulation parameters", simulationLines(cfg, epochs))
	drawBars(&svg, leftX, top+rowH+gap, leftW, rowH, "Network compute by GPU type", hardwarePowerShares(cfg), colors, true)
	drawBars(&svg, leftX, top+2*(rowH+gap), leftW, rowH, "Cumulative GPU reward vs 8xH100", cumulativeGPURewards(cfg, epochs), colors, false)
	drawTextPanel(&svg, leftX, top+3*(rowH+gap), leftW, rowH, "GPU preferences", preferenceLines(cfg, modelIDs, epochs))

	shareSeries := make(map[string][]float64)
	var targetBands []band
	for _, modelID := range modelIDs {
		for _, item := range epochs {
			shareSeries[modelID] = append(shareSeries[modelID], item.Shares[modelID])
		}
		model := configModel(cfg, modelID)
		target := float64(model.TargetShareBPS) / 10000
		zone := float64(cfg.Controller.TargetZoneBPS) / 10000
		targetBands = append(targetBands, band{
			low:   maxFloat(0, target-zone),
			high:  minFloat(1, target+zone),
			color: colors[modelID],
		})
	}
	drawLines(&svg, rightX, top, rightW, rowH, "Model compute shares - target zones shaded",
		shareSeries, colors, 0, 1, targetBands, nil)

	for row, modelID := range nonBase {
		var baseRatios, effectiveRatios []float64
		for _, item := range epochs {
			baseRatios = append(baseRatios, item.Base[modelID]/item.Base[cfg.BaseModel])
			effectiveRatios = append(effectiveRatios, item.Effective[modelID]/item.Effective[cfg.BaseModel])
		}
		series := map[string][]float64{
			"base coefficient ratio":      baseRatios,
			"effective coefficient ratio": effectiveRatios,
		}
		ratioColors := map[string]string{
			"base coefficient ratio":      "#64748b",
			"effective coefficient ratio": colors[modelID],
		}
		var guides []guide
		for _, gpu := range sortedKeys(cfg.GPUCounts) {
			guides = append(guides, guide{
				value: float64(cfg.Throughput[cfg.BaseModel][gpu]) / float64(cfg.Throughput[modelID][gpu]),
				label: gpu,
			})
		}
		low, high := rangeWithGuides(series, guides)
		drawLines(&svg, rightX, top+(row+1)*(rowH+gap), rightW, rowH,
			modelID+" switching thresholds", series, ratioColors, low, high, nil, guides)
	}

	gpuSeries := make(map[string][]float64)
	for _, gpu := range sortedKeys(cfg.GPUCounts) {
		for _, item := range epochs {
			gpuSeries[gpu] = append(gpuSeries[gpu], item.GPUReward[gpu])
		}
	}
	drawLines(&svg, rightX, top+(len(nonBase)+1)*(rowH+gap), rightW, rowH,
		"GPU reward per epoch relative to 8xH100", gpuSeries, colors,
		minimum(gpuSeries), maximum(gpuSeries), nil, nil)
	svg.WriteString(`</svg>`)
	return os.WriteFile(path, []byte(svg.String()), 0o644)
}

func drawLines(
	svg *strings.Builder,
	x, y, width, height int,
	title string,
	series map[string][]float64,
	colors map[string]string,
	low, high float64,
	bands []band,
	guides []guide,
) {
	if high <= low {
		high = low + 1
	}
	fmt.Fprintf(svg, `<text x="%d" y="%d" font-family="sans-serif" font-size="17" font-weight="bold">%s</text>`,
		x, y-15, html.EscapeString(title))
	fmt.Fprintf(svg, `<rect x="%d" y="%d" width="%d" height="%d" fill="#f8fafc" stroke="#cbd5e1"/>`,
		x, y, width, height)
	toY := func(value float64) float64 {
		return float64(y+height) - (value-low)*float64(height)/(high-low)
	}
	for _, item := range bands {
		topY, bottomY := toY(item.high), toY(item.low)
		fmt.Fprintf(svg, `<rect x="%d" y="%.1f" width="%d" height="%.1f" fill="%s" opacity="0.08"/>`,
			x, topY, width, bottomY-topY, item.color)
	}
	for _, item := range guides {
		py := toY(item.value)
		fmt.Fprintf(svg, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="#94a3b8" stroke-dasharray="5,4"/>`,
			x, py, x+width, py)
		fmt.Fprintf(svg, `<text x="%d" y="%.1f" font-family="sans-serif" font-size="10" fill="#64748b">%s %.3f</text>`,
			x+width-95, py-3, html.EscapeString(item.label), item.value)
	}
	drawEpochTicks(svg, x, y, width, height, len(firstSeries(series)))

	endpoints := make([]endpointLabel, 0, len(series))
	for index, name := range sortedKeys(series) {
		values := series[name]
		var points []string
		for i, number := range values {
			px := float64(x)
			if len(values) > 1 {
				px += float64(i*width) / float64(len(values)-1)
			}
			points = append(points, fmt.Sprintf("%.1f,%.1f", px, toY(number)))
		}
		color := colors[name]
		if color == "" {
			color = "#2563eb"
		}
		fmt.Fprintf(svg, `<polyline fill="none" stroke="%s" stroke-width="2" points="%s"/>`,
			color, strings.Join(points, " "))
		final := float64(0)
		if len(values) > 0 {
			final = values[len(values)-1]
			fmt.Fprintf(svg, `<circle cx="%d" cy="%.1f" r="3" fill="%s"/>`, x+width, toY(final), color)
			fmt.Fprintf(svg, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="%s" stroke-width="1" stroke-dasharray="4,3" opacity="0.55"/>`,
				x+width*3/4, toY(final), x+width, toY(final), color)
			endpoints = append(endpoints, endpointLabel{name: name, value: final, y: toY(final), color: color})
		}
		slot := width / maxInt(1, len(series))
		fmt.Fprintf(svg, `<text x="%d" y="%d" font-family="sans-serif" font-size="10" fill="%s">%s = %.6g</text>`,
			x+index*slot, y+height+34, color, html.EscapeString(name), final)
	}
	drawEndpointLabels(svg, x, y, width, height, endpoints)
	fmt.Fprintf(svg, `<text x="%d" y="%d" font-family="monospace" font-size="10">%.4g</text>`, x-48, y+9, high)
	fmt.Fprintf(svg, `<text x="%d" y="%d" font-family="monospace" font-size="10">%.4g</text>`, x-48, y+height, low)
}

func drawEpochTicks(svg *strings.Builder, x, y, width, height, count int) {
	if count == 0 {
		return
	}
	for tick := 0; tick <= 5; tick++ {
		index := tick * (count - 1) / 5
		px := float64(x)
		if count > 1 {
			px += float64(index*width) / float64(count-1)
		}
		fmt.Fprintf(svg, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="#e2e8f0"/>`,
			px, y, px, y+height)
		fmt.Fprintf(svg, `<text x="%.1f" y="%d" text-anchor="middle" font-family="monospace" font-size="10">%d</text>`,
			px, y+height+14, index)
	}
}

func drawEndpointLabels(svg *strings.Builder, x, y, width, height int, labels []endpointLabel) {
	sort.Slice(labels, func(i, j int) bool { return labels[i].y < labels[j].y })
	const spacing = 16.0
	for index := range labels {
		labels[index].y = maxFloat(labels[index].y, float64(y+12))
		if index > 0 {
			labels[index].y = maxFloat(labels[index].y, labels[index-1].y+spacing)
		}
	}
	if len(labels) > 0 && labels[len(labels)-1].y > float64(y+height-5) {
		shift := labels[len(labels)-1].y - float64(y+height-5)
		for index := range labels {
			labels[index].y -= shift
		}
	}
	labelX := x + width - 205
	for _, label := range labels {
		fmt.Fprintf(svg, `<rect x="%d" y="%.1f" width="198" height="15" rx="2" fill="white" opacity="0.88"/>`,
			labelX, label.y-12)
		fmt.Fprintf(svg, `<text x="%d" y="%.1f" font-family="monospace" font-size="11" fill="%s">%s = %.6g</text>`,
			labelX+4, label.y, label.color, html.EscapeString(label.name), label.value)
	}
}

func drawTextPanel(svg *strings.Builder, x, y, width, height int, title string, lines []string) {
	fmt.Fprintf(svg, `<text x="%d" y="%d" font-family="sans-serif" font-size="17" font-weight="bold">%s</text>`,
		x, y-15, html.EscapeString(title))
	fmt.Fprintf(svg, `<rect x="%d" y="%d" width="%d" height="%d" fill="#f8fafc" stroke="#cbd5e1"/>`,
		x, y, width, height)
	for index, line := range lines {
		fmt.Fprintf(svg, `<text x="%d" y="%d" font-family="monospace" font-size="12">%s</text>`,
			x+12, y+22+index*17, html.EscapeString(line))
	}
}

func drawBars(svg *strings.Builder, x, y, width, height int, title string, values map[string]float64, colors map[string]string, percent bool) {
	fmt.Fprintf(svg, `<text x="%d" y="%d" font-family="sans-serif" font-size="17" font-weight="bold">%s</text>`,
		x, y-15, html.EscapeString(title))
	fmt.Fprintf(svg, `<rect x="%d" y="%d" width="%d" height="%d" fill="#f8fafc" stroke="#cbd5e1"/>`,
		x, y, width, height)
	maxValue := maximum(map[string][]float64{"values": mapValues(values)})
	for index, name := range sortedKeys(values) {
		barY := y + 18 + index*48
		barWidth := 0
		if maxValue > 0 {
			barWidth = int(values[name] / maxValue * float64(width-145))
		}
		fmt.Fprintf(svg, `<text x="%d" y="%d" font-family="sans-serif" font-size="11">%s</text>`,
			x+8, barY+15, html.EscapeString(name))
		fmt.Fprintf(svg, `<rect x="%d" y="%d" width="%d" height="20" fill="%s"/>`,
			x+95, barY, barWidth, colors[name])
		label := fmt.Sprintf("%.2f", values[name])
		if percent {
			label = fmt.Sprintf("%.1f%%", values[name]*100)
		}
		fmt.Fprintf(svg, `<text x="%d" y="%d" font-family="monospace" font-size="11">%s</text>`,
			x+105+barWidth, barY+15, label)
	}
}

func simulationLines(cfg config, epochs []epoch) []string {
	lines := []string{
		"dataset=" + cfg.Name,
		fmt.Sprintf("hosts=%d nodes=%d seed=%d epochs=%d", cfg.Hosts, totalNodes(cfg.GPUCounts), cfg.Seed, cfg.Epochs),
		fmt.Sprintf("Z=%g  s_min=%s  s_max=%s", float64(cfg.Controller.TargetZoneBPS)/10000, cfg.Controller.StepMin, cfg.Controller.StepMax),
		fmt.Sprintf("bootstrap_s_max=%s  bootstrap_share=%g", cfg.Controller.BootstrapStepMax, float64(cfg.Controller.BootstrapShareBPS)/10000),
		fmt.Sprintf("epsilon=%s  max_passes=%d", cfg.Epsilon, cfg.MaxPasses),
		fmt.Sprintf("all shares stay in target zones from epoch %d", convergenceEpoch(cfg, epochs)),
		"Targets:",
	}
	for _, model := range cfg.Models {
		lines = append(lines, fmt.Sprintf("  %s: %.2f", model.ID, float64(model.TargetShareBPS)/10000))
	}
	lines = append(lines, "Fixed coefficients:")
	for _, model := range cfg.Models {
		if model.Min == model.Max {
			lines = append(lines, fmt.Sprintf("  %s: %s", model.ID, model.Min))
		}
	}
	return lines
}

func convergenceEpoch(cfg config, epochs []epoch) int {
	zone := float64(cfg.Controller.TargetZoneBPS) / 10000
	for start := range epochs {
		stable := true
		for _, item := range epochs[start:] {
			for _, model := range cfg.Models {
				target := float64(model.TargetShareBPS) / 10000
				if item.Shares[model.ID] < target-zone || item.Shares[model.ID] > target+zone {
					stable = false
					break
				}
			}
			if !stable {
				break
			}
		}
		if stable {
			return start
		}
	}
	return -1
}

func hardwarePowerShares(cfg config) map[string]float64 {
	result := make(map[string]float64)
	total := float64(0)
	for gpu, count := range cfg.GPUCounts {
		result[gpu] = float64(count) * float64(cfg.Throughput[cfg.BaseModel][gpu])
		total += result[gpu]
	}
	for gpu := range result {
		result[gpu] /= total
	}
	return result
}

func cumulativeGPURewards(cfg config, epochs []epoch) map[string]float64 {
	result := make(map[string]float64, len(cfg.GPUCounts))
	for _, item := range epochs {
		for gpu, reward := range item.GPUReward {
			result[gpu] += reward
		}
	}
	return result
}

func preferenceLines(cfg config, modelIDs []string, epochs []epoch) []string {
	var lines []string
	for _, gpu := range sortedKeys(cfg.GPUCounts) {
		seen := make(map[string]bool)
		for _, item := range epochs {
			seen[bestModel(cfg, modelIDs, gpu, item.Effective)] = true
		}
		if len(seen) > 1 {
			lines = append(lines, fmt.Sprintf("%s: switches %s", gpu, strings.Join(sortedKeys(seen), " <-> ")))
			continue
		}
		final := epochs[len(epochs)-1]
		best := bestModel(cfg, modelIDs, gpu, final.Effective)
		bestValue := float64(cfg.Throughput[best][gpu]) * final.Effective[best]
		runner := ""
		runnerValue := float64(0)
		for _, modelID := range modelIDs {
			if modelID == best {
				continue
			}
			value := float64(cfg.Throughput[modelID][gpu]) * final.Effective[modelID]
			if value > runnerValue {
				runner, runnerValue = modelID, value
			}
		}
		lines = append(lines, fmt.Sprintf("%s: %s", gpu, best))
		if bestValue > 0 {
			lines = append(lines, fmt.Sprintf("  runner %s at %.1f%%", runner, runnerValue/bestValue*100))
		}
	}
	return lines
}

func bestModel(cfg config, modelIDs []string, gpu string, effective map[string]float64) string {
	best := modelIDs[0]
	bestValue := float64(cfg.Throughput[best][gpu]) * effective[best]
	for _, modelID := range modelIDs[1:] {
		value := float64(cfg.Throughput[modelID][gpu]) * effective[modelID]
		if value > bestValue {
			best, bestValue = modelID, value
		}
	}
	return best
}

func rangeWithGuides(series map[string][]float64, guides []guide) (float64, float64) {
	low, high := minimum(series), maximum(series)
	for _, item := range guides {
		low = minFloat(low, item.value)
		high = maxFloat(high, item.value)
	}
	margin := (high - low) * 0.08
	if margin == 0 {
		margin = 0.1
	}
	return low - margin, high + margin
}

func seriesColors(names []string) map[string]string {
	palette := []string{"#2563eb", "#dc2626", "#16a34a", "#9333ea", "#ea580c", "#0891b2", "#64748b"}
	result := make(map[string]string, len(names))
	for index, name := range names {
		if _, exists := result[name]; !exists {
			result[name] = palette[index%len(palette)]
		}
	}
	return result
}

func configModel(cfg config, modelID string) model {
	for _, model := range cfg.Models {
		if model.ID == modelID {
			return model
		}
	}
	panic("model config not found: " + modelID)
}

func totalNodes(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func mapValues(values map[string]float64) []float64 {
	result := make([]float64, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func firstSeries(series map[string][]float64) []float64 {
	for _, values := range series {
		return values
	}
	return nil
}

func minimum(series map[string][]float64) float64 {
	first := true
	var result float64
	for _, values := range series {
		for _, value := range values {
			if first || value < result {
				result, first = value, false
			}
		}
	}
	return result
}

func maximum(series map[string][]float64) float64 {
	first := true
	var result float64
	for _, values := range series {
		for _, value := range values {
			if first || value > result {
				result, first = value, false
			}
		}
	}
	return result
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
