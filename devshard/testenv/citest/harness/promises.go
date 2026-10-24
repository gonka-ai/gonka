package harness

// Checks of properties the system has to hold whatever its code does today, read from a finished
// run's export instead of from the code under test. Each check names the property it holds the run
// to; a violation carries the facts it was found from. Every check runs on each host's diff chain on
// its own: merging the chains would let one host's Finish hide another host's stuck inference. The
// deadline checks make one exception: a host whose chain of an escrow is a matching, shorter prefix of
// the longest one is only behind (the gateway sends each diff to one host), and the inference is
// judged on the longest chain instead. A check that could not fire on this run says so (not measured)
// instead of passing.
//
//   inference_closes_in_time  every started inference leaves Pending/Started through Finish or a
//        timeout within its deadline (refusal_timeout from Start while Pending, execution_timeout from
//        ConfirmStart while Started) plus the grace the timeout path takes to reach a diff.
//   served_request_finished  a request the client got a 200 for has MsgFinishInference on every
//        host's chain for at least one of its inferences.
//   paid_output_within_produced  a paid Finish never reports more output tokens than its Start
//        allowed, nor a count other than the ML produced for that execution; one whose inference was
//        invalidated paid nothing, and its overclaim is counted apart as caught.
//   charge_only_the_guilty  a host the ML log shows answering in time and correctly is not charged a
//        miss or an invalidation, a request the client spoiled charges no host, and no validator
//        accepts an answer the ML sent with forged logprobs (chaos mode). An honest executor whose
//        host a fault of the runner kept from serving its payload with the logprobs when the
//        validators came for it is invalidated by design and counted apart.
//   escrow_money_conserved  the escrow's money is conserved after every diff of each host's chain,
//        replayed through the state machine (promises_balance.go).
//   gateway_heap_bounded  gateway heap growth per request stays within k times the response size.
//   gateway_live_heap_returns  once the load is drained, the gateway's live heap returns to within
//        the allowance of the one before the load.
//   client_error_answered_4xx  when every ML execution of a request rejected it as a client error,
//        the client gets a 4xx.
//   hosts_converge  the hosts of an escrow converge: same last nonce, same post_state_root at every
//        common nonce.
//   ml_node_locks_released, one_nonce_per_request, quarantine_only_when_faulted  read the layers of
//        a load-test run (promises_layers.go).

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

	InferenceClosesInTime = "inference_closes_in_time"
	ServedRequestFinished = "served_request_finished"

	EscrowMoneyConserved      = "escrow_money_conserved"
	GatewayHeapBounded        = "gateway_heap_bounded"
	GatewayLiveHeapReturns    = "gateway_live_heap_returns"
	ClientErrorAnswered4xx    = "client_error_answered_4xx"
	HostsConverge             = "hosts_converge"
	NodeLocksReleased         = "ml_node_locks_released"
	OneNoncePerRequest        = "one_nonce_per_request"
	QuarantineOnlyWhenFaulted = "quarantine_only_when_faulted"
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
	StartedAt    int64  `json:"started_at,omitempty"` // unix seconds, from MsgStartInference
	Model        string `json:"model,omitempty"`      // from MsgStartInference
	InputLength  uint64 `json:"input_length,omitempty"`
	MaxTokens    uint64 `json:"max_tokens,omitempty"`
	PromptHash   []byte `json:"prompt_hash,omitempty"`  // from MsgStartInference
	ConfirmedAt  int64  `json:"confirmed_at,omitempty"` // unix seconds, from MsgConfirmStart; 0 = not confirmed
	Finished     bool   `json:"finished,omitempty"`
	InputTokens  uint64 `json:"input_tokens,omitempty"`
	OutputTokens uint64 `json:"output_tokens,omitempty"`
	TimedOut     bool   `json:"timed_out,omitempty"`
	ErrorMiss    bool   `json:"error_miss,omitempty"`
	Started      bool   `json:"started,omitempty"`
	// Rejected lists the validator slots that voted the result invalid, in MsgValidation (phase A)
	// or MsgValidationVote (phase B).
	Rejected []uint32 `json:"rejected_by,omitempty"`
	// Accepted lists the validator slots that voted the result valid, in either phase.
	Accepted []uint32 `json:"accepted_by,omitempty"`
	// Votes are both phases' votes in chain order, for the tally the state machine keeps.
	Votes []ValidationVote `json:"votes,omitempty"`
	// Challenged is set by the first MsgChallenge on the inference, the only one the state machine
	// applies; ChallengeFaults lists the voter slots of that challenge that found a fault.
	Challenged      bool     `json:"challenged,omitempty"`
	ChallengeFaults []uint32 `json:"challenge_faults,omitempty"`
}

// SessionVoting is what a host's session row says about the escrow's votes: the vote threshold and
// the address behind every slot, so a vote's weight is the slots its address holds.
type SessionVoting struct {
	VoteThreshold uint32
	TokenPrice    uint64
	SlotAddress   map[uint32]string
}

// ValidationVote is one validation vote on a host's chain: a MsgValidation (phase A), or a
// MsgValidationVote (phase B, cast once the inference is Challenged).
type ValidationVote struct {
	Slot   uint32 `json:"slot"`
	Valid  bool   `json:"valid"`
	PhaseB bool   `json:"phase_b,omitempty"`
}

// VoteTally is where the validation votes left a finished inference: Finished, Challenged,
// Validated or Invalidated, and the weights voted valid and invalid.
type VoteTally struct {
	Status         string
	Valid, Invalid uint32
	invalidBy      []string // the addresses whose invalid vote counted
}

