#!/usr/bin/env bash
#
# Run the full M0 acceptance suite: build, cross-build, test, vet.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "==> go build ./..."
go build ./...

echo "==> GOOS=windows GOARCH=amd64 go build ./..."
GOOS=windows GOARCH=amd64 go build ./...

echo "==> go test ./..."
go test ./...

echo "==> go vet ./..."
go vet ./...

echo "all checks passed"
