# Ревью безопасности AI-buddy — 15.11.2026

Проверялась рабочая копия на коммите `2020c77` (дерево чистое, совпадает с HEAD). SHA-256 проверенных файлов (244 шт.) — в [source-manifest.json](source-manifest.json), сводка дефектов — в [findings.json](findings.json), тесты-доказательства — в [evidence/](evidence/), порядок их запуска — в разделе 7. Логи прогонов в репозиторий не входят (`*.log` в `.gitignore`); результат воспроизводится командами из раздела 7.

Исходники проекта не менялись. Commit, push и PR не делались. Тесты запускались во временной копии (`git archive HEAD` в scratchpad) в одноразовом Compose-проекте `secreview-30091`; после прогона он удалён вместе со своими томами. Основной стенд `ai-buddy` и его тома не трогались. Настоящая LLM не вызывалась: угрозы со стороны модели проверялись подменным `hostileProvider`, который возвращает заданный, в том числе враждебный, JSON. К semenovm.ru и 177.202.73.214 запросов не было. Файл `.env` не открывался.

## 1. Итог

| Уровень | Кол-во | Дефекты |
|---|---|---|
| P0 | 0 | — |
| P1 | 0 | — |
| P2 | 1 | SR-01 |
| P3 | 5 | SR-02, SR-03, SR-04, SR-05, SR-06 |

**Перепроверка R1–R4:** ни один не воспроизводится на текущем коде (подробно в разделе 3). В репозитории появились регрессионные тесты `TestAstra*`, и они проходят.

**Базовая линия:** полный `go test ./...` для backend и mocks во временной копии проходит. Падают только три моих теста-доказательства для SR-01, SR-02 и SR-03 (воспроизведение — раздел 7). `npm audit` по `frontend/package-lock.json` нашёл 0 уязвимостей ([evidence/npm-audit.json](evidence/npm-audit.json)).

**Что проверено:** все HTTP-эндпоинты API (20 маршрутов) и имитаций (13 маршрутов и панель), сессии, Origin/CSRF и CORS, панель ведущего, межсервисный секрет, подтверждения и гонки, worker и сверка, LLM-конвейер с адаптером OpenRouter, очиститель ввода, SQL, миграции и роли PostgreSQL, Redis, фронтенд (XSS, ссылки, хранилище браузера), Dockerfile, compose, nginx, скрипт упаковки, секреты в рабочей копии и в истории git (10 коммитов), логи.

**Что не проверено** — см. раздел 6. Главное: govulncheck (нет локально), тайминговые утечки, реальная модель OpenRouter, внешний nginx-vhost сервера.

## 2. Дефекты

### SR-01 · P2 · Серверный срок демо-сессии не истекает при замороженном DEMO_NOW

- **Место:** [backend/internal/api/session.go:161](../../backend/internal/api/session.go) — `expires_at = Clock.Now()+SessionTTL`; [session.go:186](../../backend/internal/api/session.go) — проверка `expires_at > Clock.Now()`. Часы — бизнес-`cfg.Clock` ([backend/internal/app/app.go:58](../../backend/internal/app/app.go)). `DEMO_NOW` по умолчанию задан в [compose.yaml:39](../../compose.yaml) и [.env.example:3](../../.env.example). Деплой наследует этот default, если в серверном `.env` нет другого значения (серверный `.env` не проверялся).
- **Триггер:** любой стенд с `DEMO_NOW`: бизнес-часы стоят на одном моменте.
- **Эксплуатация и последствие:** `expires_at` всегда на `SESSION_TTL` впереди «текущего» бизнес-времени, поэтому сервер никогда не считает сессию просроченной. `Max-Age=8h` ограничивает только браузер. Украденный или сохранённый токен `buddy_session` (например, с общего демо-компьютера) работает бессрочно — до logout, повторного входа из того же браузера или demo-reset. Тест: срок сессии истёк 71 ч 20 мин назад, а `/api/me` отвечает 200.
- **Нарушенный контракт:** [ADR 0009](../../docs/adr/0009-operational-clock.md) — бизнес-часы применяются к правилам, срокам карточек и подтверждений и пользовательским отметкам; операционные моменты и технические сроки идут по реальному UTC. Срок сессии туда не входит. Также нарушается конфигурационный смысл `SESSION_TTL` и требование «действующей демо-сессии» ([identity-and-permissions](../../openspec/changes/build-onboarding-prototype/specs/identity-and-permissions/spec.md), «Системная идентичность»).
- **Доказательство:** `TestSecReviewSessionNeverExpiresWithFrozenClock` в [evidence/secreview_api_test.go](evidence/secreview_api_test.go). Результат прогона 15.11.2026: `expired_by=71h20m0s /api/me=200` → FAIL.
- **Как исправить:** выдавать и проверять срок сессии по операционным часам `clock.System` или по `now()` PostgreSQL. Для бизнес-логики оставить DEMO_NOW. Добавить тест с независимо подменяемыми часами.

