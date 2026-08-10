#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VERSION_FILE="$ROOT/VERSION"
VERSION=$(tr -d '[:space:]' < "$VERSION_FILE")
case "$VERSION" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  [0-9]*.[0-9]*.[0-9]*) VERSION="v$VERSION" ;;
  *) echo "invalid semver in VERSION: $VERSION" >&2; exit 1 ;;
esac
if [ "${1:-}" != "--check" ]; then
  echo "release version: $VERSION"
  echo "create the annotated tag explicitly: git tag -a $VERSION -m \"VKVM $VERSION\""
fi
test -s "$ROOT/RELEASE_NOTES.md"
grep -q "$VERSION" "$ROOT/RELEASE_NOTES.md" || grep -q "${VERSION#v}" "$ROOT/RELEASE_NOTES.md"
echo "release metadata verified"
