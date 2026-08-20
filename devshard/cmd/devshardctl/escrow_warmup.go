package main

import (
	"context"
	"log"
	"sync"
	"time"

	"devshard/host"
	"devshard/user"
)

// A host holds no state until its first diff, so it can neither validate nor vote while the tally still
// counts it as a verifier. Consecutive nonces map onto consecutive slots, so groupSize probes cover the
// group exactly once.
const (
	warmupProbeTimeout   = 2 * time.Minute
	warmupResolveTimeout = 1 * time.Minute
	warmupCatchUpTimeout = 2 * time.Minute
	warmupBudget         = 20 * time.Minute

	warmupDecision      = "warmup_probe"
	warmupOutcomeServed = "served"
	warmupOutcomeFailed = "failed"
)

type sendRecorder interface {
	RealSend(escrowID string, nonce uint64, sentAt time.Time, quarantine string)
	ProbeServed(escrowID string, nonce uint64)
}

type warmupMetrics interface {
	RecordGatewaySlotDecision(decision GatewaySlotDecisionMetric)
}

type warmupDeps struct {
	sender      probeSender
	recorder    sendRecorder
	metrics     warmupMetrics
	participant func(hostIdx int) string
	escrowID    string
	model       string
}

func (d warmupDeps) observe(hostIdx int, reason string) {
	if d.metrics == nil {
		return
	}
	participant := ""
	if d.participant != nil {
		participant = d.participant(hostIdx)
	}
	d.metrics.RecordGatewaySlotDecision(GatewaySlotDecisionMetric{
		ParticipantKey: participant,
		Model:          d.model,
		EscrowID:       d.escrowID,
		Decision:       warmupDecision,
		Reason:         reason,
	})
}

type probeSender interface {
	sendProbe(ctx context.Context, params user.InferenceParams) (nonce uint64, hostIdx int, err error)
	resolveUnserved(ctx context.Context, nonce uint64) error
	catchUpAllHosts(ctx context.Context) error
}

type sessionProbeSender struct {
	session  *user.Session
	mu       sync.Mutex
	unserved map[uint64]unservedProbe
}

type unservedProbe struct {
	payload *host.InferencePayload
	sentAt  time.Time
}

func (s *sessionProbeSender) sendProbe(ctx context.Context, params user.InferenceParams) (uint64, int, error) {
	prepared, err := s.session.PrepareInference(params)
	if err != nil {
		return 0, 0, err
	}
	nonce, hostIdx, sentAt := prepared.Nonce(), prepared.HostIdx(), time.Now()
	resp, sendErr := s.session.SendOnly(ctx, prepared, nil, nil)
	if sendErr == nil {
		sendErr = s.session.ProcessResponse(hostIdx, resp, nonce)
	}
	if sendErr != nil {
		s.mu.Lock()
		if s.unserved == nil {
			s.unserved = make(map[uint64]unservedProbe)
		}
		s.unserved[nonce] = unservedProbe{payload: prepared.Payload(), sentAt: sentAt}
		s.mu.Unlock()
	}
	return nonce, hostIdx, sendErr
}

func (s *sessionProbeSender) catchUpAllHosts(ctx context.Context) error {
	return s.session.CatchUpAllHosts(ctx)
}

// A timeout polls the whole group, so raising one mid-pass asks slots the pass has not reached yet and
// they answer "session not found". The nonce is still resolved, only once every host has been contacted.
func (s *sessionProbeSender) resolveUnserved(ctx context.Context, nonce uint64) error {
	s.mu.Lock()
	probe, waiting := s.unserved[nonce]
	delete(s.unserved, nonce)
	s.mu.Unlock()
	if !waiting || s.session.IsNonceFinished(nonce) {
		return nil
	}
	resolveCtx, cancelResolve := context.WithTimeout(ctx, warmupResolveTimeout)
	defer cancelResolve()
	_, err := s.session.HandleTimeout(resolveCtx, nonce, probe.sentAt, probe.payload)
	return err
}

func warmEscrowHosts(ctx context.Context, deps warmupDeps, latestNonce uint64) {
	if deps.sender == nil || latestNonce != 0 {
		return
	}
	sendCtx, cancelSend := context.WithTimeout(ctx, warmupProbeTimeout)
	nonce, hostIdx, probeErr := deps.sender.sendProbe(sendCtx, ghostProbeParams(deps.model))
	cancelSend()
	if nonce == 0 {
		log.Printf("escrow_warmup_failed escrow=%s model=%q error=%v", deps.escrowID, deps.model, probeErr)
		return
	}
	if deps.recorder != nil {
		deps.recorder.RealSend(deps.escrowID, nonce, time.Now(), "")
	}

	// The probe signs and persists its diff before the send, so the group can be taught the escrow even
	// when that probe never arrived. Catch-up spends no nonce and reaches hosts the dispatch never will.
	catchUpCtx, cancelCatchUp := context.WithTimeout(ctx, warmupCatchUpTimeout)
	catchUpErr := deps.sender.catchUpAllHosts(catchUpCtx)
	cancelCatchUp()

	if probeErr != nil {
		deps.observe(hostIdx, warmupOutcomeFailed)
		if err := deps.sender.resolveUnserved(ctx, nonce); err != nil {
			log.Printf("escrow_warmup_timeout_failed escrow=%s nonce=%d error=%v", deps.escrowID, nonce, err)
		}
	} else {
		if deps.recorder != nil {
			deps.recorder.ProbeServed(deps.escrowID, nonce)
		}
		deps.observe(hostIdx, warmupOutcomeServed)
	}
	log.Printf("escrow_warmup_completed escrow=%s model=%q nonce=%d slot=%d served=%t catch_up_error=%v",
		deps.escrowID, deps.model, nonce, hostIdx, probeErr == nil, catchUpErr)
}

func startEscrowWarmup(rt *devshardRuntime, recorder sendRecorder, metrics warmupMetrics) {
	if rt == nil || rt.session == nil {
		return
	}
	deps := warmupDeps{
		sender:      &sessionProbeSender{session: rt.session},
		recorder:    recorder,
		metrics:     metrics,
		participant: rt.session.HostParticipantKey,
		escrowID:    rt.id,
		model:       rt.model,
	}
	go warmUntilStopped(deps, rt.session.Nonce(), rt.stopped)
}

func warmUntilStopped(deps warmupDeps, latestNonce uint64, stop <-chan struct{}) {
	ctx, cancelWarmup := context.WithTimeout(context.Background(), warmupBudget)
	defer cancelWarmup()
	go func() {
		select {
		case <-stop:
			cancelWarmup()
		case <-ctx.Done():
		}
	}()
	warmEscrowHosts(ctx, deps, latestNonce)
}
