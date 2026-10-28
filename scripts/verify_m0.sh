#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
api_pid=""
web_pid=""

cleanup() {
  [ -z "$web_pid" ] || kill "$web_pid" 2>/dev/null || true
  [ -z "$api_pid" ] || kill "$api_pid" 2>/dev/null || true
  wait "$web_pid" 2>/dev/null || true
  wait "$api_pid" 2>/dev/null || true
  rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

wait_for() {
  local url="$1" label="$2"
  for _ in $(seq 1 60); do
    if curl --fail --silent --show-error "$url" >"$tmp/$label.json" 2>/dev/null; then
      return 0
    fi
    sleep 0.25
  done
  printf '%s did not become healthy\n' "$label" >&2
  return 1
}

cd "$root"
API_DEPENDENCIES=postgresql,redis \
API_POSTGRESQL_ADDR=127.0.0.1:5432 \
API_REDIS_ADDR=127.0.0.1:6379 \
go run ./apps/api/cmd/api >"$tmp/api.log" 2>&1 &
api_pid=$!

pnpm --dir apps/web start --hostname 127.0.0.1 --port 3000 >"$tmp/web.log" 2>&1 &
web_pid=$!

wait_for http://127.0.0.1:8080/health/live api-live
wait_for http://127.0.0.1:8080/health/ready api-ready
wait_for http://127.0.0.1:3000/api/health web-health

expected_live='{"status":"live"}'
expected_ready='{"status":"ready","checks":{"postgresql":{"status":"ok"},"process":{"status":"ok"},"redis":{"status":"ok"}}}'
expected_web='{"status":"ok","service":"cinema-web","milestone":"M0","checks":{"runtime":"ok"}}'

[ "$(tr -d '\n' <"$tmp/api-live.json")" = "$expected_live" ] || { cat "$tmp/api-live.json"; exit 1; }
[ "$(tr -d '\n' <"$tmp/api-ready.json")" = "$expected_ready" ] || { cat "$tmp/api-ready.json"; exit 1; }
[ "$(tr -d '\n' <"$tmp/web-health.json")" = "$expected_web" ] || { cat "$tmp/web-health.json"; exit 1; }

printf 'M0 smoke verification passed: API live/ready and web health are exact.\n'
