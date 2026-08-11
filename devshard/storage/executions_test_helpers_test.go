package storage

import "context"

// executionStoreStub lets narrow storage test doubles satisfy Storage without
// weakening the production interface's mandatory execution-fencing contract.
type executionStoreStub struct{}

func (executionStoreStub) ClaimExecution(context.Context, uint64, string, uint64, string) (ExecutionClaim, error) {
	return ExecutionClaim{Acquired: true, Fence: 1, Status: ExecutionClaimed}, nil
}

func (executionStoreStub) GetExecution(context.Context, uint64, string, uint64) (ExecutionClaim, error) {
	return ExecutionClaim{}, ErrExecutionNotFound
}

func (executionStoreStub) MarkExecutionDispatched(context.Context, uint64, string, uint64, string, uint64, string) error {
	return nil
}

func (executionStoreStub) AbandonExecution(context.Context, uint64, string, uint64, string, uint64) error {
	return nil
}

func (executionStoreStub) CompleteExecution(context.Context, uint64, string, uint64, string, uint64, []byte) error {
	return nil
}
