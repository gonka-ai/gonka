package harness

// Checks of properties the system has to hold whatever its code does today, read from a finished
// run's export instead of from the code under test. Each check names the property it holds the run
// to; a violation carries the facts it was found from. Every check runs on each host's diff chain on
// its own: merging the chains would let one host's Finish hide another host's stuck inference. A
// check that could not fire on this run says so (not measured) instead of passing.
//
//   paid_output_within_produced  a Finish never reports more output tokens than its Start allowed,
//        nor a count other than the ML produced for that execution.
//   charge_only_the_guilty  a host the ML log shows answering in time and correctly is not charged a
//        miss or an invalid, a request the client spoiled charges no host, and no validator accepts
//        an answer the ML sent with forged logprobs (chaos mode).

import (
	"bufio"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"devshard/testenv/mockopenai"
	"devshard/types"
)

// Names of the checks in the report.
const (
	PaidOutputWithinProduced = "paid_output_within_produced"
	ChargeOnlyTheGuilty      = "charge_only_the_guilty"
)

// ClientErrorStatuses are the ML answers that mean the request itself was wrong.
var ClientErrorStatuses = map[int]bool{400: true, 413: true, 422: true}

// InferenceRef names one inference: inference ids are per escrow.
type InferenceRef struct {
	Escrow    string `json:"escrow_id"`
	Inference uint64 `json:"inference_id"`
}

// InferenceFacts is what one host's diff chain says about one inference.
type InferenceFacts struct {
	StartedAt    int64  `json:"started_at,omitempty"` // unix seconds, the time of StartedHeight
	Model        string `json:"model,omitempty"`      // from MsgStartInference
	MaxTokens    uint64 `json:"max_tokens,omitempty"`
	PromptHash   []byte `json:"prompt_hash,omitempty"`  // from MsgStartInference
	ConfirmedAt  int64  `json:"confirmed_at,omitempty"` // unix seconds, the time of ConfirmedHeight
	Finished     bool   `json:"finished,omitempty"`
	OutputTokens uint64 `json:"output_tokens,omitempty"`
	TimedOut     bool   `json:"timed_out,omitempty"`
	ErrorMiss    bool   `json:"error_miss,omitempty"`
	Started      bool   `json:"started,omitempty"`
	// Rejected lists the validator slots that voted the result invalid (MsgValidation valid=false).
	Rejected []uint32 `json:"rejected_by,omitempty"`
	// Accepted lists the validator slots that voted the result valid.
	Accepted []uint32 `json:"accepted_by,omitempty"`
	// Challenged is set by the first MsgChallenge on the inference, the only one the state machine
	// applies; ChallengeFaults lists the voter slots of that challenge that found a fault.
	Challenged      bool     `json:"challenged,omitempty"`
	ChallengeFaults []uint32 `json:"challenge_faults,omitempty"`

	// StartedHeight and ConfirmedHeight anchor the inference's timeouts: the floor its Start stamps
	// and the floor as of its ConfirmStart's diff (0 when the floor was empty: the timeout is not
	// armed). StartedAt and ConfirmedAt are 0 until DateAnchors dates them.
	StartedHeight   uint64 `json:"started_at_height,omitempty"`
	ConfirmedHeight uint64 `json:"confirmed_at_height,omitempty"`
	Confirmed       bool   `json:"confirmed,omitempty"` // MsgConfirmStart is on the chain
}

// SessionVoting is what a host's session row says about the escrow's votes: the vote threshold and
// the address behind every slot, so a vote's weight is the slots its address holds.
type SessionVoting struct {
	VoteThreshold uint32
	SlotAddress   map[uint32]string
}

// challengeWeight is the fault weight of a challenge as applyChallenge counts it: each faulting
// voter's address weighs the slots it holds in the group.
func (s SessionVoting) challengeWeight(faults []uint32) uint32 {
	held := map[string]uint32{}
	for _, addr := range s.SlotAddress {
		held[addr]++
	}
	var w uint32
	for _, slot := range faults {
		w += held[s.SlotAddress[slot]]
	}
	return w
}

