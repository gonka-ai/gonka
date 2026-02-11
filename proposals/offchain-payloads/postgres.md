# PostgreSQL Payload Storage

## Overview

PostgreSQL backend for inference payloads with automatic fallback to file storage.

## Architecture

```
                    ┌─────────────────────────────────────────┐
                    │              CachedStorage              │
                    │         (1000 entry LRU cache)          │
                    └─────────────────┬───────────────────────┘
                                      │
                    ┌─────────────────▼───────────────────────┐
                    │            HybridStorage                │
                    │      (PG primary, file fallback)        │
                    └────────┬────────────────────┬───────────┘
                             │                    │
              ┌──────────────▼──────┐    ┌───────▼───────────┐
              │   PostgresStorage   │    │    FileStorage    │
              │   (partitioned)     │    │  (epoch dirs)     │
              └─────────────────────┘    └───────────────────┘
```

## Storage Selection

```
PGHOST set? ──▶ Try connect ──▶ Success ──▶ HybridStorage(PG + File)
    │                              │
    ▼                              ▼
 FileStorage                   FileStorage (fallback)
```

Environment variables (standard libpq):
- `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGPASSWORD`

## Data Flow

### Store
```
HybridStorage.Store()
    │
    ├──▶ PostgresStorage.Store() ──▶ success ──▶ done
    │           │
    │           ▼ error
    │
    └──▶ FileStorage.Store() (fallback)
```

### Retrieve
```
HybridStorage.Retrieve()
    │
    ├──▶ PostgresStorage.Retrieve()
    │           │
    │           ├──▶ found ──▶ return
    │           │
    │           ▼ error OR not found
    │
    └──▶ FileStorage.Retrieve() (check file too)
```

## Schema

Partitioned table with instant pruning via `DROP TABLE`:

```sql
CREATE TABLE inferences (
    epoch_id BIGINT NOT NULL,
    inference_id TEXT NOT NULL,
    prompt_payload TEXT,
    response_payload TEXT,
    prompt_hash TEXT,        -- for debugging
    response_hash TEXT,      -- for debugging
    created_at TIMESTAMP DEFAULT NOW(),
    PRIMARY KEY (epoch_id, inference_id)
) PARTITION BY RANGE (epoch_id);
```

Partitions created lazily:
```sql
CREATE TABLE inferences_epoch_100 
PARTITION OF inferences 
FOR VALUES FROM (100) TO (101);
```

## Pruning

Traditional DELETE marks rows dead, requires vacuum. With partitions:

```
PruneEpoch(100)
    │
    ▼
DROP TABLE inferences_epoch_100  ──▶  Instant, zero I/O
```

## Performance Tuning

PostgreSQL config for 5MB payloads:

```
min_wal_size = 4GB       # Buffer for heavy writes
max_wal_size = 16GB      # Avoid frequent checkpoints
checkpoint_timeout = 15min
```

## Cache Layer

LRU cache with size limit prevents unbounded memory:

```
CachedStorage
├── maxSize: 1000 entries
├── ttl: 3 minutes
└── eviction: oldest accessed first
```

## Files

```
decentralized-api/payloadstorage/
├── storage.go           # Interface
├── file_storage.go      # File backend
├── postgres_storage.go  # PostgreSQL backend
├── hybrid_storage.go    # PG + file composite
├── cached_storage.go    # LRU cache wrapper
├── factory.go           # Auto-selects based on env
└── hash.go              # Hash computation

deploy/join/
└── docker-compose.postgres.yml  # Production deployment
```

## Deployment

```yaml
# docker-compose.postgres.yml
services:
  db:
    image: postgres:18.1-bookworm
    volumes:
      - ./postgres:/var/lib/postgresql  # Note: /postgresql not /postgresql/data for v18+
    command: ["-c", "min_wal_size=4GB", "-c", "max_wal_size=16GB", "-c", "checkpoint_timeout=15min"]
    
  db-backup:
    image: prodrigestivill/postgres-backup-local
    environment:
      - SCHEDULE=@daily
      - BACKUP_KEEP_DAYS=7
```

## Usage

Enable PostgreSQL:
```bash
export PGHOST=localhost PGPORT=5432 PGDATABASE=payloads PGUSER=payloads PGPASSWORD=secret
```

If `PGHOST` unset or connection fails, file storage used automatically.

