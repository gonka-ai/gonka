package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestGatewayDashboardReferencesRegisteredMetrics(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	registered := registeredGatewayMetricNames(t)
	for _, metric := range dashboardMetricNames(data) {
		if metricRegistered(metric, registered) {
			continue
		}
		t.Errorf("dashboard references unregistered metric %q", metric)
	}
}

func TestGatewayDashboardHasRequiredControlsAndRows(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	variables := dashboardVariables(t, dashboard)
	for _, name := range []string{"model", "participant_key", "escrow_id", "reason", "min_attempts"} {
		v, ok := variables[name]
		require.True(t, ok, "missing dashboard variable %q", name)
		require.NotEqual(t, float64(2), v["hide"], "dashboard variable %q must be visible", name)
	}
	require.Equal(t, "participant_key (optional drilldown)", variables["participant_key"]["label"])
	require.Equal(t, "reason (optional detail filter)", variables["reason"]["label"])

	rows := dashboardRows(t, dashboard)
	for _, title := range []string{
		"Overall Gateway Health",
		"Suspect Addresses (start here)",
		"Explain Selected Participant",
		"Global Cost and Policy Pressure",
		"Escrow and Runtime Diagnostics",
	} {
		require.Contains(t, rows, title, "missing dashboard row %q", title)
	}

	panels := dashboardPanels(t, dashboard)
	panelTitles := dashboardPanelTitles(t, panels)
	for _, title := range []string{
		"Average Request Rate",
		"Range User Success Rate",
		"Range Critical User Failures",
		"Range Cache Hit Share",
		"Gateway Capacity Scale",
		"Worst Model Capacity Scale",
		"Model Capacity Scale by Model",
		"HTTP Chat Route p95 (gateway-wide)",
		"Gateway Admission Rejections by Reason (gateway-wide)",
		"Successful Requests with Hidden Participant Failures by Reason",
		"Suspect Address Scorecard",
		"Real Attempts Started by Role and Reason",
		"Terminal Attempts by Outcome and Visibility",
		"Failed Attempts by Reason and Visibility",
		"User-Visible Winner Share by Participant",
		"Participant Receipt p95 by Address",
		"Participant TTFT / First Content p95 by Address",
		"Participant Prefill per Input Token p95 by Address",
		"Participant Total Attempt Time p95 by Address",
		"No-Receipt and Empty-Stream Rates",
		"Transport and EOF Failure Rates",
		"Current Participant Quarantine Mode",
		"Winner Failures After User-Visible Content",
		"Global Sent Attempts per User Request",
		"Global Failed Sends per Successful User Request",
		"Extra Real Sends per User Request",
		"Ghost or No-Send Slots per User Request",
		"Top Skipped-Slot Reasons",
		"Timeout Actions by Kind and Action",
		"Timeout Skip and Failure Reasons Over Time",
		"Model x Escrow No-Send Pressure",
		"Runtime Picker Choices by Model and Escrow",
		"Escrow Blocked Participant Count",
	} {
		require.Contains(t, panelTitles, title, "missing dashboard panel %q", title)
	}
	for _, panel := range panels {
		title, _ := panel["title"].(string)
		if !strings.Contains(title, "Heatmap") {
			continue
		}
		require.Equal(t, "heatmap", panel["type"], "panel %q says heatmap but is type %q", title, panel["type"])
	}
	require.NotContains(t, string(data), `reason=~".*`, "dashboard should use bounded reason values, not broad reason regexes")
}