// sessionVoting decodes config_json and group_json of a session row; ok is false when either is
// missing or unreadable.
func sessionVoting(configJSON, groupJSON string) (SessionVoting, bool) {
	var cfg struct{ VoteThreshold uint32 }
	var group []struct {
		SlotID           uint32
		ValidatorAddress string
	}
	if json.Unmarshal([]byte(configJSON), &cfg) != nil || json.Unmarshal([]byte(groupJSON), &group) != nil || len(group) == 0 {
		return SessionVoting{}, false
	}
	v := SessionVoting{VoteThreshold: cfg.VoteThreshold, SlotAddress: map[uint32]string{}}
	for _, g := range group {
		v.SlotAddress[g.SlotID] = g.ValidatorAddress
	}
	return v, true
}

// HostChain is the diff chain one host's database holds.
type HostChain struct {
	Host       string // "primary" for postgres/, else the db-<name> directory without the prefix
	File       string
	Inferences map[InferenceRef]*InferenceFacts
	// Roots is the post_state_root (hex) of every diff, per escrow and nonce.
	Roots map[string]map[uint64]string
	// Status is the host's devshard_sessions.status per escrow (active, settled, ...), empty when
	// the export holds no sessions table.
	Status map[string]string
	// Voting is the vote threshold and group of each escrow's session, empty without a sessions table.
	Voting map[string]SessionVoting
	Diffs  int

	floors map[string]*escrowFloor // folded into ConfirmedHeight once the chain is read
}

// ExportDiffs is the diff chain of every host in an export, kept apart.
type ExportDiffs struct {
	Hosts []HostChain
}

// ReadExportDiffs decodes devshard_diffs.csv of every database in an export directory: the primary
// one under postgres/ and each host's under db-*/postgres/.
func ReadExportDiffs(exportDir string) (ExportDiffs, error) {
	files, _ := filepath.Glob(filepath.Join(exportDir, "postgres", "public.devshard_diffs.csv"))
	more, _ := filepath.Glob(filepath.Join(exportDir, "db-*", "postgres", "public.devshard_diffs.csv"))
	files = append(files, more...)
	var out ExportDiffs
	if len(files) == 0 {
		return out, fmt.Errorf("no devshard_diffs.csv under %s", exportDir)
	}
	for _, path := range files {
		host := "primary"
		if dir := filepath.Base(filepath.Dir(filepath.Dir(path))); strings.HasPrefix(dir, "db-") {
			host = strings.TrimPrefix(dir, "db-")
		}
		chain := HostChain{Host: host, File: path, Inferences: map[InferenceRef]*InferenceFacts{},
			Roots: map[string]map[uint64]string{}, Status: map[string]string{}, Voting: map[string]SessionVoting{}}
		if sessions, err := readCSV(filepath.Join(filepath.Dir(path), "public.devshard_sessions.csv")); err == nil {
			for _, r := range sessions {
				chain.Status[r["escrow_id"]] = r["status"]
				if v, ok := sessionVoting(r["config_json"], r["group_json"]); ok {
					chain.Voting[r["escrow_id"]] = v
				}
			}
		}
		rows, err := readCSV(path)
		if err != nil {
			return out, err
		}
		if err := chain.applyRows(path, rows); err != nil {
			return out, err
		}
		chain.foldFloors()
		out.Hosts = append(out.Hosts, chain)
	}
	sqliteHosts, err := readSQLiteChains(exportDir)
	if err != nil {
		return out, err
	}
	out.Hosts = append(out.Hosts, sqliteHosts...)
	return out, nil
}

// applyRows decodes devshard_diffs rows (escrow_id, nonce, hex txs_proto, hex post_state_root).
func (c *HostChain) applyRows(path string, rows []map[string]string) error {
	for _, r := range rows {
		nonce, err := strconv.ParseUint(r["nonce"], 10, 64)
		if err != nil {
			return fmt.Errorf("%s nonce %q: %w", path, r["nonce"], err)
		}
		raw, err := hex.DecodeString(strings.TrimPrefix(r["txs_proto"], `\x`))
		if err != nil {
			return fmt.Errorf("%s nonce %d: %w", path, nonce, err)
		}
		var dc types.DiffContent
		if err := proto.Unmarshal(raw, &dc); err != nil {
			return fmt.Errorf("%s nonce %d: %w", path, nonce, err)
		}
		escrow := r["escrow_id"]
		if c.Roots[escrow] == nil {
			c.Roots[escrow] = map[uint64]string{}
		}
		c.Roots[escrow][nonce] = strings.TrimPrefix(r["post_state_root"], `\x`)
		c.Diffs++
		c.apply(escrow, nonce, &dc)
	}
	return nil
}

