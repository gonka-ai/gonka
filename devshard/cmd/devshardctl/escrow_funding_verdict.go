package main

import "context"

// escrowFundingVerdict carries one escrow's refusal to pay for the request back to the pooled route,
// which hands the request to another escrow instead of failing the client. Handler goroutine only.
type escrowFundingVerdict struct {
	refused bool
}

type escrowFundingVerdictContextKey struct{}

func withEscrowFundingVerdict(ctx context.Context, verdict *escrowFundingVerdict) context.Context {
	return context.WithValue(ctx, escrowFundingVerdictContextKey{}, verdict)
}

// escrowFundingVerdictFromContext reports whether a pooled route is waiting for this escrow's verdict.
// The single-escrow routes record none and answer the client themselves.
func escrowFundingVerdictFromContext(ctx context.Context) (*escrowFundingVerdict, bool) {
	verdict, recorded := ctx.Value(escrowFundingVerdictContextKey{}).(*escrowFundingVerdict)
	return verdict, recorded
}
