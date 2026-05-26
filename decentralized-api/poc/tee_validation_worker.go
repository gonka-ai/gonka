package poc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/teeverify"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
)

const (
	teeValidationVerifyTimeout = 10 * time.Second
	teeValidationQueryTimeout  = 5 * time.Second
	maxValidationReasonBytes   = 512
	maxValidationReasonRunes   = 512
)

type TeeValidationQueryClient interface {
	PendingTeeAttestationsForStage(
		ctx context.Context,
		in *inferencetypes.QueryPendingTeeAttestationsForStageRequest,
		opts ...grpc.CallOption,
	) (*inferencetypes.QueryPendingTeeAttestationsForStageResponse, error)
}

type TeeValidationWorker struct {
	recorder    cosmosclient.CosmosMessageClient
	tracker     *chainphase.ChainPhaseTracker
	verifiers   *teeverify.Registry
	chainParams TeeChainParamsProvider
	queryClient TeeValidationQueryClient

	ownAddress string
	interval   time.Duration
	stop       chan struct{}
	done       chan struct{}

	mu                sync.Mutex
	enabled           bool
	disabledReason    string
	stagePocHeight    int64
	stageVoted        map[string]struct{}
	lastTallyHeight   int64
	lastTallyCount    int
	lastAttemptAt     time.Time
	lastAttemptHeight int64
	lastError         string
}

type TeeValidationWorkerStatus struct {
	Enabled           bool      `json:"enabled"`
	DisabledReason    string    `json:"disabled_reason,omitempty"`
	IntervalSeconds   float64   `json:"interval_seconds"`
	Providers         []string  `json:"providers"`
	LastTallyHeight   int64     `json:"last_tally_height"`
	LastTallyCount    int       `json:"last_tally_count"`
	LastAttemptHeight int64     `json:"last_attempt_height"`
	LastAttemptAt     time.Time `json:"last_attempt_at"`
	LastError         string    `json:"last_error"`
}

func (w *TeeValidationWorker) Status() TeeValidationWorkerStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	var providers []string
	if w.verifiers != nil {
		providers = w.verifiers.Providers()
	}
	return TeeValidationWorkerStatus{
		Enabled:           w.enabled,
		DisabledReason:    w.disabledReason,
		IntervalSeconds:   w.interval.Seconds(),
		Providers:         providers,
		LastTallyHeight:   w.lastTallyHeight,
		LastTallyCount:    w.lastTallyCount,
		LastAttemptHeight: w.lastAttemptHeight,
		LastAttemptAt:     w.lastAttemptAt,
		LastError:         w.lastError,
	}
}

func NewTeeValidationWorker(
	recorder cosmosclient.CosmosMessageClient,
	tracker *chainphase.ChainPhaseTracker,
	verifiers *teeverify.Registry,
	chainParams TeeChainParamsProvider,
	queryClient TeeValidationQueryClient,
	ownAddress string,
	interval time.Duration,
) *TeeValidationWorker {
	w := &TeeValidationWorker{
		recorder:    recorder,
		tracker:     tracker,
		verifiers:   verifiers,
		chainParams: chainParams,
		queryClient: queryClient,
		ownAddress:  ownAddress,
		interval:    interval,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		enabled:     true,
	}
	disable := func(reason string) *TeeValidationWorker {
		w.enabled = false
		w.disabledReason = reason
		close(w.done)
		logging.Warn("TeeValidationWorker disabled", inferencetypes.PoC,
			"reason", w.disabledReason,
			"tee_validation_unavailable", true)
		return w
	}
	if recorder == nil {
		return disable("no recorder")
	}
	if tracker == nil {
		return disable("no chain phase tracker")
	}
	if strings.TrimSpace(ownAddress) == "" {
		return disable("empty own address")
	}
	if interval <= 0 {
		return disable("interval<=0")
	}
	if verifiers == nil {
		return disable("no teeverify.Registry")
	}
	if queryClient == nil {
		return disable("no query client")
	}
	go w.run()
	logging.Info("TeeValidationWorker started", inferencetypes.PoC,
		"interval", interval, "providers", verifiers.Providers())
	return w
}

func (w *TeeValidationWorker) Close() {
	select {
	case <-w.stop:
		return
	default:
		close(w.stop)
	}
	<-w.done
	logging.Info("TeeValidationWorker stopped", inferencetypes.PoC)
}

