#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ -e .env ]]; then echo '.env уже существует; его значения сохранены.'; exit 0; fi
umask 077
service_secret="$(openssl rand -hex 32)"
presenter_secret="$(openssl rand -hex 32)"
config_tmp="$(mktemp "${TMPDIR:-/private/tmp}/buddy-env.XXXXXX")"
trap 'rm -f "$config_tmp"' EXIT
awk -v service="$service_secret" -v presenter="$presenter_secret" '
 /^SERVICE_SECRET=/ {print "SERVICE_SECRET="service;next}
 /^DEMO_ADMIN_SECRET=/ {print "DEMO_ADMIN_SECRET="presenter;next}
 {print}
' .env.example > "$config_tmp"
# noclobber also protects a file created concurrently after the existence check.
(set -o noclobber; cat "$config_tmp" > .env)
echo 'Создан локальный .env с отдельными секретами API/имитаций и ведущего. Ключ LLM не задан.'
