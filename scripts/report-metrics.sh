#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
compose=(docker compose)
if [[ -n "${METRICS_PROJECT:-}" ]]; then compose+=(-p "$METRICS_PROJECT"); fi
compose+=(-f compose.yaml)
if [[ -n "${METRICS_ENV_FILE:-}" ]]; then compose+=(--env-file "$METRICS_ENV_FILE"); fi
if [[ "${METRICS_ACCEPTANCE:-}" == 1 ]]; then compose+=(-f infra/acceptance.compose.yaml); fi
zone="$("${compose[@]}" config --format json | python3 -c 'import json,sys; print(json.load(sys.stdin)["services"]["api"]["environment"]["DEMO_TIMEZONE"])')"
"${compose[@]}" exec -T postgres env "PGOPTIONS=-c timezone=$zone" sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" exec psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d buddy_api -At' < backend/internal/metrics/report.sql
