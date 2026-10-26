package loadtest

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
)

// promisesHoldPoll is how often the hold reads the Gateway's open inferences, and promisesChainPoll
// how often it reads the hosts' chains.
const (
	promisesHoldPoll  = 5 * time.Second
	promisesChainPoll = 30 * time.Second
)

// openInference is an inference the Gateway still holds Pending or Started, with the time the
// deadline checks call it late if it is still open then.
type openInference struct {
	Escrow string
	ID     string
	Due    time.Time
}

// holdStep is the hold's decision at one read: wait until Wake, or stop for Reason.
type holdStep struct {
	Wait    bool
	Reason  string
	Open    int       // open inferences still within their deadline
	NextDue time.Time // the earliest of their deadlines
	LastDue time.Time // the latest
	Wake    time.Time
}

// decideHold decides the hold at now from the inferences the Gateway holds open. It stops when none
// is open, when every open one is past its deadline (the checks judge it as it stands), or at limit;
// otherwise it reads again after poll, or at limit if that comes first.
func decideHold(open []openInference, now, limit time.Time, poll time.Duration) holdStep {
	var step holdStep
	for _, o := range open {
		if now.After(o.Due) {
			continue
		}
		step.Open++
		if step.NextDue.IsZero() || o.Due.Before(step.NextDue) {
			step.NextDue = o.Due
		}
		if o.Due.After(step.LastDue) {
			step.LastDue = o.Due
		}
	}
	switch {
	case len(open) == 0:
		step.Reason = "no inference open"
	case step.Open == 0:
		step.Reason = "every open inference past its deadline"
	case !now.Before(limit):
		step.Reason = "limit reached"
	default:
		step.Wait, step.Wake = true, now.Add(poll)
		if step.Wake.After(limit) {
			step.Wake = limit
		}
	}
	return step
}

// openInferences picks from one read of the Gateway the inferences still Pending or Started, each
// due as the deadline checks of citest/harness reckon it: refusal_timeout from its start while
// Pending, execution_timeout from its confirmation while Started, plus the Gateway's grace, one
// timeout sweep and the checks' slack. Burned nonces and served warm-up probes are left out: they stay
// open until settlement, and the checks leave them to it. An escrow whose timeouts are unknown is left
// out too; holdForPromises reads them first.
func openInferences(snapshot orphanSnapshot, timeouts map[string]harness.EscrowTimeouts, judge orphanJudge) []openInference {
	grace := harness.TimeoutSkew + harness.TimeoutVoteWindow + harness.TimeoutSweepPeriod + harness.DeadlineSlack
	var open []openInference
	for _, runtime := range snapshot.Runtimes {
		t, ok := timeouts[runtime.EscrowID]
		if !ok {
			continue
		}
		ghosts := judge.Ghosts.escrow(runtime.EscrowID)
		for id, inference := range runtime.Inferences {
			if _, ghost := ghosts[id]; ghost || judge.WarmUps.has(runtime.EscrowID, id) {
				continue
			}
			from, limit := inference.StartedAt, time.Duration(t.Refusal)*time.Second
			switch inference.Status {
			case "pending":
			case "started":
				limit = time.Duration(t.Execution) * time.Second
				if inference.ConfirmedAt != 0 {
					from = inference.ConfirmedAt
				}
			default:
				continue
			}
			open = append(open, openInference{Escrow: runtime.EscrowID, ID: id, Due: time.Unix(from, 0).Add(limit + grace)})
		}
	}
	return open
}

// sessionTimeouts is the part of GET /v1/state the hold reads: the escrow's session timeouts.
type sessionTimeouts struct {
	Session struct {
		Config struct {
			RefusalTimeout   int64 `json:"refusal_timeout"`
			ExecutionTimeout int64 `json:"execution_timeout"`
		} `json:"config"`
	} `json:"session"`
}