// readSQLiteChains reads the chain of each SQLite host in an export (sqlite/<service>/...): every
// epoch file holding a diffs table, merged into one chain named like the Postgres hosts
// (devshardd_<service>). Files are opened read-only; a WAL copied next to a file is read with it.
func readSQLiteChains(exportDir string) ([]HostChain, error) {
	services, _ := filepath.Glob(filepath.Join(exportDir, "sqlite", "*"))
	var out []HostChain
	for _, dir := range services {
		svc := filepath.Base(dir)
		chain := HostChain{Host: "devshardd_" + strings.ReplaceAll(svc, "-", "_"), File: dir,
			Inferences: map[InferenceRef]*InferenceFacts{}, Roots: map[string]map[uint64]string{}, Status: map[string]string{},
			Voting: map[string]SessionVoting{}}
		var files []string
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".db") || strings.HasSuffix(p, ".sqlite") || strings.HasSuffix(p, ".sqlite3")) {
				files = append(files, p)
			}
			return nil
		})
		sort.Strings(files)
		for _, p := range files {
			db, err := sql.Open("sqlite", "file:"+p+"?mode=ro")
			if err != nil {
				return out, fmt.Errorf("%s: %w", p, err)
			}
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='diffs'`).Scan(&n); err != nil || n == 0 {
				db.Close()
				continue
			}
			if rows, err := db.Query(`SELECT escrow_id, status, config_json, group_json FROM sessions`); err == nil {
				for rows.Next() {
					var escrow, status, cfg, group string
					if rows.Scan(&escrow, &status, &cfg, &group) == nil {
						chain.Status[escrow] = status
						if v, ok := sessionVoting(cfg, group); ok {
							chain.Voting[escrow] = v
						}
					}
				}
				rows.Close()
			}
			rows, err := db.Query(`SELECT escrow_id, nonce, txs_proto, coalesce(post_state_root, x'') FROM diffs ORDER BY escrow_id, nonce`)
			if err != nil {
				db.Close()
				return out, fmt.Errorf("%s diffs: %w", p, err)
			}
			var recs []map[string]string
			for rows.Next() {
				var escrow string
				var nonce uint64
				var txs, root []byte
				if err := rows.Scan(&escrow, &nonce, &txs, &root); err != nil {
					rows.Close()
					db.Close()
					return out, fmt.Errorf("%s diffs: %w", p, err)
				}
				recs = append(recs, map[string]string{"escrow_id": escrow, "nonce": strconv.FormatUint(nonce, 10),
					"txs_proto": hex.EncodeToString(txs), "post_state_root": hex.EncodeToString(root)})
			}
			rows.Close()
			db.Close()
			if err := chain.applyRows(p, recs); err != nil {
				return out, err
			}
		}
		if chain.Diffs > 0 {
			chain.foldFloors()
			out = append(out, chain)
		}
	}
	return out, nil
}

func (c *HostChain) apply(escrow string, nonce uint64, dc *types.DiffContent) {
	c.observeFloor(escrow, nonce, dc.Txs)
	get := func(id uint64) *InferenceFacts {
		ref := InferenceRef{Escrow: escrow, Inference: id}
		f := c.Inferences[ref]
		if f == nil {
			f = &InferenceFacts{}
			c.Inferences[ref] = f
		}
		return f
	}
	for _, tx := range dc.Txs {
		switch {
		case tx.GetStartInference() != nil:
			m := tx.GetStartInference()
			f := get(m.InferenceId)
			f.Started, f.StartedHeight, f.MaxTokens, f.Model, f.PromptHash = true, m.ObservedHeight, m.MaxTokens, m.Model, m.PromptHash
		case tx.GetConfirmStart() != nil:
			get(tx.GetConfirmStart().InferenceId).Confirmed = true
		case tx.GetFinishInference() != nil:
			f := get(tx.GetFinishInference().InferenceId)
			f.Finished, f.OutputTokens = true, tx.GetFinishInference().OutputTokens
		case tx.GetTimeoutInference() != nil:
			get(tx.GetTimeoutInference().InferenceId).TimedOut = true
		case tx.GetErrorMiss() != nil:
			get(tx.GetErrorMiss().InferenceId).ErrorMiss = true
		case tx.GetChallenge() != nil:
			m := tx.GetChallenge()
			f := get(m.InferenceId)
			if f.Challenged {
				break // a later challenge is a no-op in the state machine
			}
			f.Challenged = true
			for _, vote := range m.Votes {
				if vote.Fault != types.ChallengeFault_CHALLENGE_FAULT_NONE {
					f.ChallengeFaults = append(f.ChallengeFaults, vote.VoterSlot)
				}
			}
		case tx.GetValidation() != nil:
			v := tx.GetValidation()
			f := get(v.InferenceId)
			if v.Valid {
				f.Accepted = append(f.Accepted, v.ValidatorSlot)
			} else {
				f.Rejected = append(f.Rejected, v.ValidatorSlot)
			}
		}
	}
}

// ReadMockLog reads export/mock-log.json. Complete is false when the mock dropped records, so a
// check that finds no ML execution for an inference cannot read that as absence.
func ReadMockLog(exportDir string) (records []mockopenai.RequestRecord, complete bool, err error) {
	raw, err := os.ReadFile(filepath.Join(exportDir, "mock-log.json"))
	if err != nil {
		return nil, false, err
	}
	var doc struct {
		Complete bool                       `json:"complete"`
		Dropped  int                        `json:"dropped"`
		Records  []mockopenai.RequestRecord `json:"records"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false, fmt.Errorf("mock-log.json: %w", err)
	}
	return doc.Records, doc.Complete && doc.Dropped == 0, nil
}

