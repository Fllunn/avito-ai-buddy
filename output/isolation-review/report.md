# Ревью изоляции данных между пользователями AI-buddy — 15.11.2026

Проверялся коммит `4afe6ef` (рабочая копия чистая, совпадает с HEAD). Сводка дефектов — в [findings.json](findings.json), тесты-доказательства и логи — в [evidence/](evidence/), порядок запуска — в разделе 5. Логи в git не попадают (`*.log` в `.gitignore`), результат воспроизводится командами из раздела 5.

Исходники проекта не менялись. Commit, push, merge, rebase и PR не делались, ветки не создавались. Тесты запускались во временной копии (`git archive HEAD` в scratchpad) в одноразовом Compose-проекте `isoreview-8593` (PostgreSQL 16.6 и `golang:1.27.0-alpine`). После прогона проект удалён вместе со своими томами. Основной стенд `ai-buddy` и его тома не трогались. Модули Go из `go.sum` скачаны в одноразовый том и удалены вместе с ним. Vitest запускался в той же временной копии на Node 24.22.0 с копией `node_modules`. Настоящая LLM не вызывалась: контекст модели проверялся подменным `recordingProvider`, который записывает каждый кадр `agent.Context` и строку запроса для `Recognize` и `SelectFragments`. К semenovm.ru и серверу запросов не было. `.env` не открывался. Подагенты не использовались.

Выводы [предыдущего ревью](../security-review/report.md) не повторяются. Его IDOR-пробы (`TestSecReviewConcurrencyAndIDOR`, `TestSecReviewMocksIsolation`) здесь расширены: одновременная работа двух стажёров, контекст LLM, фоновые обработчики, курсоры, коллизии ID, роли, побочные каналы и фронтенд.

## 1. Итог

**Главный вопрос: может ли один пользователь увидеть или изменить данные другого?** Через API, чат, план, карточки, маршруты, ответы бота и контекст LLM — **нет**. Ни один сценарий из пунктов 1–8 не дал утечки без условий или при условии, доступном пользователю. Найдены одна ограниченная утечка признаков, одно место, где изоляция держится на внешнем сервисе, и два места для укрепления.

| Уровень | Кол-во | Дефекты |
|---|---|---|
| P0 | 0 | — |
| P1 | 0 | — |
| P2 | 2 | IR-01, IR-03 |
| P3 | 3 | IR-02, IR-04, IR-05 |

- **IR-01 (P2).** Локальная проекция `service_objects` не привязана к владельцу. Если сервис выдаст тот же номер `ABS-…` другому пользователю, запись Алины получит payload Максима (даты, статус, причину), и это будет видно в её плане. Пользователь не может создать это условие сам. Поэтому P2, а не P1.
- **IR-03 (P2).** `message_id` и `id` сообщений чата — глобальная последовательность. Разница между двумя своими id показывает, сколько сообщений за это время появилось у всех остальных пользователей.
- **IR-02 (P3).** `OfferNext` связывает `dialogue_state` с карточкой и попыткой без проверки `user_id`. Через API это не эксплуатируется: `card_id` пишет только сервер.
- **IR-04 (P3).** Фронтенд не сверяет владельца ответа с профилем вкладки. Во второй вкладке того же браузера после входа другого профиля показываются данные Максима под заголовком «Привет, Алина!».
- **IR-05 (P3).** Сверка в имитации по неизвестному ключу закрепляет ключ за вызывающим, а ответ (200 или 404) различает «чужой» и «свободный» ключ. Это доступно только владельцу `SERVICE_SECRET`.

**Тесты.** Написано 14 Go-тестов и 4 теста Vitest. 15 проходят: это доказательства изоляции по пунктам 1–8. 4 падают на текущем коде: IR-01, IR-02, IR-03 и IR-04. Полный `go test ./...` для backend и mocks во временной копии проходит, кроме этих трёх демо-тестов ([evidence/run-full-suite.log](evidence/run-full-suite.log)). Существующие `App.test.tsx` проходят вместе с новыми.

**Не проверено:** реальная модель OpenRouter, тайминговые каналы, black-box прогон собранных образов против HTTP-имитаций, связка API ↔ mocks под нагрузкой, логи процессов на стенде (подробно в разделе 4).

## 2. Дефекты

### IR-01 · P2 · Проекция заявки не привязана к владельцу

