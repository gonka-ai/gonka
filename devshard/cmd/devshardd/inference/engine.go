package inference

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"common/chain"
	"common/logging"
	mlnodeclient "common/nodemanager"
	mlnodegen "common/nodemanager/gen"
	"devshard"
	"devshard/observability"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// acquireTimeout bounds each Acquire RPC so a dead dapi fails fast and we
// can enter local-cache fallback without waiting on gRPC dial/backoff.
const acquireTimeout = 2 * time.Second

// maxAcquireAttempts is used on the gRPC path when dapi is up but has no free
// nodes (ResourceExhausted). More attempts than the in-process broker path
// because dapi's broker may need a few seconds to update node IntendedStatus
// after an epoch phase transition.
const maxAcquireAttempts = 10

// fallbackSlotWait is how long fallback waits when every known node is at its
// local capacity bound before retrying PickNode.
const fallbackSlotWait = 100 * time.Millisecond

// maxSessionIDLength bounds the gateway-supplied session id before it keys a cache
// namespace or an mlnode binding; a longer one is dropped rather than stored.
const maxSessionIDLength = 512

// cacheSaltField is vLLM's own knob: prefix-cache blocks are shared only between requests whose salt matches.
const cacheSaltField = "cache_salt"

// sessionIDTooLongOnce logs once per process: a healthy gateway token is a fixed 64-hex
// digest, so a longer id here means a foreign gateway or a changed token format.
var sessionIDTooLongOnce sync.Once

// Engine implements devshard.InferenceEngine for the standalone devshardd binary.
// It acquires a locked ML node via NodeManager gRPC, POSTs directly, and releases
// with an outcome reflecting the result.
//
// When dapi is unreachable it falls back to mgr's passively learned cache and
// round-robins direct HTTP without lock/release. When capacity has been
// observed via ListNodeCapacity, fallback is bounded by capacity.Cache.
type Engine struct {
	mlClient        *mlnodeclient.Client
	mgr             *mlnodeclient.Manager
	capacity        *mlnodeclient.Cache
	payloadStore    PayloadStore
	httpClient      *http.Client
	chainParams     ChainParamsProvider
	phase           *chain.Phase
	affinityEnabled bool
}

// NewEngine creates an Engine backed by a NodeManager gRPC client and optional
// passive ML-node cache for dapi-unreachable fallback. capacity may be nil,
// in which case fallback is unbounded (matches old-dapi/never-observed behavior).
// affinityEnabled is the participant's own switch over mlnode stickiness only; the cache
// salt rides with any non-empty session id, because isolation is not the participant's to waive.
func NewEngine(
	mlClient *mlnodeclient.Client,
	mgr *mlnodeclient.Manager,
	capacity *mlnodeclient.Cache,
	payloadStore PayloadStore,
	chainParams ChainParamsProvider,
	phase *chain.Phase,
	affinityEnabled bool,
) *Engine {
	return &Engine{
		mlClient:        mlClient,
		mgr:             mgr,
		capacity:        capacity,
		payloadStore:    payloadStore,
		httpClient:      NewNoRedirectClient(mlNodeHTTPTimeout),
		chainParams:     chainParams,
		phase:           phase,
		affinityEnabled: affinityEnabled,
	}
}

// Execute runs an inference on an ML node acquired via NodeManager gRPC.
//
// Flow: ModifyRequestBody -> POST to /v1/chat/completions -> processor ->
// canonicalize + store payloads.
// Node acquisition prefers gRPC (dapi authoritative); on dapi-unreachable it
// falls back to the passive ML-node cache.
func (e *Engine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	return executeInference(ctx, req, e.payloadStore, e.phase.EpochID(), func(ctx context.Context, model string, body []byte) (*http.Response, error) {
		return e.executeMLRequest(ctx, model, req.EscrowID, req.SessionID, body)
	}, e.chainParams)
}

func (e *Engine) executeMLRequest(ctx context.Context, model, escrowID, sessionID string, body []byte) (*http.Response, error) {
	if len(sessionID) > maxSessionIDLength {
		sessionIDTooLongOnce.Do(func() {
			logging.Warn("Dropping session id longer than the bound", types.Inferences, "length", len(sessionID), "max", maxSessionIDLength)
		})
		sessionID = ""
	}
	// The salt only narrows which KV blocks may be reused, so it rides along with the id itself.
	if sessionID != "" {
		body = withCacheSalt(body, escrowID, sessionID)
	}
	// The switch governs mlnode stickiness, which is the part that changes this node's scheduling.
	stickySessionID := sessionID
	if !e.affinityEnabled {
		stickySessionID = ""
	}
	resp, err := e.doWithLockedNode(ctx, observability.PathExecute, model, escrowID, stickySessionID, func(endpoint string) (*http.Response, error) {
		url := endpoint + "/v1/chat/completions"
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			return nil, observability.Classify(observability.ReasonApplicationErr, observability.WhereEngineMLNodeCall, reqErr)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		observability.InjectRequestContext(ctx, httpReq.Header)
		observability.AttachRequestID(httpReq)
		return e.httpClient.Do(httpReq)
	})
	if err != nil {
		return nil, fmt.Errorf("execute inference: %w", err)
	}
	return resp, nil
}