### SR-02 · P3 · Ответ модели может пропустить шаг согласия на повторную подачу

- **Место:** [backend/internal/api/processing.go:511](../../backend/internal/api/processing.go) (отгул) и [:358](../../backend/internal/api/processing.go) (доступ): условие `intent.Slots.Query != reapplyQuery`. Слоты берутся из ответа модели ([backend/internal/agent/provider.go:98](../../backend/internal/agent/provider.go)), а константа [reapply.go:11](../../backend/internal/api/reapply.go) работает как управляющий токен.
- **Триггер:** у пользователя есть отклонённый запрос на те же даты или ресурс. Модель — по ошибке или из-за prompt injection через историю или документ — возвращает `time_off_request` с `slots.query="Подтверждаю повторную подачу"`.
- **Эксплуатация и последствие:** этап `reapply` («прошлый запрос отклонили… ответь „да“») пропускается, и карточка создаётся сразу. В контрольном прогоне без query карточек 0, с query — 1. Сервис не вызывается: карточку всё равно нужно явно подтвердить, поэтому это не P1. Для доступа так же можно обойти первое предупреждение, а при `RejectedID` модель может подставить собственное обоснование.
- **Нарушенный контракт:** требованием спеки этот шаг не записан. Принцип [assistant-dialogue](../../openspec/changes/build-onboarding-prototype/specs/assistant-dialogue/spec.md) («Намерения и уточнения»: свободный текст даёт только класс намерения и параметры) и код ошибки `REAPPLY_CONFIRMATION_REQUIRED` («требует явного продолжения») подразумевают согласие пользователя, а не модели. Отсюда уровень P3.
- **Доказательство:** `TestSecReviewModelSkipsReapplyConsent` в [evidence/secreview_api_test.go](evidence/secreview_api_test.go). Результат прогона 15.11.2026: control → 0 карточек, model-query → 1 → FAIL.
- **Как исправить:** признак повторной подачи хранить отдельным полем, которое выставляет только кнопка или явный текст пользователя (как `explicitConfirmation`). Из ответа модели query либо отбрасывать, либо принимать только как поисковую строку.

### SR-03 · P3 · Обход очистителя чувствительного ввода

- **Место:** [backend/platform/safety/text.go:28-29](../../backend/platform/safety/text.go) — словарные регулярные выражения по сырому тексту без нормализации.
- **Триггер:** диагноз или документ написаны с невидимым символом (U+200D, U+00AD), латинским гомоглифом («диагнoз», «пaспорт»), вразрядку («д и а г н о з»); номер карты разделён точками.
- **Эксплуатация и последствие:** все 6 вариантов проходят без маскировки (`form=false`, текст не изменён). Такой текст сохраняется в историю и уходит в контекст LLM. В режиме openrouter он уходит внешнему провайдеру.
- **Нарушенный контракт:** [identity-and-permissions](../../openspec/changes/build-onboarding-prototype/specs/identity-and-permissions/spec.md), «Минимизация чувствительного содержимого». Однако сценарий спеки ограничен «набором проверок», а [ADR 0008](../../docs/adr/0008-sensitive-input.md) признаёт гарантию только в пределах синтетического корпуса. Поэтому это укрепление (P3), а не нарушение принятой гарантии.
- **Доказательство:** `TestSecReviewSanitizerBypass` в [evidence/secreview_safety_test.go](evidence/secreview_safety_test.go): контроль закрыт, 6 обходов → FAIL.
- **Как исправить:** перед проверкой нормализовать текст — NFKC, удалить символы формата Cf, свернуть смешанные латинско-кириллические гомоглифы, проверять и вариант без пробелов. Расширить шаблон номеров (точки, `/`). Вынести корпус обходов в тесты.

