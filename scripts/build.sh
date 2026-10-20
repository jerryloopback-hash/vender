#!/usr/bin/env bash
#
# Build vender for Linux and Windows into dist/.
#
# Usage:
#   scripts/build.sh            # both targets
#   scripts/build.sh linux      # linux/amd64 only
#   scripts/build.sh windows    # windows/amd64 only
#
# This produces the bare binaries. For distributable archives — the binaries
# plus the bundled sing-box and the licence documents — use scripts/package.sh,
# which calls this script and then stages dist/release/.
#
# Version metadata is injected with -ldflags, matching the variables in
# cmd/vender/version.go.
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"
DATE="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
LDFLAGS="-X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}"

TARGETS=("$@")
if [ "${#TARGETS[@]}" -eq 0 ]; then
	TARGETS=(linux windows)
fi

mkdir -p dist

for target in "${TARGETS[@]}"; do
	case "$target" in
	linux)
		echo "building linux/amd64 -> dist/vender"
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
			go build -trimpath -ldflags "$LDFLAGS" -o dist/vender ./cmd/vender
		;;
	windows)
		echo "building windows/amd64 -> dist/vender.exe"
		CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
			go build -trimpath -ldflags "$LDFLAGS" -o dist/vender.exe ./cmd/vender
		echo "building windows/amd64 GUI companion -> dist/vender-daemon.exe"
		CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
			go build -trimpath -ldflags "-H=windowsgui $LDFLAGS" -o dist/vender-daemon.exe ./cmd/vender-daemon
		;;
	*)
		echo "unknown target: $target (want linux or windows)" >&2
		exit 2
		;;
	esac
done

echo "done: ${VERSION} (commit ${COMMIT})"
