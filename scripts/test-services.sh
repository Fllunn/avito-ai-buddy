#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
# Unique isolated project; never reuse or reset the user's demonstration volumes.
project="ai-buddy-test-$$"
compose=(docker compose -p "$project" -f infra/test.compose.yaml)
cleanup() { "${compose[@]}" down --volumes >/dev/null; }
trap cleanup EXIT
"${compose[@]}" up -d --wait --wait-timeout 45
address="$("${compose[@]}" port postgres 5432)"
export TEST_API_DATABASE_URL="postgres://buddy_api:synthetic-test-api@$address/buddy_api?sslmode=disable"
export TEST_MOCKS_DATABASE_URL="postgres://buddy_mocks:synthetic-test-mocks@$address/buddy_mocks?sslmode=disable"
export TEST_REDIS_ADDR="$("${compose[@]}" port redis 6379)"
export GOCACHE="${GOCACHE:-/private/tmp/ai-buddy-go-cache}"
"$@"