### SR-04 · P3 · Нет защитных заголовков; общий origin с другими приложениями

- **Место:** [frontend/nginx.conf](../../frontend/nginx.conf) и [infra/avito-buddy.nginx.conf](../../infra/avito-buddy.nginx.conf) не задают CSP, `frame-ancestors`/`X-Frame-Options`, `X-Content-Type-Options`, `Referrer-Policy`. HSTS во внешнем vhost не проверялся. `ALLOWED_ORIGIN: https://semenovm.ru` ([infra/deploy.compose.yaml:172](../../infra/deploy.compose.yaml)) совпадает с origin других приложений сервера ([deploy design](../../openspec/changes/deploy-semenovm-subpath/design.md)).
- **Эксплуатация и последствие:**
  - `proxy_cookie_path` до `/avito_buddy/` не изолирует сессию от скриптов того же origin. XSS или вредоносная страница в любом другом приложении на semenovm.ru получает и cookie-доступ, и разрешённый Origin, то есть может действовать от имени стажёра.
  - Без `frame-ancestors` страницы того же сайта могут встроить UI во фрейм (SameSite=Lax cookie для same-site фрейма отправляется). Это открывает clickjacking кнопки подтверждения карточки.
  - API отдаёт `/demo/docs/*` как HTML без CSP. Шаблон экранирует данные (`html/template`), поэтому прямого XSS не найдено.
- **Нарушенный контракт:** явного требования к заголовкам в спеках нет. [subpath-hosting](../../openspec/changes/deploy-semenovm-subpath/specs/subpath-hosting/spec.md), «Изолированный демо-стенд», требует изоляции только на уровне портов. Поэтому P3.
- **Доказательство:** конфигурация (в findings.json — `confirmed-by-config`); запросы к публичному стенду не делались.
- **Как исправить:** добавить `Content-Security-Policy` с `default-src 'self'; frame-ancestors 'none'; object-src 'none'; base-uri 'none'`, а также `X-Content-Type-Options: nosniff` и `Referrer-Policy: no-referrer` (HSTS — во внешнем nginx). Для изоляции разместить стенд на отдельном поддомене, например avito-buddy.semenovm.ru.

### SR-05 · P3 · Упаковка ZIP построена на denylist

- **Место:** [scripts/package-submission.py:9-21](../../scripts/package-submission.py). Скрипт обходит файловую систему через `rglob` в `backend/ frontend/ mocks/ openspec/ docs/ infra/ scripts/ .agents/` и исключает только `.env*`, кэши и логи.
- **Последствие:** любой неотслеживаемый файл с секретом попадёт в архив сдачи: `infra/deploy.env`, `backend/secrets.json`, `*.pem`, `*.key`, локальный дамп БД в `docs/`. Сейчас таких файлов нет: `git status --ignored` по этим каталогам пуст, поэтому это укрепление.
- **Нарушенный контракт:** [AGENTS.md](../../AGENTS.md), «Данные и безопасность» («Секреты не коммитятся»). Нарушение потенциальное.
- **Как исправить:** собирать список из `git ls-files` (allowlist) плюс явно перечисленные артефакты. Падать, если найден файл по шаблонам секретов (`*.pem`, `*.key`, `*secret*`, `*.env`).

### SR-06 · P3 · Минимальные привилегии в инфраструктуре

- **PostgreSQL:** [infra/postgres/01-databases.sh:10-11](../../infra/postgres/01-databases.sh) — `buddy_api`/`buddy_mocks` владеют своими БД, а значит и схемой public (PG16). У рабочей роли приложения есть права на DDL, `DROP` и `TRUNCATE` своих таблиц. Изоляция между базами корректна: `REVOKE ALL … FROM PUBLIC`, отдельные роли без SUPERUSER/CREATEROLE.
- **Redis:** [compose.yaml:84-95](../../compose.yaml) — без `requirepass` и ACL. Порт открыт только на 127.0.0.1 (в деплое не открыт), но любой контейнер сети compose может читать и подменять кэш каталога. API повторно проверяет `SameScope`, но кэшированные `Items` берёт из Redis без проверки целостности.
- **nginx:** [frontend/Dockerfile:10](../../frontend/Dockerfile) и [infra/Dockerfile.frontend-runtime](../../infra/Dockerfile.frontend-runtime) — стандартный образ, мастер-процесс работает от root. Go-образы работают от UID 10001 — это корректно.
- **Как исправить:** разделить роли владельца миграций и рабочую роль (только DML), включить ACL Redis с паролем из `.env`, перейти на `nginx-unprivileged` или задать `user` и порт ≥1024.