func TestGatewayDashboardStartsWithOverallHealth(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	rows := dashboardRowTitles(t, dashboard)
	require.NotEmpty(t, rows)
	require.Equal(t, "Overall Gateway Health", rows[0], "first real dashboard row must be health-only")
	require.Equal(t, []string{
		"Overall Gateway Health",
		"Nonce Consumption",
		"Suspect Addresses (start here)",
		"Explain Selected Participant",
		"Global Cost and Policy Pressure",
		"Escrow and Runtime Diagnostics",
	}, rows)

	for _, title := range []string{
		"Gateway Scrape",
		"Average Request Rate",
		"Range User Success Rate",
		"Range Critical User Failures",
		"Range Cache Hit Share",
		"Gateway Capacity Scale",
		"Worst Model Capacity Scale",
		"Model Capacity Scale by Model",
		"What Users Saw by Outcome and Reason",
		"Top User-Visible Failure Reasons",
		"Successful Requests with Hidden Participant Failures by Reason",
		"HTTP Chat Route p95 (gateway-wide)",
		"Gateway Admission Rejections by Reason (gateway-wide)",
		"Active Requests and Input Tokens",
	} {
		panel := dashboardPanelByTitle(t, dashboard, title)
		expr := strings.Join(panelTargetExprs(panel), "\n")
		require.NotContains(t, expr, "participant_key", "health panel %q must not require participant selection", title)
	}
}

func TestGatewayDashboardStaysCompact(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))
	panels := dashboardPanels(t, dashboard)

	require.LessOrEqual(t, len(dashboardRowTitles(t, dashboard)), 6, "dashboard must stay focused")
	dataPanels := 0
	for _, panel := range panels {
		switch panel["type"] {
		case "row":
			continue
		case "text":
			t.Fatalf("dashboard must not use text-flow panels: %q", panel["title"])
		default:
			dataPanels++
		}
	}
	require.LessOrEqual(t, dataPanels, 45, "dashboard has too many data panels")

	panelTitles := dashboardPanelTitles(t, panels)
	for _, title := range []string{
		"Read This First",
		"Failure Drilldown Guide",
		"Protocol Feedback",
		"Participant Scorecard for Selected Suspect",
		"Hidden Failures for This Participant",
		"Timeout Work for This Participant",
		"Diagnostic Gateway Mechanics",
		"Worst Attempt Failure Rates by Address",
		"Participants with Hidden Loser Failures",
		"Participants with Policy Pressure",
		"Hidden Loser Failures by Participant",
		"Skipped Quarantine Slots by Participant",
		"Participants Requiring Extra Sends",
		"Participants with No-Winner Pressure",
		"Participants Entering Quarantine or No-Winner Modes",
		"Suspect Participants by Problem Events per Sent Attempt",
		"Suspect Participants by Problem Event Count",
		"Slow Participants by p95 First Content",
	} {
		require.NotContains(t, panelTitles, title, "deleted/duplicated panel or row %q returned", title)
	}
}

func TestGatewayDashboardRestoresKeyHealthSignals(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	scale := dashboardPanelByTitle(t, dashboard, "Model Capacity Scale by Model")
	scaleExpr := strings.Join(panelTargetExprs(scale), "\n")
	require.Contains(t, scaleExpr, `devshard_gateway_capacity_scale_by_model{model=~"$model"}`)
	require.NotContains(t, scaleExpr, "participant_key")

	hidden := dashboardPanelByTitle(t, dashboard, "Successful Requests with Hidden Participant Failures by Reason")
	require.Contains(t, strings.Join(panelTargetExprs(hidden), "\n"), "devshard_gateway_user_requests_with_hidden_failure_total")

	timeout := dashboardPanelByTitle(t, dashboard, "Timeout Actions by Kind and Action")
	timeoutExpr := strings.Join(panelTargetExprs(timeout), "\n")
	require.Contains(t, timeoutExpr, "devshard_gateway_timeout_actions_total")
	require.Contains(t, timeoutExpr, "by (kind, action)")
}

func TestGatewayDashboardShowsCurrentProcessRecoveryQuarantines(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	panel := dashboardPanelByTitle(t, dashboard, "Startup Recovery Quarantines - Current Process")
	require.Equal(t, "table", panel["type"])
	require.Contains(t, panel["description"], "current gateway process")
	targets := panelTargets(t, panel)
	require.Len(t, targets, 1)
	require.Equal(t, true, targets[0]["instant"])
	require.Equal(t, "table", targets[0]["format"])
	require.Equal(t,
		`devshard_gateway_startup_skipped_escrow{model=~"$model",escrow_id=~"$escrow_id",reason="local_recovery_failed"} == 1`,
		targets[0]["expr"],
	)
}