func (w *TeeValidationWorker) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.tick()
		case <-w.stop:
			return
		}
	}
}

func (w *TeeValidationWorker) tick() {
	epochState := w.tracker.GetCurrentEpochState()
	if epochState == nil || !epochState.IsSynced {
		return
	}
	if epochState.CurrentPhase == inferencetypes.InferencePhase && epochState.ActiveConfirmationPoCEvent != nil {
		return
	}
	pocHeight := GetCurrentPocStageHeight(epochState)
	if pocHeight <= 0 {
		return
	}
	if !ShouldAcceptValidatedArtifacts(epochState) {
		return
	}

	w.mu.Lock()
	if w.stagePocHeight != pocHeight {
		w.stagePocHeight = pocHeight
		w.stageVoted = make(map[string]struct{})
	}
	w.mu.Unlock()

	pending, err := w.fetchPending(pocHeight)
	if err != nil {
		w.recordError(pocHeight, err)
		logging.Warn("TeeValidationWorker: query pending failed",
			inferencetypes.PoC, "pocHeight", pocHeight, "error", err)
		return
	}
	if len(pending) == 0 {
		return
	}

	verdicts := w.runVerifications(pending)
	if len(verdicts) == 0 {
		return
	}

	sort.Slice(verdicts, func(i, j int) bool {
		if verdicts[i].SubjectAddress != verdicts[j].SubjectAddress {
			return verdicts[i].SubjectAddress < verdicts[j].SubjectAddress
		}
		return verdicts[i].NodeId < verdicts[j].NodeId
	})

	maxEntries := w.resolveMaxEntries()
	chunks := chunkEntries(verdicts, maxEntries)
	for _, chunk := range chunks {
		msg := &inferencetypes.MsgSubmitTeeValidations{
			PocStageStartBlockHeight: pocHeight,
			Validations:              chunk,
		}
		if err := w.recorder.SubmitTeeValidations(msg); err != nil {
			w.recordError(pocHeight, err)
			logging.Warn("TeeValidationWorker: chunk submission failed; will resume on next tick",
				inferencetypes.PoC,
				"pocHeight", pocHeight, "chunk_entries", len(chunk), "error", err)
			return
		}
		w.recordChunkVoted(pocHeight, chunk)
	}
}

func (w *TeeValidationWorker) fetchPending(pocHeight int64) ([]*inferencetypes.TeeAttestation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), teeValidationQueryTimeout)
	defer cancel()
	resp, err := w.queryClient.PendingTeeAttestationsForStage(ctx,
		&inferencetypes.QueryPendingTeeAttestationsForStageRequest{
			PocStageStartBlockHeight: pocHeight,
		})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return resp.Attestations, nil
}

