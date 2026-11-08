package harness

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
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
