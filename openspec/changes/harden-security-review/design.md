# Design

## Context

Основание — [proposal.md](proposal.md) и [отчёт ревью](../../../output/security-review/report.md). Тесты-доказательства находятся в `output/security-review/evidence/` и на текущем коде падают: `TestSecReviewSessionNeverExpiresWithFrozenClock`, `TestSecReviewModelSkipsReapplyConsent`, `TestSecReviewSanitizerBypass`.

Текущее устройство:

- **Сессии.** `api.Config.Clock` — бизнес-часы (`cfg.Clock`, при `DEMO_NOW` — `clock.Fixed`). Ими считаются и `expires_at` при входе ([session.go:161](../../../backend/internal/api/session.go)), и проверка в `authenticate` ([session.go:186](../../../backend/internal/api/session.go)). Хранилище сравнивает `expires_at > $now`.
- **Повторная подача.** Признак — константа `reapplyQuery` в `Slots.Query`. Её проверяют ветки `access_request` ([processing.go:358](../../../backend/internal/api/processing.go)) и `time_off_request` ([processing.go:511](../../../backend/internal/api/processing.go)). Легитимные источники признака:
  - кнопка формы (`/api/dialogue/actions`, `kind=button`, frontend `RequestForms.tsx`);
  - ответ «да» в стадии `reapply` (`processing.go:167-171`), который проходит через `explicitConfirmation`.
  Для доступа продолжение без признака допускается при `state.RejectedID == prior.ID && params.Justification != ""`. Обоснование при этом может прийти из ответа модели.
- **Очиститель.** `safety.Local.Clean` проверяет сырой текст регулярными выражениями. Нормализации нет, шаблон номеров допускает только пробел и дефис.
- **nginx.** `frontend/nginx.conf` не задаёт заголовков безопасности; внешний `infra/avito-buddy.nginx.conf` тоже.
- **Упаковка.** `scripts/package-submission.py` обходит каталоги через `rglob` и исключает лишнее по denylist.

## Goals / Non-Goals

**Goals:**

- закрыть SR-01, SR-02, SR-05, а также SR-03 и SR-04 в объёме proposal;
- превратить тесты-доказательства в постоянные регрессионные тесты;
- не менять схему БД, публичный API и контракты имитаций.

**Non-Goals:**

- SR-06 (роли PostgreSQL, ACL Redis, непривилегированный nginx);
- поддомен и изменения внешнего nginx;
- свёртка гомоглифов и разрядки;
- rate limiting;
- изменения деплоя;
- настоящие вызовы LLM в тестах.

## Decisions

### 1. Операционные часы сессии

`api.Config` получает поле `SessionClock clock.Clock`. Если оно `nil`, используется `clock.System{}`. Через `SessionClock` вычисляются `expires_at` в `login` и `now` в `authenticate`. `Config.Clock` остаётся бизнес-часами для `me.capabilities`, `Welcome`, карточек и правил. `app.Factory` явно передаёт `clock.System{}`.

Тесты подменяют `SessionClock` и `Clock` независимо: продвинуть операционные часы при неизменной `DEMO_NOW` → 401, и наоборот. Существующие тесты, которые полагаются на один `Clock`, получают `SessionClock`, равный тем же часам, либо адаптируются.

Альтернатива — `now()` PostgreSQL в запросе. Отклонена: она смешивает источник времени со storage и мешает детерминированным тестам. Так же поступает панель ведущего (`OperationalClock`), что делает решение согласованным с ADR 0009. Новый ADR не нужен, это уточнение ADR 0009: срок сессии — технический срок.

### 2. Происхождение признака повторной подачи

В `process` фиксируется происхождение намерений: `fromModel = true`, если они получены через `Recognizer`. Ветки `inferred`, `explicitConfirmation` и `button` дают `fromModel = false`.

Сразу после распознавания для намерений модели выполняется:

- `Slots.Query == reapplyQuery` → `Slots.Query = ""`, то есть признак не принимается;
- в ветке `access_request` условие продолжения после показанного отказа учитывает обоснование только при `!fromModel`. Обоснование из текста пользователя приходит через `mergeFollowup` (`inferred`), из формы — через `button`.

Стадия `reapply` и кнопка формы работают как прежде: подстановка `reapplyQuery` выполняется уже после проверки явного согласия. Проверка роли (`authz.ServiceAction`) идёт до этих веток и не меняется.

Альтернатива — отдельное булево поле в `DialogueState` или `Slots`. Отклонена ради минимального изменения: контракт кнопки (`slots.query`) уже используется frontend, а сохранение признака в `Slots` от модели исключается фильтром.

### 3. Нормализация в `safety.Local.Clean`

Порядок обработки:

1. проверка UTF-8 и длины (≤4000 рун) — как сейчас;
2. `norm.NFKC.String` из `golang.org/x/text/unicode/norm`;
3. удаление рун `unicode.Cf`;
4. проверка `suspicion` по нормализованному тексту;
5. маскирование `secrets` и `numbers`.

