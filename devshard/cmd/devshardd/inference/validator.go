package inference

import (
	"bytes"
	"common/chain"
	"common/completionapi"
	commonvalidation "common/validation"
	"context"
	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/logging"
	"devshard/observability"
	"devshard/signing"
	"devshard/storage"
	"devshard/transport"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

// leaseOps is satisfied by storage.LeaseStore; extracted as interface for testing.
type leaseOps interface {
	Acquire(ctx context.Context, escrowId string, inferenceId uint64, epochId uint64, owner storage.LeaseOwner) (bool, error)
	DescribeLease(ctx context.Context, escrowId string, inferenceId, epochId uint64) (storage.LeaseInfo, bool, error)
	SetResult(ctx context.Context, escrowId string, inferenceId, epochId uint64, status storage.LeaseStatus, owner storage.LeaseOwner) error
	OwnsPendingLease(ctx context.Context, escrowId string, inferenceId, epochId uint64, owner storage.LeaseOwner) (bool, error)
	Release(ctx context.Context, escrowId string, inferenceId, epochId uint64, owner storage.LeaseOwner) error
}

type acquireKey struct {
	escrowID    string
	inferenceID uint64
}

// Validator implements devshard.ValidationEngine for the standalone devshardd binary.
// It performs ML-based inference validation without lease deduplication.
// Use LeaseValidator to add Postgres-based lease deduplication on top.
type Validator struct {
	bridge                  bridge.MainnetBridge
	recorder                PayloadAuthClient
	engine                  *Engine
	phase                   *chain.Phase
	boundVersion            string
	chainParams             ChainParamsProvider
	thresholds              ValidationThresholdResolver
	vocabularySizes         VocabularyResolver
	voteFalseOnFetchFailure bool
	payloadHTTPClient       *http.Client
	payloadSigner           signing.Signer
	rpcEndpoints            transport.EndpointSet
	payloadRPCMu            sync.Mutex
	payloadRPCs             map[string]*transport.RPCClient
	fetchPayloads           payloadFetchFunc
	executeML               mlExecuteFunc
	pool                    *flipPool
	echoMode                string
}

type payloadFetchFunc func(ctx context.Context, req devshardpkg.ValidateRequest, inferenceID string, epochID uint64) ([]byte, []byte, error)

type mlExecuteFunc func(ctx context.Context, model, escrowID, path string, body []byte) (*http.Response, error)

// mlCall posts a body to a path of the ML node one validation holds.
type mlCall func(ctx context.Context, path string, body []byte) (*http.Response, error)

// How the echo validation takes part in a vote (DEVSHARD_ECHO_VALIDATION).
const (
	// EchoShadow: the logprob comparison votes; the echo scores the same
	// inference beside it, on the same ML node and validation credit, and its
	// verdict is only logged.
	EchoShadow = "shadow"
	// EchoEnforce: the echo votes.
	EchoEnforce = "enforce"
	// EchoOff: the logprob comparison alone.
	EchoOff = "off"
)

// shadowTimeout bounds the echo that runs beside a vote it does not cast.
const shadowTimeout = 2 * time.Minute

// The pooled bound over a participant's recent answers uses the 99.9 %
// quantile and needs a few answers before it speaks.
const (
	pooledQuantile   = 3.09
	pooledMinAnswers = 4
	poolWindow       = 32
	poolMaxAge       = 24 * time.Hour
)

// NewValidator creates a Validator. boundVersion is the runtime version string used
// to construct the payload request path. thresholds resolves the per-model
// similarity pass threshold (long-poll snapshot first, chain fallback).
// vocabularySizes bounds enforced token ids by the model's vocab before the replay reaches the ML node.
// voteFalseOnFetchFailure converts executor-attributable payload failures into
// Valid:false instead of abandoning the attempt. echoMode is EchoShadow,
// EchoEnforce or EchoOff.
func NewValidator(
	br bridge.MainnetBridge,
	recorder PayloadAuthClient,
	engine *Engine,
	phase *chain.Phase,
	boundVersion string,
	chainParams ChainParamsProvider,
	thresholds ValidationThresholdResolver,
	vocabularySizes VocabularyResolver,
	voteFalseOnFetchFailure bool,
	echoMode string,
) *Validator {
	return &Validator{
		bridge:                  br,
		recorder:                recorder,
		engine:                  engine,
		phase:                   phase,
		boundVersion:            boundVersion,
		chainParams:             chainParams,
		thresholds:              thresholds,
		vocabularySizes:         vocabularySizes,
		voteFalseOnFetchFailure: voteFalseOnFetchFailure,
		payloadHTTPClient:       newPayloadFetchClient(),
		pool:                    newFlipPool(poolWindow, poolMaxAge),
		echoMode:                echoMode,
	}
}

func (v *Validator) CanValidate(model string) bool {
	return v.engine == nil || v.engine.creditAvailable(model)
}

func (v *Validator) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	// Reject exhausted models before payload fetching. Acquisition later reserves
	// atomically, since another escrow may spend the credit during the fetch.
	if !v.CanValidate(req.Model) {
		return nil, devshardpkg.ErrValidationDeferred
	}
	inferenceID := strconv.FormatUint(req.InferenceID, 10)

	epochID := resolveValidationEpoch(v.phase, req.EpochID)
	promptPayload, responsePayload, err := v.fetchPayloadsFor(ctx, req, inferenceID, epochID)
	if err != nil {
		if verdict := executorFaultVerdict(ctx, v.phase, req, epochID, err, v.voteFalseOnFetchFailure); verdict != nil {
			return verdict, nil
		}
		if errors.Is(err, ErrPayloadRateLimited) {
			return nil, observability.Classify(observability.ReasonRateLimited, observability.WhereRuntimeValidate, fmt.Errorf("fetch payloads from executor: %w", err))
		}
		if errors.Is(err, commonvalidation.ErrPayloadGone) {
			logging.Info("devshard validation skipped: payload pruned on executor",
				types.Validation,
				"inferenceId", inferenceID,
				"executor", req.ExecutorAddress,
				"epoch", epochID,
			)
			return nil, fmt.Errorf("%w: %v", devshardpkg.ErrValidationSkipped, err)
		}
		return nil, observability.Classify(observability.ReasonPayloadFetchErr, observability.WhereRuntimeValidate, fmt.Errorf("fetch payloads from executor: %w", err))
	}

	if _, err := completionapi.ModifyRequestBodyWithLogprobsMode(promptPayload, int32(req.InferenceID), v.chainParams.LogprobsMode()); err != nil {
		tagged := tagExecutorPayloadFault(err)
		if verdict := executorFaultVerdict(ctx, v.phase, req, epochID, tagged, v.voteFalseOnFetchFailure); verdict != nil {
			return verdict, nil
		}
		return nil, observability.Classify(observability.ReasonValidationBuildErr, observability.WhereRuntimeValidate, fmt.Errorf("modify request body for validation: %w", err))
	}
	if _, err := commonvalidation.UnmarshalResponsePayload(responsePayload); err != nil {
		tagged := tagExecutorPayloadFault(err)
		if verdict := executorFaultVerdict(ctx, v.phase, req, epochID, tagged, v.voteFalseOnFetchFailure); verdict != nil {
			return verdict, nil
		}
		return nil, observability.Classify(observability.ReasonOriginalParseErr, observability.WhereRuntimeValidate, fmt.Errorf("parse original response: %w", err))
	}

	if v.echoMode == EchoEnforce {
		return v.echoVerdict(ctx, req, inferenceID, promptPayload, responsePayload)
	}
	var shadow *shadowOutcome
	var beside func(mlCall)
	if v.echoMode == EchoShadow {
		beside = func(call mlCall) {
			shadow = v.shadowEcho(ctx, req, inferenceID, promptPayload, responsePayload, call)
		}
	}
	verdict, err := v.logprobVerdict(ctx, req, inferenceID, promptPayload, responsePayload, epochID, beside)
	if shadow != nil {
		logShadow(req, inferenceID, verdict, err, shadow)
	}
	return verdict, err
}

