package loadtest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"devshard/testenv/config"
	"devshard/testenv/mockchain/ledgerface"
)

// jsonlPoller collects samples on a schedule, appends each one to a JSONL
// artifact and keeps them for the invariant evaluators.
type jsonlPoller[T any] struct {
	cancel  context.CancelFunc
	done    chan struct{}
	collect func(ctx context.Context, label string) []T
	file    *os.File
	encoder *json.Encoder
	mu      sync.Mutex
	samples []T
	errs    []string
	once    sync.Once
}

// pollSchedule blocks until the next sample is due and returns its label, or
// false when ctx ends or the schedule is exhausted.
type pollSchedule func(ctx context.Context) (string, bool)

func everyInterval(interval time.Duration) pollSchedule {
	var ticker *time.Ticker
	return func(ctx context.Context) (string, bool) {
		if ticker == nil {
			ticker = time.NewTicker(interval)
		}
		select {
		case <-ctx.Done():
			ticker.Stop()
			return "", false
		case <-ticker.C:
			return "", true
		}
	}
}

type labeledTime struct {
	At    time.Time
	Label string
}

func atTimes(times []labeledTime) pollSchedule {
	sorted := append([]labeledTime(nil), times...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	next := 0
	return func(ctx context.Context) (string, bool) {
		if next >= len(sorted) {
			<-ctx.Done()
			return "", false
		}
		item := sorted[next]
		next++
		if !waitForArrival(ctx, item.At) {
			return "", false
		}
		return item.Label, true
	}
}

const pollCollectTimeout = 20 * time.Second

func startJSONLPoller[T any](ctx context.Context, path, firstLabel string, schedule pollSchedule, collect func(context.Context, string) []T) (*jsonlPoller[T], error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	child, cancel := context.WithCancel(ctx)
	p := &jsonlPoller[T]{cancel: cancel, done: make(chan struct{}), collect: collect, file: file, encoder: json.NewEncoder(file)}
	if firstLabel != "" {
		p.sample(child, firstLabel)
	}
	go func() {
		defer close(p.done)
		for {
			label, ok := schedule(child)
			if !ok {
				return
			}
			p.sample(child, label)
		}
	}()
	return p, nil
}

func (p *jsonlPoller[T]) sample(ctx context.Context, label string) {
	callCtx, cancel := context.WithTimeout(ctx, pollCollectTimeout)
	defer cancel()
	samples := p.collect(callCtx, label)
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, sample := range samples {
		if err := p.encoder.Encode(sample); err != nil {
			p.errs = append(p.errs, err.Error())
		}
	}
	p.samples = append(p.samples, samples...)
}

// halt ends the schedule and waits for its sample in flight; the artifact stays open for samples
// the caller takes until stop. It is safe on a nil poller.
func (p *jsonlPoller[T]) halt() {
	if p == nil {
		return
	}
	p.cancel()
	<-p.done
}

// stop ends the schedule, takes one last sample labeled finalLabel unless it
// is empty, and closes the artifact. It is safe on a nil poller.
func (p *jsonlPoller[T]) stop(finalLabel string) []T {
	if p == nil {
		return nil
	}
	p.once.Do(func() {
		p.cancel()
		<-p.done
		if finalLabel != "" {
			p.sample(context.Background(), finalLabel)
		}
		if err := p.file.Close(); err != nil {
			p.errs = append(p.errs, err.Error())
		}
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]T(nil), p.samples...)
}

// GatewayDevshard is one row of GET /v1/admin/devshards. Resident is true when
// the gateway attached a runtime snapshot, which it does exactly for IDs still
// in its runtime registry, including retirements deferred by in-flight work.
type GatewayDevshard struct {
	ID                string `json:"id"`
	Model             string `json:"model"`
	Active            bool   `json:"active"`
	SettlementPending bool   `json:"settlement_pending"`
	Resident          bool   `json:"resident"`
	Nonce             uint64 `json:"nonce"`
}

func fetchGatewayDevshards(ctx context.Context, gatewayURL, apiKey string) ([]GatewayDevshard, error) {
	var body struct {
		Devshards []struct {
			ID                string `json:"id"`
			Model             string `json:"model"`
			Active            bool   `json:"active"`
			SettlementPending bool   `json:"settlement_pending"`
			Runtime           *struct {
				Nonce uint64 `json:"nonce"`
			} `json:"runtime"`
		} `json:"devshards"`
	}
	if err := getJSON(ctx, gatewayURL+"/v1/admin/devshards", apiKey, &body); err != nil {
		return nil, err
	}
	out := make([]GatewayDevshard, 0, len(body.Devshards))
	for _, d := range body.Devshards {
		row := GatewayDevshard{ID: d.ID, Model: d.Model, Active: d.Active, SettlementPending: d.SettlementPending, Resident: d.Runtime != nil}
		if d.Runtime != nil {
			row.Nonce = d.Runtime.Nonce
		}
		out = append(out, row)
	}
	return out, nil
}

func residentIDs(devshards []GatewayDevshard) []string {
	var ids []string
	for _, d := range devshards {
		if d.Resident {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

func getJSON(ctx context.Context, url, apiKey string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		reply, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return fmt.Errorf("GET %s returned %s: %s", url, response.Status, strings.TrimSpace(string(reply)))
	}
	return json.NewDecoder(response.Body).Decode(out)
}

// ThrottleSample is one GW-dbg throttle snapshot of a resident runtime, from
// GET /v1/admin/devshards/{id}/participants. The snapshot has no quarantine
// reason; reasons come from the transitions counter in metrics.jsonl.
type ThrottleSample struct {
	At           time.Time             `json:"at"`
	EscrowID     string                `json:"escrow_id,omitempty"`
	Participants []ThrottleParticipant `json:"participants,omitempty"`
	Error        string                `json:"error,omitempty"`
}

type ThrottleParticipant struct {
	ParticipantKey        string `json:"participant_key"`
	Quarantined           bool   `json:"quarantined"`
	ShadowQuarantined     bool   `json:"shadow_quarantined,omitempty"`
	ProbeQuarantined      bool   `json:"probe_quarantined,omitempty"`
	QuarantineMode        string `json:"quarantine_mode,omitempty"`
	QuarantineUntil       string `json:"quarantine_until,omitempty"`
	QuarantineRemainingMS int64  `json:"quarantine_remaining_ms,omitempty"`
	FailureStrikes        int    `json:"failure_strikes,omitempty"`
	Blocked               bool   `json:"blocked"`
}

func (p ThrottleParticipant) inQuarantine() bool {
	return p.Quarantined || p.ShadowQuarantined || p.ProbeQuarantined
}

func collectThrottle(gatewayURL, apiKey string) func(context.Context, string) []ThrottleSample {
	return func(ctx context.Context, _ string) []ThrottleSample {
		now := time.Now().UTC()
		devshards, err := fetchGatewayDevshards(ctx, gatewayURL, apiKey)
		if err != nil {
			return []ThrottleSample{{At: now, Error: err.Error()}}
		}
		var samples []ThrottleSample
		for _, id := range residentIDs(devshards) {
			sample := ThrottleSample{At: time.Now().UTC(), EscrowID: id}
			var body struct {
				Participants []ThrottleParticipant `json:"participants"`
			}
			if err := getJSON(ctx, gatewayURL+"/v1/admin/devshards/"+id+"/participants", apiKey, &body); err != nil {
				sample.Error = err.Error()
			}
			sample.Participants = body.Participants
			samples = append(samples, sample)
		}
		return samples
	}
}

// PendingSample is the GW-dbg pending view of one resident runtime.
// /v1/debug/pending lists queued txs without timestamps, so the age comes from
// /v1/debug/inferences: started_at (unix seconds) of pending/started records.
type PendingSample struct {
	At              time.Time `json:"at"`
	EscrowID        string    `json:"escrow_id,omitempty"`
	Active          bool      `json:"active"`
	Nonce           uint64    `json:"nonce"`
	PendingTxs      int       `json:"pending_txs"`
	Open            int       `json:"open_inferences"`
	MaxAgeSeconds   float64   `json:"max_age_seconds"`
	OldestInference string    `json:"oldest_inference,omitempty"`
	OldestStatus    string    `json:"oldest_status,omitempty"`
	// OpenStarted maps each open nonce to its started_at, so no-nonce-left-pending can leave burned nonces out.
	OpenStarted map[string]int64 `json:"open_started,omitempty"`
	Error       string           `json:"error,omitempty"`
}

// DebugInference is the subset of a /v1/debug/inferences record the
// invariants read.
type DebugInference struct {
	Status       string `json:"status"`
	ExecutorSlot uint32 `json:"executor_slot"`
	Model        string `json:"model"`
	ActualCost   uint64 `json:"actual_cost"`
	ReservedCost uint64 `json:"reserved_cost"`
	StartedAt    int64  `json:"started_at"`
}

func fetchDebugInferences(ctx context.Context, runtimeURL, apiKey string) (map[string]DebugInference, error) {
	var body struct {
		Inferences json.RawMessage `json:"inferences"`
	}
	if err := getJSON(ctx, runtimeURL+"/v1/debug/inferences", apiKey, &body); err != nil {
		return nil, err
	}
	return decodeInferenceRecords(body.Inferences)
}

// collectPending samples the pending view of each resident escrow and hands seen every inference list it reads.
func collectPending(gatewayURL, apiKey string, driver config.GatewayDriver, seen func(escrowID string, inferences map[string]DebugInference)) func(context.Context, string) []PendingSample {
	return func(ctx context.Context, _ string) []PendingSample {
		devshards, err := fetchGatewayDevshards(ctx, gatewayURL, apiKey)
		if err != nil {
			return []PendingSample{{At: time.Now().UTC(), Error: err.Error()}}
		}
		var samples []PendingSample
		for _, d := range devshards {
			if !d.Resident {
				continue
			}
			base := scopedGatewayURL(gatewayURL, d.ID)
			sample := PendingSample{At: time.Now().UTC(), EscrowID: d.ID, Active: d.Active}
			var errs []string
			var err error
			if sample.Nonce, sample.PendingTxs, err = fetchPendingView(ctx, base, apiKey, driver); err != nil {
				errs = append(errs, err.Error())
			}
			inferences, err := fetchDebugInferences(ctx, base, apiKey)
			if err != nil {
				errs = append(errs, err.Error())
			} else if seen != nil {
				seen(d.ID, inferences)
			}
			summarizePending(&sample, inferences)
			sample.Error = strings.Join(errs, "; ")
			samples = append(samples, sample)
		}
		return samples
	}
}

func summarizePending(sample *PendingSample, inferences map[string]DebugInference) {
	for id, inference := range inferences {
		if inference.Status != "pending" && inference.Status != "started" {
			continue
		}
		sample.Open++
		if inference.StartedAt <= 0 {
			continue
		}
		if sample.OpenStarted == nil {
			sample.OpenStarted = map[string]int64{}
		}
		sample.OpenStarted[id] = inference.StartedAt
		age := sample.At.Sub(time.Unix(inference.StartedAt, 0)).Seconds()
		if age > sample.MaxAgeSeconds {
			sample.MaxAgeSeconds, sample.OldestInference, sample.OldestStatus = age, id, inference.Status
		}
	}
}

// HostStatsSample is one Host-stats read through docker exec. Kind is
// "shards" (GET /stats/shards), "shard" (/stats/shards/:id) or "rpc"
// (/stats/rpc, merged by versiond across children).
type HostStatsSample struct {
	At            time.Time                `json:"at"`
	Host          string                   `json:"host"`
	Participant   string                   `json:"participant"`
	Kind          string                   `json:"kind"`
	EscrowID      string                   `json:"escrow_id,omitempty"`
	Nonce         uint64                   `json:"nonce,omitempty"`
	StateRoot     string                   `json:"state_root,omitempty"`
	HostStats     map[string]HostSlotStats `json:"host_stats,omitempty"`
	Group         []HostSlot               `json:"group,omitempty"`
	ActiveEscrows []string                 `json:"active_escrows,omitempty"`
	RPCRequests   uint64                   `json:"rpc_requests,omitempty"`
	RPCBanned     uint64                   `json:"rpc_banned,omitempty"`
	RPCZoneBanned map[string]uint64        `json:"rpc_zone_banned,omitempty"`
	Error         string                   `json:"error,omitempty"`
}

type HostSlotStats struct {
	Missed  uint32 `json:"missed"`
	Invalid uint32 `json:"invalid"`
	Cost    uint64 `json:"cost"`
}

// HostSlot mirrors types.SlotAssignment, which has no JSON tags.
type HostSlot struct {
	SlotID           uint32 `json:"SlotID"`
	ValidatorAddress string `json:"ValidatorAddress"`
}

// hostStatsProbe prints one "<key>\t<body>" line per route. The stats routes
// are versionless through the versiond proxy, unlike /<version>/metrics.
const hostStatsProbe = `base="$1"; shift
for key in shards rpc "$@"; do
  case "$key" in
    shards) path=/stats/shards ;;
    rpc) path=/stats/rpc ;;
    *) path="/stats/shards/$key" ;;
  esac
  body=$(wget -q -T 3 -O - "$base$path" 2>/dev/null) || body=""
  printf '%s\t%s\n' "$key" "$body"
done`

func dockerHostStats(ctx context.Context, dir, project, compose, service string, escrowIDs []string) (string, error) {
	args := append([]string{"compose", "-p", project, "-f", compose, "exec", "-T", service, "sh", "-c", hostStatsProbe, "probe", fmt.Sprintf("http://127.0.0.1:%d", config.DefaultHostPort)}, escrowIDs...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	return probeCommandOutput(cmd, service)
}

func parseHostStatsProbe(output string, at time.Time, host, participant string) []HostStatsSample {
	var samples []HostStatsSample
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for scanner.Scan() {
		key, body, found := strings.Cut(scanner.Text(), "\t")
		if !found {
			continue
		}
		sample := HostStatsSample{At: at, Host: host, Participant: participant}
		if body == "" {
			sample.Kind, sample.Error = "shard", "empty response"
			if key == "shards" || key == "rpc" {
				sample.Kind = key
			} else {
				sample.EscrowID = key
			}
			samples = append(samples, sample)
			continue
		}
		var err error
		switch key {
		case "shards":
			sample.Kind = "shards"
			var shards struct {
				ActiveEscrows []string `json:"active_escrows"`
			}
			err = json.Unmarshal([]byte(body), &shards)
			sample.ActiveEscrows = shards.ActiveEscrows
		case "rpc":
			sample.Kind = "rpc"
			var rpc struct {
				Host struct {
					Requests uint64 `json:"requests"`
					Banned   uint64 `json:"banned"`
					Zones    []struct {
						Zone   string `json:"zone"`
						Banned uint64 `json:"banned"`
					} `json:"zones"`
				} `json:"host"`
			}
			err = json.Unmarshal([]byte(body), &rpc)
			sample.RPCRequests, sample.RPCBanned = rpc.Host.Requests, rpc.Host.Banned
			for _, zone := range rpc.Host.Zones {
				if zone.Banned > 0 {
					if sample.RPCZoneBanned == nil {
						sample.RPCZoneBanned = map[string]uint64{}
					}
					sample.RPCZoneBanned[zone.Zone] += zone.Banned
				}
			}
		default:
			sample.Kind, sample.EscrowID = "shard", key
			var detail struct {
				Nonce     uint64                   `json:"nonce"`
				StateRoot string                   `json:"state_root_and_protocol_version"`
				HostStats map[string]HostSlotStats `json:"host_stats"`
				Group     []HostSlot               `json:"group"`
			}
			err = json.Unmarshal([]byte(body), &detail)
			sample.Nonce, sample.StateRoot, sample.HostStats, sample.Group = detail.Nonce, detail.StateRoot, detail.HostStats, detail.Group
		}
		if err != nil {
			sample.Error = err.Error()
		}
		samples = append(samples, sample)
	}
	return samples
}

func collectHostStats(opts RunnerConfig, cfg *config.File, project, compose, gatewayURL, apiKey string) func(context.Context, string) []HostStatsSample {
	return func(ctx context.Context, _ string) []HostStatsSample {
		devshards, err := fetchGatewayDevshards(ctx, gatewayURL, apiKey)
		if err != nil {
			return []HostStatsSample{{At: time.Now().UTC(), Kind: "shards", Error: err.Error()}}
		}
		ids := residentIDs(devshards)
		var mu sync.Mutex
		var wg sync.WaitGroup
		var samples []HostStatsSample
		for _, host := range cfg.Hosts {
			wg.Add(1)
			go func(host config.HostCfg) {
				defer wg.Done()
				at := time.Now().UTC()
				participant := participantAddress(cfg, host)
				output, err := dockerHostStats(ctx, opts.TestenvDir, project, compose, host.ID, ids)
				var got []HostStatsSample
				if err != nil {
					got = []HostStatsSample{{At: at, Host: host.ID, Participant: participant, Kind: "shards", Error: err.Error()}}
				} else {
					got = parseHostStatsProbe(output, at, host.ID, participant)
				}
				mu.Lock()
				samples = append(samples, got...)
				mu.Unlock()
			}(host)
		}
		wg.Wait()
		sort.SliceStable(samples, func(i, j int) bool { return samples[i].Host+samples[i].EscrowID < samples[j].Host+samples[j].EscrowID })
		return samples
	}
}

// AccountingSnapshot is a GW-acct read for all participants, taken at the
// workload start, at every event boundary (start, end + grace), right before
// each epoch advance, and after drain.
type AccountingSnapshot struct {
	At           time.Time               `json:"at"`
	Label        string                  `json:"label"`
	EpochIndex   uint64                  `json:"epoch_index"`
	Participants []accountingParticipant `json:"participants,omitempty"`
	Error        string                  `json:"error,omitempty"`
}

func collectAccounting(statsURL string) func(context.Context, string) []AccountingSnapshot {
	return func(ctx context.Context, label string) []AccountingSnapshot {
		snapshot, err := fetchAccountingSnapshot(ctx, statsURL)
		out := AccountingSnapshot{At: time.Now().UTC(), Label: label, EpochIndex: snapshot.EpochIndex, Participants: snapshot.Participants}
		if err != nil {
			out.Error = err.Error()
		}
		return []AccountingSnapshot{out}
	}
}

// DapiHostSample is one mock-dapi GET /testenv/host-stats read. Counters are
// cumulative; the "unmapped" row counts callers bound to no host.
type DapiHostSample struct {
	At    time.Time              `json:"at"`
	Hosts map[string]DapiHostRow `json:"hosts,omitempty"`
	Error string                 `json:"error,omitempty"`
}

// DapiHostRow mirrors mockdapi.HostStats.
type DapiHostRow struct {
	Challenged          bool              `json:"challenged"`
	Nodes               []string          `json:"nodes,omitempty"`
	Allocations         uint64            `json:"allocations"`
	NodeAllocations     map[string]uint64 `json:"node_allocations,omitempty"`
	ChallengeRejections uint64            `json:"challenge_rejections"`
	AcquireErrors       uint64            `json:"acquire_errors"`
}

const dapiUnmappedHost = "unmapped"

func collectDapiHosts(dapiURL string) func(context.Context, string) []DapiHostSample {
	return func(ctx context.Context, _ string) []DapiHostSample {
		out := DapiHostSample{At: time.Now().UTC()}
		if err := getJSON(ctx, dapiURL+"/testenv/host-stats", "", &out.Hosts); err != nil {
			out.Error = err.Error()
		}
		return []DapiHostSample{out}
	}
}

// accountingBoundaries lists snapshot times for planned events. Real event
// times can drift from the plan by a command's duration; the evaluators
// compare snapshots against the real windows in events.json.
func accountingBoundaries(started time.Time, events []plannedEvent, grace time.Duration) []labeledTime {
	var times []labeledTime
	for _, event := range events {
		times = append(times, labeledTime{At: started.Add(event.window.start), Label: event.ID + ":start"})
		times = append(times, labeledTime{At: started.Add(event.window.end + grace), Label: event.ID + ":end+grace"})
		if event.Action == "epoch_advance" {
			times = append(times, labeledTime{At: started.Add(event.window.start - time.Second), Label: event.ID + ":pre_epoch"})
		}
	}
	return times
}

// LeaseCount is one row of the no-skipped-validation-leases Postgres query.
type LeaseCount struct {
	InstanceAddress string `json:"instance_address"`
	Status          string `json:"status"`
	Count           int    `json:"count"`
}

const leaseCountQuery = "SELECT instance_address, status, count(*) FROM devshard_validation_leases GROUP BY 1, 2 ORDER BY 1, 2"

// queryLeaseCounts reads the lease table of every Postgres service of the stack
// (one per participant with per_participant storage) and returns the rows it
// could read, with an error naming each service it could not. A service whose
// hosts are all in excluded (e.g. sqlite hosts) is not probed.
func queryLeaseCounts(ctx context.Context, dir, project, compose string, cfg *config.File, excluded map[string]string) ([]LeaseCount, error) {
	services := config.PostgresServices(cfg)
	if len(services) == 0 {
		return nil, fmt.Errorf("no Postgres service in the stack")
	}
	// A service shared with an included host (an HA replica) is still probed.
	included := map[string]bool{}
	for _, host := range cfg.Hosts {
		if _, skip := excluded[host.ID]; !skip {
			included[config.PostgresServiceFor(cfg, host)] = true
		}
	}
	rows := []LeaseCount{}
	var failures []string
	for _, service := range services {
		if !included[service] {
			continue
		}
		cmd := exec.CommandContext(ctx, "docker", "compose", "-p", project, "-f", compose, "exec", "-T", service,
			"psql", "-U", cfg.Postgres.User, "-d", cfg.Postgres.Database, "-tA", "-F", "\t", "-c", leaseCountQuery)
		cmd.Dir = dir
		output, err := probeCommandOutput(cmd, service)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		parsed, err := parseLeaseCounts(output)
		if err != nil {
			failures = append(failures, service+": "+err.Error())
			continue
		}
		rows = append(rows, parsed...)
	}
	if len(failures) > 0 {
		return rows, errors.New(strings.Join(failures, "; "))
	}
	return rows, nil
}

func parseLeaseCounts(output string) ([]LeaseCount, error) {
	var rows []LeaseCount
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			return nil, fmt.Errorf("unexpected psql row %q", line)
		}
		count, err := strconv.Atoi(strings.TrimSpace(fields[2]))
		if err != nil {
			return nil, fmt.Errorf("psql row %q: %w", line, err)
		}
		rows = append(rows, LeaseCount{InstanceAddress: fields[0], Status: fields[1], Count: count})
	}
	return rows, nil
}

// fetchChainLedger reads the mock-chain settle txs and escrow table.
func fetchChainLedger(ctx context.Context, chainURL string) ([]ledgerface.TxView, []ledgerface.EscrowView, error) {
	var txs struct {
		Txs []ledgerface.TxView `json:"txs"`
	}
	if err := getJSON(ctx, chainURL+"/testenv/txs?type=settle", "", &txs); err != nil {
		return nil, nil, err
	}
	var escrows struct {
		Escrows []ledgerface.EscrowView `json:"escrows"`
	}
	if err := getJSON(ctx, chainURL+"/testenv/escrows", "", &escrows); err != nil {
		return nil, nil, err
	}
	return txs.Txs, escrows.Escrows, nil
}

// countLogLines counts lines containing needle; responses-compress reads compose.log with it.
func countLogLines(path, needle string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)
	count := 0
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), needle) {
			count++
		}
	}
	return count, scanner.Err()
}

// escrowMissingLine is devshardctl's startup line for an escrow it marks inactive
// because the chain lookup said NotFound (cmd/devshardctl/main.go).
var escrowMissingLine = regexp.MustCompile(`devshard (\S+) escrow missing on chain, marking inactive`)

// readEscrowMissing returns the escrow IDs of every escrowMissingLine in the log.
func readEscrowMissing(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	ids := []string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if m := escrowMissingLine.FindStringSubmatch(line); m != nil {
			ids = append(ids, m[1])
		} else if id, ok := gatewayEscrowGone(line); ok {
			ids = append(ids, id)
		}
	}
	return ids, scanner.Err()
}