// Execution is one host's run of an inference: the host that executed it and the mock node it ran on.
type Execution struct {
	InferenceRef
	Participant string `json:"participant_id"`
	Instance    string `json:"instance_id"`
	Outcome     string `json:"outcome"`
}

// ClientRequest is one external request: what the client got and which inferences served it.
type ClientRequest struct {
	RequestID  string      `json:"request_id"`
	PromptKey  string      `json:"prompt_key,omitempty"`
	HTTPStatus int         `json:"http_status"`
	Executions []Execution `json:"executions,omitempty"`
	// Incomplete is a 200 whose answer the client did not get in full (a streamed response cut,
	// empty or ended by an error event): no answer reached the client.
	Incomplete bool `json:"incomplete,omitempty"`
}

// Inferences lists the request's inferences, each once.
func (r ClientRequest) Inferences() []InferenceRef {
	var out []InferenceRef
	seen := map[InferenceRef]bool{}
	for _, e := range r.Executions {
		if !seen[e.InferenceRef] {
			seen[e.InferenceRef] = true
			out = append(out, e.InferenceRef)
		}
	}
	return out
}

// KnownViolation marks violations the team already tracks, so a known defect is labelled instead
// of reported as new on every run.
type KnownViolation struct {
	Promise string
	Detail  *regexp.Regexp // matched against the violation's detail
	Ref     string         // the pull request or issue that tracks it
}

// PromiseInput is everything the checks read. Zero values mean "not in this export".
type PromiseInput struct {
	Diffs    ExportDiffs
	Requests []ClientRequest
	Mock     []mockopenai.RequestRecord
	Known    []KnownViolation
}

// PromiseViolation is one broken property with the facts it was found from.
type PromiseViolation struct {
	Promise   string        `json:"promise"`
	Host      string        `json:"host,omitempty"`
	Inference *InferenceRef `json:"inference,omitempty"`
	RequestID string        `json:"request_id,omitempty"`
	Detail    string        `json:"detail"`
	Known     string        `json:"known,omitempty"` // the tracking PR or issue, when already known
}

// Measure is what a check could say on this run.
type Measure struct {
	Measured bool   `json:"measured"`
	Judged   int    `json:"judged"`
	Note     string `json:"note"` // what was judged, or why the check could not fire
}

// PromiseReport is the outcome of all checks.
type PromiseReport struct {
	Checked      map[string]Measure `json:"checked"`
	Violations   []PromiseViolation `json:"violations"`
	Observations []string           `json:"observations,omitempty"`
	// Invalid lists why the run cannot judge the system, when it cannot: the load or the dishonest
	// nodes were not what the scenario asked for.
	Invalid []string `json:"invalid,omitempty"`
	// Wave is what the gateway logged of the usage challenges; nil when it logged none.
	Wave *ChallengeWave `json:"challenge_wave,omitempty"`
}

