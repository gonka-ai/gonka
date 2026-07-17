package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	internaldevshard "decentralized-api/internal/devshard"
	"decentralized-api/payloadstorage"

	"devshard"
	mlnodeclient "devshard/mlnode"
	nmgen "devshard/nodemanager/gen"
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

// devshardEngine implements devshard.InferenceEngine for the standalone
// devshardd binary. Unlike dapi's in-process adapter it has no broker; it
// acquires a locked ML node via NodeManager gRPC, POSTs directly, and releases
// with an outcome reflecting the result.
//
// When dapi is unreachable it falls back to mgr's passively learned cache and
// round-robins direct HTTP without lock/release.
type devshardEngine struct {
	mlClient     *mlnodeclient.Client
	mgr          *mlnodeclient.Manager
	payloadStore payloadstorage.PayloadStorage
	httpClient   *http.Client
	chainParams  internaldevshard.ChainParamsProvider
}

func newDevshardEngine(
	mlClient *mlnodeclient.Client,
	mgr *mlnodeclient.Manager,
	payloadStore payloadstorage.PayloadStorage,
	httpClient *http.Client,
	chainParams internaldevshard.ChainParamsProvider,
) *devshardEngine {
	return &devshardEngine{
		mlClient:     mlClient,
		mgr:          mgr,
		payloadStore: payloadStore,
		httpClient:   httpClient,
		chainParams:  chainParams,
	}
}

// Execute runs an inference on an ML node acquired via NodeManager gRPC.
//
// Flow: ModifyRequestBody -> POST to /v1/chat/completions -> processor ->
// canonicalize + store payloads.
// Node acquisition prefers gRPC (dapi authoritative); on dapi-unreachable it
// falls back to the passive ML-node cache.
func (e *devshardEngine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	return internaldevshard.ExecuteInferenceWithExecutor(
		ctx,
		req,
		e.payloadStore,
		req.EpochID,
		e.executeMLRequest,
		e.chainParams,
	)
}

func (e *devshardEngine) executeMLRequest(ctx context.Context, model string, body []byte) (*http.Response, error) {
	resp, err := e.doWithLockedNode(ctx, observability.PathExecute, model, func(endpoint string) (*http.Response, error) {
		url := endpoint + "/v1/chat/completions"
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			return nil, reqErr
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
func (e *devshardEngine) doWithLockedNode(
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
		outcome := nmgen.ReleaseOutcome_SUCCESS

		lastReason = observability.ClassifyMLNodeHTTP(resp, httpErr, ctx.Err())
		observability.IncMLNodeAttempt(path, lastReason, acq.NodeId)
		observability.ObserveMLNodeCall(path, acq.NodeId, observability.MetricPhaseTotal, started)

		switch lastReason {
		case observability.ReasonTransportErr, observability.ReasonTimeout:
			outcome = nmgen.ReleaseOutcome_TRANSPORT_ERROR
			lastErr = httpErr
		case observability.ReasonHTTP5xx:
			// Upstream 5xx: also rotate nodes.
			resp.Body.Close()
			outcome = nmgen.ReleaseOutcome_TRANSPORT_ERROR
			lastErr = fmt.Errorf("upstream status %d", resp.StatusCode)
			resp = nil
		case observability.ReasonHTTP4xx:
			// 4xx surfaced to caller without rotation.
		}

		// Release must fire regardless of outcome to release the lock.
		if releaseErr := e.mlClient.Release(ctx, acq.LockId, outcome); releaseErr != nil {
			observability.IncMLNodeAttempt(path, observability.ReasonReleaseErr, acq.NodeId)
			// Release failure is logged via lastErr but does not block
			// retries or the caller -- the lock will eventually expire.
			if lastErr == nil {
				lastReason = observability.ReasonReleaseErr
				lastErr = fmt.Errorf("release: %w", releaseErr)
			}
		}

		if outcome == nmgen.ReleaseOutcome_SUCCESS {
			return resp, nil
		}

		// Failure: rotate excluded set and retry via gRPC.
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
func (e *devshardEngine) doWithFallbackNodes(
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
			resp.Body.Close()
			lastErr = fmt.Errorf("upstream status %d", resp.StatusCode)
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

// Compile-time check.
var _ devshard.InferenceEngine = (*devshardEngine)(nil)
