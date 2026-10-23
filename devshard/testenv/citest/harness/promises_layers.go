package harness

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"devshard/testenv/mockopenai"
)

// ReadExecutors names the host that executed each inference of a load-test run, from the
// compose.log the runner keeps. A host writes one "devshard request terminal" line for every
// inference it executed (observability.recordTerminal), carrying the escrow and the inference id;
// validators do not write it. Unless a scenario binds each ML node to one host, the nodes are one
// pool every host draws from, so the executor is known per inference, not per node. A line without
// an inference id (a receipt the host never signed) names no execution.
func ReadExecutors(composeLog string) (map[InferenceRef]string, error) {
	f, err := os.Open(composeLog)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[InferenceRef]string{}
	marker := []byte(`"msg":"devshard request terminal"`)
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && bytes.Contains(line, marker) {
			if ref, host, ok := terminalLine(line); ok {
				out[ref] = host
			}
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// terminalLine takes one `service-1 | {json}` line of `docker compose logs` written by a host.
func terminalLine(line []byte) (InferenceRef, string, bool) {
	prefix, body, ok := bytes.Cut(line, []byte(" | "))
	if !ok {
		return InferenceRef{}, "", false
	}
	start := bytes.IndexByte(body, '{')
	if start < 0 {
		return InferenceRef{}, "", false
	}
	var e struct {
		Escrow    string `json:"escrow_id"`
		Inference uint64 `json:"inference_id"`
	}
	if json.Unmarshal(body[start:], &e) != nil || e.Escrow == "" || e.Inference == 0 {
		return InferenceRef{}, "", false
	}
	// The prefix is the container: the service and its replica number.
	service := strings.TrimSpace(string(prefix))
	if i := strings.LastIndexByte(service, '-'); i > 0 {
		if _, err := strconv.Atoi(service[i+1:]); err == nil {
			service = service[:i]
		}
	}
	return InferenceRef{Escrow: e.Escrow, Inference: e.Inference}, service, true
}

// ChallengeWave counts what the gateway logged of the usage challenges of a run: each challenge's
// outcome and reason (challenge_finished, or challenge_failed when it could not run), and how the
// voters it asked answered (challenge_vote*). A voter asked again after a 429 or 503 leaves no line
// per attempt: VoteBusy counts the ones still busy at the challenge deadline.
type ChallengeWave struct {
	Outcomes   map[string]int `json:"outcomes"`
	Reasons    map[string]int `json:"reasons"`
	Votes      int            `json:"votes"`
	Rejected   map[string]int `json:"rejected_by_cause"`
	NotAwaited int            `json:"not_awaited"`
	VoteFailed int            `json:"vote_failed"`
	VoteBusy   int            `json:"vote_failed_busy"`
	// TriggerToVerdictMs is, for every challenge whose start was logged, the milliseconds from
	// challenge_triggered to its outcome, in increasing order. An empty-stream challenge starts when
	// the gateway applies the Finish.
	TriggerToVerdictMs []int64 `json:"trigger_to_verdict_ms,omitempty"`
}

// Asks is how many voter answers the gateway logged.
func (w ChallengeWave) Asks() int {
	n := w.Votes + w.NotAwaited + w.VoteFailed
	for _, c := range w.Rejected {
		n += c
	}
	return n
}

// ReadChallengeWave reads the gateway's challenge lines from compose.log. It returns nil when the
// gateway logged none, as a gateway without the challenge does.
func ReadChallengeWave(composeLog string) (*ChallengeWave, error) {
	f, err := os.Open(composeLog)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	w := &ChallengeWave{Outcomes: map[string]int{}, Reasons: map[string]int{}, Rejected: map[string]int{}}
	triggered := map[string]time.Time{}
	seen := false
	marker := []byte(`"msg":"challenge_`)
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && bytes.Contains(line, marker) && w.gatewayLine(line, triggered) {
			seen = true
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if !seen {
		return nil, nil
	}
	slices.Sort(w.TriggerToVerdictMs)
	return w, nil
}

// gatewayLine counts one `devshardctl-1 | {json}` line and says whether it was a challenge line.
// triggered keeps the start of every challenge still running, by escrow and nonce.
func (w *ChallengeWave) gatewayLine(line []byte, triggered map[string]time.Time) bool {
	service, body, ok := bytes.Cut(line, []byte(" | "))
	if !ok || !bytes.HasPrefix(bytes.TrimSpace(service), []byte("devshardctl")) {
		return false
	}
	start := bytes.IndexByte(body, '{')
	if start < 0 {
		return false
	}
	var e struct {
		Time        time.Time       `json:"time"`
		Msg         string          `json:"msg"`
		Escrow      string          `json:"escrow"`
		Nonce       json.RawMessage `json:"nonce"`
		Reason      string          `json:"reason"`
		Outcome     string          `json:"outcome"`
		RejectCause string          `json:"reject_cause"`
		Error       string          `json:"error"`
	}
	if json.Unmarshal(body[start:], &e) != nil {
		return false
	}
	key := e.Escrow + "/" + string(e.Nonce)
	verdict := func() {
		if at, ok := triggered[key]; ok && !e.Time.IsZero() {
			w.TriggerToVerdictMs = append(w.TriggerToVerdictMs, e.Time.Sub(at).Milliseconds())
			delete(triggered, key)
		}
	}
	switch e.Msg {
	case "challenge_triggered":
		triggered[key] = e.Time
	case "challenge_finished":
		w.Outcomes[e.Outcome]++
		w.Reasons[e.Reason]++
		verdict()
	case "challenge_failed":
		w.Outcomes["failed"]++
		w.Reasons[e.Reason]++
		verdict()
	case "challenge_vote":
		w.Votes++
	case "challenge_vote_rejected":
		w.Rejected[e.RejectCause]++
	case "challenge_vote_not_awaited":
		w.NotAwaited++
	case "challenge_vote_failed":
		w.VoteFailed++
		// The voter's last answer before the deadline: the connect code of a 429 or a 503.
		if strings.Contains(e.Error, "resource_exhausted") || strings.Contains(e.Error, "unavailable") {
			w.VoteBusy++
		}
	default:
		return false
	}
	return true
}

// LoadtestLayers is what the lock, nonce and quarantine checks read from a load-test run: the
// ML-node lock journal of mock-dapi, the nonces the gateway burned without sending, the gateway's
// per-host failure events, its quarantines and the runner's host fault windows. All of it comes from
// files the runner already writes: compose.log, requests.jsonl and host-faults.json (or events.json).
type LoadtestLayers struct {
	Locks map[string]*NodeLock // by lock id
	// LockLogEnd is the last mock-dapi line: locks taken within LockGrace of it may still be released.
	LockLogEnd time.Time
	Burns      []NonceBurn
	Strikes    []HostStrike
	// RequestsPerMinute counts the client's requests by the minute they started (UTC, truncated).
	RequestsPerMinute map[time.Time]int
	Transitions       []QuarantineTransition
	FaultWindows      []FaultWindow
	// PayloadMisses are the validations a host voted false because it could not fetch the executor's
	// payload.
	PayloadMisses []PayloadMiss
	// GatewayHostLines counts gateway log lines that carried a host label, and GatewayLines every
	// gateway log line read: a run whose gateway logged nothing reads as not measured instead of as
	// zero burns or zero quarantines.
	GatewayHostLines int
	GatewayLines     int
	// LogCut says why compose.log does not hold the whole run (the runner kept only the tail of each
	// container's log); the checks that read it then give no verdict. Empty when it is whole.
	LogCut string
	// MLUnknown says why the ML nodes' faults are not known (no mock log, or one the export could not
	// take whole); a quarantine may then be a node's due, and quarantine_only_when_faulted gives no
	// verdict. Empty when the mock log is whole.
	MLUnknown string
}

// NodeLock is one ML-node lock mock-dapi handed out, and whether a release for it came back. A lock id
// mock-dapi hands out again (its counter restarts with it) is a new lock; the earlier one is kept
// under the id and its acquire time.
type NodeLock struct {
	ID         string
	Node       string
	Request    string
	AcquiredAt time.Time
	Released   bool
	Outcome    string // the release outcome; empty while unreleased
}

// NonceBurn is a nonce the gateway committed for a host's slot and never sent (ghost_probe_skipped).
type NonceBurn struct {
	At     time.Time
	Escrow string
	Host   string // the last 8 characters of the participant address
	Reason string
}

// HostStrike is a gateway event that counts against a host: an empty stream, a suppressed stream, a
// failed attempt, a deferred suspicious winner or a skipped escalation.
type HostStrike struct {
	At     time.Time
	Host   string
	Stage  string
	Escrow string
	// StreamBytes is how much of the stream the gateway read before it judged it empty.
	StreamBytes int64
}

// QuarantineTransition is one quarantine the gateway put a participant in.
type QuarantineTransition struct {
	At          time.Time
	Participant string
	Mode        string
	Reason      string
}

// FaultWindow is a runner fault aimed at one participant, with the grace given after it. A window
// with no end stays open. StopsPayload says the fault keeps the host from serving its payload to a
// validator: a pause, a kill or restart of its container, or its public port taken down.
type FaultWindow struct {
	Event        string    `json:"event"`
	Host         string    `json:"host"`
	Participant  string    `json:"participant"`
	From         time.Time `json:"from"`
	To           time.Time `json:"to"`
	GracedTo     time.Time `json:"graced_to"`
	StopsPayload bool      `json:"stops_payload,omitempty"`
}

// PayloadMiss is a validation a host voted false because the executor's payload could not be
// fetched: the line payloadMissMsg it writes when it gives up (executorFaultVerdict,
// cmd/devshardd/inference/validator.go), which the vote carries as the reason
// executor_payload_unavailable. At is when it gave up.
type PayloadMiss struct {
	At            time.Time `json:"at"`
	Escrow        string    `json:"escrow_id"`
	Inference     uint64    `json:"inference_id"`
	ValidatorHost string    `json:"validator_host"`
	Validator     string    `json:"validator"` // the validator host's address, once its identity line is read
	Executor      string    `json:"executor_address"`
	Cause         string    `json:"cause"`
}

// payloadMissMsg is the line a validator writes when it votes false for want of the executor's
// payload.
const payloadMissMsg = "validation voting false: executor payload unavailable"

// publicDownTripFile is the trip file that takes a host's public port down (PublicFaultDownFile,
// cmd/devshardd/public_fault_testenv.go): every public route, the payload one among them, answers 503.
const publicDownTripFile = "devshard-fault-public-down"

// payloadStoppingActions are the runner event actions that stop a host serving its payload; a
// tripfile does when it is publicDownTripFile. A throttle slows the host, a PoC challenge takes its
// ML nodes, a marker or a chain participant mode changes what the chain or the gateway think of it:
// none of them keeps a validator from its payload.
var payloadStoppingActions = map[string]bool{"pause": true, "container_kill": true, "container_restart": true}

var strikeStages = map[string]bool{
	"empty_stream": true, "stream_suppressed": true, "attempt_failed": true,
	"suspicious_winner_deferred": true, "escalation_skipped": true,
}

// quarantineLines are the gateway log messages written next to every quarantine transition
// (cmd/devshardctl/participant_limiter.go), with the mode and reason the transition records.
// participant_limit_activated is the HTTP quarantine; its reason depends on the answer, so the
// report names the status instead. A line that names its quarantine_mode overrides the mode here.
var quarantineLines = map[string][2]string{
	"participant_limit_activated":                        {"probe", ""},
	"participant_limit_escrow_lookup_limited_quarantine": {"probe", "escrow_lookup_limited_quarantine"},
	"participant_limit_eof_transport_quarantine":         {"probe", "eof_transport_quarantine"},
	"participant_limit_transport_failure":                {"probe", "transport_failure_quarantine"},
	"participant_limit_empty_stream_quarantine":          {"shadow", "empty_stream_quarantine"},
	"participant_limit_stalled_winner_quarantine":        {"shadow", "stalled_winner_quarantine"},
}

// hostIdentityMsg is the line every host writes at start with the address it signs as
// (cmd/devshardd/app.go); it ties a host service to its participant.
const hostIdentityMsg = "devshardd: validation lease identity"

// ReadLoadtestLayers reads compose.log, requests.jsonl and host-faults.json of a runner output
// directory, or events.json when there is no host-faults.json. A run without compose.log has no layers.
func ReadLoadtestLayers(dir string) (*LoadtestLayers, error) {
	l := &LoadtestLayers{Locks: map[string]*NodeLock{}, RequestsPerMinute: map[time.Time]int{}}
	participants := map[string]string{}
	if err := l.readComposeLog(filepath.Join(dir, "compose.log"), participants); err != nil {
		return nil, err
	}
	for i := range l.PayloadMisses {
		l.PayloadMisses[i].Validator = participants[l.PayloadMisses[i].ValidatorHost]
	}
	if err := readJSONLines(filepath.Join(dir, "requests.jsonl"), func(line []byte) error {
		var r struct {
			StartedAt time.Time `json:"started_at"`
			Outcome   string    `json:"outcome"`
		}
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		// A request the generator dropped never reached the gateway.
		if !r.StartedAt.IsZero() && r.Outcome != "dropped_by_generator" {
			l.RequestsPerMinute[r.StartedAt.UTC().Truncate(time.Minute)]++
		}
		return nil
	}); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var err error
	if l.FaultWindows, err = faultWindowsFromHostFaults(dir, participants); err != nil {
		return nil, err
	}
	if _, statErr := os.Stat(filepath.Join(dir, "host-faults.json")); os.IsNotExist(statErr) {
		if l.FaultWindows, err = faultWindowsFromEvents(dir, participants); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// faultWindowsFromEvents builds the fault windows from events.json, the record a runner with an
// event timeline (workload.events, host_faults among them as pause events) keeps of the faults it
// ran. Every event aimed at a host service opens a window from its start to its end: a pause, a
// throttle, a restart or kill, a trip file, a chain participant mode, a PoC challenge or a marker.
// Events aimed at an ML node or the whole stack name no host. An event that never started opens no
// window; one that started and never ended stays open to the end of the run.
func faultWindowsFromEvents(dir string, participants map[string]string) ([]FaultWindow, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "events.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var events []struct {
		ID        string     `json:"id"`
		Action    string     `json:"action"`
		Target    string     `json:"target"`
		StartedAt *time.Time `json:"started_at"`
		EndedAt   *time.Time `json:"ended_at"`
		Params    struct {
			File string `json:"file"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		return nil, fmt.Errorf("events.json: %w", err)
	}
	var out []FaultWindow
	for _, e := range events {
		if e.StartedAt == nil || !hostEventTargets.MatchString(e.Target) {
			continue
		}
		w := FaultWindow{Event: e.ID + " " + e.Action, Host: e.Target, Participant: participants[e.Target], From: *e.StartedAt,
			StopsPayload: payloadStoppingActions[e.Action] || e.Action == "tripfile" && filepath.Base(e.Params.File) == publicDownTripFile}
		if e.EndedAt != nil {
			w.To, w.GracedTo = *e.EndedAt, e.EndedAt.Add(faultGrace)
		}
		out = append(out, w)
	}
	return out, nil
}

// hostEventTargets are the host services an event can aim at.
var hostEventTargets = regexp.MustCompile(`^versiond-[0-9]+$`)

// faultGrace is how long after a host fault ends its participant is still taken as faulted, before
// the run's timeouts are added (GraceFaultWindows): a host that just came back still holds the
// inferences it took before the pause, and the gateway times them out only after their timeout, the
// vote window and a sweep, quarantining the host meanwhile.
const faultGrace = TimeoutSkew + TimeoutVoteWindow + TimeoutSweepPeriod

// stoppedFetches names, by validator address, the payload misses of an inference that fell inside a
// fault window of the executor's host that stopped it serving its payload, with that window.
func (l *LoadtestLayers) stoppedFetches(executorHost string, ref InferenceRef) map[string]stoppedFetch {
	if l == nil || executorHost == "" {
		return nil
	}
	out := map[string]stoppedFetch{}
	for _, m := range l.PayloadMisses {
		if m.Escrow != ref.Escrow || m.Inference != ref.Inference || m.Validator == "" {
			continue
		}
		for _, w := range l.FaultWindows {
			if w.Host == executorHost && w.StopsPayload && !m.At.Before(w.From) && (w.To.IsZero() || !m.At.After(w.To)) {
				if _, seen := out[m.Validator]; !seen {
					out[m.Validator] = stoppedFetch{Miss: m, Window: w}
				}
				break
			}
		}
	}
	return out
}

// stopBetween returns a fault window of the host that stopped it serving and overlaps from..to.
func (l *LoadtestLayers) stopBetween(host string, from, to time.Time) (FaultWindow, bool) {
	if l == nil || host == "" {
		return FaultWindow{}, false
	}
	for _, w := range l.FaultWindows {
		if w.Host == host && w.StopsPayload && !w.From.After(to) && (w.To.IsZero() || !w.To.Before(from)) {
			return w, true
		}
	}
	return FaultWindow{}, false
}

// stoppedFetch is a payload miss and the fault window of the executor's host it fell in.
type stoppedFetch struct {
	Miss   PayloadMiss
	Window FaultWindow
}

// GraceFaultWindows extends the grace after every fault window by the largest refusal plus execution
// timeout of the run's escrows: an inference the host took just before the pause is timed out that
// much later.
func (l *LoadtestLayers) GraceFaultWindows(timeouts map[string]EscrowTimeouts) {
	var longest int64
	for _, t := range timeouts {
		longest = max(longest, t.Refusal+t.Execution)
	}
	for i := range l.FaultWindows {
		if w := &l.FaultWindows[i]; !w.To.IsZero() {
			w.GracedTo = w.To.Add(faultGrace + time.Duration(longest)*time.Second)
		}
	}
}

// faultWindowsFromHostFaults builds the fault windows from host-faults.json, the record the runner
// keeps of the pauses it ran (workload.host_faults): the host, when it was paused and resumed.
// participants names the address behind each host service. A fault that failed to pause the host
// opens no window; one whose host was not seen resumed stays open to the end of the run.
func faultWindowsFromHostFaults(dir string, participants map[string]string) ([]FaultWindow, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "host-faults.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var faults []struct {
		Host      string    `json:"host"`
		Mode      string    `json:"mode"`
		PausedAt  time.Time `json:"paused_at"`
		ResumedAt time.Time `json:"resumed_at"`
	}
	if err := json.Unmarshal(raw, &faults); err != nil {
		return nil, fmt.Errorf("host-faults.json: %w", err)
	}
	var out []FaultWindow
	for i, f := range faults {
		if f.PausedAt.IsZero() {
			continue
		}
		w := FaultWindow{Event: fmt.Sprintf("host_faults[%d] %s", i, f.Mode), Host: f.Host,
			Participant: participants[f.Host], From: f.PausedAt, StopsPayload: true}
		if !f.ResumedAt.IsZero() {
			w.To, w.GracedTo = f.ResumedAt, f.ResumedAt.Add(faultGrace)
		}
		out = append(out, w)
	}
	return out, nil
}

func (l *LoadtestLayers) readComposeLog(path string, participants map[string]string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			l.composeLine(line, participants)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// composeLine takes one `service-1 | {json}` line of `docker compose logs`.
func (l *LoadtestLayers) composeLine(line []byte, participants map[string]string) {
	service, body, ok := bytes.Cut(line, []byte(" | "))
	if !ok {
		return
	}
	start := bytes.IndexByte(body, '{')
	if start < 0 {
		return
	}
	body = body[start:]
	switch {
	case bytes.HasPrefix(service, []byte("mock-dapi")):
		if !bytes.Contains(body, []byte(`"msg":"mlnode_`)) {
			return
		}
		var e struct {
			Time    time.Time `json:"time"`
			Msg     string    `json:"msg"`
			Outcome string    `json:"outcome"`
			LockID  string    `json:"lock_id"`
			NodeID  string    `json:"node_id"`
			Request string    `json:"request_id"`
		}
		if json.Unmarshal(body, &e) != nil {
			return
		}
		if e.Time.After(l.LockLogEnd) {
			l.LockLogEnd = e.Time
		}
		switch {
		case e.Msg == "mlnode_acquire" && e.Outcome == "acquired" && e.LockID != "":
			if lock, seen := l.Locks[e.LockID]; seen {
				if lock.AcquiredAt.IsZero() {
					// Its release was logged first.
					lock.Node, lock.Request, lock.AcquiredAt = e.NodeID, e.Request, e.Time
					return
				}
				l.Locks[e.LockID+"@"+lock.AcquiredAt.UTC().Format(time.RFC3339Nano)] = lock
			}
			l.Locks[e.LockID] = &NodeLock{ID: e.LockID, Node: e.NodeID, Request: e.Request, AcquiredAt: e.Time}
		case e.Msg == "mlnode_release" && e.LockID != "":
			lock, seen := l.Locks[e.LockID]
			if !seen {
				// A release logged before its acquire still closes the lock.
				lock = &NodeLock{ID: e.LockID, Node: e.NodeID}
				l.Locks[e.LockID] = lock
			}
			lock.Released, lock.Outcome = true, e.Outcome
		}
	case bytes.HasPrefix(service, []byte("devshardctl")):
		var e struct {
			Time        time.Time `json:"time"`
			Stage       string    `json:"stage"`
			Msg         string    `json:"msg"`
			Escrow      string    `json:"escrow"`
			Host        string    `json:"host"`
			Reason      string    `json:"reason"`
			StreamBytes int64     `json:"stream_bytes_read"`
		}
		if json.Unmarshal(body, &e) != nil {
			return
		}
		l.GatewayLines++
		if t, ok := quarantineLine(e.Time, e.Msg); ok {
			l.Transitions = append(l.Transitions, t)
			return
		}
		if e.Host == "" {
			return
		}
		l.GatewayHostLines++
		stage := e.Stage
		if stage == "" {
			stage = e.Msg
		}
		switch {
		case stage == "ghost_probe_skipped":
			l.Burns = append(l.Burns, NonceBurn{At: e.Time, Escrow: e.Escrow, Host: e.Host, Reason: e.Reason})
		case strikeStages[stage]:
			l.Strikes = append(l.Strikes, HostStrike{At: e.Time, Host: e.Host, Stage: stage, Escrow: e.Escrow, StreamBytes: e.StreamBytes})
		}
	case bytes.HasPrefix(service, []byte("versiond")):
		identity, miss := bytes.Contains(body, []byte(hostIdentityMsg)), bytes.Contains(body, []byte(payloadMissMsg))
		if !identity && !miss {
			return
		}
		var e struct {
			Time      time.Time `json:"time"`
			Msg       string    `json:"msg"`
			Address   string    `json:"instance_address"`
			Escrow    string    `json:"escrow_id"`
			Inference uint64    `json:"inference_id"`
			Executor  string    `json:"executor_address"`
			Cause     string    `json:"cause"`
		}
		if json.Unmarshal(body, &e) != nil {
			return
		}
		// The prefix is the container: the service and its replica number.
		name := strings.TrimSpace(string(service))
		if i := strings.LastIndexByte(name, '-'); i > 0 {
			if _, err := strconv.Atoi(name[i+1:]); err == nil {
				name = name[:i]
			}
		}
		switch {
		case e.Msg == hostIdentityMsg && e.Address != "":
			participants[name] = e.Address
		case e.Msg == payloadMissMsg && e.Escrow != "" && e.Inference != 0:
			l.PayloadMisses = append(l.PayloadMisses, PayloadMiss{At: e.Time, Escrow: e.Escrow, Inference: e.Inference,
				ValidatorHost: name, Executor: e.Executor, Cause: e.Cause})
		}
	}
}

// quarantineLine reads a gateway "participant_limit_... participant_key=<address> ..." message.
func quarantineLine(at time.Time, msg string) (QuarantineTransition, bool) {
	fields := strings.Fields(msg)
	if len(fields) < 2 {
		return QuarantineTransition{}, false
	}
	kind, ok := quarantineLines[fields[0]]
	if !ok {
		return QuarantineTransition{}, false
	}
	t := QuarantineTransition{At: at, Mode: kind[0], Reason: kind[1]}
	for _, f := range fields[1:] {
		key, value, _ := strings.Cut(f, "=")
		switch {
		case key == "participant_key":
			t.Participant = value
		case key == "status" && t.Reason == "":
			t.Reason = "status " + value
		case key == "quarantine_mode" && value != "":
			t.Mode = value
		}
	}
	return t, t.Participant != ""
}

// hostLabel is the gateway's host label of a participant: the last 8 characters of its address.
func hostLabel(participant string) string {
	if len(participant) <= 8 {
		return participant
	}
	return participant[len(participant)-8:]
}

// faultedAt reports whether the runner aimed a fault at the participant around t (its window plus grace).
func (l *LoadtestLayers) faultedAt(participant string, t time.Time) bool {
	label := hostLabel(participant)
	for _, w := range l.FaultWindows {
		if w.Participant != participant && hostLabel(w.Participant) != label {
			continue
		}
		if !t.Before(w.From) && (w.GracedTo.IsZero() || !t.After(w.GracedTo)) {
			return true
		}
	}
	return false
}

// checkLockBalance holds the run to ml_node_locks_released: every ML-node lock a host took comes
// back to node-manager. A lock taken within LockGrace of the end of the journal may still be
// released and is not judged.
func checkLockBalance(in PromiseInput, rep *PromiseReport) {
	l := in.Layers
	if l == nil || len(l.Locks) == 0 {
		rep.Checked[NodeLocksReleased] = notMeasured("no mock-dapi lock journal in compose.log")
		return
	}
	if l.LogCut != "" {
		rep.Checked[NodeLocksReleased] = notMeasured(l.LogCut)
		return
	}
	grace := in.LockGrace
	if grace == 0 {
		grace = 3 * time.Minute
	}
	cutoff := l.LockLogEnd.Add(-grace)
	type nodeTally struct {
		acquired, released int
		unreleased         []*NodeLock
	}
	nodes := map[string]*nodeTally{}
	judged, open := 0, 0
	for _, lock := range l.Locks {
		if lock.AcquiredAt.IsZero() {
			continue
		}
		n := nodes[lock.Node]
		if n == nil {
			n = &nodeTally{}
			nodes[lock.Node] = n
		}
		n.acquired++
		switch {
		case lock.Released:
			n.released++
		case lock.AcquiredAt.After(cutoff):
			open++
			continue
		default:
			n.unreleased = append(n.unreleased, lock)
		}
		judged++
	}
	names := make([]string, 0, len(nodes))
	for name := range nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	var summary []string
	for _, name := range names {
		n := nodes[name]
		summary = append(summary, fmt.Sprintf("%s %d/%d", name, n.released, n.acquired))
		if len(n.unreleased) == 0 {
			continue
		}
		sort.Slice(n.unreleased, func(i, j int) bool { return n.unreleased[i].AcquiredAt.Before(n.unreleased[j].AcquiredAt) })
		kinds := map[string]int{}
		for _, lock := range n.unreleased {
			kind, _, _ := strings.Cut(lock.Request, "-")
			kinds[kind]++
		}
		first, last := n.unreleased[0], n.unreleased[len(n.unreleased)-1]
		rep.Violations = append(rep.Violations, PromiseViolation{Promise: NodeLocksReleased, Node: name, RequestID: first.Request,
			Detail: fmt.Sprintf("%d of %d locks on %s never released (acquired %s to %s; by request kind %v)",
				len(n.unreleased), n.acquired, name, first.AcquiredAt.UTC().Format(time.RFC3339), last.AcquiredAt.UTC().Format(time.RFC3339), kinds)})
	}
	rep.Checked[NodeLocksReleased] = measured(judged, "%d ML-node locks judged, released/acquired by node: %s; %d taken within %s of the journal end not judged",
		judged, strings.Join(summary, ", "), open, grace)
}

// checkNonceBurn holds the run to one_nonce_per_request: a request spends one nonce. Burns are
// counted per host and minute against the client's requests of that minute; BurnAllowance is the
// burns per request a minute may reach (zero, or no positive number, reports without a verdict).
// Burns of a host inside its fault windows are the fault's and are not judged; burns in a minute
// without requests have nothing to be weighed against and are reported.
func checkNonceBurn(in PromiseInput, rep *PromiseReport) {
	l := in.Layers
	if l != nil && l.LogCut != "" {
		rep.Checked[OneNoncePerRequest] = notMeasured(l.LogCut)
		return
	}
	if l == nil || len(l.RequestsPerMinute) == 0 || l.GatewayHostLines == 0 {
		rep.Checked[OneNoncePerRequest] = notMeasured("no request log, or no gateway log line with a host label")
		return
	}
	type key struct {
		host   string
		minute time.Time
	}
	burns := map[key]int{}
	reasons := map[string]map[string]int{}
	faulted := 0
	for _, b := range l.Burns {
		if l.faultedAt(b.Host, b.At) {
			faulted++
			continue
		}
		k := key{b.Host, b.At.UTC().Truncate(time.Minute)}
		burns[k]++
		if reasons[b.Host] == nil {
			reasons[b.Host] = map[string]int{}
		}
		reasons[b.Host][b.Reason]++
	}
	keys := make([]key, 0, len(burns))
	for k := range burns {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].host != keys[j].host {
			return keys[i].host < keys[j].host
		}
		return keys[i].minute.Before(keys[j].minute)
	})
	allowed := in.BurnAllowance > 0 // false for NaN too
	worst, compared := 0.0, 0
	for _, k := range keys {
		requests := l.RequestsPerMinute[k.minute]
		if requests == 0 {
			rep.Observations = append(rep.Observations, fmt.Sprintf("%s: host %s %s — %d nonces burned without sending in a minute without requests",
				OneNoncePerRequest, k.host, k.minute.Format("15:04"), burns[k]))
			continue
		}
		compared++
		ratio := float64(burns[k]) / float64(requests)
		worst = max(worst, ratio)
		line := fmt.Sprintf("%s: host %s %s — %d nonces burned without sending against %d requests (%.2f per request)",
			OneNoncePerRequest, k.host, k.minute.Format("15:04"), burns[k], requests, ratio)
		if ratio >= 0.1 {
			rep.Observations = append(rep.Observations, line)
		}
		if allowed && ratio > in.BurnAllowance {
			rep.Violations = append(rep.Violations, PromiseViolation{Promise: OneNoncePerRequest, Host: k.host,
				Detail: fmt.Sprintf("%d nonces burned in %s against %d requests (%.2f per request, %.2f allowed); reasons over the run %v",
					burns[k], k.minute.Format("15:04"), requests, ratio, in.BurnAllowance, reasons[k.host])})
		}
	}
	note := fmt.Sprintf("%d burned nonces over %d host-minutes; worst %.2f per request", len(l.Burns)-faulted, len(keys), worst)
	if faulted > 0 {
		note += fmt.Sprintf("; %d within host fault windows not judged", faulted)
	}
	if !allowed {
		rep.Checked[OneNoncePerRequest] = notMeasured("no allowance: " + note)
		return
	}
	rep.Checked[OneNoncePerRequest] = Measure{Measured: true, Judged: compared, Note: note}
}

// checkQuarantineCause holds the run to quarantine_only_when_faulted: a participant the runner did
// not fault is not quarantined. Each such quarantine carries what the gateway held against that host
// just before it. In a run whose ML nodes failed or answered dishonestly (chaos, a fault, a fault
// rule) the gateway may rightly quarantine the hosts that relayed those answers, so the check does
// not judge it. One participant's transitions of one kind within a second are one quarantine: the
// gateway logs one for every request of it in flight.
func checkQuarantineCause(in PromiseInput, rep *PromiseReport) {
	l := in.Layers
	if l == nil || l.GatewayLines == 0 {
		rep.Checked[QuarantineOnlyWhenFaulted] = notMeasured("no gateway log in compose.log")
		return
	}
	if l.LogCut != "" {
		rep.Checked[QuarantineOnlyWhenFaulted] = notMeasured(l.LogCut)
		return
	}
	if l.MLUnknown != "" {
		rep.Checked[QuarantineOnlyWhenFaulted] = notMeasured(fmt.Sprintf(
			"%s, so a quarantine outside the host fault windows may be a node's due; %d quarantine lines", l.MLUnknown, len(l.Transitions)))
		return
	}
	for _, m := range in.Mock {
		if m.Phase != mockopenai.PhaseExecute {
			continue
		}
		if how := mlFault(m); how != "" {
			rep.Checked[QuarantineOnlyWhenFaulted] = notMeasured(fmt.Sprintf(
				"%s, so a quarantine outside the host fault windows may be its due; %d quarantine lines", how, len(l.Transitions)))
			return
		}
	}
	for _, w := range l.FaultWindows {
		if w.Participant == "" {
			rep.Checked[QuarantineOnlyWhenFaulted] = notMeasured(fmt.Sprintf(
				"the runner paused %s, whose participant compose.log does not name (no %q line)", w.Host, hostIdentityMsg))
			return
		}
	}
	window := in.QuarantineCauseWindow
	if window == 0 {
		window = time.Minute
	}
	quarantines := quarantineBursts(l.Transitions)
	for _, q := range quarantines {
		t := q.first
		if l.faultedAt(t.Participant, t.At) {
			continue
		}
		lines := ""
		if q.lines > 1 {
			lines = fmt.Sprintf(", %d lines", q.lines)
		}
		rep.Violations = append(rep.Violations, PromiseViolation{Promise: QuarantineOnlyWhenFaulted, Host: hostLabel(t.Participant),
			Detail: fmt.Sprintf("%s quarantine (%s%s) of %s at %s outside its fault windows; %s",
				t.Mode, t.Reason, lines, t.Participant, t.At.UTC().Format(time.RFC3339), l.strikeSummary(hostLabel(t.Participant), t.At.Add(-window), t.At))})
	}
	rep.Checked[QuarantineOnlyWhenFaulted] = Measure{Measured: true, Judged: len(quarantines),
		Note: fmt.Sprintf("%d quarantines (%d lines) judged against %d host fault windows", len(quarantines), len(l.Transitions), len(l.FaultWindows))}
}

// mlFault says how an ML execution failed or lied, or "" when it answered honestly: chaos other than
// ok, a fault the node was set to inject, or a fault rule that fired.
func mlFault(m mockopenai.RequestRecord) string {
	switch {
	case m.Chaos == mockopenai.ChaosDelay:
		return "the ML node failed (chaos delay)"
	case m.Chaos != "" && m.Chaos != mockopenai.ChaosOK:
		return fmt.Sprintf("the ML node answered dishonestly (chaos %s)", m.Chaos)
	case m.Rule != "":
		return fmt.Sprintf("the ML node failed (rule %s, %s)", m.Rule, m.Outcome)
	case strings.HasPrefix(m.Outcome, "fault_"):
		return fmt.Sprintf("the ML node failed (%s)", m.Outcome)
	}
	return ""
}

// quarantineBurst is one quarantine: the first transition and how many lines the gateway logged of it.
type quarantineBurst struct {
	first QuarantineTransition
	lines int
}

// quarantineBursts folds the transitions of one participant, mode and reason that follow each other
// within a second into one quarantine, in log order.
func quarantineBursts(ts []QuarantineTransition) []quarantineBurst {
	var out []quarantineBurst
	last := map[QuarantineTransition]int{} // by participant, mode and reason: the burst it extends
	at := map[QuarantineTransition]time.Time{}
	for _, t := range ts {
		k := QuarantineTransition{Participant: t.Participant, Mode: t.Mode, Reason: t.Reason}
		if i, ok := last[k]; ok && t.At.Sub(at[k]) <= time.Second && !t.At.Before(at[k]) {
			out[i].lines++
			at[k] = t.At
			continue
		}
		last[k], at[k] = len(out), t.At
		out = append(out, quarantineBurst{first: t, lines: 1})
	}
	return out
}

// strikeSummary tells what the gateway logged against a host in [from, to]: counts by stage, how many
// empty streams had bytes read, the escrows involved and the largest burst within one millisecond.
func (l *LoadtestLayers) strikeSummary(host string, from, to time.Time) string {
	stages := map[string]int{}
	escrows := map[string]int{}
	bursts := map[time.Time]int{}
	withBytes, empty, maxBurst := 0, 0, 0
	for _, s := range l.Strikes {
		if s.Host != host || s.At.Before(from) || s.At.After(to) {
			continue
		}
		stages[s.Stage]++
		escrows[s.Escrow]++
		if s.Stage == "empty_stream" {
			empty++
			if s.StreamBytes > 0 {
				withBytes++
			}
			ms := s.At.Truncate(time.Millisecond)
			bursts[ms]++
			maxBurst = max(maxBurst, bursts[ms])
		}
	}
	if len(stages) == 0 {
		return fmt.Sprintf("no strike events against %s in the %s before", host, to.Sub(from))
	}
	return fmt.Sprintf("in the %s before: %v; %d of %d empty streams had bytes read; escrows %v; largest burst %d in one millisecond",
		to.Sub(from), stages, withBytes, empty, escrows, maxBurst)
}