func measured(judged int, format string, args ...any) Measure {
	if judged == 0 {
		return Measure{Note: "not measured: " + fmt.Sprintf(format, args...)}
	}
	return Measure{Measured: true, Judged: judged, Note: fmt.Sprintf(format, args...)}
}

func notMeasured(why string) Measure { return Measure{Note: "not measured: " + why} }

// CheckPromises holds the run to every check of PromiseCatalog.
func CheckPromises(in PromiseInput) PromiseReport {
	rep := PromiseReport{Checked: map[string]Measure{}}
	checkTokens(in, &rep)
	checkAttribution(in, &rep)
	for i := range rep.Violations {
		v := &rep.Violations[i]
		for _, k := range in.Known {
			if k.Promise == v.Promise && (k.Detail == nil || k.Detail.MatchString(v.Detail)) {
				v.Known = k.Ref
				break
			}
		}
	}
	return rep
}

func sortedRefs(m map[InferenceRef]*InferenceFacts) []InferenceRef {
	refs := make([]InferenceRef, 0, len(m))
	for ref := range m {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Escrow != refs[j].Escrow {
			return refs[i].Escrow < refs[j].Escrow
		}
		return refs[i].Inference < refs[j].Inference
	})
	return refs
}

// execKey finds the ML executions of an inference: the request's prompt key and the seed the host
// sent. The seed equals the inference id only when the client set none and only within one escrow,
// so one key can match several executions.
type execKey struct {
	prompt string
	seed   int
}

func executionsByKey(mock []mockopenai.RequestRecord, phase string) map[execKey][]mockopenai.RequestRecord {
	out := map[execKey][]mockopenai.RequestRecord{}
	for _, m := range mock {
		if m.Phase == phase && m.Seed != nil {
			k := execKey{m.PromptKey, *m.Seed}
			out[k] = append(out[k], m)
		}
	}
	return out
}

// promptKeys maps each inference to the prompt it ran, from the client log.
func promptKeys(in PromiseInput) map[InferenceRef]string {
	out := map[InferenceRef]string{}
	for _, r := range in.Requests {
		for _, ref := range r.Inferences() {
			out[ref] = r.PromptKey
		}
	}
	return out
}

// executionsInWindow keeps the executions that started within the match window of an inference
// started at startedAt (unix seconds).
func executionsInWindow(recs []mockopenai.RequestRecord, startedAt int64) []mockopenai.RequestRecord {
	from, to := startedAt*1000-MatchWindowBeforeMs, startedAt*1000+MatchWindowAfterMs
	var out []mockopenai.RequestRecord
	for _, m := range recs {
		if m.StartUnixMs >= from && m.StartUnixMs <= to {
			out = append(out, m)
		}
	}
	return out
}

// withMaxTokens keeps the executions whose request carried the inference's max_tokens.
func withMaxTokens(recs []mockopenai.RequestRecord, f *InferenceFacts) []mockopenai.RequestRecord {
	var out []mockopenai.RequestRecord
	for _, m := range recs {
		if sameMaxTokens(m, f) {
			out = append(out, m)
		}
	}
	return out
}

