# Binary Singularity — dev/binary-singularity

> **НАМ ЛУЧШЕ ЭТО ИСПОЛЬЗОВАТЬ, ОТХОДЯ ОТ ПРОСТЫХ ИСТИН**

Branch: `dev/binary-singularity`
Relates to: [GiP #860](https://github.com/gonka-ai/gonka/discussions/860) · [PR #859](https://github.com/gonka-ai/gonka/pull/859)

---

## Что здесь

Эта ветка содержит:

| Компонент | Путь | Что делает |
|-----------|------|-----------|
| **Деплой стек** (4 уровня) | `deploy/binary-singularity/` | LITE/MEDIUM/HARD/PRODUCTION |
| **Quality middleware** | `examples/quality-middleware/` | L6/L8/L9/DX + mesh CPU pool |
| **K8s overlay** | `test-net-cloud/k8s/overlays/binary-singularity/` | Kustomize на реальный кластер |
| **Агент** | `gonkalabs/gonka-agent` → `dev/binary-singularity` | gonka CLI + слот стор |
| **Бинарь агента** | `gonka-agent/bin/gonka` | Готовый бинарь, 6MB, статик |

---

## Зачем это существует — тезисы GiP #860

### Проблема (измеренная, не предполагаемая)

Живые данные сети Gonka (эпохи 161–191, **2 503 595 инференсов**):

```
Composite QualityScore = 0.7236

Узкие места:
  L8 Latency consistency: 0.32   (CV = 0.68, σ = 876ms — высокая дисперсия)
  L0 Compute stability:   0.65   (вес упал на 60% пик-к-минимуму)
  L6 Reuse (shared):      0.00   (M=571 нод → hit_rate ≈ 0)
  L4 Usefulness:          не измеряется — механизм не существует
```

**Качество инференса невидимо для протокола.** PoC измеряет вычисления. Качество ответа — нет.

### Матрица качества (L0–L9)

10 осей, каждая активируется через governance:

| Ось | Измеряет | Источник | Статус |
|-----|---------|---------|--------|
| L0 | Стабильность вычислений (CV веса PoC) | Chain | Существует (#856) |
| L1 | Доступность (heartbeat) | Chain | Существует |
| L2 | Корректность (RTV validated rate) | Chain | Существует |
| L3 | Релевантность (cosine prompt↔response) | DAPI auto | #859 infra |
| **L4** | **Полезность (feedback участников)** | `X-Inference-Feedback` | **Реализовано в агенте** |
| L5 | Исход (developer webhook) | Developer callback | Запланировано |
| **L6** | **Переиспользование (cache hit rate)** | #859 | **Измеряется** |
| L7 | Stream fidelity (SSE completeness) | DAPI auto | Запланировано |
| **L8** | **Latency consistency (σ/μ)** | DAPI auto | **Измеряется в QM** |
| **L9** | **Completion rate** | Chain | **Измеряется в QM** |

Composite: `QualityScore = Σ(wi × Li)` — веса через governance.

### Специализация — математический аргумент

```
M=571 (Qwen3-32B, shared): hit_rate = 0.000473
M=12  (QwQ-32B, low-M):    hit_rate = 0.0225    → 47.6× улучшение
M=1   (unique model):       hit_rate = 0.27      → 571× улучшение
```

Экономика специализации математична, не спекулятивна. Нода на одной модели
получает 100% трафика этой модели → максимальный hit rate → максимальный
CacheQualityWeight → больше наград → глубже специализация. Петля замкнута.

### Binary Singularity — доказательство PQM > 1.0

Дистилляция успешных решений в бинарные паттерны (PatternSlot: 384-мерный
вектор + задача + решение). CPU cosine search за 5ms вместо 120ms GPU-инференса.

**Результаты 4 экспериментов на Bookworm (CPU only, 20MB RAM):**

| Эксперимент | Запусков | PQM | Слотов | Вердикт |
|------------|----------|-----|--------|---------|
| 1 baseline | 256 | — | 4 | базовая линия |
| 2 масштаб | 9 216 | **0.988** | 4 | Hub: APPROVED |
| 3 K3s mesh | 15 360 | **1.001** | 6 | ПРЕВЫШАЕТ GPU |
| 4 raw input | 11 520 | **1.020** | 197 | ПРЕВЫШАЕТ GPU |

**PQM > 1.0 доказан.** Бинарный слой даёт качество лучше одиночного GPU-инференса.

---

## Для кого и как использовать

### Для разработчиков — 3 минуты до запуска

```bash
# Скачать агент (бинарь, без зависимостей)
curl -L https://github.com/gonkalabs/gonka-agent/raw/dev/binary-singularity/bin/gonka \
  -o gonka && chmod +x gonka

# Или собрать из исходников
git clone https://github.com/gonkalabs/gonka-agent
cd gonka-agent && go build -o bin/gonka ./cmd/gonka

# Запустить
cp .env.example .env  # заполнить GONKA_API_KEY
./gonka "your task"
```

Агент:
- Подключён к сети Gonka через opengnk
- Отправляет `X-Inference-Feedback` (L4 сигнал) автоматически
- Хранит решения в PatternSlot store (~/.gonka-cache/slots/)
- Делится паттернами с mesh pool через quality-middleware

### Для исследователей — подключить любые данные

```bash
# Подать любой файл как бинарный инпут
BS_RAW_INPUT=/path/to/your/research/notes.txt ./gonka "проанализируй"

# Файл идёт as-is — без предобработки. Возможные источники:
#   git log --oneline > input.txt
#   cat ~/Документы/notes.md
#   any downloaded spec, log, dataset
```

Параметры:
```bash
BS_CHUNK_LINES=50        # строк на чанк
BS_MIN_SIM_BPS=7500      # порог точности (0.75 cosine)
BS_EMBED_URL=http://localhost:8686  # embedder
BS_QUALITY_URL=http://localhost:9090  # quality-middleware (mesh pool)
```

### Для операторов нод — 4 уровня деплоя

#### LITE — локальный тест (1GB RAM, 15 мин)
```bash
cd deploy/binary-singularity/lite
cp .env.example .env && ./run.sh
```

#### MEDIUM — с opengnk proxy (2GB RAM, живой инференс)
```bash
cd deploy/binary-singularity/medium
cp .env.example .env
# Заполнить GONKA_PRIVATE_KEY + GONKA_ADDRESS
docker compose up -d
curl http://localhost:9090/quality/stats | jq .
```

#### HARD — K3s mesh (4GB RAM, полный тест)
```bash
cd deploy/binary-singularity/hard
./run.sh  # 1 server + 3 agents
./run.sh --raw-input /path/to/data  # с бинарным инпутом
```

#### PRODUCTION — на реальной ноде (поверх deploy/join/)
```bash
cd deploy/binary-singularity/production
cp .env.example .env
# GONKA_PRIVATE_KEY, GONKA_ADDRESS, GONKA_API_KEY
docker compose up -d
```

Архитектура на реальной ноде:
```
EXISTING NODE (deploy/join/)
  tmkms → node → api (DAPI :9000/:9100/:9200)
  mlnode-308 → inference (nginx :8081/:5050)
         │
BINARY SINGULARITY LAYER (этот файл)
  embedder        :8686  — CPU-only, all-MiniLM-L6-v2
  opengnk         :8081  — SDK proxy, secp256k1 signing
  quality-middleware :9090  — L6/L8/L9/DX + mesh CPU pool
  bs-runtime             — continuous slot distillation
```

#### K8s overlay (поверх существующего genesis/join)
```bash
kubectl apply -k test-net-cloud/k8s/overlays/binary-singularity/
```
Патчит DAPI deployment для включения семантического кеша.

### Для SDK-клиентов — opengnk совместим с OpenAI

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://<node>:8081/v1",
    api_key="not-needed"  # подпись через secp256k1 в opengnk
)
response = client.chat.completions.create(
    model="Qwen/Qwen3-235B-A22B-Instruct-2507-FP8",
    messages=[{"role": "user", "content": "your request"}]
)
```

### Для всех остальных

Если вы не программист — это нормально. Если у вас есть повторяющиеся задачи
(юридические документы, ЖКХ процедуры, исследовательские запросы, обучение) —
бинарный слой запоминает успешные решения и применяет их быстрее с каждым разом.

Подключить: `BS_RAW_INPUT=/path/to/your/file` — и система находит паттерны сама.

---

## Как это работает технически

```
Ваш запрос
    │
    ├─ PatternSlot store (локальный)
    │   └─ cosine search: sim ≥ 0.75 → инжектируется как контекст
    │
    ├─ Mesh pool (quality-middleware)
    │   └─ POST /quality/search → паттерны от других участников
    │
    ├─ Semcache (локальный)
    │   └─ семантический кеш предыдущих сессий
    │
    └─ LLM (Gonka network) → ответ
         │
         └─ при успехе:
              - Distill → новый PatternSlot (vec + task + solution)
              - ShareToMesh → POST /quality/slots/share
              - X-Inference-Feedback: resolved
```

Участник 1 решил задачу → слот появляется в mesh pool →
Участник 2 ищет похожую задачу → находит слот Участника 1 →
LLM получает контекст → решает быстрее → PQM растёт.

---

## Связь с протоколом (PR #859 / GiP #860)

| Уровень | Что | Протокол затронут? |
|---------|-----|-------------------|
| PatternSlot store | Клиент / DAPI | Нет — ниже консенсуса |
| quality-middleware | DAPI обёртка | Нет — внешний компонент |
| Hub check | `/api/public/stats` | Только чтение |
| CacheQualityParams | Governance-controlled | По умолчанию disabled |
| PruningState (fields 5-8) | Protocol additive | Аддитивно, не ломает #1-4 |
| Key prefixes (48-51) | Above EpochGroup (47) | Без коллизий |

---

## Что дальше (Phase 1+)

- `GET /v1/models/profiles` — quality scores + специализация нод
- `GetQualityWeightedExecutor` — трафик по QualityScore вместо random
- `X-Suggested-Model`, `X-Task-Archetype` — протокол подсказывает лучшую модель
- SDK (TypeScript / Python) — drop-in замена openai с автоматическим routing

Все эти фичи строятся на инфраструктуре этой ветки.

---

## Благодарности

@blizko, @akup, @gmorgachev — ваши вопросы в PR #859 сделали систему
точнее. KV-cache и request-level cache действительно разные слои.
Специализация M=1 действительно математична. Data race действительно была.

Эта ветка — ответ на "нужны данные, не предположения."

**PQM = 1.020 измерен. Не вычислен. Измерен.**

Beta for researchers encourages faith in you,
and we believe in you and love you, thank you guys ♥
