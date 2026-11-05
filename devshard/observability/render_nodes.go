package observability

import (
	"context"
	"net/url"
)

type renderNodeObserverKey struct{}

// WithRenderNodeObserver attaches a diagnostic observer to one render count.
func WithRenderNodeObserver(ctx context.Context, observe func(nodeID, endpoint string)) context.Context {
	return context.WithValue(ctx, renderNodeObserverKey{}, observe)
}

// ObserveRenderNode reports the acquired node without credentials or URL query parameters.
func ObserveRenderNode(ctx context.Context, nodeID, endpoint string) {
	observe, ok := ctx.Value(renderNodeObserverKey{}).(func(string, string))
	if !ok || observe == nil {
		return
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		observe(nodeID, "<invalid>")
		return
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	observe(nodeID, parsed.String())
}
