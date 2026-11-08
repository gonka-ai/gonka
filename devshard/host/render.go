package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"devshard/logging"
)

// ChallengeRenderer counts tokens the way the model's ML node built them, without a GPU: the
// /v1/chat/completions/render route of the host's own ML node, or a vLLM render server
// (`vllm launch render <model>`), runs the model's chat template and tokenizer on CPU. Serves
// reports whether it has a way to count for the model; a voter without one does not vote. The
// counts also return where they were made, for the vote log.
type ChallengeRenderer interface {
	Serves(model string) bool
	// CountPrompt counts the prompt tokens of a chat request.
	CountPrompt(ctx context.Context, model string, request []byte) (uint64, string, error)
	// CountTurn counts the tokens of an assistant turn appended to the request's messages: the
	// request with the turn, less the request with an empty assistant turn.
	CountTurn(ctx context.Context, model string, request []byte, turn map[string]any) (uint64, string, error)
}

// MLNodeRender posts a render request to one of the host's ML nodes serving the model and returns
// the node's answer. busy is true when no ML node of the model could be taken, as while the
// network runs PoC; the count can be tried again later.
type MLNodeRender func(ctx context.Context, model, path string, body []byte) (status int, raw []byte, busy bool, err error)

// RenderSourceMLNode names a count made on the host's own ML node in the vote log.
const RenderSourceMLNode = "ml_node"

// WithChallengeRenderer gives the host's challenge votes a renderer for the usage counts.
func WithChallengeRenderer(r ChallengeRenderer) HostOption {
	return func(h *Host) { h.renderer = r }
}

// WithBinaryVersion names this devshardd's build in the host's vote logs.
func WithBinaryVersion(v string) HostOption {
	return func(h *Host) { h.binaryVersion = v }
}

const (
	renderTimeout      = 10 * time.Second
	renderMaxRespBytes = 32 << 20
)

var (
	errNoRenderServer = errors.New("no render server for model")
	// errRenderModelMissing: the server answered 404, it does not serve the model.
	errRenderModelMissing = errors.New("render server does not serve the model")
	// errRenderRefused: the server refused the body (400, 422), so another server would too.
	errRenderRefused = errors.New("render server refused the body")
	// errRenderBusy: no ML node was free to count and no render server counted either.
	errRenderBusy = errors.New("no ML node free to render")
)

// ParseRenderURLs reads "model=url1|url2,model=url". A model with several render servers spreads
// its counts over them. Empty means none.
func ParseRenderURLs(raw string) (map[string][]string, error) {
	urls := map[string][]string{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		model, list, ok := strings.Cut(entry, "=")
		model = strings.TrimSpace(model)
		if !ok || model == "" {
			return nil, fmt.Errorf("render url entry %q: want model=url", entry)
		}
		for _, url := range strings.Split(list, "|") {
			url = strings.TrimRight(strings.TrimSpace(url), "/")
			if url == "" {
				return nil, fmt.Errorf("render url entry %q: empty url", entry)
			}
			if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
				return nil, fmt.Errorf("render url entry %q: want an http(s) url", entry)
			}
			urls[model] = append(urls[model], url)
		}
	}
	return urls, nil
}

// RenderClient counts on the host's own ML node first and, when the node cannot, on the model's
// render servers in turn, moving on to the next server when one fails.
type RenderClient struct {
	urls   map[string][]string
	mlNode MLNodeRender
	next   sync.Map // model -> *atomic.Uint64
	// routeMissing holds the models whose ML node answered the render route with 404, warned once.
	routeMissing sync.Map
	client       *http.Client
}

// NewRenderClient counts with mlNode, when set, and the render servers in urls.
func NewRenderClient(urls map[string][]string, mlNode MLNodeRender) *RenderClient {
	return &RenderClient{urls: urls, mlNode: mlNode, client: &http.Client{}}
}

func (r *RenderClient) Serves(model string) bool { return r.mlNode != nil || len(r.urls[model]) > 0 }

func (r *RenderClient) CountPrompt(ctx context.Context, model string, request []byte) (uint64, string, error) {
	body, err := renderBody(request)
	if err != nil {
		return 0, "", err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, "", err
	}
	return r.renderChat(ctx, model, raw)
}

