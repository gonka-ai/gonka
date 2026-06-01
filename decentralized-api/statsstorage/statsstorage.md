---
id: statsstorage
title: implementation
paths_filter: ["./decentralized-api/statsstorage/**"]
---
# Stats Storage

The `statsstorage` package provides an off-chain storage system for tracking inference metrics and performance data. It is managed by a `ManagedStorage` wrapper that handles automatic data pruning based on a retention policy (default: 30 days).

## Architecture
- **StatsStorage (Interface)**: Defines operations for recording and querying inference records.
- **ManagedStorage**: Wraps any `StatsStorage` implementation to provide automated pruning and periodic cleanup.
- **PostgresStorage**: Recommended production backend. Stores data in a relational database with optimized indexing.
- **FileStorage**: Experimental backend that stores each record as an individual JSON file.
- **DisabledStorage**: Default fallback that returns errors for all stats operations.

## Configuration

### PostgreSQL (Recommended)
Enabled automatically if `PGHOST` is set. Uses standard PostgreSQL environment variables (`PGUSER`, `PGPASSWORD`, etc.).

### File Storage (Experimental)
- **Status**: Disabled by default.
- **Activation**: Set `DAPI_STATS_FILE_STORAGE_ENABLED=true`.
- **Warning**: Enabling `FileStorage` is **NOT RECOMMENDED** for high-volume nodes. It can lead to inode exhaustion, performance degradation, and system crashes. Use at your own risk.
- **Path**: Configurable via `DAPI_STATS_STORAGE_PATH` (defaults to `/root/.dapi/data/stats`).

### Retention
- **Policy**: Controlled by `DAPI_STATS_RETENTION_DAYS` (default: 30).
- **Cleanup**: Runs automatically every 24 hours.

## Developer Guide for AI Agents
- **Factory**: Use `NewStatsStorage(ctx)` to instantiate the appropriate backend based on environment variables.
- **Interfaces**: Always code against the `StatsStorage` interface.
- **Errors**: Expect `ErrStatsDisabled` if no storage backend is configured.
