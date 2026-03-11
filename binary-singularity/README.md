# Binary Singularity — Bookworm Experiment

**Доказательство концепции бинарной сингулярности** — архитектура, где тяжёлый reasoning (LLM/AEON) живёт на хостах, а у клиентов работают лёгкие **бинарные PatternSlot'ы** с runtime `match → execute → validate → distill`.

## Что это

Binary Singularity — расширение [GiP #860](../GiP_860_discussion.md) и [PR #859](../PR_859_description.md) для протокола Gonka: помимо семантического кеша (L1/L2), клиенты хранят и исполняют **компактные бинарные слоты-паттерны** — готовые «рецепты» решений, без необходимости GPU на стороне клиента.

## Результаты эксперимента (256 runs, bookworm CPU-only)

| Mode | Runs | Hit Rate | Avg Latency | Completion | GPU Saved |
|------|------|----------|-------------|------------|-----------|
| **baseline** | 64 | 0% | 124 ms | 100% | 0 |
| **cache_only** | 64 | 0% | 124 ms | 100% | 0 |
| **cache+slots** | 64 | **93.75%** | **9.7 ms** | 42% | **60** |
| **full_stack** | 64 | **100%** | **5.4 ms** | 31% | **64** |

- **Латентность**: с 124ms (GPU inference) до **5.4ms** (binary slot) — **23× ускорение**
- **GPU экономия**: 124 из 256 runs без GPU (48.4%), при slots+full_stack — до **100%** hit rate
- **4 слота покрыли 4 домена**: `algo_basic`, `auth_flow`, `deploy_docker`, `http_handler`
- **128 использований** 4 слотов (avg 32 reuse/slot), total reward = 47

> Completion rate < 100% в slot modes — ожидаемо: slot execute в dry-run режиме проверяет только `tests_pass` heuristic. В production runtime с реальным CI — completion будет выше.

## Архитектура

```
┌─────────────────────────────────┐
│  HOST (k8s/k3s/docker)         │
│  ┌─────────┐  ┌──────────┐    │
│  │  DAPI   │←→│  mlnode   │    │   ← Тяжёлый reasoning
│  │ (cache, │  │(embedder, │    │     (LLM/AEON)
│  │ quality)│  │ inference)│    │
│  └────┬────┘  └──────────┘    │
│       │                        │
│  QualityReporter + SlotReporter│   ← L-оси, PQM, slot метрики
└───────┼────────────────────────┘
        │
        │ (binary slots + metrics)
        ▼
┌─────────────────────────────────┐
│  CLIENT (gonka-agent/SDK)      │
│  ┌─────────────────────┐      │
│  │  PatternSlot Store  │      │   ← Бинарный стор (pattern_slots.bin)
│  │  ┌───┐┌───┐┌───┐   │      │
│  │  │ S1││ S2││ S3│   │      │
│  │  └───┘└───┘└───┘   │      │
│  └──────┬──────────────┘      │
│         │                      │
│  match → execute → validate    │   ← Лёгкий runtime (без GPU)
│         │                      │
│  distill_to_slot (если MISS)   │   ← Дистилляция из DAPI
└─────────────────────────────────┘
```

## Структура

```
binary-singularity/
├── patternslot/           # Go-пакет: PatternSlot TLV-формат, store, matcher, executor, validator, distill, metrics
│   ├── slot.go            # Тип + бинарная сериализация (697 bytes/slot)
│   ├── store.go           # File-based binary store (atomic writes)
│   ├── matcher.go         # ANN matching (int8 cosine similarity)
│   ├── executor.go        # Action execution (READ_FILE, APPLY_PATCH, RUN_COMMAND, CALL_API)
│   ├── validator.go       # Result validation (tests, build, lint, health)
│   ├── distill.go         # distill_to_slot: DAPI response → compact slot
│   ├── metrics.go         # L6/L8/L9/PQM/GPU$ metrics collection
│   └── slot_test.go       # 6 tests (all PASS)
├── runtime/main.go        # Standalone runtime binary (match→execute→validate loop)
├── mocknode/main.go       # Mock inference server (deterministic, reproducible)
├── embedder/              # CPU embedding server (fastembed, 384-dim)
├── dapi/slot_reporter.go  # DAPI extension: SlotQualityEvent + SlotEpochSummary
├── scenarios/
│   ├── matrix.json        # 4×16×4 scenario definition
│   └── runner/main.go     # Scenario runner (runs full matrix)
├── docker-compose.yml     # Bookworm stack
├── Dockerfile.runtime     # Runtime + runner image
├── Dockerfile.mocknode    # Mock inference image
├── scripts/run-bookworm.sh # Master orchestration script
├── config/                # Node config + env
└── results/               # Experiment artifacts (JSON)
```

## Как запустить

### Локально (без Docker, нужен Go 1.22+ и Python 3.11+)

```bash
cd binary-singularity/

# 1. Установить embedder
pip install fastembed

# 2. Запустить embedder
python3 embedder/server.py &

# 3. Запустить mock-node
go run ./mocknode/ &

# 4. Запустить scenario matrix
go run ./scenarios/runner/ \
  --matrix scenarios/matrix.json \
  --output results/local \
  --embedder http://localhost:8686 \
  --dapi http://localhost:8080

# 5. Посмотреть результаты
cat results/local/experiment_report.json | python3 -m json.tool
```

### Docker (bookworm)

```bash
cd binary-singularity/
./scripts/run-bookworm.sh
```

## PatternSlot Binary Format

Compact TLV (Tag-Length-Value) wire format:

| Section | Tag | Contents |
|---------|-----|----------|
| Header  | —   | Magic `PSLT` + Version `1` |
| Identity | `0x01` | slot_id (UUID), task_hash (SHA-256), domain_id, version |
| Matching | `0x02` | embed_int8[384], sim_threshold_bps, feature_bits |
| Binding  | `0x03` | file_path_hash, line_span, pipeline_id |
| Actions  | `0x04` | N × (opcode + payload): READ_FILE, APPLY_PATCH, RUN_COMMAND, CALL_API |
| Checks   | `0x05` | expected_checks (bitmask), sim_bps_mean, coherence_bps_mean, success_rate_bps, flags |
| Metrics  | `0x07` | usage_count, last_success_epoch, reward_sum, timestamps |
| End      | `0xFF` | — |

Typical slot size: **~700 bytes**. 10,000 slots = ~7 MB.

## Метрики (L-оси + PQM + GPU/$)

- **L6** (Reuse): hit_rate, slot_hit_rate, reuse_count
- **L8** (Latency): avg/p95/CV — slot hit: 5ms vs GPU: 124ms
- **L9** (Completion): tasks resolved / total
- **PQM**: QualityScore × CacheEfficiency × AvgConfidence
- **GPU/$**: inferences_saved × 1.28s / 3600 × $2.50/h

## Связь с GiP #860, PR #859

- PatternSlot — это формализация **Quality Growth** из CONSENSUS §12
- SlotReporter расширяет **CacheQualityEpochSummary** доп. полями
- Бинарные слоты — **синтезированный reasoning-слой** для клиентов
- MeaningPoints = семантическая карта, из которой растут новые слоты

Подробнее: [docs/GPU_savings_over_distance.md](../docs/GPU_savings_over_distance.md) §7–8
