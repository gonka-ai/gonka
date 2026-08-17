package validation

import (
	"context"
	"net/http"
)

// PayloadFetchSpan closes an in-flight payload-fetch observation.
type PayloadFetchSpan interface {
	FinishErr(err *error)
}

type noopPayloadFetchSpan struct{}

func (noopPayloadFetchSpan) FinishErr(*error) {}

// PayloadFetchObserver wires tracing and request metadata for payload HTTP fetches.
// decentralized-api registers the v2 implementation at init via SetPayloadFetchObserver.
type PayloadFetchObserver interface {
	StartPayloadFetch(ctx context.Context, requestURL, validatorAddress string, epochID int64) (context.Context, PayloadFetchSpan)
	InjectRequestContext(ctx context.Context, headers http.Header)
	AttachRequestID(req *http.Request)
}

type noopPayloadFetchObserver struct{}

func (noopPayloadFetchObserver) StartPayloadFetch(ctx context.Context, _ string, _ string, _ int64) (context.Context, PayloadFetchSpan) {
	return ctx, noopPayloadFetchSpan{}
}

func (noopPayloadFetchObserver) InjectRequestContext(context.Context, http.Header) {}

func (noopPayloadFetchObserver) AttachRequestID(*http.Request) {}

var payloadFetchObserver PayloadFetchObserver = noopPayloadFetchObserver{}

// SetPayloadFetchObserver installs payload-fetch observability hooks.
func SetPayloadFetchObserver(o PayloadFetchObserver) {
	if o == nil {
		payloadFetchObserver = noopPayloadFetchObserver{}
		return
	}
	payloadFetchObserver = o
}
