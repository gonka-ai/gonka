package loadtest

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"devshard/testenv/config"
)

// invariantRun owns the pollers that feed the end-of-run invariants.
type invariantRun struct {
	opts       RunnerConfig
	cfg        *config.File
	scenario   Scenario
	project    string
	compose    string
	gatewayURL string
	apiKey     string
	settings   invariantSettings
	started    time.Time
	throttle   *jsonlPoller[ThrottleSample]
	pending    *jsonlPoller[PendingSample]
	hostStats  *jsonlPoller[HostStatsSample]
	dapiHosts  *jsonlPoller[DapiHostSample]
	accounting *jsonlPoller[AccountingSnapshot]
	// bindingChecked is set once warnBinding has judged the warm-up sample.
	bindingChecked bool
}

// startInvariantRun starts the pollers right before the workload; started is
// the workload start the event timeline is planned against.
func startInvariantRun(ctx context.Context, opts RunnerConfig, cfg *config.File, project, compose, gatewayURL, statsURL, apiKey string, scenario Scenario, started time.Time) (*invariantRun, error) {
	r := &invariantRun{opts: opts, cfg: cfg, scenario: scenario, project: project, compose: compose, gatewayURL: gatewayURL, apiKey: apiKey, settings: resolveInvariantSettings(scenario), started: started}
	planned, _, err := planEvents(scenario, cfg)
	if err != nil {
		return nil, err
	}
	out := func(name string) string { return filepath.Join(opts.OutputDir, name) }
	if r.accounting, err = startJSONLPoller(ctx, out("accounting-snapshots.jsonl"), "baseline", atTimes(accountingBoundaries(started, planned, r.settings.Grace)), collectAccounting(statsURL)); err != nil {
		return nil, err
	}
	if r.throttle, err = startJSONLPoller(ctx, out("throttle.jsonl"), "start", everyInterval(throttlePollEvery), collectThrottle(gatewayURL, apiKey)); err != nil {
		r.stop()
		return nil, err
	}
	if r.pending, err = startJSONLPoller(ctx, out("pending.jsonl"), "start", everyInterval(pendingPollEvery), collectPending(gatewayURL, apiKey)); err != nil {
		r.stop()
		return nil, err
	}
	if r.hostStats, err = startJSONLPoller(ctx, out("host-stats.jsonl"), "start", everyInterval(hostStatsPollEvery), collectHostStats(opts, cfg, project, compose, gatewayURL, apiKey)); err != nil {
		r.stop()
		return nil, err
	}
	collectDapi := collectDapiHosts(fmt.Sprintf("http://127.0.0.1:%d", cfg.MockDapi.HTTPPort))
	warmup := started.Add(r.settings.SteadyAfter)
	bound := boundHosts(scenario)
	if r.dapiHosts, err = startJSONLPoller(ctx, out("dapi-host-stats.jsonl"), "start", everyInterval(dapiHostsPollEvery), func(ctx context.Context, label string) []DapiHostSample {
		samples := collectDapi(ctx, label)
		r.warnBinding(samples, warmup, bound)
		return samples
	}); err != nil {
		r.stop()
		return nil, err
	}
	return r, nil
}

// stop ends all pollers with a final sample each; it is idempotent.
func (r *invariantRun) stop() {
	if r == nil {
		return
	}
	r.accounting.stop("final")
	r.throttle.stop("final")
	r.pending.stop("final")
	r.hostStats.stop("final")
	r.dapiHosts.stop("final")
}

// warnBinding logs once, loudly, at the first sample after warm-up when the
// ML-node binding is not in effect; faults-fired-and-ml-nodes-bound.host_binding fails the run at the end.
func (r *invariantRun) warnBinding(samples []DapiHostSample, warmup time.Time, bound map[string]bool) {
	if r.bindingChecked || len(bound) == 0 || len(samples) == 0 || samples[0].At.Before(warmup) {
		return
	}
	r.bindingChecked = true
	if problems := bindingProblems(nil, samples[0], bound); len(problems) > 0 {
		log.Printf("loadtest: stage=binding_check result=FAIL ML-node-to-host binding is NOT in effect: %s", strings.Join(problems, "; "))
		return
	}
	log.Printf("loadtest: stage=binding_check result=ok hosts=%d", len(bound))
}