func TestGatewayDashboardHasNoGridOverlap(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	occupied := make(map[[2]int]string)
	for _, panel := range dashboardPanels(t, dashboard) {
		title, _ := panel["title"].(string)
		grid, ok := panel["gridPos"].(map[string]any)
		require.True(t, ok, "panel %q missing gridPos", title)
		gridInt := func(field string) int {
			v, numeric := grid[field].(float64)
			require.True(t, numeric, "panel %q gridPos.%s is not a number", title, field)
			return int(v)
		}
		x := gridInt("x")
		y := gridInt("y")
		w := gridInt("w")
		h := gridInt("h")
		for yy := y; yy < y+h; yy++ {
			for xx := x; xx < x+w; xx++ {
				key := [2]int{xx, yy}
				if other, exists := occupied[key]; exists {
					t.Fatalf("panels %q and %q overlap at x=%d y=%d", other, title, xx, yy)
				}
				occupied[key] = title
			}
		}
	}
}

func TestGatewayDashboardSuspectPanelsDiscoverParticipants(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	rows := dashboardRowTitles(t, dashboard)
	require.GreaterOrEqual(t, len(rows), 3)
	require.Equal(t, "Nonce Consumption", rows[1], "nonce row must stay after overall health")
	require.Equal(t, "Suspect Addresses (start here)", rows[2], "suspect row must come after nonce consumption")

	participantModelGroup := regexp.MustCompile(`by \([^)]*participant_key[^)]*model`)
	panel := dashboardPanelByTitle(t, dashboard, "Suspect Address Scorecard")
	expr := strings.Join(panelTargetExprs(panel), "\n")
	require.Contains(t, expr, `participant_key!="unknown"`, "scorecard must exclude unknown participant attribution")
	require.Regexp(t, participantModelGroup, expr, "scorecard must group by participant_key and model")
	require.NotContains(t, expr, `reason=~"$reason"`, "scorecard must not require reason selection")
	for _, forbidden := range []string{"escrow_id", "devshard_id", "host_idx", "address"} {
		require.NotContains(t, expr, forbidden, "scorecard must not use %s as identity", forbidden)
	}
}

func TestGatewayDashboardSuspectPanelsLinkToParticipantDrilldown(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	panel := dashboardPanelByTitle(t, dashboard, "Suspect Address Scorecard")
	links := panelFieldLinks(panel)
	require.NotEmpty(t, links, "scorecard must offer drilldown links")
	require.Contains(t, strings.Join(links, "\n"), "var-participant_key=${__data.fields.participant_key}", "scorecard must link selected participant into the dashboard variable")
	require.Contains(t, strings.Join(links, "\n"), "var-model=${__data.fields.model}", "scorecard must link selected model into the dashboard variable")
}

func TestGatewayDashboardSuspectScorecardRendersReadableTable(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	panel := dashboardPanelByTitle(t, dashboard, "Suspect Address Scorecard")
	require.Equal(t, "table", panel["type"], "scorecard must be a table")
	require.GreaterOrEqual(t, len(panelTargets(t, panel)), 10, "scorecard must have explicit evidence targets")
	for _, target := range panelTargets(t, panel) {
		require.Equal(t, "table", target["format"], "scorecard must ask Prometheus for table data")
	}
	require.NotEmpty(t, panelTransformationByID(t, panel, "merge"), "scorecard must merge evidence targets")
	organize := panelTransformationByID(t, panel, "organize")
	options, ok := organize["options"].(map[string]any)
	require.True(t, ok, "scorecard organize transform missing options")
	excluded, ok := options["excludeByName"].(map[string]any)
	require.True(t, ok, "scorecard must hide noisy columns")
	require.Equal(t, true, excluded["Time"], "scorecard must hide Time column")
	renames, ok := options["renameByName"].(map[string]any)
	require.True(t, ok, "scorecard must rename value columns")
	renamed := make(map[string]struct{})
	for _, value := range renames {
		if name, ok := value.(string); ok {
			renamed[name] = struct{}{}
		}
	}
	for _, column := range []string{
		"participant_key",
		"model",
		"started_attempts\ncount",
		"finished_attempts\ncount",
		"failure_rate\nfailed/finished",
		"issues_per_started\ncount/start",
		"issue_count\ncount",
		"failed_attempts\ncount",
		"no_send_slots\ncount",
		"no_winner_attempts\ncount",
		"timeout_skipped_failed\ncount",
		"inference_transport_errors\ncount",
		"limit_rejections\ncount",
		"p95_ttft_first_content\nseconds",
		"p95_receipt\nseconds",
		"p95_prefill_per_input_token\nseconds",
		"p95_total_attempt\nseconds",
	} {
		require.Contains(t, renamed, column, "scorecard missing readable column %q", column)
	}
	require.NotContains(t, renamed, "transport_errors\ncount", "scorecard transport errors must be inference-only")
}