func (r *RenderClient) CountTurn(ctx context.Context, model string, request []byte, turn map[string]any) (uint64, string, error) {
	with, without, err := turnBodies(request, turn)
	if err != nil {
		return 0, "", err
	}
	base, source, err := r.renderChat(ctx, model, without)
	if err != nil {
		return 0, source, err
	}
	n, err := r.renderChatFrom(ctx, model, with, source)
	if err != nil {
		return r.retryTurnOnRenderServers(ctx, model, with, without, source, err)
	}
	if n < base {
		return 0, source, nil
	}
	return n - base, source, nil
}

// retryTurnOnRenderServers starts the whole pair again on a different server. Counts from
// different renderers cannot be subtracted: their chat templates may have different baselines.
func (r *RenderClient) retryTurnOnRenderServers(ctx context.Context, model string, with, without []byte, source string, firstErr error) (uint64, string, error) {
	if ctx.Err() != nil {
		return 0, source, firstErr
	}
	urls := r.urls[model]
	if len(urls) == 0 {
		return 0, source, firstErr
	}
	counter, _ := r.next.LoadOrStore(model, new(atomic.Uint64))
	start := counter.(*atomic.Uint64).Add(1) - 1
	var lastErr error
	var lastURL string
	for i := range uint64(len(urls)) {
		url := urls[(start+i)%uint64(len(urls))]
		if url == source {
			continue
		}
		lastURL = url
		base, err := r.renderChatFrom(ctx, model, without, url)
		if err == nil {
			var n uint64
			n, err = r.renderChatFrom(ctx, model, with, url)
			if err == nil {
				if n < base {
					return 0, url, nil
				}
				return n - base, url, nil
			}
		}
		lastErr = err
		if ctx.Err() != nil {
			return 0, url, err
		}
	}
	if lastErr == nil {
		return 0, source, firstErr
	}
	return 0, source, fmt.Errorf("%w; fallback %s: %v", firstErr, lastURL, lastErr)
}

// renderChat counts the prompt tokens of a chat completions request as the ML node builds them:
// /v1/chat/completions/render parses the same ChatCompletionRequest, so message normalization
// (reasoning_content), reasoning_effort and the template options apply the same way.
func (r *RenderClient) renderChat(ctx context.Context, model string, body []byte) (uint64, string, error) {
	return r.post(ctx, model, "/v1/chat/completions/render", body)
}

// renderChatFrom keeps both halves of a turn count on the source that counted the empty turn.
func (r *RenderClient) renderChatFrom(ctx context.Context, model string, body []byte, source string) (uint64, error) {
	var raw []byte
	var err error
	if source == RenderSourceMLNode {
		raw, err = r.postToMLNode(ctx, model, "/v1/chat/completions/render", body)
	} else {
		raw, err = r.postTo(ctx, source, "/v1/chat/completions/render", body)
	}
	if err != nil {
		return 0, err
	}
	return renderTokenCount(raw)
}

