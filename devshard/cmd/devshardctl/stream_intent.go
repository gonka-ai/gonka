package main

import "context"

// streamClientIntent records whether the client's ORIGINAL request asked for
// SSE (stream:true) and for stream_options.include_usage. The gateway will
// force stream upstream (Step 9); without this the client-facing shape branch
// and usage-chunk suppression cannot tell a streaming client from a JSON one.
// Zero value means non-streaming / no usage — historical default.
type streamClientIntent struct {
	wantsStream bool // original request had stream:true
	wantsUsage  bool // original request had stream_options.include_usage:true
}

func streamClientIntentFromRequest(req chatRequest) streamClientIntent {
	return streamClientIntent{
		wantsStream: req.Stream,
		// include_usage is only meaningful with stream:true; PreValidation strips
		// stream_options otherwise, so a non-streaming client never carries one.
		wantsUsage: req.Stream && req.StreamOptions.IncludeUsage,
	}
}

type streamIntentContextKey struct{}

func withStreamClientIntent(ctx context.Context, intent streamClientIntent) context.Context {
	return context.WithValue(ctx, streamIntentContextKey{}, intent)
}

func streamClientIntentFromContext(ctx context.Context) streamClientIntent {
	intent, _ := ctx.Value(streamIntentContextKey{}).(streamClientIntent)
	return intent
}

// clientRequestIntent is everything about the ORIGINAL client request that the
// normalized body can no longer answer, captured by whoever first parsed that
// request.
type clientRequestIntent struct {
	stream  streamClientIntent
	logprob logprobClientIntent
}

type clientRequestIntentContextKey struct{}

// withClientRequestIntent pins the client's intent for the in-process gateway →
// runtime-proxy handoff.
//
// The gateway normalizes before forwarding, which force-sets stream,
// stream_options.include_usage, logprobs, and top_logprobs on the wire body, and
// serveChatToRuntime hands that body to the proxy, which normalizes it a second
// time. Intent re-derived from the forwarded body is therefore the forced
// values, not the client's ask: a stream:false client would be routed to
// handleStreaming and served SSE, and forced logprobs would survive the
// client-facing strip. The proxy prefers this value and falls back to the body
// only when it is the first parser (direct-to-runtime requests and tests).
//
// This travels in the request context rather than a header because the handoff
// is in-process: nothing on the wire can forge it.
func withClientRequestIntent(ctx context.Context, intent clientRequestIntent) context.Context {
	return context.WithValue(ctx, clientRequestIntentContextKey{}, intent)
}

func clientRequestIntentFromContext(ctx context.Context) (clientRequestIntent, bool) {
	intent, ok := ctx.Value(clientRequestIntentContextKey{}).(clientRequestIntent)
	return intent, ok
}

// resolveClientRequestIntent picks the intent the response boundary must honor,
// preferring the pinned copy over anything derived from a body that a previous
// hop may already have force-rewritten. The second result names the source for
// logs.
func resolveClientRequestIntent(ctx context.Context, req chatRequest) (clientRequestIntent, string) {
	if pinned, ok := clientRequestIntentFromContext(ctx); ok {
		return pinned, "gateway"
	}
	return clientRequestIntent{
		stream:  streamClientIntentFromRequest(req),
		logprob: logprobClientIntentFromRequest(req),
	}, "body"
}