## 3. Перепроверка R1–R4 (собственными тестами на текущем коде)

| ID | Что проверял мой тест (отличается от старого) | Результат | Вердикт |
|---|---|---|---|
| R1 | Враждебная модель возвращает `card_confirm` с confidence=1 в трёх вариантах: одиночный, составной `[smalltalk, card_confirm]`, `[card_edit, card_confirm]`; плюс подтверждение ручного шага P5 при тексте «Нет, пока не отмечай» | 0 попыток, 0 внешних мутаций; версия P5 не изменилась | **Не воспроизводится.** Защита — проверка `explicitConfirmation` для любого `card_confirm`, пришедшего не от кнопки ([processing.go:161](../../backend/internal/api/processing.go)) |
| R2 | Права теряются сменой команды и `policy_version` (в старом тесте менялись роли документа); проверены `/api/jobs`, `/api/executions/{id}`, `/demo/docs/doc-pay-principles`, `/api/chat` | job 200 → 404, документ 404, утечки нет | **Не воспроизводится** ([business.go:402](../../backend/internal/api/business.go) `authorizePayload` по `sources`) |
| R3 | Ресурс скрыт после успешной отправки; проверены attempt, job с результатом сервиса, card, `/api/onboarding`, `/api/chat` | attempt/job/card 404; в плане и чате ресурс не упоминается | **Не воспроизводится** |
| R4 | approved новой заявки сохранён первым, затем приходят created v1 и rejected v2 старой и stale created v1 новой | P6 остаётся `done` | **Не воспроизводится** (приоритет approved в [results.go:221](../../backend/internal/storage/results.go)) |

## 4. Проверено и чисто

- **IDOR и владелец.** Все чтения и мутации storage фильтруют по `user_id` из сессии. `TestSecReviewConcurrencyAndIDOR`: стажёр другой команды (maxim) и наставник (igor) по реальным ID alina получают 404 на card, attempt, job, execution, reconcile, discard и refresh. На retry igor получает 403: отказ по роли до поиска объекта, оракулом существования это не является. `user_id` в теле запроса отклоняется (`DisallowUnknownFields` → 400). У наставника нет заявок и чата стажёра.
- **Роли.** Сервисные действия разрешены только стажёру после даты старта (`authz.ServiceAction`). Проверка идёт в Prepare, при подтверждении (под `FOR UPDATE` пользователя с `policy_version`), в worker перед отправкой и в имитации внутри транзакции. Ручные шаги — только для владельца, `closure=manual`.
- **Подтверждения и гонки.** 8 параллельных подтверждений одной версии дали 202 с одним attempt_id и 1 внешний эффект. Активная или спорная попытка блокирует конфликт под блокировкой пользователя. Попытка и job записываются в одной транзакции до вызова. `tools.Execute` сверяет сохранённую попытку (`sending`, `may_have_sent`, параметры). Lease-токен проверяется перед каждой записью результата. Неизвестный исход переводит в сверку по прежнему ключу, `not_created` закрывает ключ в имитации.
- **Имитации.** Bearer-секрет сравнивается за постоянное время, запрос с `Origin` отклоняется (401 даже с верным секретом). Чужой ключ, чужая заявка и скрытый ресурс дают 404, закрытый — 403. `TestSecReviewMocksIsolation` проходит. Панель: отдельный секрет ≥32 байт, отличный от SERVICE_SECRET (проверяется в [config.go:91](../../backend/platform/config/config.go)). Сессия ведущего хранит хэш токена, срок 1 ч по операционным часам, cookie HttpOnly и SameSite=Strict с Path=/demo. Для мутаций Origin обязателен. Панель не проксируется наружу.
- **Сессии API.** Токен 32 байта из crypto/rand, в БД хранится только SHA-256. При входе сессия ротируется (старый хэш удаляется). Cookie HttpOnly, SameSite=Lax, Secure при https-origin. Logout удаляет запись. Мутации без Origin или с чужим Origin отклоняются (403). CORS отражает только точный `ALLOWED_ORIGIN`. Исключение — срок сессии (SR-01).
- **LLM.**
  - В контекст модели попадают только разрешённые документы (фильтр в SQL и `authz.Document`), ресурсы без hidden (closed — урезанная проекция), история владельца после повторной очистки. Перед и после вызова проверяется `SameScope`.
  - Ответ модели проходит строгий JSON (`DisallowUnknownFields`, ≤64 КБ), whitelist намерений, confidence в [0,1], очистку слотов.
  - Фрагменты ответа берутся только из авторизованных документов по ID и границам, свободный текст модели пользователю не показывается.
  - Системные инструкции не отражаются.
  - OpenRouter: ключ только в заголовке, редиректы запрещены, тело ограничено 1 МБ, ошибки без тела, таймаут через context и `LLM_TIMEOUT`, в логах нет ни ключа, ни тела.