- **Место:** [backend/internal/storage/results.go:197](../../backend/internal/storage/results.go). Upsert `ON CONFLICT(service,object_id) DO UPDATE SET … payload=EXCLUDED.payload` не обновляет `user_id` и не проверяет совпадение владельца. Чтение по ключу без владельца — [:173](../../backend/internal/storage/results.go). Ключ таблицы `PRIMARY KEY(service,object_id)` — [002_actions.sql:87](../../backend/internal/storage/migrations/002_actions.sql). Чтение плана — по `user_id` строки ([runtime.go:75](../../backend/internal/storage/runtime.go), [onboarding.go:41](../../backend/internal/api/onboarding.go)).
- **Сценарий:** в API лежит `ABS-2001` Алины (version 1). Сервис отдаёт Максиму объект с тем же номером и большей версией. `cleanResult` пропускает его, потому что `object.UserID == user` для Максима. Upsert перезаписывает payload, но строка остаётся за Алиной. Для доступа (`ACC-`) запись случайно откатывается: `projectAccessPlan` ищет строку по новому `user_id`, не находит её и возвращает `no rows`. Для отгула проекции нет, поэтому upsert фиксируется.
- **Условие:** номер заявки повторно выдан другому пользователю. Так бывает при выборочном пересоздании БД имитаций (последовательности `access_number`/`absence_number` начинаются заново), восстановлении из копии, при подключении другого стенда имитаций или при ошибке адаптера корпоративного сервиса. Скрипт [demo-reset.sh](../../scripts/demo-reset.sh) сбрасывает обе БД и в этом порядке опасного состояния не создаёт. Пользователь условие вызвать не может.
- **Последствие:** Алина видит в своём плане даты, статус и причину отказа Максима. Максим свою заявку не видит, потому что строка не его. Лента Максима получает уведомление, событие `status_changed` пишется на Максима.
- **Нарушенный контракт:** [identity-and-permissions](../../openspec/changes/build-onboarding-prototype/specs/identity-and-permissions/spec.md), «Личные данные и назначения» («только собственную переписку, заявки и запросы»); [AGENTS.md](../../AGENTS.md), «Права пользователя».
- **Доказательство:** `TestIsoServiceObjectOwnerNotBound` в [evidence/isoreview_api_test.go](evidence/isoreview_api_test.go). Прогон 15.11.2026: `access reuse: err=no rows… payload.user_id=alina`; `time_off reuse: err=<nil> row.user_id=alina payload.user_id=maxim alina_plan_has_maxim=true` → FAIL.
- **Как исправить:** при совпадении `(service,object_id)` и другом владельце не обновлять запись, а возвращать `ErrContract` (`WHERE service_objects.user_id=EXCLUDED.user_id` в `DO UPDATE` плюс проверка числа строк). В `saveObject` проверять владельца найденной строки. Желательно включить `user_id` в ключ проекции и уведомлений. Добавить регрессионный тест на повторный номер для обоих сервисов.

### IR-03 · P2 · Глобальные id сообщений раскрывают активность других пользователей

- **Место:** `messages.id bigint GENERATED ALWAYS AS IDENTITY` — [002_actions.sql:6](../../backend/internal/storage/migrations/002_actions.sql). id отдаётся в `POST /api/chat/messages` (`message_id`, [business.go:174](../../backend/internal/api/business.go)) и `POST /api/dialogue/actions` (`message_ids`), а также в каждом элементе `GET /api/chat` (`id`, [messages.go:15](../../backend/internal/storage/messages.go)).
- **Сценарий:** Максим отправляет сообщение и получает `message_id=1`. Алина отправляет три сообщения. Следующий `message_id` Максима — 5, лента: `ids=[1 5 6]`.
- **Последствие:** любой пользователь может измерять объём активности всех остальных: сообщения, ответы бота, уведомления о статусах, приветствия. Если опрашивать часто, видно, когда «соседи» активны. Содержимое не раскрывается, поэтому это ограниченная утечка признаков. Личная `sequence` и `next_cursor` при этом изолированы.
- **Нарушенный контракт:** [agent-execution-visibility](../../openspec/changes/redesign-buddy-experience/specs/agent-execution-visibility/spec.md), «Авторизация каждого чтения» (идентификаторы не дают признаков чужих данных) — по духу; [identity-and-permissions](../../openspec/changes/build-onboarding-prototype/specs/identity-and-permissions/spec.md), «Проверки до раскрытия и выполнения».
- **Доказательство:** `TestIsoSideChannelGlobalMessageIDs` в [evidence/isoreview_api_test.go](evidence/isoreview_api_test.go). Прогон: `maxim message_id 1 -> 5; chat ids=[1 5 6]; gap=3 (alina sent 3)` → FAIL.
- **Как исправить:** наружу отдавать непрозрачный идентификатор (UUID или `client_message_id`) либо личную `sequence`. Внутренний `bigint` оставить для связей в БД. Связи маршрутов `execution_links(kind='message', object_id=id)` перевести на тот же внешний id или на `(user_id, sequence)`.

