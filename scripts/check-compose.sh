#!/bin/sh
set -eu
cd "$(dirname "$0")/.."

# This project owns only disposable synthetic test data and publishes no ports.
compose() {
  docker compose --env-file .env.example -p ai-buddy-infra-check \
    -f compose.yaml -f infra/compose.check.yaml "$@"
}
cleanup() {
  compose down --volumes --remove-orphans >/dev/null 2>&1
}
trap cleanup EXIT HUP INT TERM

compose config --quiet
compose up -d --build --wait --wait-timeout 180

permissions=$(compose exec -T postgres psql -U postgres -d postgres -Atqc \
  "SELECT has_database_privilege('buddy_api','buddy_mocks','CONNECT'), has_database_privilege('buddy_mocks','buddy_api','CONNECT'), rolsuper, rolcreatedb FROM pg_roles WHERE rolname='buddy_api'")
[ "$permissions" = 'f|f|f|f' ] || { echo 'Database isolation check failed'; exit 1; }

compose exec -T postgres sh -c 'PGPASSWORD="$API_DB_PASSWORD" psql -h 127.0.0.1 -U buddy_api -d buddy_api -v ON_ERROR_STOP=1 -c "CREATE TABLE infra_probe (value text NOT NULL); INSERT INTO infra_probe VALUES ('\''persisted'\'');"' >/dev/null
compose exec -T redis redis-cli SET ai-buddy-infra-probe persisted >/dev/null

check_state() {
  marker=$(compose exec -T postgres sh -c 'PGPASSWORD="$API_DB_PASSWORD" psql -h 127.0.0.1 -U buddy_api -d buddy_api -Atqc "SELECT value FROM infra_probe"')
  [ "$marker" = 'persisted' ] || { echo 'PostgreSQL persistence check failed'; exit 1; }
  marker=$(compose exec -T redis redis-cli GET ai-buddy-infra-probe)
  [ "$marker" = 'persisted' ] || { echo 'Redis persistence check failed'; exit 1; }
  compose exec -T api wget -q -O /dev/null http://127.0.0.1:8080/healthz
  compose exec -T mocks wget -q -O /dev/null http://127.0.0.1:8080/healthz
  compose exec -T frontend wget -q -O /dev/null http://127.0.0.1/ui-healthz
  compose exec -T frontend sh -c 'wget -q -O - http://127.0.0.1/ | grep -q "AI-buddy"'
}

check_state
compose stop
compose up -d --wait --wait-timeout 180
check_state
compose down
compose up -d --wait --wait-timeout 180
check_state
printf '%s\n' 'Compose checks passed: isolation, health, stop/start and down/up persistence.'