func TestGatewayDashboardAvoidsAmbiguousPanels(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))
	panels := dashboardPanels(t, dashboard)

	titles := make(map[string]struct{})
	queries := make(map[string]string)
	for _, panel := range panels {
		title, _ := panel["title"].(string)
		require.NotEmpty(t, title, "panel missing title")
		require.NotContains(t, title, "host_idx", "dashboard timing must be address-level, not host-slot level")
		require.NotContains(t, title, "Host Timing", "dashboard timing must be address-level, not host-slot level")
		require.NotContains(t, title, "Escrow Slot", "dashboard timing must be address-level, not host-slot level")
		require.NotEqual(t, "Diagnostic CTTFL per Input Token p95", title, "CTTFL panel title is too easy to confuse with user-visible TTFT")
		if _, exists := titles[title]; exists {
			t.Fatalf("duplicate panel title %q", title)
		}
		titles[title] = struct{}{}

		expr := strings.Join(panelTargetExprs(panel), "\n")
		if expr == "" {
			continue
		}
		if otherTitle, exists := queries[expr]; exists {
			t.Fatalf("duplicate PromQL in panels %q and %q: %s", otherTitle, title, expr)
		}
		queries[expr] = title
	}
}

func TestGatewayDashboardDoesNotUseHostSlotTiming(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	require.NotContains(t, string(data), "host_idx", "dashboard must not require mapping host slots to participant addresses")
	for _, metric := range []string{
		"devshard_host_receipt_seconds",
		"devshard_host_first_token_seconds",
		"devshard_host_cttfl_seconds_per_input_token",
		"devshard_host_total_time_seconds",
	} {
		require.NotContains(t, string(data), metric, "dashboard must use participant timing metrics instead of host-slot timing metrics")
	}
}

func TestGatewayDashboardParticipantTimingByAddressAndModel(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	for title, metric := range map[string]string{
		"Participant Receipt p95 by Address":                 "devshard_gateway_participant_receipt_seconds_bucket",
		"Participant TTFT / First Content p95 by Address":    "devshard_gateway_participant_first_content_seconds_bucket",
		"Participant Prefill per Input Token p95 by Address": "devshard_gateway_participant_prefill_seconds_per_input_token_bucket",
		"Participant Total Attempt Time p95 by Address":      "devshard_gateway_participant_total_attempt_seconds_bucket",
	} {
		panel := dashboardPanelByTitle(t, dashboard, title)
		expr := strings.Join(panelTargetExprs(panel), "\n")
		require.Contains(t, expr, metric, "panel %q must use participant timing metric", title)
		require.Contains(t, expr, "by (participant_key, model, le)", "panel %q must group timing by participant_key and model", title)
		for _, forbidden := range []string{"host_idx", "devshard_id", "address"} {
			require.NotContains(t, expr, forbidden, "panel %q must not use %s as timing identity", title, forbidden)
		}
	}
}

func TestGatewayDashboardUsesEscrowFilterForDevshardDiagnostics(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	for _, title := range []string{
		"Escrow Blocked Participant Count",
		"Runtime Picker Choices by Model and Escrow",
		"Non-Inference Transport Errors by Path",
	} {
		if title == "Non-Inference Transport Errors by Path" {
			continue
		}
		panel := dashboardPanelByTitle(t, dashboard, title)
		for _, expr := range panelTargetExprs(panel) {
			require.Contains(t, expr, `devshard_id=~"$escrow_id"`, "panel %q must honor the visible escrow_id filter", title)
		}
	}
}

