package inference

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"common/chain"
	mlnodeclient "common/nodemanager"
	mlnodegen "common/nodemanager/gen"
	"devshard"
	"devshard/observability"

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

// Engine implements devshard.InferenceEngine for the standalone devshardd binary.
// It acquires a locked ML node via NodeManager gRPC, POSTs directly, and releases
// with an outcome reflecting the result.
//
// When dapi is unreachable it falls back to mgr's passively learned cache and
// round-robins direct HTTP without lock/release.
type Engine struct {
	mlClient     *mlnodeclient.Client
	mgr          *mlnodeclient.Manager
	payloadStore PayloadStore
	httpClient   *http.Client
	chainParams  ChainParamsProvider
	phase        *chain.Phase
}

// NewEngine creates an Engine backed by a NodeManager gRPC client and optional
// passive ML-node cache for dapi-unreachable fallback.
func NewEngine(
	mlClient *mlnodeclient.Client,
	mgr *mlnodeclient.Manager,
	payloadStore PayloadStore,
	chainParams ChainParamsProvider,
	phase *chain.Phase,
) *Engine {
	return &Engine{
		mlClient:     mlClient,
		mgr:          mgr,
		payloadStore: payloadStore,
		httpClient:   NewNoRedirectClient(mlNodeHTTPTimeout),
		chainParams:  chainParams,
		phase:        phase,
	}
}

func (e *Engine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	return executeInference(ctx, req, e.payloadStore, e.phase.EpochID(), e.executeMLRequest, e.chainParams)
}

func (e *Engine) executeMLRequest(ctx context.Context, model string, body []byte) (*http.Response, error) {
	resp, err := e.doWithLockedNode(ctx, observability.PathExecute, model, func(endpoint string) (*http.Response, error) {
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

// doWithLockedNode tries NodeManager gRPC first. On success it records the
// node in the passive cache (Observe), POSTs, and Releases. If dapi is
// unreachable it falls back to mgr.PickNode round-robin without lock/release.
// ResourceExhausted (dapi up, no free nodes) stays on the gRPC retry path.
func (e *Engine) doWithLockedNode(
	ctx context.Context,
	path observability.Path,
	model string,
	fn func(endpoint string) (*http.Response, error),
) (*http.Response, error) {
	var excluded []string
	excludedSet := make(map[string]struct{})
	var lastErr error
	lastReason := observability.ReasonAcquireErr

	for attempt := 0; attempt < maxAcquireAttempts; attempt++ {
		acqCtx, cancel := context.WithTimeout(ctx, acquireTimeout)
		acq, err := e.mlClient.Acquire(acqCtx, model, excluded)
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

	lastErr := fmt.Errorf("acquire: %w", acquireErr)
	lastReason := observability.ReasonAcquireErr

	for {
		if ctx.Err() != nil {
			lastReason = observability.ReasonTimeout
			return nil, observability.Classify(lastReason, observability.WhereEngineMLNodeCall, ctx.Err())
		}

		endpoint, nodeID, ok := e.mgr.PickNode(model, excluded)
		if !ok {
			observability.IncMLNodeAttempt(path, lastReason, "")
			return nil, observability.Classify(
				lastReason,
				observability.WhereEngineMLNodeCall,
				fmt.Errorf("mlnode fallback: no cached nodes for model %q: %w", model, lastErr),
			)
		}

		started := time.Now()
		resp, httpErr := fn(endpoint)
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