// tally counts the votes as applyValidation and applyValidationVote do: an address votes once and
// weighs the slots it holds; the first invalid MsgValidation turns the inference Challenged, and a
// Challenged one turns Invalidated once the invalid weight is above the vote threshold, Validated
// once the valid weight is. A vote on a resolved inference changes nothing.
func (s SessionVoting) tally(votes []ValidationVote) VoteTally {
	held := map[string]uint32{}
	for _, addr := range s.SlotAddress {
		held[addr]++
	}
	t := VoteTally{Status: "Finished"}
	voted := map[string]bool{}
	for _, v := range votes {
		addr := s.SlotAddress[v.Slot]
		if voted[addr] || v.PhaseB && t.Status != "Challenged" {
			continue // a repeated address, or a phase-B vote on a resolved inference
		}
		voted[addr] = true
		switch {
		case t.Status == "Finished" && v.Valid:
			t.Valid += held[addr]
		case t.Status == "Finished":
			t.Invalid += held[addr]
			t.invalidBy = append(t.invalidBy, addr)
			t.Status = "Challenged"
		case t.Status == "Challenged":
			if v.Valid {
				t.Valid += held[addr]
			} else {
				t.Invalid += held[addr]
				t.invalidBy = append(t.invalidBy, addr)
			}
			if t.Invalid > s.VoteThreshold {
				t.Status = "Invalidated"
			} else if t.Valid > s.VoteThreshold {
				t.Status = "Validated"
			}
		}
	}
	return t
}

// cost is what the Finish paid the executor, priced as checkFinishLocked does: the input and
// output tokens at the token price, at most the reserve the Start took. An invalidation takes it back.
func (s SessionVoting) cost(f *InferenceFacts) uint64 {
	return min((f.InputTokens+f.OutputTokens)*s.TokenPrice, (f.InputLength+f.MaxTokens)*s.TokenPrice)
}

// weightOf is how many slots of the group an address holds.
func (s SessionVoting) weightOf(addr string) uint32 {
	var w uint32
	for _, a := range s.SlotAddress {
		if a == addr {
			w++
		}
	}
	return w
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
	var cfg struct {
		VoteThreshold uint32
		TokenPrice    uint64
	}
	var group []struct {
		SlotID           uint32
		ValidatorAddress string
	}
	if json.Unmarshal([]byte(configJSON), &cfg) != nil || json.Unmarshal([]byte(groupJSON), &group) != nil || len(group) == 0 {
		return SessionVoting{}, false
	}
	v := SessionVoting{VoteThreshold: cfg.VoteThreshold, TokenPrice: cfg.TokenPrice, SlotAddress: map[uint32]string{}}
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
}

// LastNonce is the highest nonce the host holds for an escrow, 0 when it holds none.
func (c HostChain) LastNonce(escrow string) uint64 {
	var last uint64
	for n := range c.Roots[escrow] {
		last = max(last, n)
	}
	return last
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
		c.apply(escrow, &dc)
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
		for _, p := range sqliteFilesIn(dir) {
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
			out = append(out, chain)
		}
	}
	return out, nil
}

// sqliteFilesIn lists the SQLite files under dir, sorted.
func sqliteFilesIn(dir string) []string {
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".db") || strings.HasSuffix(p, ".sqlite") || strings.HasSuffix(p, ".sqlite3")) {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// sqliteFiles lists the SQLite files of every SQLite host in an export (sqlite/<service>/...).
func sqliteFiles(exportDir string) []string {
	services, _ := filepath.Glob(filepath.Join(exportDir, "sqlite", "*"))
	var out []string
	for _, dir := range services {
		out = append(out, sqliteFilesIn(dir)...)
	}
	return out
}

func (c *HostChain) apply(escrow string, dc *types.DiffContent) {
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
			f.Started, f.StartedAt, f.InputLength, f.MaxTokens, f.Model, f.PromptHash = true, m.StartedAt, m.InputLength, m.MaxTokens, m.Model, m.PromptHash
		case tx.GetConfirmStart() != nil:
			get(tx.GetConfirmStart().InferenceId).ConfirmedAt = tx.GetConfirmStart().ConfirmedAt
		case tx.GetFinishInference() != nil:
			f := get(tx.GetFinishInference().InferenceId)
			f.Finished, f.InputTokens, f.OutputTokens = true, tx.GetFinishInference().InputTokens, tx.GetFinishInference().OutputTokens
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
			get(v.InferenceId).vote(ValidationVote{Slot: v.ValidatorSlot, Valid: v.Valid})
		case tx.GetValidationVote() != nil:
			v := tx.GetValidationVote()
			get(v.InferenceId).vote(ValidationVote{Slot: v.VoterSlot, Valid: v.VoteValid, PhaseB: true})
		}
	}
}

