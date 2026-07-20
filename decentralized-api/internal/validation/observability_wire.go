package validation

import (
	"context"
	"net/http"

	commonvalidation "common/validation"
	"decentralized-api/observability"
)

type dapiPayloadFetchObserver struct{}

type dapiPayloadFetchSpan struct {
	op *observability.Operation
}

func (s dapiPayloadFetchSpan) FinishErr(err *error) {
	s.op.FinishErr(err)
}

func (dapiPayloadFetchObserver) StartPayloadFetch(ctx context.Context, requestURL, validatorAddress string, epochID int64) (context.Context, commonvalidation.PayloadFetchSpan) {
	ctx, op := observability.Inference.StartPayloadFetch(ctx, requestURL, validatorAddress, epochID)
	return ctx, dapiPayloadFetchSpan{op: op}
}

func (dapiPayloadFetchObserver) InjectRequestContext(ctx context.Context, headers http.Header) {
	observability.Inference.InjectRequestContext(ctx, headers)
}

func (dapiPayloadFetchObserver) AttachRequestID(req *http.Request) {
	observability.AttachRequestID(req)
}

func init() {
	commonvalidation.SetPayloadFetchObserver(dapiPayloadFetchObserver{})
}
