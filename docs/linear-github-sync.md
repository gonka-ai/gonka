# Синхронизация GitHub PR → Linear

Автоматизация: каждый пул-реквест в репозитории `gonka-ai/gonka` (включая PR от внешних
контрибьюторов из форков) автоматически заводится и ведётся тикетом в Linear в команде
**GON / Gonka-core**. Внутренние комментарии в Linear наружу в GitHub **не попадают**.

Реализовано кастомным GitHub Action, потому что штатная интеграция Linear такого не умеет:
- workflow: `.github/workflows/linear-pr-sync.yml`
- логика: `.github/scripts/linear-pr-sync/` (см. README там же)

## Что происходит автоматически

| Событие в GitHub | Что делается в Linear |
|---|---|
| PR **открыт** (без milestone) | Parent-тикет в команде GON, статус **Triage**. First-time contributor → лейбл. Review-сабтикеты «`<название> — review needed`» (Backlog) на Диму и Гаврю. Ревьюеры запрашиваются и на самом PR. |
| PR открыт автором-ревьюером | Если PR открыл Дима — ревью только на Гаврю, и наоборот. |
| **Навесили milestone** (релиз) | Parent + сабтикеты переезжают из Triage в проект релиза `Upgrade <milestone>` (создаётся, если его нет). |
| Мейнтейнер поставил на доске **Status = Accepted** | Подхватывается плановым воркфлоу **`accept-reconcile.yml`** (раз в ~15 мин): parent (и сабтикеты) уезжают из Triage — в проект релиза (если есть milestone) либо в **«Backlog for future mainnet upgrades»** (статус parent **Backlog**). Идемпотентно: если тикет уже в каком-то проекте — не трогаем (не перетираем ручные переносы). Статус на доске может менять только человек с доступом к проекту, т.е. это «принял мейнтейнер/коллаборатор». |
| Ревьюер **аппрувнул** PR | Его review-сабтикет → **Done**. |
| PR **смёржен** | Parent → **Merged. Ready for testing**. Review-сабтикет → **Done** только если ревьюер реально аппрувил, иначе → **Not done**. Создаётся QA-сабтикет «`<название> — Testing`» (Todo) на **Марию Митину**, в описании отмечены Мария и dbogdan. |
| PR **закрыт** Димой/Гаврей (без merge) | Parent → **Done**, их сабтикет → **Done**, второй → **Cancelled**. |
| PR **закрыт** кем-то посторонним (без merge) | Parent + review-сабтикеты → **Cancelled**, QA-тикет не создаётся. |
| **merged/approved без milestone** | Parent уезжает в разборочный проект **«Merged — no milestone (to sort)»** — видно, что влилось/одобрено, но не распределено по релизам. Остальной пайплайн отрабатывает. |

> **Fail-soft:** синхронизация с Linear — вспомогательная автоматизация и **никогда не роняет** проверку PR. Если ключ/конфиг отсутствует (например, секрет протух) или случилась любая ошибка — воркфлоу пишет **warning** и завершается успешно (зелёная галочка). Поскольку сбои «тихие», отдельный воркфлоу-мониторинг (`.github/workflows/integration-healthcheck.yml`) периодически проверяет ключ и заводит/обновляет GitHub-issue, если интеграция сломалась.
>
> **Принятие PR:** приёмка выражается сменой **Status = Accepted** на доске триажа. События доски Projects v2 нельзя ловить воркфлоу, поэтому раз в ~15 минут запускается `accept-reconcile.yml`, читает доску и двигает соответствующий тикет в Linear. Менять статус на доске может только человек с доступом к проекту, внешний контрибьютор — нет.

## Настройка (что нужно один раз сделать)

### 1. Linear API-ключ → GitHub Secret
1. Linear → Settings → API → создать Personal API key (лучше от служебного аккаунта).
2. GitHub → репозиторий → Settings → Secrets and variables → Actions → **Secrets** →
   New repository secret: имя `LINEAR_API_KEY`, значение — ключ.

> Ключ хранится **только** в GitHub Secrets, в коде/репозитории его нет.

### 2. Repository Variables
GitHub → Settings → Secrets and variables → Actions → **Variables**:

| Variable | Обязательна | Значение |
|---|---|---|
| `LINEAR_TEAM_KEY` | да | `GON` |
| `LINEAR_REVIEWERS` | да | JSON (см. ниже) |

`LINEAR_REVIEWERS` (одной строкой):

```json
[{"github":"DimaOrekhovPS","email":"dima.orekhov@productscience.ai","name":"Dima Orekhov"},{"github":"GLiberman","email":"gabriel@productscience.ai","name":"Gabriel Liberman"}]
```

Остальные переменные опциональны — дефолты уже совпадают с нашим воркспейсом
(статусы `Backlog`/`Done`/`Canceled`/`Not done`/`Merged. Ready for testing`, команда `QA`,
ассайн QA `maria.mitina@productscience.ai`, префикс проектов `Upgrade ` и т.д.).
Полный список — в `.github/scripts/linear-pr-sync/README.md`.

### 3. Доступы
- Дима и Габриель должны быть **коллабораторами репозитория** — иначе GitHub не даст
  запросить у них ревью (тикеты в Linear всё равно создадутся).

## Как поменять поведение
Почти всё настраивается через Variables без правки кода: ревьюеры, названия статусов/проектов,
ассайн QA, суффиксы тикетов, вкл/выкл авто-запрос ревьюеров (`LINEAR_REQUEST_GITHUB_REVIEWERS=false`).