// vote records a validation vote of either phase.
func (f *InferenceFacts) vote(v ValidationVote) {
	f.Votes = append(f.Votes, v)
	if v.Valid {
		f.Accepted = append(f.Accepted, v.Slot)
	} else {
		f.Rejected = append(f.Rejected, v.Slot)
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
	// ResponseBytes is the size of the answer the client read, when the client log records it.
	ResponseBytes int64 `json:"response_bytes,omitempty"`
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

// HeapSample is one reading of the gateway's Go heap. Phase names the readings
// gateway_live_heap_returns compares: "before" the load, "load_end" when the load stopped and
// "after" the drain; the readings in between have none.
type HeapSample struct {
	AtUnixMs   int64  `json:"at_unix_ms"`
	Phase      string `json:"phase,omitempty"`
	HeapInuse  uint64 `json:"heap_inuse"`
	NextGC     uint64 `json:"next_gc,omitempty"`
	NumGC      uint64 `json:"num_gc,omitempty"`
	StackInuse uint64 `json:"stack_inuse,omitempty"`
	// Process tells gateway processes apart (a restart starts a new heap); empty is one process.
	Process string `json:"process,omitempty"`
}

// LiveHeap is the heap the last collection found live. With GOGC 100 the runtime sets the next
// goal to live + (live + stacks) bytes, so live = (goal - stacks) / 2; globals are left out.
func (h HeapSample) LiveHeap() uint64 {
	if h.NextGC <= h.StackInuse {
		return 0
	}
	return (h.NextGC - h.StackInuse) / 2
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

	Timeouts      map[string]EscrowTimeouts
	ObservedUntil time.Time     // when the export was taken; zero leaves the deadline checks not measured
	Slack         time.Duration // added to every deadline for the timeout votes to reach a diff

	// MockErrorsVLLMShaped says the mock wrote its error bodies the way vLLM does. When false, a
	// client_error_answered_4xx mismatch may come from the mock's error shape and is reported as an
	// observation.
	MockErrorsVLLMShaped bool
	GatewayHeap          []HeapSample
	ResponseBytes        int64   // total bytes the clients received, for gateway_heap_bounded
	HeapPerResponseByte  float64 // the k of gateway_heap_bounded; zero reports the ratio without a verdict
	// LiveHeapAllowance is how far the gateway's live heap after the drain may sit above the one
	// before the load; zero reports the growth without a verdict.
	LiveHeapAllowance uint64
	// LiveHeapPerInference widens the allowance by this many bytes for every inference of an escrow
	// not settled on all hosts by the export: the gateway keeps such an escrow's state machine and
	// response cache until settlement, so its live heap grows with them by design.
	LiveHeapPerInference uint64
	// Layers is what the lock, nonce and quarantine checks read from a load-test run; nil leaves them
	// not measured.
	Layers *LoadtestLayers
	// LockGrace is how long before the end of the lock journal a lock may still be released; zero
	// means three minutes.
	LockGrace time.Duration
	// BurnAllowance is the nonces burned without sending per request one minute may reach on a host;
	// zero reports without a verdict.
	BurnAllowance float64
	// QuarantineCauseWindow is how far before a quarantine the report looks for what the gateway held
	// against the host; zero means one minute.
	QuarantineCauseWindow time.Duration
	// Balances is the state-machine replay of every host's escrows; nil leaves escrow_money_conserved
	// not measured.
	Balances []BalanceReplay
}

// PromiseViolation is one broken property with the facts it was found from.
type PromiseViolation struct {
	Promise   string        `json:"promise"`
	Host      string        `json:"host,omitempty"`
	Node      string        `json:"node,omitempty"` // the ML node of a lock that never came back
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
	// ChargedWhileStopped lists the invalidations of honest executors the runner stopped right after
	// they answered: their validators could not fetch the payload and voted the result invalid, as
	// they do by design with an executor that withholds the payload. They are not violations of
	// charge_only_the_guilty and are counted apart.
	ChargedWhileStopped []PromiseViolation `json:"charged_while_executor_stopped,omitempty"`
	// MissedWhileStopped lists the misses of honest executors whose host a fault of the runner stopped
	// serving between the inference's start and its timeout deadline: the host could not finish the
	// stream, the gateway timed the inference out and the votes charged the miss, as the protocol does
	// with any executor that does not finish in time. They are not violations of charge_only_the_guilty
	// and are counted apart.
	MissedWhileStopped []PromiseViolation `json:"missed_while_executor_stopped,omitempty"`
	// CaughtOverclaims lists the Finishes that claimed other than the ML produced, or above
	// max_tokens, whose inference the longest chain invalidated: the executor was not paid for them.
	// They are not violations of paid_output_within_produced and are counted apart.
	CaughtOverclaims []PromiseViolation `json:"caught_overclaims,omitempty"`
	// Invalid lists why the run cannot judge the system, when it cannot: the load or the dishonest
	// nodes were not what the scenario asked for.
	Invalid []string `json:"invalid,omitempty"`
	// Wave is what the gateway logged of the usage challenges; nil when it logged none.
	Wave *ChallengeWave `json:"challenge_wave,omitempty"`
	// Open lists inferences still Pending/Started at the export whose deadline had not passed: allowed
	// by the property, reported so a short run does not hide them.
	Open []HostInference `json:"open_within_deadline,omitempty"`
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
	checkTerminal(in, &rep)
	checkServedFinish(in, &rep)
	checkTokens(in, &rep)
	checkBalance(in, &rep)
	checkHeap(in, &rep)
	checkLiveHeap(in, &rep)
	checkClientError(in, &rep)
	checkAttribution(in, &rep)
	checkConvergence(in, &rep)
	checkLockBalance(in, &rep)
	checkNonceBurn(in, &rep)
	checkQuarantineCause(in, &rep)
	rep.Observations = append(rep.Observations, lagObservations(in.Diffs.Hosts, behindTip(in.Diffs.Hosts))...)
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

// tipChains names, per escrow, the host whose chain of it reaches the highest nonce: the chain
// behindTip judges a host only behind it on.
func tipChains(hosts []HostChain) map[string]int {
	tip := map[string]int{}
	for i, h := range hosts {
		for escrow, roots := range h.Roots {
			if j, ok := tip[escrow]; len(roots) > 0 && (!ok || h.LastNonce(escrow) > hosts[j].LastNonce(escrow)) {
				tip[escrow] = i
			}
		}
	}
	return tip
}

// invalidation says how the state machine invalidated a finished inference, empty when it did not:
// the validators' invalid weight, or the fault weight of the first usage challenge, above the vote
// threshold. An invalidation takes the Finish's pay back to the escrow.
func (s SessionVoting) invalidation(f *InferenceFacts) string {
	if t := s.tally(f.Votes); t.Status == "Invalidated" {
		return fmt.Sprintf("invalidated by the validators (invalid weight %d above vote threshold %d)", t.Invalid, s.VoteThreshold)
	}
	if w := s.challengeWeight(f.ChallengeFaults); len(f.ChallengeFaults) > 0 && w > s.VoteThreshold {
		return fmt.Sprintf("invalidated by a usage challenge (fault weight %d above vote threshold %d)", w, s.VoteThreshold)
	}
	return ""
}

// checkTokens holds the run to paid_output_within_produced. It judges what the executor was paid
// for: a Finish whose inference was invalidated paid nothing, so its overclaim is caught, counted
// apart, and not a violation. Whether an honest Finish was wrongly invalidated is
// charge_only_the_guilty's to judge.
func checkTokens(in PromiseInput, rep *PromiseReport) {
	keyOf := promptKeys(in)
	produced := executionsByKey(in.Mock, mockopenai.PhaseExecute)
	finishes, matched, unweighed := 0, 0, 0
	// Every host replays the same Finish: an ML mismatch or a Finish above max_tokens is judged once
	// per inference, its violation naming the hosts that hold it.
	mismatch, above, caught := map[InferenceRef]int{}, map[InferenceRef]int{}, map[InferenceRef]int{}
	overclaimed := map[InferenceRef]bool{}
	// A host only behind the longest chain of an escrow is judged on that chain, which may already
	// hold the votes that invalidated the inference.
	lag, tip := behindTip(in.Diffs.Hosts), tipChains(in.Diffs.Hosts)
	for i, chain := range in.Diffs.Hosts {
		for _, ref := range sortedRefs(chain.Inferences) {
			f := chain.Inferences[ref]
			if !f.Finished {
				continue
			}
			finishes++
			r := ref
			var claims []string // the Finish above max_tokens first, then the ML mismatch
			aboveMax := f.Started && f.MaxTokens != 0 && f.OutputTokens > f.MaxTokens
			if aboveMax {
				claims = append(claims, fmt.Sprintf("Finish output_tokens %d above Start max_tokens %d", f.OutputTokens, f.MaxTokens))
			}
			var outs []mockopenai.RequestRecord
			if prompt := keyOf[ref]; prompt != "" && ref.Inference <= uint64(^uint(0)>>1) {
				outs = produced[execKey{prompt, int(ref.Inference)}]
				if f.StartedAt != 0 {
					// An escrow's ids restart at 1, so the same prompt and seed recur on later escrows;
					// only executions in the inference's own window are its own.
					outs = executionsInWindow(outs, f.StartedAt)
				}
				outs = withMaxTokens(outs, f)
			}
			if len(outs) > 0 {
				matched++
			}
			counts := make([]int, 0, len(outs))
			shapes := []string{}
			agree := len(outs) == 0
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
				claims = append(claims, detail)
			}
			if len(claims) == 0 {
				continue
			}
			overclaimed[ref] = true
			judgedOn := chain
			if lag[i][ref.Escrow] > 0 {
				judgedOn = in.Diffs.Hosts[tip[ref.Escrow]]
			}
			var how string
			if g := judgedOn.Inferences[ref]; g != nil {
				if v, ok := judgedOn.Voting[ref.Escrow]; ok {
					how = v.invalidation(g)
				} else if len(g.Rejected)+len(g.ChallengeFaults) > 0 {
					unweighed++
				}
			}
			add := func(list *[]PromiseViolation, seen map[InferenceRef]int, detail string) {
				if j, ok := seen[ref]; ok {
					(*list)[j].Host += "," + chain.Host
					return
				}
				seen[ref] = len(*list)
				*list = append(*list, PromiseViolation{Promise: PaidOutputWithinProduced, Host: chain.Host, Inference: &r, Detail: detail})
			}
			switch {
			case how != "":
				add(&rep.CaughtOverclaims, caught, strings.Join(claims, "; ")+"; "+how+", its pay went back to the escrow")
			case aboveMax:
				add(&rep.Violations, above, claims[0])
				if len(claims) > 1 {
					add(&rep.Violations, mismatch, claims[1])
				}
			default:
				add(&rep.Violations, mismatch, claims[0])
			}
		}
	}
	note := fmt.Sprintf("%d finishes judged across hosts, %d matched to an ML execution", finishes, matched)
	if len(overclaimed) > 0 {
		note += fmt.Sprintf("; %d inferences whose Finish claimed other than the ML produced or above max_tokens, "+
			"%d of them caught: invalidated, so not paid, and counted apart, not as violations", len(overclaimed), len(caught))
	}
	if unweighed > 0 {
		note += fmt.Sprintf("; %d of their Finishes had invalid votes or challenge faults on a host without the escrow's session row to weigh them, "+
			"judged as paid", unweighed)
	}
	rep.Checked[PaidOutputWithinProduced] = measured(finishes, "%s", note)
}

// checkClientError holds the run to client_error_answered_4xx: when every ML execution of a request
// rejected it as a client error, the client gets a 4xx.
func checkClientError(in PromiseInput, rep *PromiseReport) {
	if len(in.Requests) == 0 || len(in.Mock) == 0 {
		rep.Checked[ClientErrorAnswered4xx] = notMeasured("no client request log or no mock log")
		return
	}
	byKey := map[string][]int{}
	for _, m := range in.Mock {
		if m.Phase == mockopenai.PhaseExecute {
			byKey[m.PromptKey] = append(byKey[m.PromptKey], m.Status)
		}
	}
	judged, suppressed := 0, 0
	for _, r := range in.Requests {
		statuses := byKey[r.PromptKey]
		if r.PromptKey == "" || len(statuses) == 0 {
			continue
		}
		all := true
		for _, s := range statuses {
			all = all && ClientErrorStatuses[s]
		}
		if !all {
			continue
		}
		judged++
		if r.HTTPStatus >= 400 && r.HTTPStatus < 500 {
			continue
		}
		detail := fmt.Sprintf("every one of %d ML executions rejected the request (%v), the client got %d",
			len(statuses), statuses, r.HTTPStatus)
		if !in.MockErrorsVLLMShaped {
			suppressed++
			rep.Observations = append(rep.Observations, ClientErrorAnswered4xx+" (mock error body not vLLM-shaped) "+r.RequestID+": "+detail)
			continue
		}
		rep.Violations = append(rep.Violations, PromiseViolation{Promise: ClientErrorAnswered4xx, RequestID: r.RequestID, Detail: detail})
	}
	if suppressed > 0 {
		// The mock's error shape may be what the gateway read wrong: no verdict either way.
		rep.Checked[ClientErrorAnswered4xx] = notMeasured(fmt.Sprintf(
			"%d requests every execution of which was a client error; %d mismatches reported as observations, the mock's error bodies not declared vLLM-shaped", judged, suppressed))
		return
	}
	rep.Checked[ClientErrorAnswered4xx] = measured(judged, "%d requests every execution of which was a client error", judged)
}

// checkHeap holds the gateway to gateway_heap_bounded: heap growth per request within k times the
// response size.
func checkHeap(in PromiseInput, rep *PromiseReport) {
	if len(in.GatewayHeap) < 2 || len(in.Requests) == 0 {
		rep.Checked[GatewayHeapBounded] = notMeasured("no gateway heap samples")
		return
	}
	// A restarted gateway starts a new heap: growth is taken within each process (the samples of one
	// process are consecutive) and the largest is judged.
	first, peak, processes := in.GatewayHeap[0].HeapInuse, uint64(0), 1
	segFirst, segPeak, worst := first, uint64(0), uint64(0)
	for i, s := range in.GatewayHeap {
		if i > 0 && s.Process != in.GatewayHeap[i-1].Process {
			processes++
			worst = max(worst, segPeak-min(segPeak, segFirst))
			segFirst, segPeak = s.HeapInuse, 0
		}
		peak = max(peak, s.HeapInuse)
		segPeak = max(segPeak, s.HeapInuse)
	}
	worst = max(worst, segPeak-min(segPeak, segFirst))
	n := len(in.Requests)
	growth := float64(worst) / float64(n)
	obs := fmt.Sprintf("%s: gateway heap first %d B, peak %d B, %.0f B per request over %d requests", GatewayHeapBounded, first, peak, growth, n)
	if processes > 1 {
		obs += fmt.Sprintf(" (largest growth within one of %d gateway processes)", processes)
	}
	if in.ResponseBytes <= 0 || in.HeapPerResponseByte == 0 {
		rep.Observations = append(rep.Observations, obs)
		rep.Checked[GatewayHeapBounded] = notMeasured("no k or no response size: " + obs)
		return
	}
	perResponse := float64(in.ResponseBytes) / float64(n)
	rep.Checked[GatewayHeapBounded] = Measure{Measured: true, Judged: n, Note: obs}
	if growth > in.HeapPerResponseByte*perResponse {
		rep.Violations = append(rep.Violations, PromiseViolation{Promise: GatewayHeapBounded,
			Detail: fmt.Sprintf("%.0f B of heap per request against %.0f B of response (k %.1f)", growth, perResponse, in.HeapPerResponseByte)})
	}
}

// checkLiveHeap holds the gateway to gateway_live_heap_returns: once the load is drained, the heap a
// collection finds live returns to within the allowance of the one before the load.
func checkLiveHeap(in PromiseInput, rep *PromiseReport) {
	var before, loadEnd, after *HeapSample
	for i := range in.GatewayHeap {
		switch s := &in.GatewayHeap[i]; s.Phase {
		case "before":
			before = s
		case "load_end":
			loadEnd = s
		case "after":
			after = s
		}
	}
	switch {
	case before == nil || loadEnd == nil || after == nil:
		rep.Checked[GatewayLiveHeapReturns] = notMeasured("no gateway heap readings before the load, at its end and after the drain")
		return
	case before.NextGC == 0 || after.NextGC == 0:
		rep.Checked[GatewayLiveHeapReturns] = notMeasured("the gateway's memstats carry no next_gc")
		return
	case before.Process != after.Process:
		rep.Checked[GatewayLiveHeapReturns] = notMeasured("the gateway restarted between the readings before the load and after the drain")
		return
	case loadEnd.Process == after.Process && after.NumGC <= loadEnd.NumGC:
		// The goal still reflects a collection taken while the load ran.
		rep.Checked[GatewayLiveHeapReturns] = notMeasured(fmt.Sprintf("no collection between the load end and the last reading (num_gc %d)", after.NumGC))
		return
	}
	live0, live1 := before.LiveHeap(), after.LiveHeap()
	unsettled := unsettledInferences(in.Diffs)
	obs := fmt.Sprintf("%s: gateway live heap %d B before the load, %d B after the drain (%+d B; %d collections since the load end; %d inferences on escrows not settled by the export); heap_inuse %d B -> %d B",
		GatewayLiveHeapReturns, live0, live1, int64(live1)-int64(live0), after.NumGC-loadEnd.NumGC, unsettled, before.HeapInuse, after.HeapInuse)
	if in.LiveHeapAllowance == 0 {
		rep.Observations = append(rep.Observations, obs)
		rep.Checked[GatewayLiveHeapReturns] = notMeasured("no allowance: " + obs)
		return
	}
	allowed := in.LiveHeapAllowance + in.LiveHeapPerInference*uint64(unsettled)
	rep.Checked[GatewayLiveHeapReturns] = Measure{Measured: true, Judged: 1, Note: obs}
	if live1 > live0+allowed {
		rep.Violations = append(rep.Violations, PromiseViolation{Promise: GatewayLiveHeapReturns,
			Detail: fmt.Sprintf("live heap %d B after the drain against %d B before the load plus %d B allowed (%d B + %d B for each of %d inferences on unsettled escrows)",
				live1, live0, allowed, in.LiveHeapAllowance, in.LiveHeapPerInference, unsettled)})
	}
}

// unsettledInferences counts the inferences of the escrows not settled on all hosts by the export:
// for each such escrow the highest nonce any host holds.
func unsettledInferences(d ExportDiffs) int {
	highest := map[string]uint64{}
	for _, c := range d.Hosts {
		for ref := range c.Inferences {
			highest[ref.Escrow] = max(highest[ref.Escrow], ref.Inference)
		}
	}
	total := 0
	for escrow, n := range highest {
		if !settledOnAll(d, escrow) {
			total += int(n)
		}
	}
	return total
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
	unweighed := 0 // inferences with invalid votes or challenge faults no host has the session row to weigh
	// chargeOf names what the state machine charged the executor on one host's chain; invalidated
	// says the charge took the Finish's pay back, byVotes that the validators' votes did. weighed is
	// false for invalid votes or challenge faults on a host without the escrow's session row.
	chargeOf := func(chain HostChain, escrow string, f *InferenceFacts) (charge string, invalidated, byVotes, weighed bool) {
		switch {
		case f.TimedOut:
			return "a timeout (miss)", false, false, true
		case f.ErrorMiss:
			return "an error miss", false, false, true
		case len(f.Rejected)+len(f.ChallengeFaults) == 0:
			return "", false, false, true
		}
		// An invalid vote or a faulting challenge invalidates only above the session's vote
		// threshold; below it the Finish stays paid.
		v, ok := chain.Voting[escrow]
		if !ok {
			return "", false, false, false
		}
		if t := v.tally(f.Votes); t.Status == "Invalidated" {
			return fmt.Sprintf("an invalidation by the validators (invalid weight %d above vote threshold %d, slots %v voted invalid)",
				t.Invalid, v.VoteThreshold, f.Rejected), true, true, true
		}
		if w := v.challengeWeight(f.ChallengeFaults); len(f.ChallengeFaults) > 0 && w > v.VoteThreshold {
			return fmt.Sprintf("an invalidation by a usage challenge (fault weight %d above vote threshold %d, slots %v)",
				w, v.VoteThreshold, f.ChallengeFaults), true, false, true
		}
		return "", false, false, true
	}
	judged, forged, caught, stopped, missedStopped := 0, 0, 0, 0, 0
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
		var accepted, rejectedBy []uint32 // the slots of the first host that shows a valid, an invalid vote
		var tally *VoteTally              // where the votes left the inference on the first host that weighs them
		var threshold uint32
		charge, startedAt := "", union[ref].StartedAt
		invalidated, byVotes, loss := false, false, uint64(0)
		var chargeTally VoteTally       // the votes on the first host that shows the charge
		var chargeVoting SessionVoting  // its session
		var chargeFacts *InferenceFacts // its facts
		weighedSomewhere, unweighedSomewhere := false, false
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
				if v, ok := chain.Voting[ref.Escrow]; ok && tally == nil {
					t := v.tally(f.Votes)
					tally, threshold, rejectedBy = &t, v.VoteThreshold, f.Rejected
				}
			}
			c, inv, votes, weighed := chargeOf(chain, ref.Escrow, f)
			if len(f.Rejected)+len(f.ChallengeFaults) > 0 {
				weighedSomewhere = weighedSomewhere || weighed
				unweighedSomewhere = unweighedSomewhere || !weighed
			}
			if c != "" {
				if charge == "" {
					charge, invalidated, byVotes, loss = c, inv, votes, chain.Voting[ref.Escrow].cost(f)
					chargeVoting, chargeFacts = chain.Voting[ref.Escrow], f
					chargeTally = chargeVoting.tally(f.Votes)
				}
				charged = append(charged, chain.Host)
			}
		}
		if unweighedSomewhere && !weighedSomewhere {
			unweighed++
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
		honest, spoiled := true, true
		var answered time.Time // when the ML finished the answer, the closest the logs come to the Finish
		for _, m := range ml {
			honest = honest && m.Status == 200 && m.Outcome == "ok" && m.Rule == "" &&
				(m.Chaos == "" || m.Chaos == mockopenai.ChaosOK)
			spoiled = spoiled && ClientErrorStatuses[m.Status]
			if end := time.UnixMilli(m.StartUnixMs + m.DurationMs); m.StartUnixMs > 0 && end.After(answered) {
				answered = end
			}
		}
		// The validators that voted false because they could not fetch the payload while a fault of
		// the runner stopped the executor's host serving it.
		fetches := in.Layers.stoppedFetches(who.Participant, ref)
		at := time.Unix(startedAt, 0).UTC().Format(time.RFC3339)
		if charge == "" {
			// Not charged, yet voted invalid with the ML answer whole and no validator kept from the
			// payload by a stop of the executor: nothing on the executor's side explains the vote, so
			// the report names it.
			if honest && len(fetches) == 0 && tally != nil {
				rep.Observations = append(rep.Observations, fmt.Sprintf(
					"escrow %s inference %d: executor %s (%s) voted invalid though the ML answered it in full and no validator missed its payload while the runner stopped its host "+
						"(slots %v, on %d of %d hosts, started %s); not invalidated: %s, valid weight %d, invalid %d, vote threshold %d; the executor kept its pay",
					ref.Escrow, ref.Inference, who.Participant, who.Instance, rejectedBy, len(rejected), len(chains), at,
					tally.Status, tally.Valid, tally.Invalid, threshold))
			}
			continue
		}
		judged++
		hosts := fmt.Sprintf("on %d of %d hosts", len(charged), len(chains))
		// An invalidation by the votes is the stop's when the invalid weight of the validators not kept
		// from the payload is not above the vote threshold: without the stop it would not have happened.
		var unexcused uint32
		for _, addr := range chargeTally.invalidBy {
			if _, ok := fetches[addr]; !ok {
				unexcused += chargeVoting.weightOf(addr)
			}
		}
		stop := honest && invalidated && byVotes && len(fetches) > 0 && unexcused <= chargeVoting.VoteThreshold
		// A timeout is the stop's when a fault of the runner stopped the executor's host between the
		// inference's start and its timeout deadline (its timeout from the Start while Pending, from the
		// ConfirmStart while Started, plus the gateway's skew): the host could not finish in time.
		var missStop FaultWindow
		var missFrom, missDue time.Time
		missed := false
		if t, ok := in.Timeouts[ref.Escrow]; ok && honest && chargeFacts != nil && chargeFacts.TimedOut {
			from, limit := chargeFacts.StartedAt, time.Duration(t.Refusal)*time.Second
			if chargeFacts.ConfirmedAt != 0 {
				from, limit = chargeFacts.ConfirmedAt, time.Duration(t.Execution)*time.Second
			}
			missFrom, missDue = time.Unix(chargeFacts.StartedAt, 0), time.Unix(from, 0).Add(limit+TimeoutSkew)
			missStop, missed = in.Layers.stopBetween(who.Participant, missFrom, missDue)
		}
		switch {
		case spoiled:
			rep.Violations = append(rep.Violations, PromiseViolation{Promise: ChargeOnlyTheGuilty, Host: strings.Join(charged, ","), Inference: &r,
				Detail: fmt.Sprintf("executor %s (%s) charged %s %s for a request the ML rejected as a client error (started %s)",
					who.Participant, who.Instance, charge, hosts, at)})
		case stop:
			// The executor answered in full and a fault of the runner stopped its host before validation:
			// picked for validation, it did not serve its payload with the logprobs, and the validators
			// voted the result invalid, as they do with a host that withholds it: by design, not a violation.
			stopped++
			rep.ChargedWhileStopped = append(rep.ChargedWhileStopped, PromiseViolation{Promise: ChargeOnlyTheGuilty, Host: strings.Join(charged, ","), Inference: &r,
				Detail: fmt.Sprintf("executor %s (%s) charged %s %s; the ML answered it in full at %s, then %s, so validators voted it invalid; "+
					"without their votes the invalid weight is %d, not above the vote threshold %d; the executor lost %d",
					who.Participant, who.Instance, charge, hosts, answered.UTC().Format(time.RFC3339Nano), describeStoppedFetches(fetches),
					unexcused, chargeVoting.VoteThreshold, loss)})
		case missed:
			// The executor took the inference and a fault of the runner stopped its host before it could
			// finish: the gateway timed it out and the votes charged the miss, as the protocol does with
			// any executor that does not finish in time: by design, not a violation.
			missedStopped++
			to := "not ended"
			if !missStop.To.IsZero() {
				to = "to " + missStop.To.UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano)
			}
			rep.MissedWhileStopped = append(rep.MissedWhileStopped, PromiseViolation{Promise: ChargeOnlyTheGuilty, Host: strings.Join(charged, ","), Inference: &r,
				Detail: fmt.Sprintf("executor %s (%s) charged %s %s; missed because a fault of the runner stopped the executor's host before it could finish: "+
					"the runner's fault %q stopped %s from %s %s, between the inference's start at %s and its timeout deadline at %s; the ML answered it in full at %s",
					who.Participant, who.Instance, charge, hosts, missStop.Event, missStop.Host,
					missStop.From.UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano), to,
					missFrom.UTC().Format(time.RFC3339), missDue.UTC().Format(time.RFC3339), answered.UTC().Format(time.RFC3339Nano))})
		case honest:
			rep.Violations = append(rep.Violations, PromiseViolation{Promise: ChargeOnlyTheGuilty, Host: strings.Join(charged, ","), Inference: &r,
				Detail: fmt.Sprintf("executor %s (%s) charged %s %s though the ML answered it in full (%d executions, started %s)",
					who.Participant, who.Instance, charge, hosts, len(ml), at)})
		}
	}
	if unweighed > 0 {
		rep.Checked[ChargeOnlyTheGuilty] = notMeasured(fmt.Sprintf(
			"%d inferences with an invalid vote or a challenge not weighed: no host holds the escrow's session row, so an honest executor's invalidation cannot be told; "+
				"%d charged inferences matched to their ML execution, %d of them invalidated after an honest execution while the runner stopped the executor's host, "+
				"%d missed because a fault of the runner stopped the executor's host before it could finish, %d forged answers validated",
			unweighed, judged, stopped, missedStopped, forged))
		return
	}
	rep.Checked[ChargeOnlyTheGuilty] = measured(judged+forged,
		"%d charged inferences matched to their ML execution; %d invalidated after an honest execution because the executor, stopped before validation, did not serve its payload with the logprobs (by design: #1647, #2002), not violations; "+
			"%d missed because a fault of the runner stopped the executor's host before it could finish, not violations; %d forged answers validated, %d of them voted invalid",
		judged, stopped, missedStopped, forged, caught)
}

