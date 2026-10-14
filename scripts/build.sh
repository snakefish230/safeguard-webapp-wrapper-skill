#!/usr/bin/env bash
# Build Windows wrapper exe(s).  Usage: scripts/build.sh <site> [<site>...] | all
# Output: dist/<site>.exe (+ .sha256).  Requires Go >= 1.22 on PATH.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT/wrapper"
command -v go >/dev/null || export PATH="/opt/data/tools/bin:$PATH"
[ $# -ge 1 ] || { echo "usage: $0 <site>...|all"; ls sites | grep -v '^_'; exit 1; }
sites=("$@"); [ "$1" = all ] && mapfile -t sites < <(ls sites | grep -v '^_')
go mod tidy
go vet ./core/... "${sites[@]/#/./sites/}"
go test ./core/...
mkdir -p "$ROOT/dist"
VER="$(git -C "$ROOT" describe --always --dirty 2>/dev/null || echo dev)"
for s in "${sites[@]}"; do
  out="$ROOT/dist/$s.exe"
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=$VER" -o "$out" "./sites/$s"
  (cd "$ROOT/dist" && sha256sum "$s.exe" > "$s.exe.sha256")
  echo "built $out ($(du -h "$out" | cut -f1))"
done