// collectEndState runs after drain: it stops the pollers and reads what the invariants need once,
// from the stack and from compose-pre-drain.log at logPath.
func (r *invariantRun) collectEndState(ctx context.Context, rotationModels []string, logPath string) invariantEndState {
	r.stop()
	end := invariantEndState{SettlementDisabled: settlementDisabledModels(r.scenario, rotationModels), DidNotCompress: -1}
	if ticks, err := readReconcileTicks(logPath); err != nil {
		log.Printf("loadtest: invariants read versiond reconcile ticks: %v", err)
	} else {
		end.ReconcileTicks = ticks
	}
	var err error
	if end.MetricsBytes, err = completeLinesSize(filepath.Join(r.opts.OutputDir, "metrics.jsonl")); err != nil {
		log.Printf("loadtest: invariants read metrics.jsonl: %v", err)
	}
	if devshards, err := fetchGatewayDevshards(ctx, r.gatewayURL, r.apiKey); err != nil {
		log.Printf("loadtest: invariants gateway devshards: %v", err)
	} else {
		end.Devshards = devshards
		end.Inferences = map[string]map[string]DebugInference{}
		for _, d := range devshards {
			inferences, err := fetchDebugInferences(ctx, scopedGatewayURL(r.gatewayURL, d.ID), r.apiKey)
			if err != nil {
				log.Printf("loadtest: invariants inferences for %s: %v", d.ID, err)
				continue
			}
			end.Inferences[d.ID] = inferences
		}
	}
	chainURL := fmt.Sprintf("http://127.0.0.1:%d", r.cfg.MockChain.TestenvPort)
	if end.SettleTxs, end.Escrows, err = fetchChainLedger(ctx, chainURL); err != nil {
		end.ChainError = err.Error()
	}
	if ids, err := readEscrowMissing(logPath); err != nil {
		log.Printf("loadtest: invariants read escrow-missing lines: %v", err)
	} else {
		end.EscrowMissing = ids
	}
	if r.cfg.Postgres.Enabled {
		end.LeaseExcluded = leaseExcludedHosts(r.scenario)
		for _, host := range zstdPayloadHosts(r.scenario) {
			log.Printf("loadtest: invariants no-skipped-validation-leases excludes %s: %s", host, end.LeaseExcluded[host])
		}
		if end.Leases, err = queryLeaseCounts(ctx, r.opts.TestenvDir, r.project, r.compose, r.cfg, end.LeaseExcluded); err != nil {
			end.LeasesError = err.Error()
		}
	}
	if count, err := countLogLines(logPath, "did not compress"); err == nil {
		end.DidNotCompress = count
	}
	for _, host := range zstdPayloadHosts(r.scenario) {
		if end.PayloadFiles == nil {
			end.PayloadFiles = map[string]PayloadFileStats{}
		}
		dest := filepath.Join(r.opts.OutputDir, "payload-sample", host)
		end.PayloadFiles[host] = samplePayloadFiles(ctx, r.opts.TestenvDir, r.project, r.compose, host, r.cfg.Versiond.VersionName, dest)
	}
	return end
}

// settlementDisabledModels mirrors the gateway rule: a per-model override
// wins, otherwise the global rotation flag applies.
func settlementDisabledModels(s Scenario, models []string) map[string]bool {
	rotation := s.Gateway.EscrowRotation
	if !rotation.Enabled {
		return map[string]bool{}
	}
	overrides := map[string]*bool{}
	for _, model := range rotation.Models {
		overrides[model.Model] = model.SettlementEnabled
	}
	disabled := map[string]bool{}
	for _, model := range models {
		enabled := rotation.SettlementEnabled
		if override := overrides[model]; override != nil {
			enabled = *override
		}
		if !enabled {
			disabled[model] = true
		}
	}
	return disabled
}