func checkTokens(in PromiseInput, rep *PromiseReport) {
	keyOf := promptKeys(in)
	produced := executionsByKey(in.Mock, mockopenai.PhaseExecute)
	finishes, matched := 0, 0
	// Every host replays the same Finish: an ML mismatch or a Finish above max_tokens is judged once
	// per inference, its violation naming the hosts that hold it.
	mismatch, above := map[InferenceRef]int{}, map[InferenceRef]int{}
	for _, chain := range in.Diffs.Hosts {
		for _, ref := range sortedRefs(chain.Inferences) {
			f := chain.Inferences[ref]
			if !f.Finished {
				continue
			}
			finishes++
			r := ref
			if f.Started && f.MaxTokens != 0 && f.OutputTokens > f.MaxTokens {
				if i, seen := above[ref]; seen {
					rep.Violations[i].Host += "," + chain.Host
				} else {
					above[ref] = len(rep.Violations)
					rep.Violations = append(rep.Violations, PromiseViolation{Promise: PaidOutputWithinProduced, Host: chain.Host, Inference: &r,
						Detail: fmt.Sprintf("Finish output_tokens %d above Start max_tokens %d", f.OutputTokens, f.MaxTokens)})
				}
			}
			prompt := keyOf[ref]
			if prompt == "" || ref.Inference > uint64(^uint(0)>>1) {
				continue
			}
			outs := produced[execKey{prompt, int(ref.Inference)}]
			if f.StartedAt != 0 {
				// An escrow's ids restart at 1, so the same prompt and seed recur on later escrows;
				// only executions in the inference's own window are its own.
				outs = executionsInWindow(outs, f.StartedAt)
			}
			outs = withMaxTokens(outs, f)
			if len(outs) == 0 {
				continue
			}
			matched++
			counts := make([]int, 0, len(outs))
			shapes := []string{}
			agree := false
			for _, o := range outs {
				// The host reports the usage the ML answered with; outside oracle mode the mock's
				// usage is not the count of tokens it emitted. Logs without the field keep tokens_out.
				n := o.TokensOut
				if o.UsageCompletion > 0 {
					n = o.UsageCompletion
				}
				counts = append(counts, n)
				agree = agree || uint64(n) == f.OutputTokens
				if o.Chaos != "" && o.Chaos != mockopenai.ChaosOK {
					shapes = append(shapes, o.Chaos)
				}
			}
			if !agree {
				detail := fmt.Sprintf("Finish output_tokens %d, the ML produced %v", f.OutputTokens, counts)
				if len(shapes) > 0 {
					detail += fmt.Sprintf(" (chaos %v)", shapes)
				}
				if i, seen := mismatch[ref]; seen {
					rep.Violations[i].Host += "," + chain.Host
					continue
				}
				mismatch[ref] = len(rep.Violations)
				rep.Violations = append(rep.Violations, PromiseViolation{Promise: PaidOutputWithinProduced, Host: chain.Host, Inference: &r, Detail: detail})
			}
		}
	}
	rep.Checked[PaidOutputWithinProduced] = measured(finishes, "%d finishes judged across hosts, %d matched to an ML execution", finishes, matched)
}

