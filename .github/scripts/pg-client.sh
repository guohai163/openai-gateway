#!/bin/sh
set -eu

# Use the same client major as the test server and production image.
exec docker run --rm --network host \
  -e PGHOST -e PGPORT -e PGDATABASE -e PGUSER -e PGPASSWORD \
  -e PGCONNECT_TIMEOUT -e PGSSLMODE \
  -v "${TMPDIR:-/tmp}:${TMPDIR:-/tmp}:ro" \
  postgres:17-alpine "$(basename "$0")" "$@"
