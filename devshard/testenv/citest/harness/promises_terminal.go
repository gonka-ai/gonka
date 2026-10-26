package harness

// The deadline checks: every started inference leaves Pending/Started in time on every host, and a
// request the client got a 200 for has a Finish on every host.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"devshard"
	"devshard/state"
)

// Grace the gateway takes past an inference's deadline before its timeout sweep votes it out
// (cmd/devshardctl/execution_timeout_sweep.go). The sweep votes once the timeout has passed on the
// block time of the height-sync floor, which trails the wall clock: TimeoutVoteWindow bounds that lag
// and the collection of the votes. TimeoutSkew is user.TimeoutBuffer. TimeoutSweepPeriod is how often
// the sweep runs (balanceCheckInterval); a pass handles at most 8 nonces.
const (
	TimeoutSkew        = 5 * time.Second
	TimeoutVoteWindow  = 120 * time.Second
	TimeoutSweepPeriod = 30 * time.Second
)

// DeadlineSlack is added to every deadline for the timeout votes to reach a diff.
const DeadlineSlack = time.Minute

// ExportTime is when the export was taken: started_at of EXPORT-MANIFEST.json, when the first
// database was about to be read, else its exported_at, else the manifest's modification time.
func ExportTime(exportDir string) (time.Time, error) {
	path := filepath.Join(exportDir, "EXPORT-MANIFEST.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, err
	}
	var m struct {
		StartedAt  time.Time `json:"started_at"`
		ExportedAt time.Time `json:"exported_at"`
	}
	if json.Unmarshal(raw, &m) == nil {
		if !m.StartedAt.IsZero() {
			return m.StartedAt, nil
		}
		if !m.ExportedAt.IsZero() {
			return m.ExportedAt, nil
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return st.ModTime(), nil
}

// EscrowTimeouts are the escrow's deadlines in seconds, as the escrow cache recorded them.
type EscrowTimeouts struct {
	Refusal   int64 `json:"refusal_timeout"`
	Execution int64 `json:"execution_timeout"`
}

// ReadEscrowTimeouts reads refusal_timeout and execution_timeout of each escrow from the export's
// databases (the primary one under postgres/, each participant's under db-*/postgres/ and each SQLite
// host's sessions): the session config the host runs with (config_json), else the escrow cache
// (escrow_json), which some exports leave empty. A row that does not parse leaves its escrow out, and
// the deadline checks count that escrow's inferences as not judged.
func ReadEscrowTimeouts(exportDir string) (map[string]EscrowTimeouts, error) {
	out := map[string]EscrowTimeouts{}
	sources := []struct{ file, column, refusal, execution string }{
		{"public.devshard_sessions.csv", "config_json", "RefusalTimeout", "ExecutionTimeout"},
		{"public.devshard_escrow_cache.csv", "escrow_json", "refusal_timeout", "execution_timeout"},
	}
	dbs, _ := filepath.Glob(filepath.Join(exportDir, "db-*", "postgres"))
	dbs = append([]string{filepath.Join(exportDir, "postgres")}, dbs...)
	for _, src := range sources {
		for _, db := range dbs {
			if err := readTimeouts(filepath.Join(db, src.file), src.column, src.refusal, src.execution, out); err != nil {
				return nil, err
			}
		}
	}
	for _, p := range sqliteFiles(exportDir) {
		db, err := sql.Open("sqlite", "file:"+p+"?mode=ro")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		var rows []map[string]string
		if q, err := db.Query(`SELECT escrow_id, config_json FROM sessions`); err == nil {
			for q.Next() {
				var escrow, cfg string
				if q.Scan(&escrow, &cfg) == nil {
					rows = append(rows, map[string]string{"escrow_id": escrow, "config_json": cfg})
				}
			}
			q.Close()
		}
		db.Close()
		addTimeouts(rows, "config_json", "RefusalTimeout", "ExecutionTimeout", out)
	}
	return out, nil
}

// readTimeouts adds the escrows of one table that out does not hold yet.
func readTimeouts(path, column, refusalKey, executionKey string, out map[string]EscrowTimeouts) error {
	rows, err := readCSV(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	addTimeouts(rows, column, refusalKey, executionKey, out)
	return nil
}

func addTimeouts(rows []map[string]string, column, refusalKey, executionKey string, out map[string]EscrowTimeouts) {
	for _, r := range rows {
		if _, done := out[r["escrow_id"]]; done || r[column] == "" {
			continue
		}
		var doc any
		if json.Unmarshal([]byte(r[column]), &doc) != nil {
			continue
		}
		refusal, okR := findNumber(doc, refusalKey)
		execution, okE := findNumber(doc, executionKey)
		if okR && okE {
			out[r["escrow_id"]] = EscrowTimeouts{Refusal: refusal, Execution: execution}
		}
	}
}

func findNumber(doc any, key string) (int64, bool) {
	switch v := doc.(type) {
	case map[string]any:
		if n, ok := v[key].(float64); ok {
			return int64(n), true
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if n, ok := findNumber(v[k], key); ok {
				return n, true
			}
		}
	case []any:
		for _, child := range v {
			if n, ok := findNumber(child, key); ok {
				return n, true
			}
		}
	}
	return 0, false
}

// HostInference names an inference on one host's chain.
type HostInference struct {
	Host string `json:"host"`
	InferenceRef
}

// deadline is when an inference has to be out of Pending/Started on a host's chain: refusal_timeout
// from Start while Pending, execution_timeout from ConfirmStart while Started (each from the block
// time of its anchor; from is 0 when the anchor is not armed or not dated), each plus the grace
// of the timeout sweep and the slack. The sweep gives a Pending inference whose refusal payload was
// kept the same grace from Start (refusalTimeoutSweepWindow); the check holds every Pending one to it.
func deadline(f *InferenceFacts, t EscrowTimeouts, slack time.Duration) (state string, from int64, limit time.Duration, at time.Time) {
	state, from, limit = "Pending", f.StartedAt, time.Duration(t.Refusal)*time.Second
	if f.Confirmed {
		state, from, limit = "Started", f.ConfirmedAt, time.Duration(t.Execution)*time.Second
	}
	limit += TimeoutSkew + TimeoutVoteWindow + TimeoutSweepPeriod
	return state, from, limit, time.Unix(from, 0).Add(limit + slack)
}

// settledBy says whether the inferences were due by the export on every host judged for them, so the
// hosts had all the time the protocol grants to finish them and to converge. An export taken while
// the last inferences were still within their deadline shows chains in flight, not a defect; an
// escrow whose timeouts are unknown cannot be called settled (known is false then).
func settledBy(in PromiseInput, lag []map[string]uint64, refs []InferenceRef) (settled, known bool) {
	for i, chain := range in.Diffs.Hosts {
		for _, ref := range refs {
			f := chain.Inferences[ref]
			if f == nil || !f.Started || lag[i][ref.Escrow] > 0 {
				continue
			}
			t, ok := in.Timeouts[ref.Escrow]
			if !ok {
				return false, false
			}
			_, from, _, due := deadline(f, t, in.Slack)
			if from == 0 {
				return false, false
			}
			if !in.ObservedUntil.After(due) {
				return false, true
			}
		}
	}
	return true, true
}

// behindTip says, per host and escrow, by how many diffs the host's chain trails the escrow's longest
// one when it holds a matching prefix of it. The gateway sends each diff to one host and the others
// catch up on their next request, so after the load a host can be a few diffs short with no fault of
// its own: such a host is judged on the longest chain, which holds the same inferences further on,
// instead of on its own facts. A host whose roots diverge is judged on its own chain.
func behindTip(hosts []HostChain) []map[string]uint64 {
	top := func(roots map[uint64]string) uint64 {
		var n uint64
		for nonce := range roots {
			n = max(n, nonce)
		}
		return n
	}
	tip := map[string]int{}
	for i, h := range hosts {
		for escrow, roots := range h.Roots {
			if j, ok := tip[escrow]; len(roots) > 0 && (!ok || top(roots) > top(hosts[j].Roots[escrow])) {
				tip[escrow] = i
			}
		}
	}
	out := make([]map[string]uint64, len(hosts))
	for i, h := range hosts {
		out[i] = map[string]uint64{}
		for escrow, roots := range h.Roots {
			j, ok := tip[escrow]
			if !ok || j == i || len(roots) == 0 {
				continue
			}
			tipRoots := hosts[j].Roots[escrow]
			lag := top(tipRoots) - top(roots)
			if lag == 0 {
				continue
			}
			prefix := true
			for nonce, root := range roots {
				if tipRoots[nonce] != root {
					prefix = false
					break
				}
			}
			if prefix {
				out[i][escrow] = lag
			}
		}
	}
	return out
}

// lagObservations names each host judged on the longest chain instead of its own.
func lagObservations(hosts []HostChain, lag []map[string]uint64) []string {
	var out []string
	for i, h := range hosts {
		escrows := make([]string, 0, len(lag[i]))
		for e := range lag[i] {
			escrows = append(escrows, e)
		}
		sort.Strings(escrows)
		for _, e := range escrows {
			out = append(out, fmt.Sprintf("host %s behind the tip of escrow %s by %d diffs at the export; judged on the tip's chain", h.Host, e, lag[i][e]))
		}
	}
	return out
}

// ghostProbePromptHash is the prompt hash of the gateway's synthetic probe (pocProbePromptBody of
// cmd/devshardctl/poc_mode.go, which reserves state.MinReservationTokens). The session picker burns a nonce it cannot give a user request as a
// ghost: the MsgStart carries the probe and is never sent, so no host confirms it and no vote times it
// out; it stays Pending until settlement drains it (settleLiveRecordLocked in state/machine.go).
var ghostProbePromptHash = func() []byte {
	h, err := devshard.CanonicalPromptHash([]byte(fmt.Sprintf(`{"messages":[{"role":"user","content":"."}],"max_tokens":%d}`, uint64(state.MinReservationTokens))))
	if err != nil {
		panic(err)
	}
	return h
}()

// isGhost says whether an inference is a burned nonce: the probe prompt, never confirmed. Warm-up
// probes carry the same prompt; one a host refused is resolved without a kept payload and is left to
// settlement too, and one a host served has no request after it to carry its receipt into a diff.
func isGhost(f *InferenceFacts) bool {
	return !f.Confirmed && bytes.Equal(f.PromptHash, ghostProbePromptHash)
}

func checkTerminal(in PromiseInput, rep *PromiseReport) {
	if in.ObservedUntil.IsZero() {
		rep.Checked[InferenceClosesInTime] = notMeasured("export time unknown")
		return
	}
	// The check can fire only for an inference whose deadline passed before the export: those are
	// the judged ones, closed or not. A host only behind the longest chain is judged on that chain.
	lag := behindTip(in.Diffs.Hosts)
	judged, open, late, ghosts, unknown, unanchored := 0, 0, 0, 0, 0, 0
	for i, chain := range in.Diffs.Hosts {
		for _, ref := range sortedRefs(chain.Inferences) {
			f := chain.Inferences[ref]
			if !f.Started || lag[i][ref.Escrow] > 0 {
				continue
			}
			closed := f.Finished || f.TimedOut || f.ErrorMiss
			if isGhost(f) && !closed {
				ghosts++
				continue
			}
			t, ok := in.Timeouts[ref.Escrow]
			if !ok {
				unknown++
				continue
			}
			state, from, limit, due := deadline(f, t, in.Slack)
			if !closed && from == 0 {
				unanchored++
				continue
			}
			if !in.ObservedUntil.After(due) {
				if !closed {
					open++
					rep.Open = append(rep.Open, HostInference{Host: chain.Host, InferenceRef: ref})
				}
				continue
			}
			judged++
			if closed {
				continue
			}
			late++
			r := ref
			detail := fmt.Sprintf("still %s at export, %s past its deadline (%s from %s, slack %s)",
				state, in.ObservedUntil.Sub(due).Round(time.Second), limit,
				time.Unix(from, 0).UTC().Format(time.RFC3339), in.Slack)
			if chain.Status[ref.Escrow] == "settled" {
				detail += "; the escrow settled with it live, settlement charged its full reserve"
			}
			rep.Violations = append(rep.Violations, PromiseViolation{
				Promise: InferenceClosesInTime, Host: chain.Host, Inference: &r, Detail: detail,
			})
		}
	}
	rep.Checked[InferenceClosesInTime] = measured(judged, "%d inferences past their deadline at the export, counted once per host, %d of them still open; %d open within it; %d without known timeouts and %d open with an anchor not armed or not dated, not judged; %d burned nonces (gateway probes not confirmed on the chain) left to settlement",
		judged, late, open, unknown, unanchored, ghosts)
}

func checkServedFinish(in PromiseInput, rep *PromiseReport) {
	if len(in.Requests) == 0 {
		rep.Checked[ServedRequestFinished] = notMeasured("no client request log")
		return
	}
	if len(in.Diffs.Hosts) == 0 || in.ObservedUntil.IsZero() {
		rep.Checked[ServedRequestFinished] = notMeasured("no diff chain or export time unknown")
		return
	}
	lag := behindTip(in.Diffs.Hosts)
	judged, unlinked, recent, incomplete, unknown := 0, 0, 0, 0, 0
	for _, r := range in.Requests {
		if r.HTTPStatus != 200 {
			continue
		}
		if r.Incomplete {
			incomplete++
			continue
		}
		refs := r.Inferences()
		if len(refs) == 0 {
			unlinked++
			continue
		}
		switch settled, known := settledBy(in, lag, refs); {
		case !known:
			unknown++
			continue
		case !settled:
			recent++
			continue
		}
		judged++
		for i, chain := range in.Diffs.Hosts {
			finished, behind := false, true
			for _, ref := range refs {
				if lag[i][ref.Escrow] > 0 {
					continue
				}
				behind = false
				if f := chain.Inferences[ref]; f != nil && f.Finished {
					finished = true
					break
				}
			}
			if !finished && !behind {
				rep.Violations = append(rep.Violations, PromiseViolation{
					Promise: ServedRequestFinished, Host: chain.Host, RequestID: r.RequestID,
					Detail: fmt.Sprintf("client got 200, no Finish on this host for any of %v", refs),
				})
			}
		}
	}
	rep.Checked[ServedRequestFinished] = measured(judged, "%d requests with 200 judged, each on every host's chain (%d hosts); %d still within their deadline, %d without known timeouts, %d without a linked inference, %d with an answer cut short",
		judged, len(in.Diffs.Hosts), recent, unknown, unlinked, incomplete)
}