### IR-02 · P3 · OfferNext соединяет состояние диалога с карточкой без владельца

- **Место:** [backend/internal/storage/runtime.go:436](../../backend/internal/storage/runtime.go): `JOIN cards c ON c.id::text=d.payload->>'card_id'` без `c.user_id=d.user_id`, подзапрос `attempts` — без `user_id`. [:465](../../backend/internal/storage/runtime.go): статус первой карточки переноса `SELECT state FROM attempts WHERE card_id=$1` — без `user_id`.
- **Сценарий:** в `dialogue_state` Максима оказывается `card_id` карточки Алины. Через API так не получится: `card_id` и `transfer.first_card_id` пишет только сервер из собственных карточек ([processing.go](../../backend/internal/api/processing.go)). В тесте состояние подделано в БД.
- **Последствие:** закрытие карточки Алины порождает в ленте Максима «Предыдущее действие завершено. Перейдём к отгулу?…». Для переноса отказ или успех чужой попытки решил бы, продолжать ли второе действие Максима. Это нарушение принципа «каждая выборка — по владельцу», а не реальная утечка.
- **Нарушенный контракт:** [AGENTS.md](../../AGENTS.md), «Права пользователя» (проверки на бэкенде); защита в глубину для фоновых обработчиков.
- **Доказательство:** `TestIsoOfferNextJoinsForeignCard`. Прогон: `offered=true maxim messages 1 -> 2` → FAIL.
- **Как исправить:** добавить `AND c.user_id=d.user_id` в соединение, `AND user_id=d.user_id` в подзапросы `attempts` и `user_id=$2` в чтение статуса переноса.

### IR-04 · P3 · Вкладка не сверяет владельца ответа с профилем

- **Место:** [frontend/src/App.tsx:34-38](../../frontend/src/App.tsx). `me`, `plan` и `chat` разделены поколением только внутри одной вкладки (`resetSession`/`epoch`). Ответ `/api/chat` и `/api/onboarding` не проверяется на принадлежность текущему `profile.id`. `refetchOnWindowFocus: false`, поэтому `me` во второй вкладке не обновляется.
- **Сценарий:** Алина открыта в двух вкладках. В вкладке 2 выбран Максим, общий HttpOnly cookie перезаписан. Вкладка 1 при следующем обновлении ленты (отправка сообщения, опрос задания) получает ленту Максима и показывает её под «Привет, Алина!». Сообщение, набранное «как Алина», уходит от имени Максима.
- **Последствие:** в одном браузере смешиваются отображения двух демо-профилей. На сервере изоляция не нарушается: каждый ответ соответствует cookie. Но спека требует, чтобы поздние ответы прежней сессии не появлялись в новой. Сценарий «другая вкладка» в ней явно не описан, поэтому P3.
- **Нарушенный контракт:** [assistant-dialogue](../../openspec/changes/build-onboarding-prototype/specs/assistant-dialogue/spec.md), «Переключение пользователя» — по духу.
- **Доказательство:** тест `second tab of the same browser (P3 demo)` в [evidence/isoreview_app.test.tsx](evidence/isoreview_app.test.tsx) → FAIL (`expected true to be false`).
- **Как исправить:** сервер добавляет в ответы `/api/chat` и `/api/onboarding` id владельца (или версию сессии), клиент при несовпадении с `me.profile.id` сбрасывает состояние как при смене профиля. Дополнительно — `BroadcastChannel`/событие `storage` для синхронизации вкладок и `refetchOnWindowFocus` для `me`.

### IR-05 · P3 · Сверка в имитации закрепляет неизвестный ключ и различает чужой ключ

