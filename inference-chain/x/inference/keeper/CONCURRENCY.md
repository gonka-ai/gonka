# Epoch group data concurrency

The keeper’s epoch group cache and tx-scoped draft are designed to support **concurrent execution** of transactions. This is required for **Cosmos optimistic execution mode**, where multiple transactions can run in parallel before being committed.

The concurrency tests in `epoch_group_data_concurrent_test.go` check that:

1. **Multiple parallel reads** – Many read-only “transactions” see the same data and do not block each other.
2. **One writer, many readers** – After a writer commits, all readers see the written value (block cache is updated correctly).
3. **One writer, multiple Set/Get, many readers** – No deadlocks when a writer does several Set/Get in one tx while readers run in parallel (reentrant draft lock).
4. **Multiple writers and readers** – Writes are serialized at commit time, and the final read sees the last committed value.

## Running the tests

Run the concurrency tests as usual:

```bash
go test ./x/inference/keeper/ -run EpochGroupDataConcurrent -v
```

**Note on `-race`:** The tests intentionally share a single `sdk.Context` across goroutines to stress the keeper’s locking. The Cosmos SDK store and gas meter are not safe for concurrent use from multiple goroutines. With `go test -race`, the race detector may report races in the SDK layer (e.g. `cosmossdk.io/store/types.(*infiniteGasMeter).ConsumeGas`). That is a limitation of the test harness, not of the keeper’s epoch group logic. In production, each transaction gets its own context, and the keeper’s cache and draft locking are what allow safe concurrent access to epoch group data under optimistic execution.
