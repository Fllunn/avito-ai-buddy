#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
export GOCACHE="${GOCACHE:-/private/tmp/ai-buddy-go-cache}"
if [[ -z "${TEST_API_DATABASE_URL:-}" ]]; then
  exec ./scripts/test-services.sh ./scripts/coverage/run.sh
fi
mkdir -p coverage
(cd scripts/coverage && go test ./...)
for module in backend mocks; do
  (cd "$module" && go test -race -coverpkg=./... -coverprofile="../coverage/$module.out" ./...)
  (cd "$module" && go tool cover -func="../coverage/$module.out") > "coverage/$module-native.txt"
done
(cd frontend && npm run coverage)
(cd scripts/coverage && go run . ../..)