// logprobVerdict is the validation mainnet runs: the answer is replayed with
// its tokens enforced and the logprobs of both runs are compared. beside, when
// set, runs once on the ML node that answered the replay, before the node and
// the validation credit are released.
func (v *Validator) logprobVerdict(ctx context.Context, req devshardpkg.ValidateRequest, inferenceID string,
	promptPayload, responsePayload []byte, epochID uint64, beside func(mlCall)) (*devshardpkg.ValidateResult, error) {
	result, err := commonvalidation.ExecuteValidationWithPolicy(
		ctx,
		inferenceID,
		promptPayload,
		responsePayload,
		func(ctx context.Context, body []byte) (*http.Response, error) {
			return v.withMLNode(ctx, req.Model, req.EscrowID, func(call mlCall) (*http.Response, error) {
				resp, err := call(ctx, commonvalidation.ChatPath, body)
				if beside != nil && err == nil && resp != nil && resp.StatusCode == http.StatusOK {
					run := beside
					beside = nil
					run(call)
				}
				return resp, err
			})
		},
		req.InputTokens, req.OutputTokens,
		v.chainParams.LogprobsMode(),
		v.vocabularySizes.Resolve(ctx, epochID, req.Model),
		commonvalidation.DefaultShortOutputScoringPolicy.ForModel(req.Model),
	)
	if err != nil {
		return nil, classifyExecuteValidationErr(err)
	}

	valid, err := evaluateValidationResult(ctx, result, epochID, req.Model, v.thresholds)
	if err != nil {
		return nil, observability.Classify(observability.ReasonValidationBuildErr, observability.WhereRuntimeValidate,
			fmt.Errorf("evaluate validation result: %w", err))
	}
	return &devshardpkg.ValidateResult{Valid: valid}, nil
}

