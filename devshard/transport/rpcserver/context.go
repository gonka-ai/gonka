package rpcserver

import "context"

type escrowIDKey struct{}

func WithEscrowID(ctx context.Context, escrowID string) context.Context {
	return context.WithValue(ctx, escrowIDKey{}, escrowID)
}

func EscrowIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(escrowIDKey{}).(string)
	return v
}