func TestGatewayDashboardSeparatesInferenceTransportErrors(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	scorecard := dashboardPanelByTitle(t, dashboard, "Suspect Address Scorecard")
	scorecardExpr := strings.Join(panelTargetExprs(scorecard), "\n")
	require.Contains(t, scorecardExpr, `path_kind="inference"`, "scorecard transport errors must be inference-path only")
	require.NotContains(t, scorecardExpr, `path_kind!="inference"`, "scorecard must not include non-inference transport diagnostics")

	diagnostic := dashboardPanelByTitle(t, dashboard, "Non-Inference Transport Errors by Path")
	diagnosticExpr := strings.Join(panelTargetExprs(diagnostic), "\n")
	require.Contains(t, diagnosticExpr, `path_kind!="inference"`, "non-inference transport errors belong in diagnostics")
	require.Contains(t, diagnosticExpr, "by (path_kind, status)", "diagnostic transport panel should explain which path failed")
}

func TestGatewayDashboardParticipantPanelsUseParticipantKey(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	for _, title := range []string{
		"User-Visible Winner Share by Participant",
		"Participant Receipt p95 by Address",
		"Participant TTFT / First Content p95 by Address",
		"Participant Prefill per Input Token p95 by Address",
		"Participant Total Attempt Time p95 by Address",
		"No-Receipt and Empty-Stream Rates",
		"Transport and EOF Failure Rates",
		"Current Participant Quarantine Mode",
		"Winner Failures After User-Visible Content",
	} {
		panel := dashboardPanelByTitle(t, dashboard, title)
		expr := strings.Join(panelTargetExprs(panel), "\n")
		require.Contains(t, expr, "participant_key", "panel %q must support address-level investigation", title)
		require.NotContains(t, expr, "host_idx", "panel %q must not use host_idx as participant identity", title)
	}
}

func TestGatewayDashboardParticipantQualityDoesNotGroupByEscrow(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	escrowGroup := regexp.MustCompile(`by \([^)]*escrow_id`)
	for _, title := range []string{
		"Real Attempts Started by Role and Reason",
		"Terminal Attempts by Outcome and Visibility",
		"Failed Attempts by Reason and Visibility",
		"User-Visible Winner Share by Participant",
		"Participant TTFT / First Content p95 by Address",
		"Participant Total Attempt Time p95 by Address",
		"No-Receipt and Empty-Stream Rates",
		"Transport and EOF Failure Rates",
		"Current Participant Quarantine Mode",
		"Winner Failures After User-Visible Content",
	} {
		panel := dashboardPanelByTitle(t, dashboard, title)
		expr := strings.Join(panelTargetExprs(panel), "\n")
		require.False(t, escrowGroup.MatchString(expr), "panel %q must not group default participant quality by escrow_id", title)
	}
}

func TestGatewayDashboardFailureRateDenominatorsIgnoreReason(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	for _, title := range []string{
		"No-Receipt and Empty-Stream Rates",
		"Transport and EOF Failure Rates",
	} {
		panel := dashboardPanelByTitle(t, dashboard, title)
		expr := strings.Join(panelTargetExprs(panel), "\n")
		require.Contains(t, expr, "clamp_min(sum by (participant_key, model) (increase(devshard_gateway_attempts_terminal_total", "panel %q denominator must use finished attempts grouped by participant_key and model", title)
		require.NotContains(t, expr, "clamp_min(sum by (participant_key, model) (increase(devshard_gateway_attempts_started_total", "panel %q denominator must not use started attempts", title)
		require.NotContains(t, expr, "clamp_min(sum by (participant_key, model, reason) (increase(", "panel %q denominator must not include failure reason", title)
	}
}