- **Место:** [mocks/internal/service/operations.go:186](../../mocks/internal/service/operations.go): неизвестный ключ записывается как `not_created` за вызывающим и даёт 200. Чужой ключ даёт 404.
- **Сценарий и последствие:** обладатель `SERVICE_SECRET`, подставив `X-Actor-ID`, может проверить, принадлежит ли UUID кому-то, и заблокировать ещё не использованный чужой ключ: последующий create Алины с этим ключом получит 404. Через API ключи генерируются случайно (UUID v4), пользователю не показываются, а сверка вызывается только для собственной попытки. Поэтому пользователь этого не достигает.
- **Нарушенный контракт:** формально нет. Модель доверия к `SERVICE_SECRET` описана в [architecture.md](../../docs/architecture.md) и в рекомендации 3 предыдущего ревью.
- **Доказательство:** `TestIsoMocksOperationKeysPerActor` (проходит, фиксирует поведение): `reconcile foreign key -> 404`, `unknown key -> 200 not_created`, `alina create with key closed by maxim -> 404`.
- **Как исправить:** при внедрении — ключ операции в пространстве владельца (`PRIMARY KEY(actor_id, operation_key)`) или подписанный токен пользователя вместо `X-Actor-ID`. Для прототипа достаточно отметки в ADR.

## 3. Проверено и чисто

Все тесты лежат в [evidence/](evidence/), лог прогона — [run-isoreview.log](evidence/run-isoreview.log) и [run-vitest.log](evidence/run-vitest.log).

### 3.1. Параллельная работа двух стажёров — `TestIsoParallelInternsSeeOnlyOwnData` (PASS)

Алина и Максим одновременно (горутины) отправляют по 4 сообщения: маркер, вопрос по документам, запрос доступа, «Что дальше?». Затем создают карточки доступа и отгула и подтверждают их параллельно. Очередь обрабатывают 4 конкурентных обработчика `ProcessOnce` и 4 `Worker.Once`. Оба запускают refresh-задания.

- В `/api/chat`, `/api/onboarding` и `/api/executions` каждого нет маркера, имени, шагов, документов, обоснований и номеров ACC/ABS другого. Свои данные на месте.
- Каждая чужая карточка, попытка, задание сообщения, задание отправки и маршрут (`after_sequence=0`) дают 404 с телом, идентичным несуществующему объекту (кроме `request_id`). Маршрутов по 6 у каждого, множества не пересекаются.
- Курсоры: собранный вручную `cursor` из позиции Алины (её `started_at` +1 ч) не выводит Максима на её маршруты. SQL всегда фильтрует `r.user_id=$1`. `after` чата (0, 1, `next_cursor` Алины) работает по личной `sequence`.
- По коду: все чтения storage (`Card`, `Cards`, `Attempt`, `Job`, `Messages`, `Objects`, `ExecutionRepository.Read`, `Executions`, `ExecutionIDs`, `Dialogue`, `Steps`) фильтруют по `user_id` сессии. Мутации блокируют родителя `WHERE id=$1 AND user_id=$2 FOR UPDATE` до любых чтений без владельца (`ConfirmCard`, `Retry`, `changeCard`, `ReconcileNow`).

### 3.2. Контекст LLM — `TestIsoLLMContextContainsOnlyOwnData` (PASS)

`recordingProvider` записал 11 вызовов: Алина — 4 `Recognize` и 1 `SelectFragments`, Максим — 4 и 1, наставник Игорь — 1 `Recognize`.

- В кадрах и строках запроса Алины нет `maxim`, «Максим», маркера Максима, `doc-srch*`, `sergey` и номеров заявок Максима. Для Максима симметрично.
- Все `Steps` в кадре стажёра — его `intern_id`, все документы — его команды или общие.
- Кадр Игоря содержит только шаги, где он владелец (`alina-P1/P2/P8`), без переписки Алины, без `ACC-` и её обоснований.
- По коду: `Builder.Build` берёт профиль, шаги (`authz.Steps`), документы (SQL-фильтр и `authz.Document`), каталог через персональный `Directory`. История — только `Messages(user)`, повторно отфильтрованная `AuthorizedHistory` и `m.UserID != user`. Заявки в контекст не передаются вообще. `SelectFragments` получает кадр `Search.Find(user)` того же построителя.

Каталог ресурсов (включая ресурсы чужой команды с их согласующим из каталога) общий по замыслу. Спека [access-requests](../../openspec/changes/build-onboarding-prototype/specs/access-requests/spec.md) предусматривает доступ «вне рекомендации» с советом обратиться к наставнику. Это не данные другого пользователя.

### 3.3. Фоновые обработчики — `TestIsoBackgroundLeaseRestartAndDialogueState` (PASS)

