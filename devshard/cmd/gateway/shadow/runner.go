// Package shadow sends the inference probes a shadow participant receives instead of client traffic, and settles the nonces they are sent on.
package shadow

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/warmup"
	"devshard/host"
	"devshard/user"
)

// Escrows resolves the escrow a probe's nonce was committed on, draining ones included.
type Escrows interface {
	AcquireCommitted(escrowID string) (registry.EscrowSession, func(), bool)
	// HoldForVote keeps the escrow's session open and busy for a vote without counting as a request.
	HoldForVote(escrowID string) (registry.EscrowSession, func(), bool)
}

// Outcomes is satisfied by the adapter that writes a probe's result to the metric and the journal.
type Outcomes interface {
	ShadowProbed(result Result)
}

// Result is how one probe ended; FirstToken is meaningful only when Streamed.
type Result struct {
	Escrow       string
	Participant  string
	Model        string
	Nonce        uint64
	Outcome      string
	Streamed     bool
	FirstToken   time.Duration
	Total        time.Duration
	OutputTokens uint64
	Err          error
}

// Binding is everything the runner needs that only exists after the scheduler is built. See README.md.
type Binding struct {
	Escrows  Escrows
	Lifetime context.Context
	Voter    warmup.Voter
	Probes   warmup.Probes
	Outcomes Outcomes
	// HardTimeout is read once per probe: the engine's bound on a client attempt, the only bound a probe has.
	HardTimeout func() time.Duration
}

// Runner satisfies scheduler.ShadowProber. See ../docs/routing.md, "Shadow participants".
type Runner struct {
	binding Binding
	bounds  Bounds
	now     func() time.Time
	send    sendFunc
}

type sendFunc func(ctx context.Context, session registry.EscrowSession, prepared *user.PreparedInference, answer io.Writer, onReceipt func()) (*host.HostResponse, error)

var _ scheduler.ShadowProber = (*Runner)(nil)

func New(now func() time.Time, bounds Bounds) *Runner {
	return &Runner{now: now, bounds: bounds, send: dispatch}
}

// Bind is called once, before the gateway serves; a runner never bound sends no probe.
func (r *Runner) Bind(binding Binding) {
	r.binding = binding
}

func (r *Runner) Probe(request scheduler.ProbeRequest) bool {
	prepared, isPrepared := request.Assignment.Nonce.(*user.PreparedInference)
	params, hasParams := request.Params.(user.InferenceParams)
	if r.binding.Escrows == nil || !isPrepared || !hasParams {
		return false
	}
	session, release, held := r.binding.Escrows.AcquireCommitted(request.Assignment.Escrow)
	if !held {
		return false
	}
	request.Assignment.ReleaseEscrow()
	go r.run(sentProbe{request: request, prepared: prepared, params: params, session: session, release: release})
	return true
}

type sentProbe struct {
	request  scheduler.ProbeRequest
	prepared *user.PreparedInference
	params   user.InferenceParams
	session  registry.EscrowSession
	release  func()
}

func (r *Runner) run(probe sentProbe) {
	lifetime := r.binding.Lifetime
	sendCtx, cancel := context.WithTimeout(lifetime, r.binding.HardTimeout())
	defer cancel()

	answer := newCapture(r.now)
	meter := engine.NewStreamMeter(r.now, probe.request.Assignment.Host, probe.request.Assignment.Nonce)
	var acknowledged atomic.Bool
	probe.prepared.OnSend(func() {
		meter.Sent()
		answer.Sent()
	})
	response, err := r.send(sendCtx, probe.session, probe.prepared, io.MultiWriter(answer, meter), func() {
		acknowledged.Store(true)
		meter.Receipt()
	})
	total := r.now().Sub(answer.started)
	answered := acknowledged.Load() || (response != nil && len(response.Receipt) > 0)
	var confirmedAt int64
	if response != nil {
		confirmedAt = response.ConfirmedAt
	}
	read := meter.Outcome(confirmedAt, err)

	result := r.result(probe, answer, total, err)
	r.record(result, answered, read)
	probe.session.ArmQuietSettle()
	if err == nil {
		probe.release()
		r.report(result)
		return
	}
	releaseVote := r.holdForVote(probe)
	defer releaseVote()
	r.report(result)
	r.binding.Voter.Settle(lifetime, probe.request.Assignment.Escrow, probe.request.Model, probe.params, probe.prepared.Nonce(), answered)
	probe.session.ArmQuietSettle()
}

func (r *Runner) holdForVote(probe sentProbe) func() {
	_, release, held := r.binding.Escrows.HoldForVote(probe.request.Assignment.Escrow)
	if !held {
		return probe.release
	}
	probe.release()
	return release
}

func (r *Runner) report(result Result) {
	if r.binding.Outcomes != nil {
		r.binding.Outcomes.ShadowProbed(result)
	}
}

func dispatch(ctx context.Context, session registry.EscrowSession, prepared *user.PreparedInference, answer io.Writer, onReceipt func()) (*host.HostResponse, error) {
	userSession := session.UserSession()
	response, err := registry.SendRecovering(ctx, userSession, prepared, answer, onReceipt)
	if response != nil {
		err = errors.Join(err, userSession.ProcessResponse(prepared.HostIdx(), response, prepared.Nonce()))
	}
	return response, err
}

func (r *Runner) result(probe sentProbe, answer *capture, total time.Duration, err error) Result {
	result := Result{
		Escrow:      probe.request.Assignment.Escrow,
		Participant: probe.request.Assignment.Host,
		Model:       probe.request.Model,
		Nonce:       probe.prepared.Nonce(),
		Outcome:     OutcomeServed,
		Total:       total,
		Err:         err,
	}
	result.FirstToken, result.Streamed = answer.firstToken()
	result.OutputTokens = answer.completionTokens()
	if err != nil {
		result.Outcome = OutcomeFailed
	}
	return result
}

func (r *Runner) record(result Result, answered bool, read engine.AttemptOutcome) {
	if r.binding.Probes == nil {
		return
	}
	line := accounting.Attempt{
		Nonce:             result.Nonce,
		OutputTokens:      int64(result.OutputTokens),
		FirstContentDelay: read.FirstContentDelay(),
		Sent:              true,
		Acknowledged:      answered,
		Usage:             accounting.UsageLoser,
		Phase:             accounting.PhaseNormal,
		Terminal:          accounting.TerminalShadowProbe,
	}
	r.binding.Probes.ProbeRecorded(result.Escrow, line.WithTiming(read))
}
