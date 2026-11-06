package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"devshard/testenv/loadtest"
	"devshard/testenv/replay"
)

// A preflight is a read-only point-in-time snapshot, not a reservation of capacity.
type environmentSnapshot struct {
	CapturedAt       time.Time            `json:"captured_at"`
	Gateway          string               `json:"gateway"`
	Ready            bool                 `json:"ready"`
	Problems         []string             `json:"problems,omitempty"`
	Warnings         []string             `json:"warnings,omitempty"`
	Workload         environmentWorkload  `json:"workload"`
	AdvertisedModels []string             `json:"advertised_models"`
	Models           []environmentModel   `json:"models"`
	Network          environmentNetwork   `json:"network"`
	Capacity         *environmentCapacity `json:"capacity,omitempty"`
	Escrows          []environmentEscrow  `json:"escrows,omitempty"`
	Probes           []environmentProbe   `json:"probes"`
}

type environmentProbe struct {
	Name       string          `json:"name"`
	URL        string          `json:"url"`
	CapturedAt time.Time       `json:"captured_at"`
	HTTPStatus int             `json:"http_status"`
	Duration   time.Duration   `json:"duration"`
	Error      string          `json:"error,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
	Preview    string          `json:"preview,omitempty"`
}

type environmentWorkload struct {
	Duration    string `json:"duration"`
	Traffic     string `json:"traffic"`
	MaxInFlight int    `json:"max_in_flight"`
}

type environmentModel struct {
	ID                  string                    `json:"id"`
	Listed              bool                      `json:"listed"`
	Ready               bool                      `json:"ready"`
	Weight              float64                   `json:"weight"`
	DatasetRows         *int                      `json:"dataset_rows,omitempty"`
	MaxRequestedTokens  int64                     `json:"max_requested_tokens"`
	ContextLength       *int64                    `json:"context_length,omitempty"`
	MaxCompletionTokens *int64                    `json:"max_completion_tokens,omitempty"`
	SupportedParameters []string                  `json:"supported_parameters,omitempty"`
	Capacity            *environmentModelCapacity `json:"capacity,omitempty"`
	Limiter             *environmentLimiter       `json:"limiter,omitempty"`
	LimiterScope        string                    `json:"limiter_scope,omitempty"`
	FreeConcurrency     *int64                    `json:"free_concurrency_slots,omitempty"`
	EscrowIDs           []string                  `json:"escrow_ids,omitempty"`
	Problems            []string                  `json:"problems,omitempty"`
}

type environmentNetwork struct {
	BlockHeight           *json.Number `json:"block_height,omitempty"`
	EpochIndex            *json.Number `json:"epoch_index,omitempty"`
	Phase                 string       `json:"phase,omitempty"`
	ConfirmationPoCActive *bool        `json:"is_confirmation_poc_active,omitempty"`
	NextPoCStart          *json.Number `json:"next_poc_start,omitempty"`
}

type environmentCapacity struct {
	HostCount          *int                                `json:"host_count,omitempty"`
	AvailableHostCount *int                                `json:"available_host_count,omitempty"`
	AvailablePercent   *float64                            `json:"available_percent,omitempty"`
	Models             map[string]environmentModelCapacity `json:"models,omitempty"`
	EscrowWeights      map[string]float64                  `json:"escrow_weights,omitempty"`
}

type environmentModelCapacity struct {
	CurrentWeight    *float64 `json:"current_weight,omitempty"`
	TotalWeight      *float64 `json:"total_weight,omitempty"`
	AvailablePercent *float64 `json:"available_percent,omitempty"`
	Routable         *bool    `json:"routable,omitempty"`
	AccessEnabled    *bool    `json:"access_enabled,omitempty"`
}

type environmentLimiter struct {
	MaxConcurrent        *int64                        `json:"max_concurrent_requests,omitempty"`
	EffectiveConcurrent  *int64                        `json:"effective_max_concurrent_requests,omitempty"`
	InFlight             *int64                        `json:"in_flight_requests,omitempty"`
	MaxInputTokens       *int64                        `json:"max_input_tokens_in_flight,omitempty"`
	InFlightInputTokens  *int64                        `json:"in_flight_input_tokens,omitempty"`
	EffectiveInputTokens *int64                        `json:"effective_max_input_tokens_in_flight,omitempty"`
	Models               map[string]environmentLimiter `json:"models,omitempty"`
}

type environmentEscrow struct {
	ID                   string       `json:"id"`
	Model                string       `json:"model"`
	Active               *bool        `json:"active,omitempty"`
	OnHold               bool         `json:"on_hold"`
	Phase                string       `json:"phase,omitempty"`
	Balance              *json.Number `json:"balance,omitempty"`
	Nonce                *json.Number `json:"nonce,omitempty"`
	ActiveRequests       *int64       `json:"active_requests,omitempty"`
	ReservedTokens       *int64       `json:"reserved_tokens,omitempty"`
	ChainPhase           string       `json:"chain_phase,omitempty"`
	ConfirmationPoCPhase string       `json:"confirmation_poc_phase,omitempty"`
	RequestsBlocked      *bool        `json:"requests_blocked,omitempty"`
	BlockReason          string       `json:"block_reason,omitempty"`
}

type environmentStatus struct {
	environmentEscrow
	EscrowID  string               `json:"escrow_id"`
	Capacity  *environmentCapacity `json:"capacity"`
	Limiter   *environmentLimiter  `json:"limiter"`
	Devshards []environmentEscrow  `json:"devshards"`
}

type advertisedModel struct {
	ID                  string   `json:"id"`
	ContextLength       *int64   `json:"context_length"`
	MaxModelLen         *int64   `json:"max_model_len"`
	MaxCompletionTokens *int64   `json:"max_completion_tokens"`
	SupportedParameters []string `json:"supported_parameters"`
}

func collectEnvironment(ctx context.Context, client *http.Client, base, key string, scenario loadtest.Scenario, dataset *replay.Dataset, timeout time.Duration) *environmentSnapshot {
	origin, _ := url.Parse(base)
	epochURL := (&url.URL{Scheme: origin.Scheme, Host: origin.Host, Path: "/v1/epochs/latest"}).String()
	probes := []environmentProbe{{Name: "models", URL: base + "/v1/models"}, {Name: "status", URL: base + "/v1/status"}, {Name: "epoch", URL: epochURL}}
	var wg sync.WaitGroup
	for i := range probes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			auth := ""
			if probes[i].Name != "epoch" {
				auth = key
			}
			probes[i] = probeEnvironment(ctx, client, probes[i], auth, key, timeout)
		}(i)
	}
	wg.Wait()
	e := &environmentSnapshot{CapturedAt: time.Now().UTC(), Gateway: base, Probes: probes, Workload: environmentWorkload{Duration: scenario.Workload.Duration, Traffic: scenario.Workload.Traffic.Type, MaxInFlight: scenario.Workload.MaxInFlight}}
	if e.Workload.Traffic == "" {
		e.Workload.Traffic = "closed_loop"
	}
	if e.Workload.Traffic == "constant" {
		e.Workload.Traffic = fmt.Sprintf("constant %.2f requests/s", scenario.Workload.Traffic.RPS)
	}
	var listing struct {
		Data *[]advertisedModel `json:"data"`
	}
	advertised := map[string]advertisedModel{}
	if probes[0].Error != "" {
		e.Problems = append(e.Problems, "model discovery failed: "+probes[0].Error)
	} else if err := json.Unmarshal(probes[0].Body, &listing); err != nil || listing.Data == nil {
		e.Problems = append(e.Problems, "model discovery returned no valid data array")
	} else {
		for _, m := range *listing.Data {
			advertised[m.ID] = m
		}
	}
	for id := range advertised {
		e.AdvertisedModels = append(e.AdvertisedModels, id)
	}
	sort.Strings(e.AdvertisedModels)
	var status environmentStatus
	statusKnown := false
	if probes[1].Error != "" {
		e.Warnings = append(e.Warnings, "gateway status unavailable: "+probes[1].Error)
	} else if err := json.Unmarshal(probes[1].Body, &status); err != nil {
		e.Warnings = append(e.Warnings, "gateway status could not be decoded")
	} else {
		statusKnown = true
		e.Capacity = status.Capacity
		e.Escrows = status.Devshards
		if status.EscrowID != "" {
			r := status.environmentEscrow
			r.ID = status.EscrowID
			if r.Model == "" && len(scenario.RequestModels()) == 1 {
				r.Model = scenario.RequestModels()[0]
			}
			e.Escrows = append(e.Escrows, r)
		}
	}
	if probes[2].Error != "" {
		e.Warnings = append(e.Warnings, "epoch status unavailable: "+probes[2].Error)
	} else {
		var epoch struct {
			BlockHeight  *json.Number `json:"block_height"`
			Phase        string       `json:"phase"`
			Confirmation *bool        `json:"is_confirmation_poc_active"`
			Stages       struct {
				Index *json.Number `json:"epoch_index"`
				Next  *json.Number `json:"next_poc_start"`
			} `json:"epoch_stages"`
		}
		if err := json.Unmarshal(probes[2].Body, &epoch); err != nil {
			e.Warnings = append(e.Warnings, "epoch status could not be decoded")
		} else {
			e.Network = environmentNetwork{BlockHeight: epoch.BlockHeight, EpochIndex: epoch.Stages.Index, Phase: epoch.Phase, ConfirmationPoCActive: epoch.Confirmation, NextPoCStart: epoch.Stages.Next}
		}
	}
	for _, id := range scenario.RequestModels() {
		meta, listed := advertised[id]
		m := environmentModel{ID: id, Listed: listed, Weight: 1, ContextLength: meta.ContextLength, MaxCompletionTokens: meta.MaxCompletionTokens, SupportedParameters: meta.SupportedParameters}
		if m.ContextLength == nil {
			m.ContextLength = meta.MaxModelLen
		}
		if mix := scenario.Workload.Request.Mix; mix != nil {
			for _, choice := range mix.Models {
				if choice.Value == id {
					m.Weight = choice.Weight
				}
			}
		}
		if !listed {
			m.Problems = append(m.Problems, "not advertised by /v1/models")
		}
		if dataset != nil {
			count := 0
			for i := 0; i < dataset.Len(); i++ {
				row, _ := dataset.Sample(i)
				if row.Model == id {
					count++
					if row.MaxTokens > m.MaxRequestedTokens {
						m.MaxRequestedTokens = row.MaxTokens
					}
					if row.Logprobs && meta.SupportedParameters != nil && !containsParameter(meta.SupportedParameters, "logprobs") {
						e.Warnings = appendUnique(e.Warnings, id+": logprobs requested but not advertised")
					}
				}
			}
			m.DatasetRows = &count
		} else {
			m.MaxRequestedTokens = configuredMaxTokens(scenario, id)
		}
		if m.MaxCompletionTokens != nil && m.MaxRequestedTokens > *m.MaxCompletionTokens {
			m.Problems = append(m.Problems, fmt.Sprintf("requested max_tokens %d exceeds advertised completion limit %d", m.MaxRequestedTokens, *m.MaxCompletionTokens))
		}
		if m.ContextLength != nil && m.MaxRequestedTokens >= *m.ContextLength {
			e.Warnings = appendUnique(e.Warnings, id+": requested output cap leaves no advertised context budget for input; exact token counts are unknown")
		}
		if status.Capacity != nil {
			if c, ok := status.Capacity.Models[id]; ok {
				m.Capacity = &c
				if c.CurrentWeight != nil && *c.CurrentWeight <= 0 || c.CurrentWeight == nil && c.TotalWeight != nil && *c.TotalWeight <= 0 || c.AvailablePercent != nil && *c.AvailablePercent <= 0 {
					m.Problems = append(m.Problems, "no live host capacity")
				}
				if c.Routable != nil && !*c.Routable {
					m.Problems = append(m.Problems, "model is not routable")
				}
				if c.AccessEnabled != nil && !*c.AccessEnabled {
					m.Problems = append(m.Problems, "model access is disabled")
				}
			} else if status.Capacity.AvailableHostCount != nil && *status.Capacity.AvailableHostCount == 0 {
				m.Problems = append(m.Problems, "no available gateway hosts")
			}
		}
		if status.Limiter != nil {
			if limit, ok := status.Limiter.Models[id]; ok {
				m.Limiter = &limit
				m.LimiterScope = "model"
			} else {
				m.Limiter = status.Limiter
				m.LimiterScope = "gateway"
			}
			if m.Limiter.MaxConcurrent != nil && *m.Limiter.MaxConcurrent > 0 && m.Limiter.EffectiveConcurrent != nil && *m.Limiter.EffectiveConcurrent == 0 {
				m.Problems = appendUnique(m.Problems, "effective concurrency limit is zero")
			}
			if m.Limiter.EffectiveConcurrent != nil && *m.Limiter.EffectiveConcurrent > 0 && m.Limiter.InFlight != nil {
				free := max(int64(0), *m.Limiter.EffectiveConcurrent-*m.Limiter.InFlight)
				m.FreeConcurrency = &free
			}
			if m.Limiter.EffectiveConcurrent != nil && *m.Limiter.EffectiveConcurrent > 0 && m.Limiter.InFlight != nil && *m.Limiter.InFlight >= *m.Limiter.EffectiveConcurrent {
				e.Warnings = append(e.Warnings, id+": concurrency slots are currently occupied")
			}
		}
		knownEscrows := 0
		usableEscrows := 0
		for _, escrow := range e.Escrows {
			if escrow.Model == id {
				m.EscrowIDs = append(m.EscrowIDs, escrow.ID)
				knownEscrows++
				blocked := escrow.OnHold || escrow.Active != nil && !*escrow.Active || escrow.RequestsBlocked != nil && *escrow.RequestsBlocked || escrow.Balance != nil && escrow.Balance.String() == "0" || escrow.Phase != "" && escrow.Phase != "active"
				if !blocked {
					usableEscrows++
				}
				if e.Network.Phase == "" {
					e.Network.Phase = escrow.ChainPhase
				}
			}
		}
		if knownEscrows > 0 && usableEscrows == 0 {
			m.Problems = append(m.Problems, "no usable escrow (inactive, held, blocked, finalizing, or zero balance)")
		}
		if statusKnown && status.Devshards != nil && knownEscrows == 0 {
			m.Problems = append(m.Problems, "no escrow for this model in gateway status")
		}
		if m.Capacity == nil && m.Limiter == nil {
			e.Warnings = append(e.Warnings, id+": live capacity is unknown; status does not expose it")
		}
		m.Ready = len(m.Problems) == 0
		for _, problem := range m.Problems {
			e.Problems = append(e.Problems, id+": "+problem)
		}
		e.Models = append(e.Models, m)
	}
	if e.Network.Phase == "" || e.Network.ConfirmationPoCActive == nil {
		e.Warnings = append(e.Warnings, "some PoC/cPOC fields are unknown")
	}
	if len(e.Escrows) == 0 {
		e.Warnings = append(e.Warnings, "escrow state is unavailable with this API access")
	}
	if strings.Contains(strings.ToLower(e.Network.Phase), "poc") || e.Network.ConfirmationPoCActive != nil && *e.Network.ConfirmationPoCActive {
		e.Warnings = append(e.Warnings, "PoC/cPOC is active; this does not block a model with usable capacity, but availability may change")
	}
	e.Warnings = append(e.Warnings, "Balances are raw API amounts; nonce headroom and affordable request count are unknown without nonce limits and pricing. Input token counts are not estimated.")
	e.Ready = len(e.Problems) == 0
	return e
}

func probeEnvironment(parent context.Context, client *http.Client, p environmentProbe, auth, secret string, timeout time.Duration) environmentProbe {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	p.CapturedAt = time.Now().UTC()
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err == nil && auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	if err == nil {
		var resp *http.Response
		resp, err = client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			p.HTTPStatus = resp.StatusCode
			var body []byte
			body, err = io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
			if err == nil && len(body) > 2<<20 {
				err = fmt.Errorf("response exceeds 2 MiB")
			}
			if secret != "" {
				body = []byte(strings.ReplaceAll(string(body), secret, "[REDACTED]"))
			}
			if json.Valid(body) {
				p.Body = body
			} else if len(body) > 0 {
				p.Preview = string(body[:min(len(body), 200)])
			}
			if err == nil && resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			if err == nil && !json.Valid(body) {
				err = fmt.Errorf("response is not valid JSON")
			}
		}
	}
	if err != nil {
		p.Error = err.Error()
		if secret != "" {
			p.Error = strings.ReplaceAll(p.Error, secret, "[REDACTED]")
		}
	}
	p.Duration = time.Since(start)
	return p
}

func configuredMaxTokens(s loadtest.Scenario, id string) int64 {
	cap := int64(s.Workload.Request.MaxTokens)
	mix := s.Workload.Request.Mix
	if mix == nil {
		return cap
	}
	if len(mix.MaxTokens) > 0 {
		cap = 0
		for _, c := range mix.MaxTokens {
			if int64(c.Value) > cap {
				cap = int64(c.Value)
			}
		}
	}
	for _, m := range mix.Models {
		if m.Value == id {
			if m.MaxTokens != nil {
				cap = int64(*m.MaxTokens)
			}
			if m.Mix != nil && len(m.Mix.MaxTokens) > 0 {
				cap = 0
				for _, c := range m.Mix.MaxTokens {
					if int64(c.Value) > cap {
						cap = int64(c.Value)
					}
				}
			}
		}
	}
	return cap
}

func containsParameter(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func appendUnique(values []string, value string) []string {
	if !containsParameter(values, value) {
		values = append(values, value)
	}
	return values
}
func shownNumber(v *json.Number) string {
	if v == nil {
		return "unknown"
	}
	return v.String()
}
func shownInt(v *int64) string {
	if v == nil {
		return "unknown"
	}
	return strconv.FormatInt(*v, 10)
}
func shownBool(v *bool) string {
	if v == nil {
		return "unknown"
	}
	return strconv.FormatBool(*v)
}
func environmentText(e *environmentSnapshot) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Snapshot: %s\n", e.CapturedAt.Format(time.RFC3339))
	verdict := "READY"
	if !e.Ready {
		verdict = "BLOCKED"
	}
	fmt.Fprintf(&out, "Status: %s\nWorkload: %s; %s; max in flight %d\n", verdict, e.Workload.Duration, e.Workload.Traffic, e.Workload.MaxInFlight)
	fmt.Fprintf(&out, "Advertised models: %s\n", safeEnvironmentCell(strings.Join(e.AdvertisedModels, ", ")))
	fmt.Fprintf(&out, "Network phase: %s\ncPOC active: %s\nBlock / epoch: %s / %s\nNext PoC block: %s\n", firstEnvironmentValue(e.Network.Phase), shownBool(e.Network.ConfirmationPoCActive), shownNumber(e.Network.BlockHeight), shownNumber(e.Network.EpochIndex), shownNumber(e.Network.NextPoCStart))
	if e.Capacity != nil && e.Capacity.AvailableHostCount != nil && e.Capacity.HostCount != nil {
		fmt.Fprintf(&out, "Available hosts: %d/%d\n", *e.Capacity.AvailableHostCount, *e.Capacity.HostCount)
	}
	descriptors := make([]loadtest.ModelSummary, len(e.Models))
	for i, m := range e.Models {
		descriptors[i].Model = m.ID
	}
	labels := loadtest.ModelColumnLabels(descriptors)
	modelLabels := map[string]string{}
	for i, m := range e.Models {
		fmt.Fprintf(&out, "%s = %s\n", labels[i], safeEnvironmentCell(m.ID))
		modelLabels[m.ID] = labels[i]
	}
	var table strings.Builder
	w := tabwriter.NewWriter(&table, 0, 4, 3, ' ', 0)
	cells := func(values []string) {
		for i := range values {
			values[i] = safeEnvironmentCell(values[i])
		}
		fmt.Fprintln(w, strings.Join(values, "\t"))
	}
	cells(append([]string{"Metric"}, labels...))
	row := func(label string, value func(environmentModel) string) {
		values := []string{label}
		for _, m := range e.Models {
			values = append(values, value(m))
		}
		cells(values)
	}
	row("Ready", func(m environmentModel) string { return strconv.FormatBool(m.Ready) })
	row("Advertised", func(m environmentModel) string { return strconv.FormatBool(m.Listed) })
	row("YAML weight", func(m environmentModel) string { return fmt.Sprintf("%g", m.Weight) })
	row("Dataset rows", func(m environmentModel) string {
		if m.DatasetRows == nil {
			return "-"
		}
		return strconv.Itoa(*m.DatasetRows)
	})
	row("Requested output cap", func(m environmentModel) string {
		if m.MaxRequestedTokens == 0 {
			return "server default"
		}
		return strconv.FormatInt(m.MaxRequestedTokens, 10)
	})
	row("Completion limit", func(m environmentModel) string { return shownInt(m.MaxCompletionTokens) })
	row("Context length", func(m environmentModel) string { return shownInt(m.ContextLength) })
	row("Available capacity", func(m environmentModel) string {
		if m.Capacity == nil || m.Capacity.AvailablePercent == nil {
			return "unknown"
		}
		return fmt.Sprintf("%.1f%%", *m.Capacity.AvailablePercent)
	})
	row("Live weight", func(m environmentModel) string {
		if m.Capacity == nil {
			return "unknown"
		}
		value := m.Capacity.CurrentWeight
		if value == nil {
			value = m.Capacity.TotalWeight
		}
		if value == nil {
			return "unknown"
		}
		return fmt.Sprintf("%.0f", *value)
	})
	row("Limit scope", func(m environmentModel) string { return firstEnvironmentValue(m.LimiterScope) })
	row("Configured concurrency", func(m environmentModel) string {
		if m.Limiter == nil {
			return "unknown"
		}
		if m.Limiter.MaxConcurrent != nil && *m.Limiter.MaxConcurrent == 0 {
			return "unlimited"
		}
		return shownInt(m.Limiter.MaxConcurrent)
	})
	row("Effective concurrency", func(m environmentModel) string {
		if m.Limiter == nil {
			return "unknown"
		}
		if m.Limiter.MaxConcurrent != nil && *m.Limiter.MaxConcurrent == 0 && m.Limiter.EffectiveConcurrent != nil && *m.Limiter.EffectiveConcurrent == 0 {
			return "unlimited"
		}
		return shownInt(m.Limiter.EffectiveConcurrent)
	})
	row("Requests in flight", func(m environmentModel) string {
		if m.Limiter == nil {
			return "unknown"
		}
		return shownInt(m.Limiter.InFlight)
	})
	row("Free concurrency slots", func(m environmentModel) string { return shownInt(m.FreeConcurrency) })
	row("Input tokens in flight", func(m environmentModel) string {
		if m.Limiter == nil {
			return "unknown"
		}
		return shownInt(m.Limiter.InFlightInputTokens)
	})
	row("Input token limit", func(m environmentModel) string {
		if m.Limiter == nil {
			return "unknown"
		}
		if m.Limiter.EffectiveInputTokens != nil && *m.Limiter.EffectiveInputTokens == 0 && m.Limiter.MaxInputTokens != nil && *m.Limiter.MaxInputTokens == 0 {
			return "unlimited"
		}
		return shownInt(m.Limiter.EffectiveInputTokens)
	})
	row("Escrow IDs", func(m environmentModel) string {
		if len(m.EscrowIDs) == 0 {
			return "unknown"
		}
		return strings.Join(m.EscrowIDs, ", ")
	})
	_ = w.Flush()
	out.WriteByte('\n')
	out.WriteString(table.String())
	escrows := append([]environmentEscrow(nil), e.Escrows...)
	sort.Slice(escrows, func(i, j int) bool { return escrows[i].ID < escrows[j].ID })
	// Bound table width when the gateway holds many escrow runtimes.
	for start := 0; start < len(escrows); start += 4 {
		group := escrows[start:min(start+4, len(escrows))]
		table.Reset()
		w = tabwriter.NewWriter(&table, 0, 4, 3, ' ', 0)
		header := []string{"Escrow"}
		for _, r := range group {
			header = append(header, r.ID)
		}
		cells(header)
		escrowRow := func(label string, value func(environmentEscrow) string) {
			values := []string{label}
			for _, r := range group {
				values = append(values, value(r))
			}
			cells(values)
		}
		escrowRow("Model", func(r environmentEscrow) string {
			if label := modelLabels[r.Model]; label != "" {
				return label
			}
			return firstEnvironmentValue(r.Model)
		})
		escrowRow("Active", func(r environmentEscrow) string { return shownBool(r.Active) })
		escrowRow("Phase", func(r environmentEscrow) string { return firstEnvironmentValue(r.Phase) })
		escrowRow("On hold", func(r environmentEscrow) string { return strconv.FormatBool(r.OnHold) })
		escrowRow("Balance (raw units)", func(r environmentEscrow) string { return shownNumber(r.Balance) })
		escrowRow("Nonce", func(r environmentEscrow) string { return shownNumber(r.Nonce) })
		escrowRow("Active requests", func(r environmentEscrow) string { return shownInt(r.ActiveRequests) })
		escrowRow("Reserved tokens", func(r environmentEscrow) string { return shownInt(r.ReservedTokens) })
		escrowRow("Live weight", func(r environmentEscrow) string {
			if e.Capacity != nil {
				if v, ok := e.Capacity.EscrowWeights[r.ID]; ok {
					return fmt.Sprintf("%.0f", v)
				}
			}
			return "unknown"
		})
		escrowRow("Requests blocked", func(r environmentEscrow) string { return shownBool(r.RequestsBlocked) })
		escrowRow("Block reason", func(r environmentEscrow) string { return firstEnvironmentValue(r.BlockReason) })
		_ = w.Flush()
		out.WriteByte('\n')
		out.WriteString(table.String())
	}
	for _, problem := range e.Problems {
		fmt.Fprintf(&out, "BLOCKED: %s\n", safeEnvironmentCell(problem))
	}
	for _, warning := range e.Warnings {
		fmt.Fprintf(&out, "NOTE: %s\n", safeEnvironmentCell(warning))
	}
	out.WriteString("This snapshot does not reserve capacity; availability and PoC phases may change during the test.\n")
	return out.String()
}
func safeEnvironmentCell(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ", "\t", " ", "\x1b", "?").Replace(s)
}
func firstEnvironmentValue(s string) string {
	if s == "" {
		return "unknown"
	}
	return safeEnvironmentCell(s)
}
func environmentMarkdown(e *environmentSnapshot) string {
	return "\n## Remote environment before workload\n\n```text\n" + strings.ReplaceAll(environmentText(e), "```", "'''") + "```\n"
}
