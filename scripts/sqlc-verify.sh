#!/usr/bin/env bash
set -euo pipefail

# Ensure ~/go/bin is on PATH (where 'go install' places binaries).
export PATH="${HOME}/go/bin:${PATH}"

# Use installed sqlc binary if available; otherwise fall back to go run.
if command -v sqlc >/dev/null 2>&1; then
  sqlc generate
else
  echo "::error::sqlc binary not found; install with: go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.0"
  exit 1
fi

if ! git diff --quiet internal/lineage/queries/ sqlc.yaml; then
  echo "::error::sqlc-generated files are out of sync with lineage.sql; run 'make sqlc' and commit."
  git --no-pager diff internal/lineage/queries/ sqlc.yaml
  exit 1
fi
echo "sqlc generated files are in sync."
