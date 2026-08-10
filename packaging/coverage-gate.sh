#!/bin/sh
set -eu

# The release gate measures the deterministic, cross-platform data plane. OS
# hooks, CoreGraphics/DDC drivers, UI handlers and live multicast sockets are
# covered by platform/integration smoke tests instead of being counted as
# unreachable unit statements on the developer host.
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
PROFILE=${1:-"${TMPDIR:-/tmp}/vkvm-coverage.out"}
GOCACHE=${GOCACHE:-${TMPDIR:-/tmp}/vkvm-go-build-cache}
export GOCACHE
cd "$ROOT"
go test -count=1 -covermode=atomic -coverprofile="$PROFILE" \
  ./internal/input ./internal/hid ./internal/protocol ./internal/security

awk '
NR == 1 { next }
{
  file = $1
  keep = index(file, "internal/input/calibration.go") ||
         index(file, "internal/input/event_buffer.go") ||
         index(file, "internal/input/injection_queue.go") ||
         index(file, "internal/input/metrics.go") ||
         index(file, "internal/input/state.go") ||
         index(file, "internal/input/types.go") ||
         index(file, "internal/hid/reports.go") ||
         index(file, "internal/protocol/") ||
         index(file, "internal/security/")
  if (keep) {
    total += $2
    if ($3 > 0) covered += $2
  }
}
END {
  if (total == 0) {
    print "coverage gate: no selected statements" > "/dev/stderr"
    exit 2
  }
  percent = 100 * covered / total
  printf "selected core coverage: %.2f%% (%d/%d)\n", percent, covered, total
  if (percent < 80) exit 1
}
' "$PROFILE"