// withCacheSalt salts one session's prefix cache, scoped to its escrow so the same session id
// from two escrows never shares KV blocks. An unparseable body passes through unchanged.
func withCacheSalt(body []byte, escrowID, sessionID string) []byte {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return body
	}
	digest := sha256.Sum256([]byte(escrowID + "\x00" + sessionID))
	// The digest is hex, so quoting it is the whole of JSON string encoding.
	fields[cacheSaltField] = json.RawMessage(`"` + hex.EncodeToString(digest[:]) + `"`)
	salted, err := json.Marshal(fields)
	if err != nil {
		return body
	}
	return salted
}

// doWithLockedNode tries NodeManager gRPC first. On success it records the
// node in the passive cache (Observe), POSTs, and Releases. If dapi is
// unreachable it falls back to mgr.PickNode round-robin without lock/release.
// ResourceExhausted (dapi up, no free nodes) stays on the gRPC retry path.
// escrowID is forwarded on Acquire so dapi can attribute per-escrow load.
func (e *Engine) doWithLockedNode(
	ctx context.Context,
	path observability.Path,
	model string,
	escrowID string,
	sessionID string,
	fn func(endpoint string) (*http.Response, error),
) (*http.Response, error) {
	var excluded []string
	excludedSet := make(map[string]struct{})
	var lastErr error
	lastReason := observability.ReasonAcquireErr

	for attempt := 0; attempt < maxAcquireAttempts; attempt++ {
		acqCtx, cancel := context.WithTimeout(ctx, acquireTimeout)
		acq, err := e.mlClient.Acquire(acqCtx, model, excluded, escrowID, sessionID)
		cancel()

		if err != nil {
			if ctx.Err() != nil {
				lastReason = observability.ReasonTimeout
				return nil, observability.Classify(lastReason, observability.WhereEngineMLNodeCall, ctx.Err())
			}
			if shouldFallback(err) {
				return e.doWithFallbackNodes(ctx, path, model, excludedSet, fn, err)
			}

			// dapi up but no nodes (ResourceExhausted) or other transient
			// acquire errors: sleep and retry; do not fall back.
			lastReason = observability.ReasonAcquireErr
			observability.IncMLNodeAttempt(path, lastReason, "")
			lastErr = fmt.Errorf("acquire: %w", err)
			select {
			case <-ctx.Done():
				lastReason = observability.ReasonTimeout
				return nil, observability.Classify(lastReason, observability.WhereEngineMLNodeCall, ctx.Err())
			case <-time.After(2 * time.Second):
			}
			continue
		}

		if e.mgr != nil {
			e.mgr.Observe(model, acq.NodeId, acq.Endpoint)
		}

		started := time.Now()
		resp, httpErr := fn(acq.Endpoint)
		outcome := mlnodegen.ReleaseOutcome_SUCCESS

		lastReason = observability.ClassifyMLNodeHTTP(resp, httpErr, ctx.Err())
		observability.IncMLNodeAttempt(path, lastReason, acq.NodeId)
		observability.ObserveMLNodeCall(path, acq.NodeId, observability.MetricPhaseTotal, started)

		switch lastReason {
		case observability.ReasonTransportErr, observability.ReasonTimeout:
			outcome = mlnodegen.ReleaseOutcome_TRANSPORT_ERROR
			lastErr = httpErr
		case observability.ReasonHTTP5xx:
			if resp != nil {
				resp.Body.Close()
			}
			outcome = mlnodegen.ReleaseOutcome_TRANSPORT_ERROR
			if resp != nil {
				lastErr = fmt.Errorf("upstream status %d", resp.StatusCode)
			}
			resp = nil
		case observability.ReasonHTTP4xx:
			// 4xx surfaced to caller without rotation.
		}

		if releaseErr := e.mlClient.Release(ctx, acq.LockId, outcome); releaseErr != nil {
			observability.IncMLNodeAttempt(path, observability.ReasonReleaseErr, acq.NodeId)
			if lastErr == nil {
				lastReason = observability.ReasonReleaseErr
				lastErr = fmt.Errorf("release: %w", releaseErr)
			}
		}

		if outcome == mlnodegen.ReleaseOutcome_SUCCESS {
			return resp, nil
		}

		if acq.NodeId != "" {
			excluded = append(excluded, acq.NodeId)
			excludedSet[acq.NodeId] = struct{}{}
		}
	}

	if lastErr == nil {
		lastErr = errors.New("no attempts made")
	}
	if lastReason == observability.ReasonOK {
		lastReason = observability.ReasonTransportErr
	}
	return nil, observability.Classify(lastReason, observability.WhereEngineMLNodeCall, lastErr)
}

