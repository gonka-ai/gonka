package inference

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	mlnodegen "common/nodemanager/gen"
	"devshard/observability"
)

const (
	// renderTimeout bounds one render call on an ML node; a render reads the chat template and the
	// tokenizer and runs no model.
	renderTimeout        = 10 * time.Second
	renderReleaseTimeout = 5 * time.Second
	renderMaxRespBytes   = 32 << 20
)

// RenderOnMLNode posts a render request (path, such as /v1/chat/completions/render) to an ML node
// serving the model and returns the node's status and body, for the counts of the challenge votes.
// The render runs on the node's CPU and takes no GPU time, so it spends no validation credit. busy
// is true when no node could be taken, as while the network runs PoC.
func (e *Engine) RenderOnMLNode(ctx context.Context, model, path string, body []byte) (int, []byte, bool, error) {
	acq, err := e.acquireNode(ctx, model, nil, "")
	if err != nil {
		return 0, nil, true, err
	}
	observability.ObserveRenderNode(ctx, acq.NodeId, acq.Endpoint)
	status, raw, err := e.postRender(ctx, acq.Endpoint+path, body)
	outcome := mlnodegen.ReleaseOutcome_SUCCESS
	if err != nil {
		outcome = mlnodegen.ReleaseOutcome_TRANSPORT_ERROR
	}
	// Release must survive cancellation of the render request, but remain bounded.
	releaseCtx, cancelRelease := context.WithTimeout(context.WithoutCancel(ctx), renderReleaseTimeout)
	defer cancelRelease()
	if releaseErr := e.releaseNode(releaseCtx, acq, outcome); releaseErr != nil {
		observability.IncMLNodeAttempt(observability.PathValidate, observability.ReasonReleaseErr, acq.NodeId)
	}
	return status, raw, false, err
}

func (e *Engine) postRender(ctx context.Context, url string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if req.URL.Host == "" || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
		return 0, nil, fmt.Errorf("invalid ML node HTTP endpoint %q", url)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, renderMaxRespBytes+1))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
}
