# Проверка личного пространства AI-buddy

Change: [redesign-buddy-experience](../../../openspec/changes/redesign-buddy-experience/tasks.md). Только синтетические данные и fake LLM. Результаты этого каталога относятся к редизайну; отчёты исходного change сохранены отдельно. Человеческая приёмка не заменяется автотестами.

## Воспроизведение

Проверки кода и покрытия:

```bash
make coverage
make build
node scripts/check-design.mjs
npx --yes @fission-ai/openspec@1.16.1 validate --all --strict
```

`make coverage` сам создаёт и удаляет отдельный PostgreSQL/Redis-стенд, запускает race detector и Vitest. Каждый модуль и суммарное покрытие должны строго превышать 80%; исключения и метод подсчёта описаны в корневом README. Реальная платная LLM не вызывается. Пакет OpenSpec 1.16.1 в текущей среде запускался из локального npm-кеша.

Для браузерных проверок нужен отдельный синтетический Compose-проект и локальный файл конфигурации на основе `.env.example` со своими случайными секретами. В выполненном прогоне файл называется `.env.redesign.local`, игнорируется git и не включается в комплект. Он сохраняется между перезапусками, его содержимое не публикуется. В нём `LLM_PROVIDER=fake`, `DEMO_NOW=2026-11-12T12:00:00+05:00`. Параметры соединения и пароли должны соответствовать начальным значениям нового тома.

```bash
docker compose -p ai-buddy-redesign-final-20261114 --env-file .env.redesign.local \
  -f compose.yaml -f infra/redesign.compose.yaml up -d --build --wait
BUDDY_ENV_FILE=.env.redesign.local node frontend/test/e2e/redesign-layout.mjs
BUDDY_ENV_FILE=.env.redesign.local node frontend/test/e2e/redesign-acceptance.mjs
BUDDY_ENV_FILE=.env.redesign.local node frontend/test/e2e/redesign-resilience.mjs
BUDDY_ENV_FILE=.env.redesign.local node frontend/test/e2e/redesign-restart.mjs
```

Порты: UI 25173, API 28080, панель 28081. PostgreSQL и Redis этого стенда не публикуются. Acceptance предполагает свежие синтетические данные: одобренную заявку нельзя повторно создать при повторном прогоне. Для чистого повторного прогона разрешено удалить тома **только данного выделенного тестового проекта** командой с теми же `-p/--env-file/-f` и `down --volumes`, затем поднять его заново. Это удаляет только его тестовую историю и заявки. Сброс основного демо-проекта для проверки не требуется.

## Доказательства

| Требования / задачи | Проверки |
|---|---|
| Иерархия, период, формы — 1.1, 5.1–5.4 | WorkspaceOverview.test, PlanItems.test, RequestForms.test, ChatMessages.test, App.test; [дизайн](../../design/buddy-experience.md) |
| Порядок, дубли, конкуренция, пагинация — 2.2–2.4 | execution/models_test, storage/executions_test; пустая и legacy-база, повтор миграции, reopen, безопасная проекция и отзыв прав |
| Корреляция и несколько дел — 3.1 | TestExecutionMultipleIntentIsolation, TestExecutionClarificationTopicAndLease, TestExecutionReplayPreservesUnsentCard |
| Фактическая модель/поиск и деградация — 3.2 | TestExecutionModelCallsAndSafeDenial, TestExecutionTimeoutAndTelemetryDegradation; кнопочный маршрут без модели |
| Подтверждения/версии/истечение — 3.3 | API business lifecycle, storage actions tests, TestExecutionFactsFollowCardAndSavedResult; до согласия число внешних мутаций 0 |
| Отправка/сверка/неизвестный исход — 3.4 | ошибка финального факта после внешнего эффекта, проверка not_created/manual_review и lease в storage/worker_test; [сбои](resilience.json), [перезапуск](restart.json) |
| Отмена/перенос и монотонность — 3.5 | TestTransferOrdersUnknownOutcomeAndDecline: оба порядка, два подтверждения, unknown и отказ от второй части; поздний created после approved в storage/worker_test |
| HTTP и совместимость — 4.1–4.2 | TestExecutionHTTPContract, legacy-миграция, повтор client_message_id и конфликт содержимого, DTO прямой формы |
| Polling и смена сессии — 4.3–4.4 | executions.test, useExecutions.test: страницы, дубли, старая revision, ожидание человека, focus, старый родитель и поздний ответ прежней сессии |
| Карта и маскот — 6.1–6.3 | ExecutionMap.test, useEventMotion.test: общая проекция, partial, unknown, skipped, мгновенный результат, refresh, stale, reduced motion; шесть локальных поз |
| Адаптация и доступность — 7.1–7.3 | [layout.json](layout.json), [контраст](contrast.json), нативные модальные диалоги, поле сообщения при высоте 450 px, отсутствие вложенного live-region карты |
| Три сценария — 8.1 | [acceptance.json](acceptance.json), [измерения локальных операций](performance.json) |
| Итоговые сборка/покрытие/OpenSpec — 8.3 | [отчёт](report.json); полные логи воспроизводимого прогона в `output/redesign/` |

## Снимки

- [Desktop](screenshots/workspace-1440.png), [1024 px](screenshots/workspace-1024.png), [mobile 390](screenshots/workspace-390.png), [320 px](screenshots/workspace-320.png).
- [План](screenshots/plan-1440.png), [мобильный план](screenshots/plan-390.png).
- [Подтверждение](screenshots/confirmation-desktop.png), [статичная мобильная карта](screenshots/route-mobile.png), [200% текста](screenshots/text-200.png).
- [Две выполненные операции переноса](screenshots/transfer-completed.png), [неизвестный исход со сверкой](screenshots/unknown-outcome.png).
- [Сравнение с исходным интерфейсом и вопросы приёмки](acceptance-review.md).

## Границы проверки

Скринридер вручную и настоящая экранная клавиатура телефона не проверялись. Проверены Chromium, системный reduced motion, клавиатура и сжатие viewport, имитирующее доступную высоту с клавиатурой. Это не заявление о полном аудите доступности. Реальный внешний LLM-провайдер в этом прогоне не использовался; timeout и отключение модели проверяются детерминированным провайдером и fake HTTP. Пользовательскую оценку удобства и человеческую приёмку следует провести отдельно.