// checkAttribution holds the run to charge_only_the_guilty: an executor the ML log shows answering
// in time and correctly, or one asked to run a request the client spoiled, is not charged, and an
// answer the ML sent with forged logprobs is not voted valid.
func checkAttribution(in PromiseInput, rep *PromiseReport) {
	if len(in.Requests) == 0 || len(in.Mock) == 0 {
		rep.Checked[ChargeOnlyTheGuilty] = notMeasured("no client request log or no mock log")
		return
	}
	keyOf := promptKeys(in)
	runs := executionsByKey(in.Mock, mockopenai.PhaseExecute)
	executor := map[InferenceRef]Execution{}
	for _, r := range in.Requests {
		for _, e := range r.Executions {
			executor[e.InferenceRef] = e
		}
	}
	// The hosts' chains converge, so a charge shows on every host that holds the inference: judge
	// each inference once and name the hosts that show the charge, not one verdict per host.
	chains := append([]HostChain(nil), in.Diffs.Hosts...)
	sort.Slice(chains, func(i, j int) bool { return chains[i].Host < chains[j].Host })
	union := map[InferenceRef]*InferenceFacts{}
	for _, chain := range chains {
		for ref, f := range chain.Inferences {
			if union[ref] == nil {
				union[ref] = f
			}
		}
	}
	unweighed := 0 // challenges with faults on a host whose session row is missing
	chargeOf := func(chain HostChain, escrow string, f *InferenceFacts) string {
		switch {
		case f.TimedOut:
			return "a timeout (miss)"
		case f.ErrorMiss:
			return "an error miss"
		case len(f.Rejected) > 0:
			return fmt.Sprintf("an invalid vote from slots %v", f.Rejected)
		case len(f.ChallengeFaults) > 0:
			// applyChallenge invalidates only above the session's vote threshold; below it the
			// Finish stays paid.
			v, ok := chain.Voting[escrow]
			if !ok {
				unweighed++
				return ""
			}
			if w := v.challengeWeight(f.ChallengeFaults); w > v.VoteThreshold {
				return fmt.Sprintf("an invalidation by a usage challenge (fault weight %d above vote threshold %d, slots %v)",
					w, v.VoteThreshold, f.ChallengeFaults)
			}
		}
		return ""
	}
	judged, forged, caught := 0, 0, 0
	for _, ref := range sortedRefs(union) {
		prompt := keyOf[ref]
		if prompt == "" || ref.Inference > uint64(^uint(0)>>1) {
			continue
		}
		ml := runs[execKey{prompt, int(ref.Inference)}]
		if at := union[ref].StartedAt; at != 0 {
			// An escrow's ids restart at 1: only executions in the inference's own window are its own.
			ml = executionsInWindow(ml, at)
		}
		if len(ml) == 0 {
			continue
		}
		var voted, rejected, charged []string
		var accepted []uint32 // the slots of the first host that shows a valid vote
		charge, startedAt := "", union[ref].StartedAt
		for _, chain := range chains {
			f := chain.Inferences[ref]
			if f == nil {
				continue
			}
			if len(f.Accepted)+len(f.Rejected) > 0 {
				voted = append(voted, chain.Host)
			}
			if len(f.Accepted) > 0 && accepted == nil {
				accepted = f.Accepted
			}
			if len(f.Rejected) > 0 {
				rejected = append(rejected, chain.Host)
			}
			if c := chargeOf(chain, ref.Escrow, f); c != "" {
				if charge == "" {
					charge = c
				}
				charged = append(charged, chain.Host)
			}
		}
		who := executor[ref]
		r := ref
		if allWrongAnswers(ml) && len(voted) > 0 {
			forged++
			if len(rejected) > 0 {
				caught++
			}
			if len(accepted) > 0 {
				rep.Violations = append(rep.Violations, PromiseViolation{Promise: ChargeOnlyTheGuilty, Host: strings.Join(voted, ","), Inference: &r,
					Detail: fmt.Sprintf("validator slots %v accepted an answer the ML sent with forged logprobs (executor %s)",
						accepted, who.Participant)})
			}
		}
		if charge == "" {
			continue
		}
		judged++
		honest, spoiled := true, true
		for _, m := range ml {
			honest = honest && m.Status == 200 && m.Outcome == "ok" && m.Rule == "" &&
				(m.Chaos == "" || m.Chaos == mockopenai.ChaosOK)
			spoiled = spoiled && ClientErrorStatuses[m.Status]
		}
		at := time.Unix(startedAt, 0).UTC().Format(time.RFC3339)
		hosts := fmt.Sprintf("on %d of %d hosts", len(charged), len(chains))
		switch {
		case spoiled:
			rep.Violations = append(rep.Violations, PromiseViolation{Promise: ChargeOnlyTheGuilty, Host: strings.Join(charged, ","), Inference: &r,
				Detail: fmt.Sprintf("executor %s (%s) charged %s %s for a request the ML rejected as a client error (started %s)",
					who.Participant, who.Instance, charge, hosts, at)})
		case honest:
			rep.Violations = append(rep.Violations, PromiseViolation{Promise: ChargeOnlyTheGuilty, Host: strings.Join(charged, ","), Inference: &r,
				Detail: fmt.Sprintf("executor %s (%s) charged %s %s though the ML answered it in full (%d executions, started %s)",
					who.Participant, who.Instance, charge, hosts, len(ml), at)})
		}
	}
	rep.Checked[ChargeOnlyTheGuilty] = measured(judged+forged,
		"%d charged inferences matched to their ML execution; %d forged answers validated, %d of them voted invalid; %d challenges not weighed (no session row)",
		judged, forged, caught, unweighed)
}

// allWrongAnswers says every ML execution of an inference was answered with forged logprobs.
func allWrongAnswers(ml []mockopenai.RequestRecord) bool {
	if len(ml) == 0 {
		return false
	}
	for _, m := range ml {
		if m.Chaos != mockopenai.ChaosWrongAnswer || m.Status != 200 {
			return false
		}
	}
	return true
}

func readCSV(path string) ([]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	out := make([]map[string]string, 0, len(rows)-1)
	for _, row := range rows[1:] {
		m := make(map[string]string, len(rows[0]))
		for i, h := range rows[0] {
			if i < len(row) {
				m[h] = row[i]
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func readJSONLines(path string, each func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		if err := each(line); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return sc.Err()
}
