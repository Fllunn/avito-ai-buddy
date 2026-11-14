# Демонстрационные корпоративные сервисы

Это синтетические API, не интерфейсы и регламенты Авито. Схемы и коды ошибок заданы в `openspec/changes/build-onboarding-prototype/specs/{access-requests,time-off-requests,confirmed-actions}/spec.md`.

## Идентичность и данные

Внутренний клиент передаёт `Authorization: Bearer <SERVICE_SECRET>` и `X-Actor-ID`; роль, команда и руководитель берутся из собственного сида имитации. Запросы с браузерным Origin не авторизуются этим способом. Пользовательские cookie помощника и секрет ведущего не дают сервисных полномочий. Собственная база `buddy_mocks` изолирована от `buddy_api`.

## Чтение

- `GET /v1/resources` — видимый каталог, допустимые уровни и возможность запроса; скрытых ресурсов нет.
- `GET /v1/grants?resource_id=…` — собственные выданные доступы.
- `GET /v1/access-requests?resource_id=…` и `GET /v1/access-requests/{id}` — собственные заявки.
- `GET /v1/time-off-rules` — демонстрационные правила и календарь.
- `GET /v1/time-off-requests` и `GET /v1/time-off-requests/{id}` — собственные отгулы.

Чужой/неизвестный/скрытый объект даёт 404. Все даты — YYYY-MM-DD, мгновения — RFC3339, версии — целые ≥ 1. Ответы не кэшируются браузером.

## Подтверждаемые операции

`POST /v1/access-requests`: `{operation_key,resource_id,level,justification}`. Обоснование 10–500 символов проходит локальную очистку. Создаётся `ACC-…`, `created`, версия 1. Открытый дубль возвращает 409 DUPLICATE_OPEN_REQUEST с собственным existing_id; выданный доступ — ALREADY_GRANTED. Разрешены только уровни каталога, видимый запрет — 403 NOT_ALLOWED.

`POST /v1/time-off-requests`: `{operation_key,start_date,end_date}`. Причина отсутствия не принимается. Проверяются все нарушения календаря, назначенный активный руководитель и пересечения с submitted/in_review/approved. Создаётся `ABS-…`, `submitted`, версия 1. Пересечение — 409 OVERLAPPING_REQUEST с собственным existing_id. Одновременные запросы одного пользователя сериализуются блокировкой actor внутри транзакции.

`POST /v1/{access-requests|time-off-requests}/{id}/cancel`: `{operation_key,expected_version}`. Доступ отменяется только в created/in_review. Отгул — в submitted/in_review/approved и строго до первого дня по DEMO_TIMEZONE. Терминальные статусы не меняются.

Ключ — UUID. Операция, её исход и объект фиксируются атомарно. Повтор того же ключа с тем же очищенным телом возвращает исходный HTTP-статус и тело; другое тело даёт IDEMPOTENCY_CONFLICT. Поздний ответ ключа не обязан содержать текущий статус объекта: для него предназначено чтение объекта.

`POST /v1/operations/{key}/reconcile` с `{}` возвращает `{state,operation_key,result?}`; result содержит kind, http_status, body. Если операции нет, транзакция закрывает ключ как not_created. Поздний вызов получает OPERATION_CLOSED. Дополнительный внутренний заголовок `X-Service: time_off` выбирает режим сбоя сверки отгулов; без него используется access. Сверка не создаёт бизнес-объект.

Ошибки: `{error:{code,fields,retryable},existing_id?}`. 400 — схема/правила, 401 — идентичность, 403 — запрет, 404 — объект, 409 — версия/конфликт/состояние. 503 SERVICE_UNAVAILABLE гарантирует отсутствие обработки; потеря ответа или иной 5xx требует сверки. Ошибка фиксации транзакции возвращает неизвестный исход, а не ложную гарантию отсутствия операции.

## Панель ведущего

В DEMO_MODE=true открой http://localhost:8081/demo и введи отдельный `DEMO_ADMIN_SECRET` из локального окружения. Создаётся непрозрачная HttpOnly/SameSite=Strict cookie с операционным TTL 1 час. Изменения требуют точного DEMO_PANEL_ORIGIN. Стажёрская сессия и SERVICE_SECRET не подходят.

- `PATCH /demo/access-requests/{id}/status`: `{expected_version,status,reason?}`. created → in_review/approved/rejected/cancelled; in_review → approved/rejected/cancelled. approved создаёт Grant в той же транзакции. rejected требует очищенную причину.
- `PATCH /demo/time-off-requests/{id}/status`: тот же формат; approved может перейти в cancelled только до первого дня.
- `PUT /demo/faults/{access|time_off}`: `{mode,delay_ms}`; задержка 0–10000 мс.

Режимы: normal; unavailable_before (503 до операции); delay_before; lose_response_after (сначала фиксация, затем разрыв ответа); reconciliation_unavailable. Для восстановления сначала верни normal, затем проверь исход в UI. Панель не меняет базу помощника: обновление плана происходит при открытии/кнопке проверки, не непрерывным опросом согласований.

## Проверка

`make integration-test` и `make coverage` из корня создают отдельные PostgreSQL/Redis и удаляют только одноразовый тестовый проект. Контрактные тесты проверяют конкуренцию, права, ключи, версии, очистку и сбои. Основные демо-тома не сбрасываются этими командами.
