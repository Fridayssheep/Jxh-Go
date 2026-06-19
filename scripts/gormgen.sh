#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

if [[ -z "${JXH_GORMGEN_DSN:-}" ]]; then
  echo "JXH_GORMGEN_DSN is required" >&2
  echo "example: user:pwd@tcp(localhost:3306)/jxh_bot?charset=utf8mb4&parseTime=True&loc=Local" >&2
  exit 1
fi

gentool \
  -db mysql \
  -dsn "${JXH_GORMGEN_DSN}" \
  -tables "knowledge_entries,knowledge_import_runs,admins,blacklists,scheduled_jobs,processed_events" \
  -outPath "internal/storage/query" \
  -fieldNullable \
  -fieldWithIndexTag \
  -fieldWithTypeTag \
  -fieldSignable
