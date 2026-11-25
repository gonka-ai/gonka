// Package timeoutclaim holds the choice between an error-miss claim and a timeout vote, the Finish a claim carries, and the timeout vote's payload. See README.md.
package timeoutclaim

import (
	"context"
	"time"

	"devshard/host"
	"devshard/user"
)

// Handler posts a timeout vote or an error-miss claim for an unfinished nonce; *user.Session satisfies it.
type Handler interface {
	HandleTimeout(ctx context.Context, nonce uint64, sendTime time.Time, payload *host.InferencePayload) (user.TimeoutResult, error)
	HandleErrorMiss(ctx context.Context, nonce uint64, finishTx, responsePayload []byte) (user.TimeoutResult, error)
}

// FinishSource yields the Finish transaction a session has queued for a nonce; *user.Session satisfies it.
type FinishSource interface {
	FinishTxFor(inferenceID uint64) []byte
}

// FinishTx is the Finish an error-miss claim carries: the one the host's reply carried when there is one, otherwise the one queued in the session.
func FinishTx(replyFinishTx []byte, nonce uint64, queued FinishSource) []byte {
	if len(replyFinishTx) > 0 || queued == nil {
		return replyFinishTx
	}
	return queued.FinishTxFor(nonce)
}

// ClaimsMiss reports whether finishTx makes the nonce an error-miss claim rather than a timeout vote: a non-empty Finish does.
func ClaimsMiss(finishTx []byte) bool {
	return len(finishTx) > 0
}

// Resolve claims an error miss when ClaimsMiss holds for finishTx and votes a timeout with payload otherwise.
func Resolve(ctx context.Context, handler Handler, nonce uint64, sendTime time.Time, payload *host.InferencePayload, finishTx, responsePayload []byte) (user.TimeoutResult, error) {
	if ClaimsMiss(finishTx) {
		return handler.HandleErrorMiss(ctx, nonce, finishTx, responsePayload)
	}
	return handler.HandleTimeout(ctx, nonce, sendTime, payload)
}