- **SQL.** Все запросы параметризованы; конкатенируются только константы колонок. Курсор пагинации — base64 JSON с валидацией UUID.
- **Хранение чувствительного текста.** Сырой текст до записи не сохраняется: очистка выполняется в хендлере до `AcceptMessage`, хэш сырого текста не хранится. Причина отказа из имитации повторно очищается.
- **Фронтенд.** `dangerouslySetInnerHTML`/`innerHTML`/`eval` не используются. Ссылки источников допускают только `/demo/docs/` или `http(s)://` (`javascript:` исключён), стоит `rel=noreferrer`. `localStorage`/`sessionStorage` не используются, persist в Zustand нет. Права решает бэкенд (`capabilities` из `/api/me`).
- **Логи.** Пять вызовов `slog` в [process.go](../../backend/platform/process/process.go): без ПДн, cookie, текстов и секретов (`config.Load` не включает значения в ошибки). События содержат только коды и длительности.
- **Секреты.** В рабочей копии и истории (10 коммитов) реальных ключей нет. Найдены только синтетические тестовые пароли вида `synt***` в [infra/test.compose.yaml](../../infra/test.compose.yaml) и тестах, а также плейсхолдеры `repl***` в `.env.example`. `.env` есть в `.gitignore` и `.dockerignore` и в истории git не встречался.
- **Порты.** Локально всё открыто только на 127.0.0.1, в деплое — только frontend на 127.0.0.1:25173. Go-контейнеры работают не от root.
- **Зависимости frontend.** `npm audit`: 0 уязвимостей.

## 5. Рекомендации (не дефекты)

1. Фильтр prompt injection в документах ([documents.go](../../backend/internal/agent/documents.go)) ищет ключевые слова («агент» + «выдай/игнорируй/покажи») и легко обходится. Сейчас это не опасно: модель не может ни выполнить действие, ни выдать свободный текст. При расширении возможностей модели этой защиты не хватит.
2. `GET /api/chat` записывает приветствие (`Welcome`) — изменение состояния на GET. Безопасно, но расходится с моделью «GET без Origin».
3. Один `SERVICE_SECRET` позволяет действовать от имени любого `X-Actor-ID`. Для внедрения нужны подписанные короткоживущие токены пользователя или mTLS. Этот риск признан в [architecture.md](../../docs/architecture.md).
4. Нет ограничения частоты для `/api/chat/messages`, демо-входа и входа ведущего. Для публичного стенда с режимом openrouter это ещё и риск стоимости.
5. Контекст LLM берёт первые 100 сообщений истории (`Messages(user, 0)` с `LIMIT 100`), а не последние. Это функциональная, а не security-проблема, но влияет на качество отказов.
6. Добавить `govulncheck` в CI и закрепить Go-тулчейн: `go.mod` требует 1.27.0, локально стоит 1.24.4.

## 6. Ограничения