// readOpenInferences reads every runtime's inferences the way the orphan check does, and each
// escrow's timeouts from its session config once.
func readOpenInferences(ctx context.Context, client *http.Client, gatewayURL, apiKey string, aggregate bool, judge orphanJudge, timeouts map[string]harness.EscrowTimeouts) ([]openInference, error) {
	snapshot, err := fetchOrphanSnapshot(ctx, client, gatewayURL, apiKey, aggregate, judge.Headers)
	if err != nil {
		return nil, err
	}
	for _, runtime := range snapshot.Runtimes {
		if _, ok := timeouts[runtime.EscrowID]; ok {
			continue
		}
		url := gatewayURL
		if runtime.EscrowID != "" {
			url = scopedGatewayURL(gatewayURL, runtime.EscrowID)
		}
		var state sessionTimeouts
		if err := getJSON(ctx, url+"/v1/state", apiKey, &state); err != nil {
			return nil, err
		}
		timeouts[runtime.EscrowID] = harness.EscrowTimeouts{Refusal: state.Session.Config.RefusalTimeout, Execution: state.Session.Config.ExecutionTimeout}
	}
	return openInferences(snapshot, timeouts, judge), nil
}

// chainReader reads the hosts' chains as the deadline checks read them from the export: the diffs,
// the escrows' timeouts and when the read began.
type chainReader func(ctx context.Context) (harness.PromiseInput, error)

// chainProbeBytes bounds one table read of the chain probe, as the export bounds a table dump.
const chainProbeBytes = 512 << 20

// chainProbe reads, while the hold lasts, what the deadline checks read of the hosts' chains in the
// export: the diffs, sessions and escrow cache of every Postgres database, and the SQLite files of a
// host that keeps its chain in them. It lays them out under dir as the export does, so
// harness.ReadExportDiffs and harness.ReadEscrowTimeouts read them as they read the export. A host only
// adds diffs (ON CONFLICT DO NOTHING), so after the first read the probe asks a database only for the
// diffs past the nonces it holds without a gap.
type chainProbe struct {
	cfg    *config.File
	stack  stackAccess
	dir    string
	held   map[string]map[string]map[uint64]bool // database, escrow, nonce
	sqlite []string                              // hosts that keep their chain in SQLite files
	modes  bool
	// headers dates the timeouts' anchor heights; without it no deadline is ahead.
	headers *headerClock
}

func (p *chainProbe) read(ctx context.Context) (harness.PromiseInput, error) {
	at := time.Now()
	for _, service := range config.PostgresServices(p.cfg) {
		if err := ctx.Err(); err != nil {
			return harness.PromiseInput{}, err
		}
		if err := p.readPostgres(service); err != nil {
			return harness.PromiseInput{}, fmt.Errorf("chain probe %s: %w", service, err)
		}
	}
	if !p.modes {
		for _, h := range p.cfg.Hosts {
			mode, err := p.stack.storageMode(h.ID)
			if err != nil {
				return harness.PromiseInput{}, fmt.Errorf("chain probe %s: storage mode: %w", h.ID, err)
			}
			if mode == "sqlite" || mode == "hybrid" {
				p.sqlite = append(p.sqlite, h.ID)
			}
		}
		p.modes = true
	}
	for _, host := range p.sqlite {
		if err := p.readSQLite(host); err != nil {
			return harness.PromiseInput{}, fmt.Errorf("chain probe %s: %w", host, err)
		}
	}
	diffs, err := harness.ReadExportDiffs(p.dir)
	if err != nil {
		return harness.PromiseInput{}, err
	}
	diffs.DateAnchors(p.headers.times(ctx))
	timeouts, err := harness.ReadEscrowTimeouts(p.dir)
	if err != nil {
		return harness.PromiseInput{}, err
	}
	return harness.PromiseInput{Diffs: diffs, Timeouts: timeouts, ObservedUntil: at, Slack: harness.DeadlineSlack}, nil
}

