#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ "${1:-}" != demo ]]; then
  echo 'Полный сброс удалит историю, сессии, заявки и ключи обоих демо-сервисов. Используй make demo-reset CONFIRM=demo.' >&2
  exit 2
fi
project="${DEMO_RESET_PROJECT:-ai-buddy}"
if [[ "$project" != ai-buddy && "$project" != ai-buddy-acceptance-* ]]; then
  echo 'Разрешён только выделенный проект AI-buddy.' >&2
  exit 2
fi
compose=(docker compose -p "$project" -f compose.yaml)
if [[ -n "${DEMO_RESET_ENV_FILE:-}" ]]; then compose+=(--env-file "$DEMO_RESET_ENV_FILE"); fi
if [[ "${DEMO_RESET_ACCEPTANCE:-}" == 1 ]]; then compose+=(-f infra/acceptance.compose.yaml); fi
# Inspect only the demo flag; never print resolved configuration with secrets.
"${compose[@]}" config --format json | python3 -c 'import json,sys; c=json.load(sys.stdin); assert all(str(c["services"][s]["environment"]["DEMO_MODE"]).lower()=="true" for s in ("api","mocks")), "Сброс разрешён только в DEMO_MODE=true"'
# No handler may run during either database reset. On any later error these
# processes stay stopped; retrying the full command is safe after partial reset.
reset_started=1
trap 'status=$?; if [[ "$reset_started" == 1 ]]; then "${compose[@]}" stop frontend api mocks >/dev/null || true; fi; exit "$status"' ERR
"${compose[@]}" stop frontend api mocks
"${compose[@]}" exec -T postgres sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d buddy_api' <<'SQL'
DROP SCHEMA IF EXISTS public CASCADE;
CREATE SCHEMA public AUTHORIZATION buddy_api;
SQL
if [[ "${DEMO_RESET_ACCEPTANCE:-}" == 1 && "${DEMO_RESET_TEST_FAIL_AFTER:-}" == api ]]; then
  echo 'Тестовый частичный сбой после сброса API.' >&2
  false
fi
"${compose[@]}" exec -T postgres sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d buddy_mocks'  <<'SQL'
DROP SCHEMA IF EXISTS public CASCADE;
CREATE SCHEMA public AUTHORIZATION buddy_mocks;
SQL
"${compose[@]}" exec -T redis redis-cli FLUSHDB
"${compose[@]}" up -d --wait --wait-timeout 60 api mocks frontend
if ! "${compose[@]}" exec -T api wget -q -O /dev/null http://127.0.0.1:8080/readyz || ! "${compose[@]}" exec -T mocks wget -q -O /dev/null http://127.0.0.1:8080/readyz; then
  "${compose[@]}" stop frontend api mocks
  echo 'Готовность не подтверждена. Стенд остановлен; повтори полный сброс после исправления причины.' >&2
  exit 1
fi
reset_started=0
echo 'Согласованный демо-сброс завершён. Данные и сессии созданы заново.'
