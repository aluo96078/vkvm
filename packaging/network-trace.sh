#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
MODE=${1:-portable}
GOCACHE=${GOCACHE:-${TMPDIR:-/tmp}/vkvm-go-build-cache}
export GOCACHE
cd "$ROOT"
go test ./internal/network -run 'CoordinatorTrace|PacketTrace|WaitsUntilFingerprintChanges' -count=1
if [ "$MODE" = "portable" ]; then
  echo "network fingerprint/packet trace evidence checks passed"
  exit 0
fi
if [ "$MODE" != "real" ]; then
  echo "usage: $0 [portable|real]" >&2
  exit 2
fi
state_trace=${VKVM_COORDINATOR_TRACE:-}
packet_trace=${VKVM_PACKET_TRACE:-}
if [ -z "$state_trace" ] || [ ! -s "$state_trace" ]; then
  echo "real coordinator trace is missing; set VKVM_COORDINATOR_TRACE" >&2
  exit 1
fi
if [ -z "$packet_trace" ] || [ ! -s "$packet_trace" ]; then
  echo "real packet trace is missing; set VKVM_PACKET_TRACE" >&2
  exit 1
fi
grep -q 'state' "$state_trace"
grep -q 'direction' "$packet_trace"
echo "network fingerprint/packet trace evidence checks passed"