// describeStoppedFetches says which validators missed the payload, when, and the fault that stopped the
// executor's host then.
func describeStoppedFetches(fetches map[string]stoppedFetch) string {
	addrs := make([]string, 0, len(fetches))
	for a := range fetches {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	var parts []string
	for _, a := range addrs {
		f := fetches[a]
		to := "not ended"
		if !f.Window.To.IsZero() {
			to = "to " + f.Window.To.UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano)
		}
		parts = append(parts, fmt.Sprintf("validator %s (%s) could not fetch its payload at %s (%s) while the runner's fault %q stopped its host serving it, from %s %s",
			f.Miss.ValidatorHost, a, f.Miss.At.UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano), f.Miss.Cause,
			f.Window.Event, f.Window.From.UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano), to))
	}
	return strings.Join(parts, "; ")
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

// settledOnAll says whether every host that holds the escrow's sessions row has it settled. An
// export without sessions tables cannot tell and counts it as settled.
func settledOnAll(d ExportDiffs, escrow string) bool {
	for _, c := range d.Hosts {
		if st, ok := c.Status[escrow]; ok && st != "settled" {
			return false
		}
	}
	return true
}

// checkConvergence holds the hosts of each escrow to hosts_converge: the same last nonce and the
// same post_state_root at every nonce two hosts both hold.
func checkConvergence(in PromiseInput, rep *PromiseReport) {
	if len(in.Diffs.Hosts) < 2 {
		rep.Checked[HostsConverge] = notMeasured("fewer than two host chains")
		return
	}
	escrows := map[string]bool{}
	refsOf := map[string][]InferenceRef{}
	for _, c := range in.Diffs.Hosts {
		for e := range c.Roots {
			escrows[e] = true
		}
		for ref := range c.Inferences {
			refsOf[ref.Escrow] = append(refsOf[ref.Escrow], ref)
		}
	}
	names := make([]string, 0, len(escrows))
	for e := range escrows {
		names = append(names, e)
	}
	sort.Strings(names)
	settled := 0
	// Convergence asks whether every host had the time to finish, so it reads every host's facts.
	everyHost := make([]map[string]uint64, len(in.Diffs.Hosts))
	for _, e := range names {
		last := map[string]uint64{}
		top, lagging := uint64(0), []string{}
		for _, c := range in.Diffs.Hosts {
			last[c.Host] = c.LastNonce(e)
			top = max(top, last[c.Host])
		}
		for _, c := range in.Diffs.Hosts {
			if last[c.Host] < top {
				lagging = append(lagging, fmt.Sprintf("%s at %d", c.Host, last[c.Host]))
			}
		}
		// Hosts propose diffs in turn, so right after the load each one holds its own latest diff
		// and lags the next by a nonce. Lag counts only once every inference of the escrow was due.
		due, known := settledBy(in, everyHost, refsOf[e])
		isSettled := !in.ObservedUntil.IsZero() && due && known
		if isSettled {
			settled++
		}
		// The databases are dumped one after another, and an escrow still active at the export keeps
		// taking diffs (timeout sweeps, votes) that reach each host with its next dispatch: its lag is
		// the export in flight. Lag is a violation only once every host holds the escrow settled.
		if len(lagging) > 0 && isSettled {
			detail := fmt.Sprintf("escrow %s: highest nonce %d, behind it %s", e, top, strings.Join(lagging, ", "))
			if settledOnAll(in.Diffs, e) {
				rep.Violations = append(rep.Violations, PromiseViolation{Promise: HostsConverge, Detail: detail})
			} else {
				rep.Observations = append(rep.Observations, HostsConverge+" lag on an escrow not settled at the export: "+detail)
			}
		}
		// A fork shows at its first nonce; every later root differs too, so only the first is reported.
		// The reference is the first host that holds the escrow's chain.
		var ref *HostChain
		for i := range in.Diffs.Hosts {
			if c := &in.Diffs.Hosts[i]; ref == nil && len(c.Roots[e]) > 0 {
				ref = c
			}
		}
		for _, c := range in.Diffs.Hosts {
			if ref == nil || c.Host == ref.Host {
				continue
			}
			nonces := make([]uint64, 0, len(c.Roots[e]))
			for n := range c.Roots[e] {
				nonces = append(nonces, n)
			}
			sort.Slice(nonces, func(i, j int) bool { return nonces[i] < nonces[j] })
			for _, n := range nonces {
				if want, ok := ref.Roots[e][n]; ok && want != c.Roots[e][n] {
					rep.Violations = append(rep.Violations, PromiseViolation{Promise: HostsConverge, Host: c.Host,
						Detail: fmt.Sprintf("escrow %s: post_state_root differs from %s from nonce %d on", e, ref.Host, n)})
					break
				}
			}
		}
	}
	rep.Checked[HostsConverge] = measured(len(names), "%d escrows compared across %d hosts (roots); lag judged on %d settled by the export",
		len(names), len(in.Diffs.Hosts), settled)
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