- **Потеря lease и перезапуск.** Старый обработчик взял задание с lease 1 с. «Перезапущенный» процесс через 5 с обработал всю очередь, включая это задание. Поздний `CompleteMessage` старого обработчика получил `ErrLeaseLost` и не изменил ни одну ленту. Для worker действий поздний `MarkSending` после перехвата задания — тоже `ErrLeaseLost`. Попытки обоих завершились `succeeded`.
- **Задания и ответы.** Нет задания, чей `user_id` отличается от автора сообщения. Ответы лежат в своих лентах. `ClaimMessage` упорядочивает задания внутри пользователя (`prior.user_id=j.user_id`), поэтому очередь одного не блокирует другого.
- **dialogue_state.** У каждого `card_id` и `execution_id` состояния принадлежат владельцу строки. `ProcessOnce` читает и пишет состояние по `work.Message.UserID`.
- **OfferNext.** Штатное приглашение «Перейдём к отгулу?» попало только Алине, лента Максима не изменилась. Дефект соединения без владельца — IR-02.
- **Refresh и уведомления.** Нет `service_objects`, где `user_id` строки отличается от `payload.user_id`. `List` и `Get` клиента интеграции отбрасывают объекты чужого `user_id`, `cleanResult` повторяет проверку. Нарушение при повторном номере — IR-01.

### 3.4. Коллизии идентификаторов — `TestIsoIdentifierCollisions` (PASS)

- **`client_message_id`.** Один `client_message_id` у Алины и Максима даёт два разных сообщения и задания (уникальность `(user_id, client_message_id)`). Повтор Максима с тем же текстом возвращает его же сообщение, с другим текстом — 409. Алина со «своим» id и текстом Максима получает 409 только против собственной записи. Кнопка с тем же id — 409 у Максима. Лента Алины не изменилась.
- **Чужие ID в path и body (17 проб Максима по объектам Алины).** Карточка (GET/PATCH/confirm/discard), попытка (GET/reconcile/retry), задания отправки и сообщения, маршрут, `POST /api/steps/alina-P5/confirmation`, `/confirm` с её `confirmation_id` на её и на своём шаге, `refresh` с её `ACC-`/`ABS-`, `POST /api/cards` `access_cancel`/`time_off_cancel` с её номерами. Всё даёт 404 с телом, равным телу для несуществующего объекта.
- **Чужие номера в слотах кнопок.** `cancel` с её `ACC-` даёт «Не удалось проверить собственный запрос.». `time_off_request` с её `ABS-` даёт «Не удалось проверить исходный запрос.». `mark_done` с `alina-P5` не даёт ни ответа, ни карточки. У Максима 0 карточек, ответы бота не содержат дат и ресурсов Алины, версия `alina-P5` не изменилась. Подтверждение Алиной её шага работает.

### 3.5. Сессии в одном браузере

`TestIsoSessionSwitchInOneBrowser` (PASS):

- Вход Максима с cookie Алины ротирует сессию. Старый cookie на `/api/me`, `/api/chat`, `/api/onboarding`, `/api/attempts/{id}` и `POST /api/chat/messages` даёт 401.
- Подтверждённая Алиной до смены попытка обработана после смены от её имени. Максим её не видит (404), в его ленте нет данных Алины.
- После logout — 401.
- Повторный вход Алины возвращает её ленту с одним приветствием и попытку `succeeded`.

Vitest ([isoreview_app.test.tsx](evidence/isoreview_app.test.tsx), 3 PASS):

- **Поздний ответ.** Ответ `/api/chat` Алины, задержанный до смены профиля, не появляется у Максима: генерация сессии и `AbortController` отбрасывают его.
- **Кэш.** Пока лента и план Максима загружаются, кэш Алины (TanStack Query `clear()`) и черновик в Zustand не показываются. Поле ввода пусто.
- **Повторный вход.** Logout → Максим → Алина показывают только собственную ленту.

Ограничение второй вкладки — IR-04.

### 3.6. Роли — `TestIsoRoleProjectionsAndTeamDocuments` (PASS)

- **Проекции наставника, руководителя и HR.** После полного сценария двух стажёров `GET /api/onboarding` даёт:
  - `igor` — ровно `alina-P1/P2/P8`;
  - `sergey` — `maxim-P1/P2/P8`;
  - `olga` — `alina-P3`;
  - `anna` — `maxim-P3`;
  - `daria` — `alina-HR`.

  У всех `requests` пустые, все шаги — их собственные. В плане и чате ни у кого нет маркеров стажёров, `ACC-`/`ABS-` и обоснований. Попытки обоих стажёров — 404.