func renderTokenCount(raw []byte) (uint64, error) {
	var out struct {
		TokenIDs []json.RawMessage `json:"token_ids"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.TokenIDs == nil {
		return 0, errors.New("render: no token_ids in response")
	}
	return uint64(len(out.TokenIDs)), nil
}

// post sends body to the host's ML node and, when the node does not count it, to the model's
// render servers, starting with the next one in turn, until one answers. A server's refusal is the
// body's and is returned at once; any other failure tries the next server. It returns where the
// count was made or last tried.
func (r *RenderClient) post(ctx context.Context, model, path string, body []byte) (uint64, string, error) {
	var nodeErr error
	if r.mlNode != nil {
		raw, err := r.postToMLNode(ctx, model, path, body)
		var count uint64
		if err == nil {
			count, err = renderTokenCount(raw)
		}
		if err == nil || ctx.Err() != nil {
			return count, RenderSourceMLNode, err
		}
		nodeErr = err
	}
	urls := r.urls[model]
	if len(urls) == 0 {
		if nodeErr != nil {
			return 0, RenderSourceMLNode, nodeErr
		}
		return 0, "", fmt.Errorf("%w %q", errNoRenderServer, model)
	}
	counter, _ := r.next.LoadOrStore(model, new(atomic.Uint64))
	start := counter.(*atomic.Uint64).Add(1) - 1
	var err error
	var url string
	for i := range uint64(len(urls)) {
		url = urls[(start+i)%uint64(len(urls))]
		raw, postErr := r.postTo(ctx, url, path, body)
		err = postErr
		var count uint64
		if err == nil {
			count, err = renderTokenCount(raw)
		}
		if err == nil || errors.Is(err, errRenderRefused) || ctx.Err() != nil {
			return count, url, err
		}
	}
	// No server counted: a busy ML node may count later, a server's own failure would not clear.
	if errors.Is(nodeErr, errRenderBusy) {
		return 0, RenderSourceMLNode, fmt.Errorf("%w; %s: %v", nodeErr, url, err)
	}
	return 0, url, err
}

// postToMLNode counts on the host's own ML node, mapping its answer as postTo maps a server's.
func (r *RenderClient) postToMLNode(ctx context.Context, model, path string, body []byte) ([]byte, error) {
	status, raw, busy, err := r.mlNode(ctx, model, path, body)
	switch {
	case busy:
		if err == nil {
			err = errors.New("no ML node taken")
		}
		return nil, fmt.Errorf("%w: %v", errRenderBusy, err)
	case err != nil:
		return nil, fmt.Errorf("ML node %s: %w", path, err)
	case len(raw) > renderMaxRespBytes:
		return nil, fmt.Errorf("ML node %s: response exceeds %d bytes", path, renderMaxRespBytes)
	case status == http.StatusNotFound:
		if _, warned := r.routeMissing.LoadOrStore(model, struct{}{}); !warned {
			logging.Warn("the ML node has no render route for the model: start vLLM 0.30 and later with "+
				"--enable-scale-out, or give devshardd a render server in DEVSHARD_RENDER_URLS",
				"subsystem", "challenge", "model", model, "path", path)
		}
		return nil, fmt.Errorf("ML node %s: %w", path, errRenderModelMissing)
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return nil, fmt.Errorf("ML node %s: status %d: %w", path, status, errRenderRefused)
	case status != http.StatusOK:
		return nil, fmt.Errorf("ML node %s: status %d", path, status)
	}
	return raw, nil
}

func (r *RenderClient) postTo(ctx context.Context, url, path string, body []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, renderMaxRespBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: read: %w", path, err)
	}
	if len(raw) > renderMaxRespBytes {
		return nil, fmt.Errorf("%s: response exceeds %d bytes", path, renderMaxRespBytes)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%s%s: %w", url, path, errRenderModelMissing)
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity:
		// The server read the body and refused it; any other status is this
		// voter's own setup (auth, limits, rate) and leaves it without a count.
		return nil, fmt.Errorf("%s: status %d: %w", path, resp.StatusCode, errRenderRefused)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s: status %d", path, resp.StatusCode)
	}
	return raw, nil
}

// renderBody is the request without streaming and output limits: the render endpoint returns the
// prompt, not a stream, and refuses a max_tokens that does not fit the context next to the prompt,
// which the prompt does not depend on.
func renderBody(request []byte) (map[string]any, error) {
	var req map[string]any
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}
	if req == nil {
		return nil, errors.New("request is not a JSON object")
	}
	for _, field := range []string{"stream", "stream_options", "max_tokens", "max_completion_tokens", "min_tokens", "logprobs_mode"} {
		delete(req, field)
	}
	return req, nil
}

// turnBodies builds the two render requests CountTurn subtracts: the request's messages followed
// by the served assistant turn, and followed by an empty one. The request's tools and template
// options stay, since templates derive the markup from them.
func turnBodies(request []byte, turn map[string]any) (with, without []byte, err error) {
	body, err := renderBody(request)
	if err != nil {
		return nil, nil, err
	}
	messages, _ := body["messages"].([]any)
	body["add_generation_prompt"] = false
	delete(body, "continue_final_message")
	body["messages"] = append(append([]any(nil), messages...), map[string]any{"role": "assistant", "content": ""})
	if without, err = json.Marshal(body); err != nil {
		return nil, nil, err
	}
	body["messages"] = append(append([]any(nil), messages...), turn)
	with, err = json.Marshal(body)
	return with, without, err
}