func TestGatewayDashboardScorecardDoesNotCompareFailuresToStarts(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	panel := dashboardPanelByTitle(t, dashboard, "Suspect Address Scorecard")
	expr := strings.Join(panelTargetExprs(panel), "\n")
	require.Contains(t, expr, "devshard_gateway_attempts_terminal_total", "scorecard must expose finished attempts for failure comparisons")
	require.NotContains(t, expr, "failed_attempts / clamp_min(sum by (participant_key, model) (increase(devshard_gateway_attempts_started_total", "failed attempts must not be compared to started attempts")

	organize := panelTransformationByID(t, panel, "organize")
	options, ok := organize["options"].(map[string]any)
	require.True(t, ok, "scorecard organize transform missing options")
	renames, ok := options["renameByName"].(map[string]any)
	require.True(t, ok, "scorecard must rename value columns")
	values := make(map[string]struct{})
	for _, value := range renames {
		if name, ok := value.(string); ok {
			values[name] = struct{}{}
		}
	}
	require.Contains(t, values, "started_attempts\ncount")
	require.Contains(t, values, "finished_attempts\ncount")
	require.Contains(t, values, "failure_rate\nfailed/finished")
	require.NotContains(t, values, "sent_attempts")
	require.NotContains(t, values, "issue_rate")
}

func TestGatewayDashboardScorecardCountColumnsAreRounded(t *testing.T) {
	path := gatewayDashboardPath(t)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var dashboard map[string]any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	panel := dashboardPanelByTitle(t, dashboard, "Suspect Address Scorecard")
	countRefs := map[string]string{
		"A": "started_attempts",
		"B": "finished_attempts",
		"E": "issue_count",
		"F": "failed_attempts",
		"G": "no_send_slots",
		"H": "no_winner_attempts",
		"I": "timeout_skipped_failed",
		"J": "inference_transport_errors",
		"K": "limit_rejections",
	}
	for _, target := range panelTargets(t, panel) {
		refID, _ := target["refId"].(string)
		name, ok := countRefs[refID]
		if !ok {
			continue
		}
		expr, _ := target["expr"].(string)
		require.Contains(t, expr, "round(", "scorecard count column %s (%s) must round Prometheus increase() extrapolation", refID, name)
	}
}

func gatewayDashboardPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRootFromCwd(t), "deploy", "join", "observability", "grafana", "dashboards", "gonka-gateway-observability.json")
}

func repoRootFromCwd(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "deploy", "join", "observability")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate repo root containing deploy/join/observability")
	return ""
}

func registeredGatewayMetricNames(t *testing.T) map[string]struct{} {
	t.Helper()
	metrics := NewDevshardMetrics()
	metrics.registry.MustRegister(newGatewayMetricsCollectorWithHostConnections(nil, nil))

	names := make(map[string]struct{})
	families, err := metrics.registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		names[family.GetName()] = struct{}{}
	}

	descCh := make(chan *prometheus.Desc, 256)
	go func() {
		defer close(descCh)
		metrics.registry.Describe(descCh)
	}()
	descRe := regexp.MustCompile(`fqName: "([^"]+)"`)
	for desc := range descCh {
		if match := descRe.FindStringSubmatch(desc.String()); len(match) == 2 {
			names[match[1]] = struct{}{}
		}
	}
	return names
}

func dashboardMetricNames(data []byte) []string {
	expr := regexp.MustCompile(`devshard_(?:gateway|http|host|runtime|speculative|inference)[a-zA-Z0-9_]*`)
	seen := make(map[string]struct{})
	for _, metric := range expr.FindAllString(string(data), -1) {
		seen[metric] = struct{}{}
	}

	metrics := make([]string, 0, len(seen))
	for metric := range seen {
		metrics = append(metrics, metric)
	}
	return metrics
}

func metricRegistered(metric string, registered map[string]struct{}) bool {
	if _, ok := registered[metric]; ok {
		return true
	}
	for _, suffix := range []string{"_bucket", "_sum", "_count"} {
		if strings.HasSuffix(metric, suffix) {
			if _, ok := registered[strings.TrimSuffix(metric, suffix)]; ok {
				return true
			}
		}
	}
	return false
}

