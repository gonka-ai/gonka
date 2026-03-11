# Notes on build issues/fixes

**Issue**

During build, something like
```
17.20 google.golang.org/protobuf/internal/filedesc: /usr/local/go/pkg/tool/linux_amd64/compile: signal: illegal instruction
```
**Solution**

Rerun the build

-----

## Binary Singularity — Build & Deploy

### Deploy tiers

| Tier | Path | Command |
|------|------|---------|
| LITE | `deploy/binary-singularity/lite/` | `./run.sh` |
| MEDIUM | `deploy/binary-singularity/medium/` | `docker compose up -d` |
| HARD | `deploy/binary-singularity/hard/` | `./run.sh` (creates k3d cluster) |
| PRODUCTION | `deploy/binary-singularity/production/` | `docker compose up -d` (on real node) |

### Production deploy on real node

```bash
# Prerequisites: deploy/join/ stack running (node + api + mlnode)
cd deploy/binary-singularity/production
cp .env.example .env
# Fill: GONKA_PRIVATE_KEY, GONKA_ADDRESS, GONKA_API_KEY
docker compose up -d

# Verify:
curl http://localhost:8686/health          # embedder
curl http://localhost:8080/health          # opengnk
curl http://localhost:9090/quality/stats   # quality-middleware + mesh pool
```

### Raw binary input (any data source)

```bash
# In any tier's .env:
BS_RAW_INPUT=/path/to/any/file    # git logs, specs, markup, anything
BS_CHUNK_LINES=50                  # lines per chunk

# The file is transmitted as-is to the embedder — zero pre-processing.
```

### gonka-agent with binary singularity

```bash
cd gonka-agent
cp .env.example .env
# Fill GONKA_API_KEY + BS_* variables
go build -o bin/gonka ./cmd/gonka
BS_RAW_INPUT=/path/to/data ./bin/gonka "your task"
```

### PR #859 conflict resolution (2026-03-11)

PruningState field layout after merge with upgrade-v0.2.11:
```
field 1-3: poc_batches, poc_validations, inference (unchanged)
field 4:   epoch_group_validations (from upstream)
field 5:   continuous_poc_commits (from PR #856)
field 6:   continuous_poc_challenges
field 7:   continuous_poc_summaries
field 8:   cache_quality_epoch_summaries (from PR #859)
```

keys.go: prefix 47=EpochGroup (upstream), 48-51=ContinuousPoC+CacheQuality (ours).

### Bookworm experiment results (4 experiments)

| Exp | Runs | PQM | Slots | Peak RAM | Verdict |
|-----|------|-----|-------|----------|---------|
| 1 | 256 | — | 4 | — | baseline |
| 2 | 9216 | 0.988 | 4 | 19.25 MB | APPROVED |
| 3 | 15360 | 1.001 | 6 | 23.1 MB | DOMINATES |
| 4 | 11520 | 1.020 | 197 | 20.8 MB | DOMINATES |

-----