// doWithFallbackNodes serves inference from the passive cache when dapi is
// unreachable. No lock/release — degraded mode. Rotates on transport/5xx.
// When capacity has been observed, each attempt takes a local in-flight slot
// for (nodeID, model); old DAPI / never-observed capacity is unbounded.
func (e *Engine) doWithFallbackNodes(
	ctx context.Context,
	path observability.Path,
	model string,
	excluded map[string]struct{},
	fn func(endpoint string) (*http.Response, error),
	acquireErr error,
) (*http.Response, error) {
	if e.mgr == nil {
		return nil, observability.Classify(
			observability.ReasonAcquireErr,
			observability.WhereEngineMLNodeCall,
			fmt.Errorf("acquire: %w", acquireErr),
		)
	}

	limit := e.capacity != nil && e.capacity.HasObservedCapacity()
	capacityExcluded := make(map[string]struct{})

	lastErr := fmt.Errorf("acquire: %w", acquireErr)
	lastReason := observability.ReasonAcquireErr

	for {
		if ctx.Err() != nil {
			lastReason = observability.ReasonTimeout
			return nil, observability.Classify(lastReason, observability.WhereEngineMLNodeCall, ctx.Err())
		}

		pickExcluded := excluded
		if limit && len(capacityExcluded) > 0 {
			pickExcluded = mergeExcluded(excluded, capacityExcluded)
		}

		endpoint, nodeID, ok := e.mgr.PickNode(model, pickExcluded)
		if !ok {
			if limit && len(capacityExcluded) > 0 {
				// Every known node is at its local bound — wait and retry.
				clear(capacityExcluded)
				select {
				case <-ctx.Done():
					lastReason = observability.ReasonTimeout
					return nil, observability.Classify(lastReason, observability.WhereEngineMLNodeCall, ctx.Err())
				case <-time.After(fallbackSlotWait):
				}
				continue
			}
			observability.IncMLNodeAttempt(path, lastReason, "")
			return nil, observability.Classify(
				lastReason,
				observability.WhereEngineMLNodeCall,
				fmt.Errorf("mlnode fallback: no cached nodes for model %q: %w", model, lastErr),
			)
		}

		acquired := false
		acquiredUnknown := false
		if limit {
			if _, known := e.capacity.Get(nodeID); known {
				if !e.capacity.TryAcquire(nodeID, model) {
					capacityExcluded[nodeID] = struct{}{}
					continue
				}
				acquired = true
			} else if !e.capacity.TryAcquireUnknown(nodeID, model) {
				// PickNode returned a node dapi never reported. Bound it with a
				// synthetic budget instead of an unbounded bypass; retry another.
				capacityExcluded[nodeID] = struct{}{}
				continue
			} else {
				acquiredUnknown = true
			}
		}

		started := time.Now()
		resp, httpErr := fn(endpoint)
		if acquired {
			e.capacity.Release(nodeID, model)
		}
		if acquiredUnknown {
			e.capacity.ReleaseUnknown(nodeID, model)
		}
		lastReason = observability.ClassifyMLNodeHTTP(resp, httpErr, ctx.Err())
		observability.IncMLNodeAttempt(path, lastReason, nodeID)
		observability.ObserveMLNodeCall(path, nodeID, observability.MetricPhaseTotal, started)

		switch lastReason {
		case observability.ReasonTransportErr, observability.ReasonTimeout:
			lastErr = httpErr
			if lastErr == nil {
				lastErr = errors.New("mlnode fallback: transport error")
			}
			if nodeID != "" {
				excluded[nodeID] = struct{}{}
			}
			continue
		case observability.ReasonHTTP5xx:
			if resp != nil {
				resp.Body.Close()
			}
			if resp != nil {
				lastErr = fmt.Errorf("upstream status %d", resp.StatusCode)
			} else {
				lastErr = errors.New("mlnode fallback: upstream 5xx")
			}
			if nodeID != "" {
				excluded[nodeID] = struct{}{}
			}
			continue
		default:
			// Success and 4xx are returned as-is (no rotation on 4xx).
			return resp, nil
		}
	}
}

func mergeExcluded(a, b map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		out[k] = struct{}{}
	}
	for k := range b {
		out[k] = struct{}{}
	}
	return out
}

// shouldFallback reports whether an Acquire error means dapi is unreachable
// and the passive cache should be used. ResourceExhausted is not a fallback
// trigger — dapi is up and remains authoritative for load balancing.
func shouldFallback(err error) bool {
	if mlnodeclient.IsUnavailable(err) {
		return true
	}
	// Short acquire timeout while the request is still live: treat as
	// unreachable so we fail over instead of sleeping on a dead dapi.
	return status.Code(err) == codes.DeadlineExceeded
}

var _ devshard.InferenceEngine = (*Engine)(nil)