// echoVerdict judges the inference on the executor's tape: the echo or, for
// what the echo cannot judge, the replay, then the participant's pool. All its
// calls go to one ML node under one validation credit.
func (v *Validator) echoVerdict(ctx context.Context, req devshardpkg.ValidateRequest, inferenceID string,
	promptPayload, responsePayload []byte) (*devshardpkg.ValidateResult, error) {
	var result commonvalidation.ValidationResult
	var echoErr error
	_, err := v.withMLNode(ctx, req.Model, req.EscrowID, func(call mlCall) (*http.Response, error) {
		lastStatus, lastErr := 0, error(nil)
		result, echoErr = commonvalidation.ExecuteEchoValidation(ctx, inferenceID, promptPayload, responsePayload,
			func(ctx context.Context, path string, body []byte) (*http.Response, error) {
				resp, err := call(ctx, path, body)
				lastStatus, lastErr = 0, err
				if resp != nil {
					lastStatus = resp.StatusCode
				}
				return resp, err
			}, req.InputTokens, req.OutputTokens)
		// What the node holder sees decides rotation: a transport error or a
		// 5xx of the last call sends the validation to another node.
		if lastErr != nil {
			return nil, lastErr
		}
		if lastStatus >= http.StatusInternalServerError {
			return &http.Response{StatusCode: lastStatus, Body: http.NoBody}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	if err != nil {
		return nil, classifyExecuteValidationErr(err)
	}
	return v.verdictOfEcho(req, inferenceID, result, echoErr)
}

// verdictOfEcho turns the echo's outcome into a verdict and adds the answer
// to the participant's pool.
func (v *Validator) verdictOfEcho(req devshardpkg.ValidateRequest, inferenceID string,
	result commonvalidation.ValidationResult, err error) (*devshardpkg.ValidateResult, error) {
	if errors.Is(err, commonvalidation.ErrEchoUnsupported) {
		return nil, fmt.Errorf("%w: %v", devshardpkg.ErrValidationSkipped, err)
	}
	if err != nil {
		return nil, classifyExecuteValidationErr(err)
	}
	switch echo := result.(type) {
	case *commonvalidation.InvalidInferenceResult:
		return &devshardpkg.ValidateResult{Valid: false, Reason: "invalid", Details: []any{"cause", echo.Reason}}, nil
	case *commonvalidation.EchoValidationResult:
		if echo.Autopass != "" {
			return &devshardpkg.ValidateResult{Valid: true, Reason: "autopass_4xx", Details: []any{"cause", echo.Autopass}}, nil
		}
		verdict := &devshardpkg.ValidateResult{Valid: echo.IsSuccessful(), Reason: "echo_fail"}
		if verdict.Valid {
			verdict.Reason = "echo_pass"
		}
		verdict.Details = []any{"steps", echo.Signals.Steps, "count", echo.Signals.Count,
			"z", echo.Signals.Z, "max_gap", echo.Signals.MaxGap}
		if echo.Replay != "" {
			verdict.Details = append(verdict.Details, "replay", echo.Replay)
		}
		if v.pool != nil {
			answers, steps, count := v.pool.Observe(req.ExecutorAddress, req.Model, echo.Signals.Steps, echo.Signals.Count)
			pooled := commonvalidation.EchoThresholdsFor(req.Model).CountPasses(steps, count, pooledQuantile)
			verdict.Details = append(verdict.Details, "pooled_answers", answers, "pooled_steps", steps, "pooled_count", count)
			if verdict.Valid && answers >= pooledMinAnswers && !pooled {
				verdict.Valid = false
				verdict.Reason = "pooled_flips"
			}
		}
		return verdict, nil
	default:
		return nil, fmt.Errorf("unknown echo validation result type %T", result)
	}
}

// shadowOutcome is what the echo would have voted beside a vote it did not cast.
type shadowOutcome struct {
	verdict *devshardpkg.ValidateResult
	err     error
	took    time.Duration
}

// shadowEcho scores the inference by the echo through call, the ML node the
// logprob comparison holds. Nothing it does reaches the vote: its errors, its
// timeout and its panics end in the outcome.
func (v *Validator) shadowEcho(ctx context.Context, req devshardpkg.ValidateRequest, inferenceID string,
	promptPayload, responsePayload []byte, call mlCall) (out *shadowOutcome) {
	started := time.Now()
	out = &shadowOutcome{}
	defer func() {
		if r := recover(); r != nil {
			out.verdict, out.err = nil, fmt.Errorf("panic: %v", r)
		}
		out.took = time.Since(started)
	}()
	shadowCtx, cancel := context.WithTimeout(ctx, shadowTimeout)
	defer cancel()
	result, err := commonvalidation.ExecuteEchoValidation(shadowCtx, inferenceID, promptPayload, responsePayload,
		func(ctx context.Context, path string, body []byte) (*http.Response, error) {
			return call(ctx, path, body)
		}, req.InputTokens, req.OutputTokens)
	out.verdict, out.err = v.verdictOfEcho(req, inferenceID, result, err)
	return out
}

// logShadow writes what the echo would have voted next to the vote cast.
func logShadow(req devshardpkg.ValidateRequest, inferenceID string, voted *devshardpkg.ValidateResult, votedErr error, shadow *shadowOutcome) {
	fields := []any{"subsystem", "validation", "inferenceId", inferenceID, "executor", req.ExecutorAddress, "model", req.Model,
		"ms", shadow.took.Milliseconds()}
	if votedErr != nil || voted == nil {
		fields = append(fields, "voted", "none")
	} else {
		fields = append(fields, "voted", voted.Valid)
	}
	switch {
	case errors.Is(shadow.err, devshardpkg.ErrValidationSkipped):
		fields = append(fields, "echo", "skipped", "cause", shadow.err.Error())
	case shadow.err != nil:
		fields = append(fields, "echo", "error", "cause", shadow.err.Error())
	default:
		fields = append(fields, "echo", shadow.verdict.Reason, "echo_valid", shadow.verdict.Valid)
		if votedErr == nil && voted != nil {
			fields = append(fields, "agree", shadow.verdict.Valid == voted.Valid)
		}
		fields = append(fields, shadow.verdict.Details...)
	}
	logging.Info("echo validation shadow", fields...)
}

func (v *Validator) fetchPayloadsFor(ctx context.Context, req devshardpkg.ValidateRequest, inferenceID string, epochID uint64) ([]byte, []byte, error) {
	if v.fetchPayloads != nil {
		return v.fetchPayloads(ctx, req, inferenceID, epochID)
	}
	return fetchPayloadsFromExecutor(
		ctx, v.bridge, v.recorder, req, inferenceID, epochID,
		devshardpkg.VersionedSessionPayloadPath(v.boundVersion, req.EscrowID),
		v.payloadHTTPClient,
		v.payloadRPC,
	)
}

// SetPayloadRPC enables Connect GetPayload. signer is the host identity
// used for Attach. Payload is always a Connect method; there is no HTTP GET.
func (v *Validator) SetPayloadRPC(signer signing.Signer, endpoints transport.EndpointSet) {
	if v == nil {
		return
	}
	v.payloadSigner = signer
	v.rpcEndpoints = endpoints
}

// ClosePayloadClients releases cached executor PeerConns. Safe if unused.
func (v *Validator) ClosePayloadClients() {
	if v == nil {
		return
	}
	v.payloadRPCMu.Lock()
	defer v.payloadRPCMu.Unlock()
	for k, c := range v.payloadRPCs {
		c.Close()
		delete(v.payloadRPCs, k)
	}
}

func (v *Validator) payloadRPC(executorURL, executorAddr, escrowID string) *transport.RPCClient {
	if v == nil || v.payloadSigner == nil || !v.rpcEndpoints.Has(transport.EndpointPayload) {
		return nil
	}
	executorURL = strings.TrimSpace(executorURL)
	executorAddr = strings.TrimSpace(executorAddr)
	escrowID = strings.TrimSpace(escrowID)
	if executorURL == "" || executorAddr == "" || escrowID == "" {
		return nil
	}
	key := executorAddr + "\x00" + executorURL + "\x00" + escrowID
	v.payloadRPCMu.Lock()
	defer v.payloadRPCMu.Unlock()
	if c := v.payloadRPCs[key]; c != nil {
		return c
	}
	cfg := transport.DefaultClientConfig()
	cfg.RoutePrefix = devshardpkg.VersionedRoutePrefix(v.boundVersion)
	cfg.QueryTimeout = payloadFetchTimeout
	httpClient := transport.NewHTTPClient(executorURL, escrowID, v.payloadSigner, cfg)
	selected := transport.SelectTransport(httpClient, executorAddr, v.rpcEndpoints, nil)
	rpc, ok := selected.(*transport.RPCClient)
	if !ok || rpc == nil || !rpc.Uses(transport.EndpointPayload) {
		return nil
	}
	if v.payloadRPCs == nil {
		v.payloadRPCs = make(map[string]*transport.RPCClient)
	}
	v.payloadRPCs[key] = rpc
	return rpc
}

const executorPayloadUnavailableReason = "executor_payload_unavailable"

// resolveValidationEpoch is the single source of the epoch used for both the
// payload fetch and the validation lease. Prefer the request (the inference's
// escrow epoch); fall back to the current phase only when the caller left it
// unset.
func resolveValidationEpoch(phase *chain.Phase, reqEpoch uint64) uint64 {
	if reqEpoch != 0 {
		return reqEpoch
	}
	return phase.EpochID()
}

// maxVerdictCauseBytes caps the cause string carried in ValidateResult.Details.
// The underlying error already carries a bounded executor body, but the details
// are logged on every publish, so keep them short.
const maxVerdictCauseBytes = 512

// executorFaultVerdict converts an executor-attributable failure into a
// Valid:false verdict. epochID is the epoch the payload was actually requested
// for (resolved by the caller), not req.EpochID, which may be unset.
func executorFaultVerdict(ctx context.Context, phase *chain.Phase, req devshardpkg.ValidateRequest, epochID uint64, err error, enabled bool) *devshardpkg.ValidateResult {
	if !enabled || err == nil || ctx.Err() != nil {
		return nil
	}
	// phase follows LatestEpoch (moves at poc_start), the escrow epoch follows
	// EffectiveEpoch (moves at set_new_validators), so escrows of epoch E keep
	// serving at phase E+1 through the PoC window. The executor stores under
	// its phase and the payload handler also looks up epochID+1, so at phase
	// E+1 a 404 is never an honest prune.
	inWindow := true
	if phase != nil {
		inWindow = phase.EpochID() <= epochID+1
	}
	reason := observability.ReasonPayloadFetchErr
	switch {
	case errors.Is(err, commonvalidation.ErrPayloadGone):
		if !inWindow {
			return nil
		}
		reason = observability.ReasonPayloadNotFound
	case errors.Is(err, commonvalidation.ErrPayloadTooLarge):
		reason = observability.ReasonPayloadTooLarge
	case errors.Is(err, errExecutorPayloadFault):
		if errors.Is(err, ErrPayloadRateLimited) {
			reason = observability.ReasonRateLimited
		}
	default:
		return nil
	}

	// The cause can quote an executor-supplied body, so both the log line and
	// the published details get the same bounded leading span.
	cause := truncateCause(err.Error())

	// No StageValidationFinished counter here: the caller publishes this verdict
	// and counts the stage as OK, so an error there would double-count.
	observability.IncValidationExecutorFault(reason)
	observability.Log(ctx, observability.LevelWarn, "validation voting false: executor payload unavailable",
		observability.StageValidationFinished, observability.WhereRuntimeValidate, req.EscrowID, reason, nil,
		"inference_id", req.InferenceID,
		"executor_address", req.ExecutorAddress,
		"epoch_id", epochID,
		"cause", cause,
	)
	return &devshardpkg.ValidateResult{
		Valid:  false,
		Reason: executorPayloadUnavailableReason,
		Details: []any{
			"cause", cause,
			"classified_reason", string(reason),
		},
	}
}

func truncateCause(s string) string {
	if len(s) <= maxVerdictCauseBytes {
		return s
	}
	return s[:maxVerdictCauseBytes] + "...(truncated)"
}

// evaluateValidationResult decides pass/fail from a validation outcome. Similarity
// results use the per-model threshold from chain/runtime config; length, token, and
// invalid outcomes fail directly without a threshold lookup.
func evaluateValidationResult(
	ctx context.Context,
	result commonvalidation.ValidationResult,
	epochID uint64,
	model string,
	thresholds ValidationThresholdResolver,
) (bool, error) {
	switch r := result.(type) {
	case *commonvalidation.SimilarityValidationResult:
		threshold, err := thresholds.Resolve(ctx, epochID, model)
		if err != nil {
			return false, err
		}
		return commonvalidation.SimilarityPassesThreshold(r.Value, threshold), nil
	case *commonvalidation.DifferentLengthValidationResult,
		*commonvalidation.DifferentTokensValidationResult,
		*commonvalidation.InvalidInferenceResult:
		return false, nil
	default:
		return false, fmt.Errorf("unknown validation result type %T", result)
	}
}

// withMLNode runs work against one ML node under one validation credit. work
// gets a call bound to that node; the response it returns is what the node
// holder classifies: a transport error or a 5xx rotates to another node.
func (v *Validator) withMLNode(ctx context.Context, model, escrowID string,
	work func(call mlCall) (*http.Response, error)) (*http.Response, error) {
	if v.executeML != nil {
		return work(func(ctx context.Context, path string, body []byte) (*http.Response, error) {
			return v.executeML(ctx, model, escrowID, path, body)
		})
	}
	resp, err := v.engine.doWithLockedNode(ctx, observability.PathValidate, model, escrowID, func(endpoint string) (*http.Response, error) {
		return work(func(ctx context.Context, path string, body []byte) (*http.Response, error) {
			return v.postToNode(ctx, endpoint, path, body)
		})
	})
	if err != nil {
		return nil, fmt.Errorf("validate inference: %w", err)
	}
	return resp, nil
}

// executeMLRequest posts one validation replay to a locked ML node.
func (v *Validator) executeMLRequest(ctx context.Context, model, escrowID string, body []byte) (*http.Response, error) {
	return v.withMLNode(ctx, model, escrowID, func(call mlCall) (*http.Response, error) {
		return call(ctx, commonvalidation.ChatPath, body)
	})
}

// postToNode posts body to path on an ML node and returns the response with
// its body read (a replay stream normalised to one JSON body).
func (v *Validator) postToNode(ctx context.Context, endpoint, path string, body []byte) (*http.Response, error) {
	url := endpoint + path
	httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	// NewRequest also accepts URLs that the HTTP transport cannot dispatch.
	if reqErr == nil && (httpReq.URL.Host == "" || (httpReq.URL.Scheme != "http" && httpReq.URL.Scheme != "https")) {
		reqErr = fmt.Errorf("invalid ML node HTTP endpoint %q", endpoint)
	}
	if reqErr != nil {
		return nil, observability.Classify(observability.ReasonApplicationErr, observability.WhereEngineMLNodeCall, reqErr)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	observability.InjectRequestContext(ctx, httpReq.Header)
	observability.AttachRequestID(httpReq)
	resp, doErr := v.engine.httpClient.Do(httpReq)
	if doErr != nil || resp.StatusCode != http.StatusOK {
		return resp, doErr
	}
	body, readErr := io.ReadAll(completionapi.NewCappedResponseReader(resp.Body))
	resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	statusCode, body, streamErr := commonvalidation.NormalizeReplayStream(resp.StatusCode, body)
	if streamErr != nil {
		logging.Warn("validator replay stream failed on ML node", "endpoint", endpoint, "error", streamErr)
		return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	}
	resp.StatusCode = statusCode
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

var _ devshardpkg.ValidationEngine = (*Validator)(nil)

// LeaseValidator wraps a ValidationEngine with Postgres-based lease deduplication so
// that only one devshardd instance validates each (escrow_id, inference_id) pair.
// The retry loop uses the inner Validator directly because it already holds the lease.
type LeaseValidator struct {
	validator devshardpkg.ValidationEngine
	phase     *chain.Phase
	leases    leaseOps
	owner     storage.LeaseOwner
	leaseTTL  time.Duration
	acquires  sync.Map // acquireKey -> acquireRec
	results   validationResultCache
}

type acquireRec struct {
	epochID uint64
	at      time.Time
}

// NewLeaseValidator wraps v with Postgres lease deduplication.
func NewLeaseValidator(v devshardpkg.ValidationEngine, phase *chain.Phase, leases leaseOps, owner storage.LeaseOwner, leaseTTL time.Duration) *LeaseValidator {
	if leaseTTL <= 0 {
		leaseTTL = 32 * time.Minute
	}
	return &LeaseValidator{
		validator: v,
		phase:     phase,
		leases:    leases,
		owner:     owner,
		leaseTTL:  leaseTTL,
	}
}

func (c *LeaseValidator) rememberAcquire(escrowID string, inferenceID, epochID uint64, at time.Time) {
	c.acquires.Store(acquireKey{escrowID: escrowID, inferenceID: inferenceID}, acquireRec{epochID: epochID, at: at})
}

func (c *LeaseValidator) forgetAcquire(escrowID string, inferenceID uint64) {
	c.acquires.Delete(acquireKey{escrowID: escrowID, inferenceID: inferenceID})
}

func (c *LeaseValidator) loadAcquire(escrowID string, inferenceID uint64) (acquireRec, bool) {
	v, ok := c.acquires.Load(acquireKey{escrowID: escrowID, inferenceID: inferenceID})
	if !ok {
		return acquireRec{}, false
	}
	rec, ok := v.(acquireRec)
	return rec, ok
}

func (c *LeaseValidator) CanValidate(model string) bool {
	return devshardpkg.CanValidate(c.validator, model)
}

func (c *LeaseValidator) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	if !c.CanValidate(req.Model) {
		return nil, devshardpkg.ErrValidationDeferred
	}
	epochID := resolveValidationEpoch(c.phase, req.EpochID)
	acquired, err := c.leases.Acquire(ctx, req.EscrowID, req.InferenceID, epochID, c.owner)
	if err != nil {
		slog.Warn("devshardd: validation lease failed",
			"escrow", req.EscrowID, "inference", req.InferenceID, "error", err)
		return nil, fmt.Errorf("acquire validation: %w", err)
	} else if !acquired {
		return nil, c.describeConflict(ctx, req.EscrowID, req.InferenceID, epochID)
	}
	c.rememberAcquire(req.EscrowID, req.InferenceID, epochID, time.Now())

	if valid, reason, ok := c.results.get(req.EscrowID, req.InferenceID); ok {
		return &devshardpkg.ValidateResult{Valid: valid, Reason: reason}, nil
	}

	result, err := c.validator.Validate(ctx, req)
	if err != nil {
		c.releaseAndForget(ctx, req.EscrowID, req.InferenceID, epochID)
		return nil, err
	}
	c.results.put(req.EscrowID, req.InferenceID, result)
	return result, nil
}

// describeConflict turns a refused acquire into an error that says what was in
// the way. The extra read costs one query on a path that has already given up
// on validating. The caller logs it, and stamps the validation cooldown unless
// ReleasedBeforeRead says the row is already gone.
func (c *LeaseValidator) describeConflict(ctx context.Context, escrowID string, inferenceID, epochID uint64) error {
	conflict := &devshardpkg.LeaseConflict{}
	info, found, err := c.leases.DescribeLease(ctx, escrowID, inferenceID, epochID)
	switch {
	case err != nil:
		conflict.Detail = fmt.Sprintf("lease read failed: %v", err)
	case !found:
		// Released or pruned between the acquire and this read, so the
		// inference is already re-pickable.
		conflict.Detail = devshardpkg.LeaseRowAbsentDetail
	default:
		conflict.Status = string(info.Status)
		conflict.Owner = info.InstanceAddr
		conflict.InstanceID = info.InstanceID
		conflict.Hostname = info.Hostname
		conflict.ClaimedAt = info.ClaimedAt
		conflict.Stale = info.Status == storage.LeaseStatusPending &&
			!info.ClaimedAt.IsZero() && time.Since(info.ClaimedAt) > c.leaseTTL
	}
	return conflict
}

// AllowValidationSubmit gates MsgValidation publish: TTL since acquire and
// current pending ownership must both still hold. On failure the caller must
// ReleaseValidationLease; this method leaves the local acquire recorded so
// release can see the epoch.
func (c *LeaseValidator) AllowValidationSubmit(ctx context.Context, escrowID string, inferenceID uint64) error {
	_, err := c.ensureLeaseStillValid(ctx, escrowID, inferenceID)
	return err
}

func (c *LeaseValidator) MarkValidationSubmitted(ctx context.Context, escrowID string, inferenceID uint64) error {
	rec, err := c.ensureLeaseStillValid(ctx, escrowID, inferenceID)
	if err != nil {
		c.forgetAcquire(escrowID, inferenceID)
		return err
	}
	err = c.leases.SetResult(ctx, escrowID, inferenceID, rec.epochID, storage.LeaseStatusSubmitted, c.owner)
	c.forgetAcquire(escrowID, inferenceID)
	if errors.Is(err, storage.ErrLeaseNotOwned) {
		return fmt.Errorf("%w: %v", devshardpkg.ErrValidationLeaseAbandoned, err)
	}
	return err
}

func (c *LeaseValidator) ReleaseValidationLease(ctx context.Context, escrowID string, inferenceID uint64) error {
	rec, ok := c.loadAcquire(escrowID, inferenceID)
	if !ok {
		return nil
	}
	releaseCtx, cancel := leaseReleaseContext(ctx)
	defer cancel()
	err := c.leases.Release(releaseCtx, escrowID, inferenceID, rec.epochID, c.owner)
	c.forgetAcquire(escrowID, inferenceID)
	return err
}

func (c *LeaseValidator) releaseAndForget(ctx context.Context, escrowID string, inferenceID, epochID uint64) {
	releaseCtx, cancel := leaseReleaseContext(ctx)
	defer cancel()
	if err := c.leases.Release(releaseCtx, escrowID, inferenceID, epochID, c.owner); err != nil {
		slog.Warn("devshardd: validation lease release failed",
			"escrow", escrowID, "inference", inferenceID, "error", err)
	}
	c.forgetAcquire(escrowID, inferenceID)
}

// leaseReleaseTimeout bounds a best-effort DELETE after Validate aborts.
// Shutdown cancels the request ctx; Release must not inherit that cancel or
// the row stays pending until TTL and blocks the sibling.
const leaseReleaseTimeout = 5 * time.Second

func leaseReleaseContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), leaseReleaseTimeout)
}

func (c *LeaseValidator) ensureLeaseStillValid(ctx context.Context, escrowID string, inferenceID uint64) (acquireRec, error) {
	rec, ok := c.loadAcquire(escrowID, inferenceID)
	if !ok {
		return acquireRec{}, fmt.Errorf("%w: missing local acquire time", devshardpkg.ErrValidationLeaseAbandoned)
	}
	if time.Since(rec.at) > c.leaseTTL {
		slog.Info("devshardd: validation lease TTL exceeded; abandon submit",
			"escrow", escrowID, "inference", inferenceID, "lease_ttl", c.leaseTTL)
		return rec, fmt.Errorf("%w: %w", devshardpkg.ErrValidationLeaseAbandoned, devshardpkg.ErrValidationLeaseTTLExceeded)
	}
	owned, err := c.leases.OwnsPendingLease(ctx, escrowID, inferenceID, rec.epochID, c.owner)
	if err != nil {
		return rec, err
	}
	if !owned {
		slog.Info("devshardd: validation lease no longer owned; abandon submit",
			"escrow", escrowID, "inference", inferenceID)
		return rec, fmt.Errorf("%w: pending lease not owned", devshardpkg.ErrValidationLeaseAbandoned)
	}
	return rec, nil
}

var _ devshardpkg.ValidationEngine = (*LeaseValidator)(nil)
var _ devshardpkg.ValidationCompletionRecorder = (*LeaseValidator)(nil)