// readPostgres reads one database: its sessions and escrow cache whole, and its new diffs.
func (p *chainProbe) readPostgres(service string) error {
	prefix := ""
	if p.cfg.Postgres.PerParticipant {
		prefix = "db-" + service + "/"
	}
	dir := filepath.Join(p.dir, prefix+"postgres")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	run := p.stack.psql(service, p.cfg.Postgres.Database)
	listing, err := run(`SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace `+
		`WHERE n.nspname = 'public' AND c.relkind IN ('r','p') AND NOT c.relispartition `+
		`AND c.relname IN ('devshard_diffs','devshard_sessions','devshard_escrow_cache') ORDER BY 1`, chainProbeBytes)
	if err != nil {
		return err
	}
	tables := strings.Fields(listing)
	for _, table := range tables {
		if table == "devshard_diffs" {
			continue
		}
		dump, err := run(fmt.Sprintf("COPY (SELECT * FROM public.%s ORDER BY 1) TO STDOUT WITH CSV HEADER", table), chainProbeBytes)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "public."+table+".csv"), []byte(dump), 0o644); err != nil {
			return err
		}
	}
	if !slices.Contains(tables, "devshard_diffs") {
		return nil
	}
	if p.held == nil {
		p.held = map[string]map[string]map[uint64]bool{}
	}
	held := p.held[service]
	if held == nil {
		held = map[string]map[uint64]bool{}
		p.held[service] = held
	}
	dump, err := run(newDiffsQuery(held), chainProbeBytes)
	if err != nil {
		return err
	}
	rows, err := csv.NewReader(strings.NewReader(dump)).ReadAll()
	if err != nil {
		return fmt.Errorf("diffs: %w", err)
	}
	path := filepath.Join(dir, "public.devshard_diffs.csv")
	var out bytes.Buffer
	w := csv.NewWriter(&out)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_ = w.Write([]string{"escrow_id", "nonce", "txs_proto", "post_state_root"})
	}
	for i, r := range rows {
		if i == 0 || len(r) != 4 {
			continue // the header
		}
		nonce, err := strconv.ParseUint(r[1], 10, 64)
		if err != nil {
			return fmt.Errorf("diffs nonce %q: %w", r[1], err)
		}
		if held[r[0]] == nil {
			held[r[0]] = map[uint64]bool{}
		}
		if held[r[0]][nonce] {
			continue
		}
		held[r[0]][nonce] = true
		_ = w.Write(r)
	}
	w.Flush()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(out.Bytes()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// newDiffsQuery asks for the diffs past the nonces held without a gap, per escrow, and every diff of
// an escrow not held yet.
func newDiffsQuery(held map[string]map[uint64]bool) string {
	const cols = "d.escrow_id, d.nonce, d.txs_proto, d.post_state_root"
	if len(held) == 0 {
		return "COPY (SELECT " + cols + " FROM public.devshard_diffs d ORDER BY 1, 2) TO STDOUT WITH CSV HEADER"
	}
	escrows := make([]string, 0, len(held))
	for e := range held {
		escrows = append(escrows, e)
	}
	sort.Strings(escrows)
	values := make([]string, 0, len(escrows))
	for _, e := range escrows {
		var top uint64
		for held[e][top+1] {
			top++
		}
		values = append(values, fmt.Sprintf("('%s', %d)", strings.ReplaceAll(e, "'", "''"), top))
	}
	return "COPY (SELECT " + cols + " FROM public.devshard_diffs d LEFT JOIN (VALUES " + strings.Join(values, ", ") +
		") AS held(escrow_id, nonce) ON held.escrow_id = d.escrow_id WHERE held.nonce IS NULL OR d.nonce > held.nonce ORDER BY 1, 2) TO STDOUT WITH CSV HEADER"
}

// readSQLite copies a host's SQLite files again, as the export copies them.
func (p *chainProbe) readSQLite(host string) error {
	listing, err := p.stack.find(host, versiondDataDir)
	if err != nil {
		return err
	}
	root := filepath.Join(p.dir, "sqlite", host)
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
		src := strings.TrimSpace(line)
		if src == "" {
			continue
		}
		local := filepath.Join(root, strings.TrimPrefix(src, "/"))
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			return err
		}
		if err := p.stack.copyFrom(host, src, local); err != nil {
			return err
		}
	}
	return nil
}

