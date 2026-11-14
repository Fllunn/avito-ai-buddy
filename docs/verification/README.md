# Проверка прототипа

Все прогоны используют синтетические данные и fake; реальная модель не вызывается. Человеческая приёмка change выполняется отдельно. Числа покрытия берутся из `coverage/summary.json`, измерения задержки — из `performance.json`.

## Карта требований к проверкам

| Требования / задачи | Проверки |
|---|---|
| Конфигурация, время, инфраструктура, 1.1–1.5 | platform/config, platform/clock, scripts/check-compose.sh, scripts/coverage/*_test.go; Linux npm ci и Docker build |
| Идентичность, права, очистка, кэш, 2.1–2.5 | api/session_test.go, authz/policy_test.go, catalog/cache_test.go; agent/TestContextScopeChangesNeverReachProvider, TestRightsAreRecheckedAfterProviderResponse; agent/safety_integration_test.go; api/TestAPIAbsenceStepsDocumentsAndSensitiveCorpus |
| История, версии, подтверждения, 3.1–3.3 | storage/actions_test.go и attempts_test.go: client_message_id, конкурентная правка, двойной клик, TTL, отказ записи job |
| Доставка, сверка, атомарность, 3.4–3.6 | actions/worker_test.go; storage/worker_test.go: lease после рестарта, расписание пяти проверок, поздний worker, unknown, закрытый ключ, ручные клики, TTL и новое подтверждение, сбой транзакции, монотонные версии |
| Контракты доступа и панель, 4.1–4.3, 4.5 | mocks/service_test.go и panel_test.go; tools/TestClosedSchemasAndConfirmationContext, api/TestReadToolsUseAuthoritativeIdentityAndClosedSchemas; integrations/client_test.go |
| A-1–A-8: ресурс, право, предварительная проверка, рекомендации, уровень и обоснование, 4.4 | api/TestAccessSelectionBranches: своя команда, синоним, неоднозначность, скрытый/закрытый ресурс, несколько уровней, явный выбор и «не знаю», admin, ресурс по задаче, продолжение/отказ, сбой предварительной проверки |
| A-9–A-12: правка, подтверждение, исход и проекция | api/TestBusinessAccessLifecycleAndReload; storage/attempts_test.go, worker_test.go; браузерный acceptance (две вкладки, created → approved → P6/P7) |
| A-13: отмена | mocks/TestCancelVersionsAndTerminalState; api/TestAPIAbsenceStepsDocumentsAndSensitiveCorpus; карточка отмены проверяется тем же worker |
| A-14: повтор после отказа | api/TestRejectionNeedsExplicitContinuationAndNewJustification |
| A-15/A-16: выданный доступ, сбои, кнопочная альтернатива | api/TestAccessSelectionBranches; mocks/TestAccessSchemaAndRules; integrations/TestTypedOutcomesAndErrorMinimization; tools.Validate; frontend/App.test.tsx; браузерный resilience |
| План, сессия UI и шаги, 5.1–5.4 | onboarding/plan_test.go; api/TestStepOwnerProjectionTTLAndDuplicateConfirmation; frontend/App.test.tsx и api/client.test.ts: Zod, поздний ответ, ожидание, формы, версия, отметка и возврат шага |
| Сквозной доступ, 5.5 | acceptance.mjs и resilience.mjs: реальный Chromium → собранный nginx/API → HTTP-имитация → PostgreSQL; проверка номера, approved/P7 и одного объекта после потери ответа |
| Поиск, LLM-контракт, диалог, 6.1–6.5 | agent/answer_test.go и provider_test.go; api/TestDialogueQueueTopicChangeAndExplicitConfirm, TestDialogueClarificationsAndButtons; безопасные источники в acceptance.mjs |
| Календарь, отгул и перенос, 7.1–7.5 | platform/calendar/calendar_test.go; mocks/time_off_test.go; api/TestTransferOrdersUnknownOutcomeAndDecline; frontend/App.test.tsx; acceptance.mjs: submitted, approved, две карточки переноса, отмена, reload |
| Полный сброс, 7.6 | reset.mjs: реальный unknown + детерминированное queued после остановки worker, частичный сбой, все обработчики остановлены, повтор, отсутствие старых jobs/ключей и поздних операций, старая сессия отклонена |
| Провайдер и метрики, 8.1–8.3 | agent/openrouter_test.go: fake HTTP, авторизация, JSON, ошибки и таймаут; storage/TestMetricsUseLatestAttemptAndExcludeRawData, TestMetricsDeduplicateSourceAndSavedAction, TestObjectObservationUsesOperationalClockAndIsStable |
| Приёмка, 9.1–9.6 | make coverage; acceptance/performance/resilience/reset JSON; PDF и видео; проверка ZIP. 9.7 требует решения человека |

Пути Go в таблице относительны backend/internal/, кроме явно указанных platform и mocks. Таблица показывает сочетание доменных, контрактных, интеграционных и браузерных проверок; она не означает отдельный браузерный прогон каждой ветки.

## Браузерный стенд

Выделенный проект `ai-buddy-acceptance-20261114`, frontend :15173, API :18080, имитации :18081. Основной проект не сбрасывается. Временный env-файл содержит только синтетические секреты; его нет в комплекте. Создать свой файл на основе .env.example, задать разные SERVICE_SECRET и DEMO_ADMIN_SECRET и fake.

```bash
docker compose -p ai-buddy-acceptance-20261114 --env-file /private/tmp/buddy-acceptance.env -f compose.yaml -f infra/acceptance.compose.yaml up -d --build --wait
DEMO_RESET_PROJECT=ai-buddy-acceptance-20261114 DEMO_RESET_ACCEPTANCE=1 DEMO_RESET_ENV_FILE=/private/tmp/buddy-acceptance.env ./scripts/demo-reset.sh demo
BUDDY_RECORD=1 node frontend/test/e2e/acceptance.mjs
node frontend/test/e2e/resilience.mjs
node frontend/test/e2e/cancel-access.mjs
node frontend/test/e2e/materials-access.mjs
node frontend/test/e2e/restart.mjs
node frontend/test/e2e/layout.mjs
node frontend/test/e2e/reset.mjs
```

После acceptance доступны выданный доступ и отгулы; resilience использует другие ресурсы. reset удаляет результаты обоих прогонов. Перед повторным acceptance требуется полный сброс. cancel-access использует заявку monitoring после resilience, materials-access использует свободный tracker-pay; их повтор требует сброса или другого свободного ресурса. Видео Playwright записывается в output/demo при BUDDY_RECORD=1; несколько страниц создают разные webm. В комплект включён основной прогон в MP4, без звука; воспроизведение замедлено вдвое для чтения.

## Профиль измерений

Локальный macOS/arm64, Docker Engine 28.3.3, один Compose-проект: Go 1.27.0, PostgreSQL 16.6, Redis 7.4.1, nginx 1.28.0; браузер Chromium Playwright 1.62.1, 1440×1000. Пять запросов прогрева, затем 100 последовательных запросов каждого вида, один пользователь и одна сессия. p95 — 95-е значение в отсортированных 100. Локальный план, история и карточка не обращаются к LLM или сервису. Профиль не является нагрузочным тестом production или прогнозом для удалённой сети.

resilience проверяет интерфейс при двухсекундной задержке реального HTTP-вызова имитации. Таймаут и поздний ответ LLM проверяются управляемым fake-провайдером в тестах агента; пользовательский ввод при ожидании проверяется компонентными тестами. Реальная задержка выбранной OpenRouter-модели пока не измерялась.

## Приёмка человеком

Следовать трём сценариям README, проверить понятность карточек и итогов, источники, две команды и поведение при сбое. Зафиксировать решение по задаче 9.7 и режим LLM. До этого change не архивируется. Коммиты, push, выдачу доступа жюри и загрузку на платформу выполняет человек.

## Дополнительный change: redesign-buddy-experience

Доказательства редизайна собраны в [отдельном отчёте](redesign/README.md): транзакционный журнал, безопасная проекция, API, корреляция, UI, маскот, карта, мобильные состояния и актуальное покрытие. Старые результаты выше относятся к исходному прототипу. Приёмка редизайна и ручная проверка скринридером отмечаются отдельно.