- **govulncheck** локально нет; глобально ничего не ставилось. Уязвимости Go-зависимостей (pgx v5.7.6, go-redis v9.7.3, x/crypto v0.37.0 и др.) не проверены.
- **Go-тесты** шли в одноразовом контейнере `golang:1.27.0-alpine`. Образ и модули из go.sum скачаны с согласия пользователя; том модулей удалён вместе с проектом.
- **Black-box прогон собранных образов** против стенда не выполнялся: проверки шли через `httptest` на реальном PostgreSQL 16.6 с `fakeRemote` вместо HTTP-имитаций. Связка API ↔ mocks покрыта существующими тестами `app`/`service`.
- **Реальная модель OpenRouter** не вызывалась (по условию). Качество следования инструкциям и устойчивость конкретной модели к injection не оценивались.
- **Тайминговые утечки** (например, 404 для существующего и несуществующего чужого ID) не измерялись. Статистически значимый замер требует отдельной нагрузочной методики.
- **Публичный стенд, внешний vhost и TLS/HSTS** не проверялись (по условию); серверный `.env` недоступен. Поэтому не подтверждено, задан ли `DEMO_NOW` на деплое (SR-01 применим, если задан или наследуется по умолчанию).
- **Фронтенд** проверен чтением кода, e2e и Vitest не запускались: локальный fnm не имеет node 24.22.0, `npm audit` выполнен системным node 22.
- **R1–R4** перепроверены вариантными тестами; утверждать, что не существует другого пути к тем же утечкам, нельзя.

## 7. Как воспроизвести

Логи прогонов не хранятся в репозитории; результат получается заново. Нужны Git Bash и Docker. Тесты запускаются во временной копии и в одноразовом Compose-проекте, рабочая копия и основной стенд не затрагиваются.

```bash
# 1. Временная копия кода на проверенном коммите (на ветке с исправлениями — указать её вместо 2020c77)
export COPY_DIR="$(mktemp -d)"
git archive 2020c77 backend mocks infra | tar -x -C "$COPY_DIR"

# 2. Тесты-доказательства в пакеты копии
E=output/security-review/evidence
cp $E/secreview_api_test.go    "$COPY_DIR/backend/internal/api/"
cp $E/secreview_safety_test.go "$COPY_DIR/backend/platform/safety/"
cp $E/secreview_mocks_test.go  "$COPY_DIR/mocks/internal/service/"

# 3. Одноразовый проект с уникальным именем: PostgreSQL 16.6 и golang:1.27.0-alpine
P=secreview-$RANDOM
docker compose -p $P -f $E/secreview.compose.yaml up -d --wait postgres
docker compose -p $P -f $E/secreview.compose.yaml run --rm go sh -c \
  "cd backend && go test -count=1 -v -run TestSecReview ./internal/api/ ./platform/safety/; \
   cd ../mocks && go test -count=1 -v -run TestSecReview ./internal/service/"

# 4. Полный набор тестов проекта (базовая линия)
docker compose -p $P -f $E/secreview.compose.yaml run --rm go sh -c \
  "cd backend && go test -count=1 -p 1 ./...; cd ../mocks && go test -count=1 ./..."

# 5. Удалить только этот проект и его тома
docker compose -p $P -f $E/secreview.compose.yaml down --volumes
```

Ожидаемый результат на коммите `2020c77`:

| Тест | Результат |
|---|---|
| `TestSecReviewR1ModelCannotConfirm`, `TestSecReviewR2JobAfterTeamChange`, `TestSecReviewR3HiddenResourceProjections`, `TestSecReviewR4LateOldStates` | PASS — R1–R4 не воспроизводятся |
| `TestSecReviewConcurrencyAndIDOR`, `TestSecReviewMocksIsolation` | PASS — контрольные проверки |
| `TestSecReviewSessionNeverExpiresWithFrozenClock` | FAIL — SR-01 |
| `TestSecReviewModelSkipsReapplyConsent` (подтест `model-query`) | FAIL — SR-02 |
| `TestSecReviewSanitizerBypass` | FAIL, 6 обходов — SR-03 |

На ветке с исправлениями (`harden-security-review`) тесты SR-01 и SR-02 проходят. `TestSecReviewSanitizerBypass` по-прежнему падает только на трёх случаях с гомоглифами и разрядкой: они согласованно оставлены вне этапа 1. `npm audit` воспроизводится командой `npm audit --prefix frontend --package-lock-only`.