// holdForPromises waits, before the logs and the export are taken, until an export would judge every
// inference the way an export at the end of the hold would: it reads the Gateway every poll and the
// hosts' chains every chainPoll, and holds while the Gateway holds an inference Pending or Started
// within its deadline, or while an inference not Finished on every host's chain is within its deadline
// on one of them (harness.DeadlinesAhead). Once neither holds any, the deadline checks judge every
// inference: closed by the export, or still open past its deadline. It ends on a fresh read of both,
// or at the latest an inference the load started can be due (PromisesWait from the end of the load).
// An inference of a settled escrow stays open: it holds the export until its deadline, and the checks
// then report the escrow settled with it live. A read that fails is logged and tried again until the
// limit.
func holdForPromises(ctx context.Context, scenario Scenario, workloadEnd time.Time, gatewayURL, apiKey string, judge orphanJudge, chains chainReader, poll, chainPoll time.Duration) error {
	began := time.Now()
	limit := workloadEnd.Add(scenario.PromisesWait())
	log.Printf("loadtest: stage=promises_hold limit=%s", time.Until(limit).Round(time.Second))
	client := &http.Client{Timeout: 5 * time.Second}
	timeouts := map[string]harness.EscrowTimeouts{}
	aggregate := scenario.severalRuntimes()
	var ahead []openInference
	var chainAt time.Time
	var chainErr error
	force, failed := false, ""
	logged, loggedAt := -1, time.Time{}
	for {
		now := time.Now()
		open, err := readOpenInferences(ctx, client, gatewayURL, apiKey, aggregate, judge, timeouts)
		fresh := force || chainAt.IsZero() || now.Sub(chainAt) >= chainPoll
		if fresh {
			ahead, chainAt, chainErr = readChainsAhead(ctx, chains)
		}
		step := holdStepFor(open, err, ahead, chainErr, now, limit, poll)
		if !step.Wait && step.Reason != "limit reached" && !fresh {
			// End only on chains read after the Gateway's read.
			force = true
			continue
		}
		force = false
		if !step.Wait {
			log.Printf("loadtest: stage=promises_hold done reason=%q gateway_open=%d chains_open=%d held=%s", step.Reason, len(open), len(ahead), time.Since(began).Round(time.Second))
			return nil
		}
		if readErr := errors.Join(err, chainErr); readErr != nil {
			if readErr.Error() != failed {
				log.Printf("loadtest: stage=promises_hold read failed: %v", readErr)
				failed = readErr.Error()
			}
		} else if step.Open != logged || now.Sub(loggedAt) >= time.Minute {
			log.Printf("loadtest: stage=promises_hold open=%d gateway_open=%d chains_open=%d next_due=%s last_due=%s limit=%s",
				step.Open, len(open), len(ahead), step.NextDue.Sub(now).Round(time.Second), step.LastDue.Sub(now).Round(time.Second), limit.Sub(now).Round(time.Second))
			logged, loggedAt, failed = step.Open, now, ""
		}
		select {
		case <-time.After(time.Until(step.Wake)):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// readChainsAhead reads the hosts' chains and names what a later export could judge otherwise.
func readChainsAhead(ctx context.Context, chains chainReader) ([]openInference, time.Time, error) {
	at := time.Now()
	in, err := chains(ctx)
	if err != nil {
		return nil, at, err
	}
	var out []openInference
	for _, d := range harness.DeadlinesAhead(in) {
		out = append(out, openInference{Escrow: d.Escrow, ID: strconv.FormatUint(d.Inference, 10), Due: d.Due})
	}
	return out, at, nil
}

// holdStepFor decides the hold on the last reads of the Gateway and of the chains: after a failed
// read nothing can be told, so the hold goes on until the limit.
func holdStepFor(open []openInference, err error, ahead []openInference, chainErr error, now, limit time.Time, poll time.Duration) holdStep {
	if err == nil && chainErr == nil {
		return decideHold(append(append([]openInference(nil), open...), ahead...), now, limit, poll)
	}
	if !now.Before(limit) {
		return holdStep{Reason: "limit reached"}
	}
	step := holdStep{Wait: true, Wake: now.Add(poll)}
	if step.Wake.After(limit) {
		step.Wake = limit
	}
	return step
}