Шаблон номеров: прежний `\b(?:\d[ -]?){12,19}\b` плюс группы по четыре цифры через точку или `/` (`\b\d{4}(?:\.\d{4}){2}\.\d{1,7}\b` и аналог для `/`). Общий вариант `\d[ ./-]?` отклонён при реализации: он маскировал диапазоны дат `16.11.2026-20.11.2026`, которые разбирает календарь. Возвращается нормализованный текст. Паника внутри уже переводится `Prepare` в `Hidden`/form, это сохраняется.

`golang.org/x/text v0.24.0` уже зафиксирован в `backend/go.sum` как indirect и переносится в direct-требования. Модуль `mocks` использует `safety` через `ai-buddy/backend` и получает ту же версию. Собственная таблица NFKC отклонена как ненадёжная. Без NFKC остаётся только удаление Cf, но тогда не закрываются полноширинные цифры и совместимые формы.

Возможное следствие: обоснование доступа с невидимыми символами станет отличаться после очистки, и `tools.Validate` его отклонит (`safe.String() != params.Justification`). Это допустимо: `Prepare` очищает обоснование раньше `Validate`. Тест подтверждает, что при этом не появляется ложный `VALIDATION_ERROR`.

Корпус тестов дополняется кейсами из `evidence/secreview_safety_test.go`, проверкой ложных срабатываний (даты `16.11.2026`, коды `space-payments`, `ACC-1001`, номера версий) и полноширинными цифрами. Гомоглифы и разрядка остаются известным ограничением, это фиксируется в ADR 0008.

### 4. Заголовки frontend-nginx

На уровне `server` в `frontend/nginx.conf` добавляются директивы с `always`. Они наследуются всеми location, включая проксированные `/api/` и `/demo/docs/`:

```
Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'
X-Frame-Options: DENY
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
```

`style-src 'unsafe-inline'` нужен для атрибутов `style` в React-компонентах (`Buddy.tsx`) и для встроенного `<style>` HTML-документа из `/demo/docs/`. Встроенных скриптов нет: `index.html` после сборки подключает только модульный скрипт из `'self'`.

`connect-src 'self'` предполагает API на том же origin, как в compose и деплое. При отдельном `VITE_API_BASE_URL` политику придётся расширить; это записывается в README и в deployment. HSTS задаётся во внешнем TLS-nginx, поэтому только рекомендуется в `docs/deployment.md`.

Проверка: интеграционный тест через `docker compose` с уникальным именем проекта и `wget -S` на `/`, `/api/me`, `/demo/docs/...`. Затем существующие e2e-сценарии (`frontend/test/e2e/acceptance.mjs`) с проверкой консоли на нарушения CSP — если среда позволяет; иначе это ручная проверка в tasks.

### 5. Упаковка по allowlist

Список файлов строится из `git ls-files -z` для корневых каталогов и файлов, плюс явный список артефактов (`coverage/*`, `output/pdf/...`, `output/demo/...`). Набор корневых каталогов прежний, включая отслеживаемый `.agents`. Затем выборка проверяется по шаблонам секретов: `.env*` кроме `.env.example`, `*.pem`, `*.key`, `*.p12`, имена с `secret`, `credential`, `id_rsa`. При совпадении скрипт печатает путь и `exit 1` до создания ZIP.

Тест — `scripts/test_package_submission.py` на `unittest` из стандартной библиотеки. Он создаёт временный git-репозиторий с неотслеживаемым файлом и файлом `deploy.pem` и вызывает функции сборки списка. Тестовые и кодовые файлы с подстрокой `secret` в имени не должны давать ложное срабатывание: шаблон применяется к имени файла и исключает `*_test.go`. Это проверяется тестом на текущем дереве.

### 6. Документация

- ADR 0008 — дополнение «Нормализация и известные обходы (гомоглифы, разрядка)»;
- `docs/deployment.md` — рекомендации SR-06, HSTS и поддомен;
- `docs/architecture.md` — абзац про операционные часы сессии.

## Risks / Trade-offs

- После перехода на операционные часы активные сессии стенда со старым `expires_at` (DEMO_NOW+8h) станут недействительными при первом запросе. Пользователи демо войдут заново; данные не теряются.
- Более строгая очистка может закрывать часть сообщений с совместимыми символами. Корпус ложных срабатываний входит в приёмку.
- CSP может заблокировать незамеченный встроенный ресурс. Это смягчается проверкой штатных сценариев в корне и под префиксом до сдачи.
- `fromModel` усложняет `process`. Ветка покрывается табличным тестом всех источников намерений.

## Migration Plan

Миграций БД нет. Порядок выката: backend и mocks (общий пакет safety) → frontend-nginx → упаковка. Откат — возврат образов, данные и схема совместимы.

## Open Questions

Нет. Объём этапа согласован в чате 15.11.2026: SR-01, SR-02, SR-04 (заголовки), SR-05 и облегчённый SR-03.
