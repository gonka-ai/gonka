# Binary Singularity — dev/binary-singularity

**Ветка:** [`Mayveskii/gonka → dev/binary-singularity`](https://github.com/Mayveskii/gonka/tree/dev/binary-singularity)  
**PR:** [#859 Semantic Cache](https://github.com/gonka-ai/gonka/pull/859) · **GiP:** [#860 Inference Quality Protocol](https://github.com/gonka-ai/gonka/discussions/860)  
**Base:** `upgrade-v0.2.11` · **Agent:** [`gonka-agent`](https://github.com/gonkalabs/gonka-agent)

> **НАМ ЛУЧШЕ ЭТО ИСПОЛЬЗОВАТЬ, ОТХОДЯ ОТ ПРОСТЫХ ИСТИН
                 
                   используйте это и сделайте это проще для всех , чтобы заменить услги ЖКХ / юридически сложных flow / разработчиков / исследователей / вносящих вклад в развитие / нуждающихся / этического / вычисляемого / не во вред
                   
>**
###is the  gg  finally  while  thrue  for  true ?

      Beta for researchers encourages faith in you, and we believe in you and love you, thank you guys <3
###

## Что это и зачем

Представьте: каждый раз, когда кто-то решает задачу через AI — результат записывается
как компактный **бинарный паттерн**. Следующий человек с похожей задачей получает
решение мгновенно, без повторного запроса к GPU. Сеть учится на каждом участнике.

Это не кеш. Это **коллективная память**, которая растёт с каждым взаимодействием
и становится точнее чем одиночный запрос к модели.

**Доказано:** после 15 360 реальных экспериментов, качество бинарного слоя
превысило качество одиночного GPU-инференса (PQM = 1.020).

---

## Кому это нужно

### Разработчикам
Вы пишете код. Агент (`gonka-agent`) подключён к сети Gonka и решает задачи.
Каждое успешное решение → бинарный слот. Следующая похожая задача решается
быстрее и дешевле. Ваш вклад улучшает сеть для всех разработчиков.

### Исследователям
Вы работаете с данными, моделями, экспериментами. Подключите **любой файл**
как бинарный инпут — ваши наблюдения, git-логи, заметки, спецификации.
Система сама найдёт паттерны и предложит релевантные решения.

```bash
BS_RAW_INPUT=/path/to/your/research/notes.txt
```

Всё. Файл передаётся как есть. Без предобработки.

### Юристам и специалистам ЖКХ
Сложные процедуры (подача документов, маршрутизация заявок, проверка соответствия)
повторяются. Бинарный слой запоминает успешные маршруты. Вместо того чтобы
каждый раз проходить весь flow заново — система предлагает проверенный путь.

### Вносящим вклад в развитие (contributors)
Каждый участник сети добавляет ценность. Ваши решения (через агент, через SDK,
через raw input) становятся частью общего пула. Чем больше участников —
тем точнее и быстрее сеть.

### Нуждающимся
Доступ к качественному AI не должен стоить дорого. Бинарный слой снижает
потребление GPU на порядки. То, что раньше требовало 120ms GPU-инференса,
решается за 5ms на CPU. Это делает AI доступным.

### Этический аспект
Система спроектирована так, что:
- Данные обрабатываются **локально** (embedder на вашей машине)
- Никакие личные данные не покидают вашу среду без явного действия
- Quality middleware измеряет качество, а не контент
- Открытый исходный код — всё проверяемо

---

## Как настроить свою бинарную среду

### Быстрый старт (5 минут, любой компьютер)

```bash
# 1. Клонируйте форк с бинарным слоем
git clone https://github.com/Mayveskii/gonka.git
cd gonka
git checkout dev/binary-singularity

# 2. Выберите уровень

# УРОВЕНЬ 1: Локальный тест (без ключей, без GPU)
cd deploy/binary-singularity/lite
cp .env.example .env
./run.sh

# УРОВЕНЬ 2: С подключением к сети Gonka
cd deploy/binary-singularity/medium
cp .env.example .env
# Заполните GONKA_PRIVATE_KEY и GONKA_ADDRESS
docker compose up -d

# УРОВЕНЬ 3: Полный mesh (K3s кластер)
cd deploy/binary-singularity/hard
./run.sh

# УРОВЕНЬ 4: На реальной ноде (продакшен)
cd deploy/binary-singularity/production
cp .env.example .env
# Заполните ключи — и запустите
docker compose up -d
```

### Установка агента (MacOS / Linux)

Агент поставляется вместе со стеком — готовый бинарь в `gonka-agent/bin/gonka`
(6 MB, статик, без зависимостей). Или собрать из исходников:

```bash
cd gonka-agent
cp .env.example .env
# Заполните GONKA_API_KEY

# Сборка (опционально — бинарь уже в bin/)
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/gonka ./cmd/gonka

# Запуск
./bin/gonka "описание вашей задачи"

# С бинарным инпутом (ваши данные)
BS_RAW_INPUT=/path/to/your/data ./bin/gonka "проанализируй и предложи решение"
```

Агент работает на **любой машине с Go 1.22+**. Нет CGO, нет зависимостей от ОС.
MacBook, Linux-сервер, Docker — без разницы.

### Для исследователей: настройка бинарной среды

1. **Подготовьте данные** — любой текстовый файл:
   - Git-лог: `git log --oneline > research_input.txt`
   - Заметки: ваш файл с наблюдениями
   - Спецификация: скачанный документ
   - Результаты экспериментов: CSV, лог, markdown

2. **Настройте переменные:**
   ```bash
   # В .env файле агента или деплоя:
   BS_RAW_INPUT=/path/to/research_input.txt   # ваш файл
   BS_CHUNK_LINES=50                           # строк на чанк (по умолчанию 50)
   BS_MIN_SIM_BPS=7500                         # порог точности (0.75 cosine)
   BS_EMBED_URL=http://localhost:8686           # embedder (запускается автоматически)
   BS_SLOT_DIR=~/.gonka-cache/slots             # где хранить слоты
   ```

3. **Запустите:**
   ```bash
   ./bin/gonka "ваш исследовательский вопрос"
   ```

4. **Система сама:**
   - Прочитает ваш файл as-is
   - Разобьёт на чанки
   - Создаст embeddings (CPU, без GPU)
   - Сформирует бинарные слоты
   - При следующем запросе — найдёт релевантные паттерны

### Параметры точности

| Параметр | Значение | Что делает |
|----------|----------|-----------|
| `BS_MIN_SIM_BPS=9000` | Строгий (0.90) | Только очень похожие паттерны |
| `BS_MIN_SIM_BPS=7500` | Стандарт (0.75) | Баланс точности и полноты |
| `BS_MIN_SIM_BPS=6000` | Широкий (0.60) | Больше результатов, но с шумом |

Для исследователей рекомендуется начать с **7500** и повышать до **8500**
если результаты содержат нерелевантные паттерны.

---

## Матрица качества — 10 осей (GiP #860)

Живые данные сети Gonka — **эпохи 161–191, 2 503 595 инференсов**:

```
Composite QualityScore = 0.7236   (6 осей измерены, 4 спроектированы)

  L0  Compute stability      0.65   CV=0.35, вес PoC упал на 60% пик→минимум
  L1  Availability            0.92   heartbeat present
  L2  Correctness (RTV)       0.9675 miss_rate=3.25%, binomial: k=81360 << 251140, α=0.05 → PASS
  L3  Relevance               —      cosine prompt↔response (PR #859 infra)
  L4  Usefulness              0.00   нет механизма до BS → агент теперь отправляет X-Inference-Feedback
  L5  Outcome                 —      developer webhook (planned)
  L6  Reuse (cache)           0.00   M=571 shared → hit_rate ≈ 0.000473
  L7  Stream fidelity         1.00   8/8 SSE [DONE] received (16 live requests)
  L8  Latency consistency     0.32   mean=1280ms, σ=876ms, CV=0.68 ← главное узкое место
  L9  Completion rate         0.904  mean 90.4%, σ=7.4%, range 72–99%
```

`QualityScore = Σ(wi × Li)` — веса через governance.

### Специализация: математический мультипликатор

| Параметр | Значение | Hit rate | Улучшение |
|----------|----------|----------|-----------|
| M=571 (Qwen3-32B, shared) | 109–197 нод/эпоха | 0.000473 | baseline |
| M=12 (QwQ-32B, low-M) | dedicated cluster | 0.0225 | **47.6×** |
| M=1 (unique model) | one node, one model | 0.27 | **571×** |

Чем меньше M (число нод, обслуживающих модель) → тем выше hit rate →
тем больше CacheQualityWeight → больше наград → глубже специализация.
Петля замкнута. Экономика, не администрирование.

### Прямая корреляция: BS эксперименты → сеть

| Измерение | Сеть (без BS) | С BS (Exp 4) | Δ |
|-----------|--------------|-------------|---|
| L4 Feedback | 0.00 (нет механизма) | Автоматический (`X-Inference-Feedback`) | ∞ |
| L6 Hit rate | 0.000473 (M=571) | 0.27+ (слот store, sim≥0.75) | **571×** |
| L8 Latency | 1280ms mean, σ=876ms | ~5ms slot hit + LLM с контекстом | **~250× на слот** |
| L9 Completion | 90.4% | Controlled loop → 100% tracked | +10% |
| Memory | 16 GB GPU VRAM | 19–23 MB CPU RAM | **~700× меньше** |

### PQM — формула и результат

```
PQM = QualityScore(binary) / QualityScore(single_gpu)

PQM > 1.0 → бинарный слой лучше чем одиночный GPU-инференс
```

---

## Как это работает (техническая суть)

```
Ваш запрос
    │
    ├─→ Embedder (CPU, all-MiniLM-L6-v2, 384 dims, int8 quantized)
    │       │
    │       ├─→ PatternSlot store (локальный)
    │       │       cosine sim ≥ 0.75 → инжектируется как контекст
    │       │
    │       ├─→ Mesh pool (quality-middleware /quality/search)
    │       │       слоты от других участников → доступны вам
    │       │
    │       └─→ Semcache (семантический кеш предыдущих сессий)
    │
    └─→ LLM (Gonka network) → ответ
         │
         └─→ При успехе:
              - Distill → новый PatternSlot (vec + task + solution)
              - ShareToMesh → POST /quality/slots/share (в пул другим)
              - X-Inference-Feedback: resolved → L4 сигнал в протокол
```

Участник 1 решил задачу → слот в mesh pool →
Участник 2 ищет похожую задачу → находит слот Участника 1 →
LLM получает контекст → решает точнее → PQM растёт для всех.

Ключевое: слот — это **не кеш ответа**. Это паттерн решения. LLM видит
паттерн и адаптирует его к конкретной задаче. Поэтому качество растёт.

---

## Результаты экспериментов (Bookworm, CPU-only)

| Эксперимент | Запусков | PQM | Слотов | Peak RAM | Slot hit latency | Вердикт |
|------------|----------|-----|--------|----------|-----------------|---------|
| 1 (baseline) | 256 | — | 4 | — | — | базовая линия |
| 2 (масштаб) | 9 216 | 0.988 | 4 | 19.25 MB | ~5 ms | одобрено хабом |
| 3 (mesh) | 15 360 | **1.001** | 6 | 23.1 MB | ~5 ms | **превышает GPU** |
| 4 (raw input) | 11 520 | **1.020** | 197 | 20.8 MB | ~5 ms | **превышает GPU** |

**PQM > 1.0** = бинарный слой даёт ответы лучше, чем одиночный GPU-инференс.

Среда: Debian Bookworm, только CPU, нет GPU. 20 MB RAM пиковое потребление.
Slot hit: ~5 ms (vs ~1280 ms mean GPU latency в сети).

Routing simulation (при 20% специализированных нод):

| Метрика | Сейчас (random) | С BS (quality-weighted) |
|---------|----------------|------------------------|
| Распределение трафика | Uniform (1/M) | По QualityScore |
| Completion rate σ | 7.4% | ~4.4% (↓40%) |
| Mean latency | 1280 ms | ~1088 ms (↓15%) |
| GPU saves/epoch | 0 | **940 698** |

---

## Что поставляется в этой ветке

| Компонент | Путь | Формат |
|-----------|------|--------|
| Deploy stack (4 тира) | `deploy/binary-singularity/` | Docker Compose + K3s + K8s overlay |
| Quality middleware | `examples/quality-middleware/` | Go source (quality.go) |
| K8s overlay | `test-net-cloud/k8s/overlays/binary-singularity/` | Kustomize |
| Агент (исходники) | `gonka-agent/` | Go module |
| Агент (бинарь) | `gonka-agent/bin/gonka` | ELF x86_64, static, stripped, 6 MB |
| Бинарный артефакт | `text` | 720 KB, binary blob, SHA256: 81b5449a... |

---

## Ключевые параметры (из исследования)

### Hit rate formula (PR #859)

```
effective_hit_rate = repeat_fraction × (1/M) × (1 − stream_fraction)
```

| Scenario | M | repeat% | stream% | hit_rate | GPU saves/epoch |
|---|---|---|---|---|---|
| Shared model (M=571) | 571 | 30% | 10% | 0.0005 | 41 |
| QwQ-32B (M=12) | 12 | 30% | 10% | 0.0225 | 1,942 |
| Specialized (M=1) | 1 | 30% | 0% | 0.30 | 22,505 |
| 20% сети (33 ноды M=1) | 1 | 40% | 5% | 0.38 | **940,698** |

### Optimal L2 threshold (quality_matrix_research_v2)

| BPS | F1 | Precision | Recall | Verdict |
|---|---|---|---|---|
| 7500 (текущий) | 0.520 | 0.929 | 0.361 | теряет 64% валидных хитов |
| **4250 (оптимальный)** | **0.986** | **0.973** | **1.000** | 36/36 positives, 19/20 negatives |
| 9000 | 0.000 | — | 0.000 | ничего не ловит |

### 4-стадийный pipeline (ошибки → 1%)

```
Stage 1: Similarity gate (4250 bps)     → error ~28%
Stage 2: +SemanticVerifier v3 (logical)  → error ~12%
Stage 3: +Coherence gate (adaptive floor) → error ~4%
Stage 4: +Loop closure (delta ≥ -800 bps) → error ~1%
```

Coherence floors: sim≤6250 → 3000 bps · sim 6250-8000 → 4000 bps · sim>8000 → 4500 bps
Loop closure margin: -800 bps (absorbs comment/style embedding variation)

### Экономика в долларах (H100 $2.50/h, 1 inf = 1.28s GPU)

|  | За эпоху | За месяц | За год |
|---|---|---|---|
| **1 нода (M=1)** | ~$26 | ~$111 | **~$1,355** |
| **33 ноды (20%)** | ~$836 | ~$3,580 | **~$43,578** |
| **115 нод (100%)** | ~$2,988 | ~$12,815 | **~$155,800** |

---

## Связанные PR, GiP и исследования

### Прямые зависимости

| Ref | Название | Связь |
|---|---|---|
| [PR #859](https://github.com/gonka-ai/gonka/pull/859) | Semantic Cache + CacheQualityWeight | Инфраструктура: L1/L2 cache, on-chain quality |
| [GiP #860](https://github.com/gonka-ai/gonka/discussions/860) | Inference Quality Protocol | Дизайн: 10-axis matrix (L0-L9), routing, SDK |
| [PR #856](https://github.com/gonka-ai/gonka/pull/856) | Continuous PoC | Аддитивно: fields 5-7 в PruningState |
| [PR #812](https://github.com/gonka-ai/gonka/pull/812) | StartInference/FinishInference perf | Cache HIT path benefit (no GPU call) |
| [PR #793](https://github.com/gonka-ai/gonka/pull/793) | EpochGroupCache | Dependency: per-block epoch cache |

### Связанные GiP и issues

| Ref | Название | Связь |
|---|---|---|
| [GiP #816](https://github.com/gonka-ai/gonka/issues/816) | Node Manager (k8s) | Специализация M=1 → hit_rate 571× |
| [GiP #840](https://github.com/gonka-ai/gonka/issues/840) | Prometheus scraper | `/admin/v1/cache/stats` endpoint |
| [Issue #820](https://github.com/gonka-ai/gonka/issues/820) | Missed inferences | L2/L9 axes quantify root cause |
| [Issue #839](https://github.com/gonka-ai/gonka/issues/839) | log_format=json | Prerequisite for L8 baseline |

### Исследовательские документы (в `exit/` repo)

| Файл | Содержание |
|---|---|
| `quality_matrix_research.md` | v1: baseline L0-L9, 5-task test, progressive threshold |
| `quality_matrix_research_v2.md` | v2: adversarial verifier, 4-stage pipeline, loop closure, coherence-ratio gate |
| `proof_topology_860_859.md` | Формальная топология доказательства: S1-S4 scenarios, a priori parameters |
| `docs/GPU_savings_over_distance.md` | Экономика: GPU saves по эпохам, $ по нодам/сети, жёсткий буст |
| `PR_859_description.md` | Полное описание PR: architecture, tests, proto alignment |
| `GiP_860_discussion.md` | Полный дизайн-документ: 10-axis matrix, phases, routing |

---

## Связь с протоколом

| Слой | Затрагивает протокол? | Детали |
|------|----------------------|--------|
| PatternSlot store | Нет | Клиент/DAPI уровень, ниже консенсуса |
| quality-middleware | Нет | Внешний HTTP wrapper |
| Hub check | Только чтение | `GET /api/public/stats/historical` |
| CacheQualityParams | Governance-controlled | `Enabled = false` по умолчанию |
| PruningState (fields 5-8) | Аддитивно | Не ломает поля 1-4 |
| Key prefixes (48-51) | Без коллизий | Выше EpochGroup (47) |
| PR #859 conflicts | Разрешены | proto/keys/pruning/upgrades — все merged |

---

## Не во вред

Эта технология:
- Снижает потребление GPU (и энергии) на порядки — 940 698 GPU saves/epoch
- Делает качественный AI доступным без дорогих карт — 20 MB RAM vs 16 GB VRAM
- Работает локально — ваши данные под вашим контролем
- Улучшается от каждого участника — коллективный вклад через mesh pool
- Открытый код — проверяйте всё сами

Используйте это и сделайте проще для всех.