- **План стажёра.** В плане Алины есть шаги других владельцев (наставника, руководителя, HR), но только её `intern_id`. Переписки и заявок владельцев там нет.
- **Документы команды по прямой ссылке:**
  - Алина: `doc-pay-principles` 200, `doc-srch-components` и `doc-srch-notes` 404;
  - Максим: `doc-pay-principles` и `doc-pay-oncall-2024` 404, `doc-srch-components` 200;
  - Игорь: `doc-srch-components` 404; Сергей: `doc-pay-principles` 404;
  - Дарья (без команды): командный документ 404, общий 200.

  Тело 404 совпадает с несуществующим документом.

### 3.7. Имитации — `TestIsoMocksPerActorProjections`, `TestIsoMocksActorHeaderIsExact`, `TestIsoMocksOperationKeysPerActor` (PASS)

- **Списки.** Одновременное создание заявок обоими. Списки `access-requests` (в том числе с `resource_id=monitoring`, где заявки есть у обоих), `time-off-requests` и `grants` у каждого содержат только свои записи. Сидовый грант Максима (`ACC-1001`) Алине не виден.
- **Чужие заявки.** GET и cancel чужих `ACC-`/`ABS-` дают 404 с тем же кодом, что у несуществующего номера.
- **Персональный каталог.** Согласующий «manager» подставляется для каждого свой: `launch-plans` — olga у Алины, anna у Максима.
- **`X-Actor-ID`.** Строгое совпадение: `Alina`, ` alina`, `alina `, `alina,maxim`, `ALINA`, `unknown`, пустой — 401. Значение с NUL даёт 503 от PostgreSQL. Это не принятие запроса, но код ошибки неточный; реальный HTTP-сервер Go такой заголовок не пропускает. Второй заголовок `X-Actor-ID` не расширяет выборку.
- **Ключи операций.** Чужой ключ (replay и reconcile) даёт 404 без результата (см. также IR-05).

### 3.8. Побочные каналы

`TestIsoEventsAndCountersAreOwnerScoped` и `TestIsoCatalogCacheIsPerUser` (PASS):

- **События и связи.** Нет событий, где `user_id` не совпадает с владельцем попытки. Нет связей `execution_links`, где `user_id` отличается от владельца маршрута или карточки. Нет уведомлений, записанных в чужую ленту. Поля событий не содержат текста сообщений. Личные `sequence`/`next_cursor` равны числу собственных сообщений (11 у обоих после сценария).
- **Коды и тела ошибок.** Для чужого и несуществующего объекта тела 404 побайтно совпадают (кроме случайного `request_id`) во всех пробах пунктов 3.1, 3.4 и 3.6. Единственное отличие кода — `retry` для не-стажёра: 403 до поиска объекта. Это отмечено в прошлом ревью и не является оракулом.
- **Redis.** Ключ `buddy:directory:<sha256(scope)>` не содержит id пользователя, по одному ключу на область прав. Конверт хранит `UserID/Version/Role/TeamID`. Подмена: конверт Максима под ключом Алины отклоняется и вызывает повторное чтение источника, у Алины остаётся её согласующий. Персональные заявки и переписка в Redis не попадают.
- **Логи.** По коду пять вызовов `slog` в [process.go](../../backend/platform/process/process.go) пишут только коды и длительности, без user_id и текстов. Логи стенда не снимались.
- **Счётчики.** Канал через глобальные id — IR-03.

## 4. Ограничения

- **Реальная модель** не вызывалась (по условию). Проверено, что модель получает только данные владельца. Как модель распорядится ими, не проверялось, но свободный текст модели пользователю не показывается (см. предыдущее ревью).
- **API ↔ имитации.** Проверки API шли через `httptest` и `fakeRemote` на реальном PostgreSQL. Изоляция в имитациях проверена отдельными тестами пакета `service`. Black-box прогон собранных образов, nginx и cookie через реальный браузер не выполнялся.
- **Фронтенд** проверен в jsdom с MSW. Две реальные вкладки браузера (IR-04) смоделированы сменой «серверного» пользователя в MSW.
- **Тайминговые каналы** (разница времени ответа для чужого и несуществующего ID, задержка очереди из-за активности соседа) не измерялись.
- **Лимит истории.** Конкурентность проверена на двух стажёрах и четырёх обработчиках. Нагрузочный прогон с десятками пользователей и обработчиков не проводился. `Messages(user, 0)` с `LIMIT 100` в тестах не достигался.
- **IR-01.** Условие (повторный номер у другого пользователя) смоделировано прямым вызовом `ApplyObject`. На стенде оно не воспроизводилось: публичный стенд и серверные БД не трогались.
- **IR-02.** Состояние подделано прямой записью в БД, через API оно недостижимо.
- **Логи процессов** проверены только по коду.