func (w *TeeValidationWorker) runVerifications(pending []*inferencetypes.TeeAttestation) []*inferencetypes.TeeValidationEntry {
	w.mu.Lock()
	voted := w.stageVoted
	w.mu.Unlock()

	out := make([]*inferencetypes.TeeValidationEntry, 0, len(pending))
	for _, att := range pending {
		if att == nil {
			continue
		}
		if att.ParticipantAddress == w.ownAddress {
			continue
		}
		key := att.ParticipantAddress + "|" + att.NodeId
		if _, done := voted[key]; done {
			continue
		}
		verifier, ok := w.verifiers.Lookup(att.Provider)
		if !ok {
			logging.Debug("TeeValidationWorker: provider unsupported, skipping",
				inferencetypes.PoC,
				"subject", att.ParticipantAddress, "node_id", att.NodeId,
				"provider", att.Provider,
			)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), teeValidationVerifyTimeout)
		report, vErr := verifier.Verify(ctx, teeverify.VerifyRequest{
			PublicKey:       att.PublicKey,
			Quote:           att.Quote,
			EnvelopeVersion: att.EnvelopeVersion,
		})
		cancel()

		entry := &inferencetypes.TeeValidationEntry{
			SubjectAddress: att.ParticipantAddress,
			NodeId:         att.NodeId,
		}
		switch {
		case vErr != nil:
			entry.Verdict = inferencetypes.TeeVerdict_TEE_VERDICT_REJECT
			entry.Reason = sanitizeValidationReason(vErr.Error())
			logging.Info("TeeValidationWorker: REJECT (verifier error)",
				inferencetypes.PoC,
				"subject", att.ParticipantAddress, "node_id", att.NodeId,
				"provider", att.Provider, "error", vErr)
		case report == nil:
			entry.Verdict = inferencetypes.TeeVerdict_TEE_VERDICT_REJECT
			entry.Reason = sanitizeValidationReason("verifier returned nil report")
			logging.Warn("TeeValidationWorker: REJECT (nil report from verifier)",
				inferencetypes.PoC,
				"subject", att.ParticipantAddress, "node_id", att.NodeId,
				"provider", att.Provider)
		default:
			seen, mErr := inferencetypes.CanonicalMeasurement(report.Measurement)
			if mErr != nil {
				entry.Verdict = inferencetypes.TeeVerdict_TEE_VERDICT_REJECT
				entry.Reason = sanitizeValidationReason(
					fmt.Sprintf("verifier produced non-canonical measurement %q: %v", report.Measurement, mErr),
				)
				logging.Warn("TeeValidationWorker: REJECT (verifier measurement not canonical)",
					inferencetypes.PoC,
					"subject", att.ParticipantAddress, "node_id", att.NodeId,
					"provider", att.Provider, "raw", report.Measurement, "error", mErr)
			} else if seen != att.Measurement {
				entry.Verdict = inferencetypes.TeeVerdict_TEE_VERDICT_REJECT
				entry.MeasurementSeen = seen
				entry.Reason = sanitizeValidationReason(
					fmt.Sprintf("measurement mismatch: extracted=%s claimed=%s", seen, att.Measurement),
				)
				logging.Warn("TeeValidationWorker: REJECT (measurement mismatch)",
					inferencetypes.PoC,
					"subject", att.ParticipantAddress, "node_id", att.NodeId,
					"provider", att.Provider, "extracted", seen, "claimed", att.Measurement)
			} else {
				entry.Verdict = inferencetypes.TeeVerdict_TEE_VERDICT_OK
				entry.MeasurementSeen = seen
			}
		}
		out = append(out, entry)
	}
	return out
}

func sanitizeValidationReason(reason string) string {
	if reason == "" {
		return ""
	}
	reason = strings.ToValidUTF8(reason, "")
	var out strings.Builder
	out.Grow(len(reason))
	bytesLeft := maxValidationReasonBytes
	runesLeft := maxValidationReasonRunes
	for _, r := range reason {
		if !unicode.IsPrint(r) {
			continue
		}
		runeSize := utf8.RuneLen(r)
		if runeSize <= 0 || runeSize > bytesLeft || runesLeft <= 0 {
			break
		}
		out.WriteRune(r)
		bytesLeft -= runeSize
		runesLeft--
	}
	return out.String()
}

func (w *TeeValidationWorker) resolveMaxEntries() int {
	n := int(inferencetypes.DefaultMaxTeeValidationEntriesPerMessage)
	if w.chainParams == nil {
		return n
	}
	ctx, cancel := context.WithTimeout(context.Background(), teeValidationQueryTimeout)
	params, err := w.chainParams.GetTeeAttestationParams(ctx)
	cancel()
	if err != nil {
		logging.Warn("TeeValidationWorker: failed to read tee_attestation_params, using default",
			inferencetypes.PoC, "default", n, "error", err)
		return n
	}
	if params != nil && params.MaxValidationEntriesPerMessage > 0 {
		n = int(params.MaxValidationEntriesPerMessage)
	}
	return n
}

func (w *TeeValidationWorker) recordChunkVoted(pocHeight int64, chunk []*inferencetypes.TeeValidationEntry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stagePocHeight != pocHeight {
		return
	}
	if w.stageVoted == nil {
		w.stageVoted = make(map[string]struct{}, len(chunk))
	}
	for _, e := range chunk {
		w.stageVoted[e.SubjectAddress+"|"+e.NodeId] = struct{}{}
	}
	w.lastTallyHeight = pocHeight
	w.lastTallyCount = len(w.stageVoted)
	w.lastAttemptAt = time.Now().UTC()
	w.lastAttemptHeight = pocHeight
	w.lastError = ""
}

func (w *TeeValidationWorker) recordError(pocHeight int64, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastAttemptAt = time.Now().UTC()
	w.lastAttemptHeight = pocHeight
	w.lastError = err.Error()
}