func dashboardVariables(t *testing.T, dashboard map[string]any) map[string]map[string]any {
	t.Helper()
	templating, ok := dashboard["templating"].(map[string]any)
	require.True(t, ok, "missing templating section")
	list, ok := templating["list"].([]any)
	require.True(t, ok, "missing templating list")

	variables := make(map[string]map[string]any)
	for _, item := range list {
		variable, ok := item.(map[string]any)
		require.True(t, ok, "dashboard variable must be an object")
		name, ok := variable["name"].(string)
		require.True(t, ok, "dashboard variable missing name")
		variables[name] = variable
	}
	return variables
}

func dashboardRows(t *testing.T, dashboard map[string]any) map[string]struct{} {
	t.Helper()
	panels := dashboardPanels(t, dashboard)

	rows := make(map[string]struct{})
	for _, item := range panels {
		panel := item
		if panel["type"] != "row" {
			continue
		}
		title, ok := panel["title"].(string)
		require.True(t, ok, "row panel missing title")
		rows[title] = struct{}{}
	}
	return rows
}

func dashboardRowTitles(t *testing.T, dashboard map[string]any) []string {
	t.Helper()
	panels := dashboardPanels(t, dashboard)

	rows := make([]string, 0)
	for _, panel := range panels {
		if panel["type"] != "row" {
			continue
		}
		title, ok := panel["title"].(string)
		require.True(t, ok, "row panel missing title")
		rows = append(rows, title)
	}
	return rows
}

func dashboardPanels(t *testing.T, dashboard map[string]any) []map[string]any {
	t.Helper()
	items, ok := dashboard["panels"].([]any)
	require.True(t, ok, "missing panels")

	panels := make([]map[string]any, 0, len(items))
	for _, item := range items {
		panel, ok := item.(map[string]any)
		require.True(t, ok, "panel must be an object")
		panels = append(panels, panel)
	}
	return panels
}

func dashboardPanelTitles(t *testing.T, panels []map[string]any) map[string]struct{} {
	t.Helper()
	titles := make(map[string]struct{})
	for _, panel := range panels {
		title, ok := panel["title"].(string)
		require.True(t, ok, "panel missing title")
		titles[title] = struct{}{}
	}
	return titles
}

func dashboardPanelByTitle(t *testing.T, dashboard map[string]any, title string) map[string]any {
	t.Helper()
	for _, panel := range dashboardPanels(t, dashboard) {
		if panel["title"] == title {
			return panel
		}
	}
	t.Fatalf("missing dashboard panel %q", title)
	return nil
}

func panelTargetExprs(panel map[string]any) []string {
	targets := panelTargets(nil, panel)
	exprs := make([]string, 0, len(targets))
	for _, target := range targets {
		expr, ok := target["expr"].(string)
		if !ok || expr == "" {
			continue
		}
		exprs = append(exprs, expr)
	}
	return exprs
}

func panelTargets(t *testing.T, panel map[string]any) []map[string]any {
	if t != nil {
		t.Helper()
	}
	items, _ := panel["targets"].([]any)
	targets := make([]map[string]any, 0, len(items))
	for _, item := range items {
		target, ok := item.(map[string]any)
		if !ok {
			continue
		}
		targets = append(targets, target)
	}
	return targets
}

func panelTransformationByID(t *testing.T, panel map[string]any, id string) map[string]any {
	t.Helper()
	items, _ := panel["transformations"].([]any)
	for _, item := range items {
		transformation, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if transformation["id"] == id {
			return transformation
		}
	}
	t.Fatalf("panel %q missing transformation %q", panel["title"], id)
	return nil
}

func panelFieldLinks(panel map[string]any) []string {
	fieldConfig, _ := panel["fieldConfig"].(map[string]any)
	defaults, _ := fieldConfig["defaults"].(map[string]any)
	items, _ := defaults["links"].([]any)
	links := make([]string, 0, len(items))
	for _, item := range items {
		link, ok := item.(map[string]any)
		if !ok {
			continue
		}
		url, ok := link["url"].(string)
		if !ok || url == "" {
			continue
		}
		links = append(links, url)
	}
	return links
}
