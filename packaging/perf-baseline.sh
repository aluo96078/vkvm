#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT=${1:-"$ROOT/performance-baseline.txt"}
GOCACHE=${GOCACHE:-${TMPDIR:-/tmp}/vkvm-go-build-cache}
export GOCACHE
{
  echo "VKVM performance baseline"
  echo "version=$(tr -d '[:space:]' < "$ROOT/VERSION")"
  echo "go=$(go version)"
  echo "platform=$(go env GOOS)/$(go env GOARCH)"
  echo "captured=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  (cd "$ROOT" && go test ./internal/input -run '^$' -bench 'Benchmark(InputMetricsPayload|InjectionQueueSubmit)$' -benchtime=1s -count=3)
} > "$OUT"
chmod 0600 "$OUT"
echo "wrote $OUT"