## 5. Как воспроизвести

Логи прогонов в git не коммитятся (`*.log` в `.gitignore`) и получаются заново. Нужны Git Bash, Docker Desktop и, для фронтенда, Node 24.22.0 через fnm. Всё выполняется во временной копии и одноразовом Compose-проекте; рабочая копия и стенд `ai-buddy` не затрагиваются.

```bash
# 1. Временная копия кода на проверенном коммите
export COPY_DIR="$(mktemp -d)"
git archive 4afe6ef | tar -x -C "$COPY_DIR"

# 2. Тесты-доказательства в пакеты копии
E=output/isolation-review/evidence
cp $E/isoreview_api_test.go     "$COPY_DIR/backend/internal/api/"
cp $E/isoreview_catalog_test.go "$COPY_DIR/backend/internal/catalog/"
cp $E/isoreview_mocks_test.go   "$COPY_DIR/mocks/internal/service/"
cp $E/isoreview_app.test.tsx    "$COPY_DIR/frontend/src/"

# 3. Одноразовый проект с уникальным именем
P=isoreview-$RANDOM
docker compose -p $P -f $E/isoreview.compose.yaml up -d --wait postgres
MSYS_NO_PATHCONV=1 docker compose -p $P -f $E/isoreview.compose.yaml run --rm go sh -c \
  "cd backend && go test -count=1 -v -run TestIso ./internal/api/ ./internal/catalog/; \
   cd ../mocks && go test -count=1 -v -run TestIso ./internal/service/" | tee $E/run-isoreview.log

# 4. Полный набор тестов проекта с тестами-доказательствами
MSYS_NO_PATHCONV=1 docker compose -p $P -f $E/isoreview.compose.yaml run --rm go sh -c \
  "cd backend && go test -count=1 -p 1 ./...; cd ../mocks && go test -count=1 ./..." | tee $E/run-full-suite.log

# 5. Удалить только этот проект и его тома
docker compose -p $P -f $E/isoreview.compose.yaml down --volumes
```

```bash
# 6. Фронтенд (Vitest + MSW) в той же копии
cp -r frontend/node_modules "$COPY_DIR/frontend/"
cd "$COPY_DIR/frontend" && eval "$(fnm env)" && fnm use 24.22.0 && npx vitest run src/isoreview_app.test.tsx src/App.test.tsx
```

Ожидаемый результат на коммите `4afe6ef`:

| Тест | Результат |
|---|---|
| `TestIsoParallelInternsSeeOnlyOwnData`, `TestIsoLLMContextContainsOnlyOwnData`, `TestIsoBackgroundLeaseRestartAndDialogueState`, `TestIsoIdentifierCollisions`, `TestIsoSessionSwitchInOneBrowser`, `TestIsoRoleProjectionsAndTeamDocuments`, `TestIsoEventsAndCountersAreOwnerScoped` | PASS — изоляция по пунктам 1–6 и 8 |
| `TestIsoCatalogCacheIsPerUser` | PASS — Redis-кэш |
| `TestIsoMocksPerActorProjections`, `TestIsoMocksActorHeaderIsExact`, `TestIsoMocksOperationKeysPerActor` | PASS — имитации |
| Vitest: `drops a late chat response…`, `does not show cached data…`, `logout and re-login…` | PASS — фронтенд |
| `TestIsoServiceObjectOwnerNotBound` | FAIL — IR-01 |
| `TestIsoSideChannelGlobalMessageIDs` | FAIL — IR-03 |
| `TestIsoOfferNextJoinsForeignCard` | FAIL — IR-02 |
| Vitest: `does not render another profile data under the current profile header` | FAIL — IR-04 |

Полный `go test ./...` проходит во всех пакетах, кроме трёх перечисленных FAIL в `internal/api`. Существующие `App.test.tsx` (9 тестов) проходят.
