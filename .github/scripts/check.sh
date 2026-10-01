#!/usr/bin/env bash
set -euo pipefail

# Keep manual image releases subject to the same checks as pull requests.
unformatted=$(gofmt -l ./*.go)
if [[ -n "$unformatted" ]]; then
  printf 'Go files need gofmt:\n%s\n' "$unformatted"
  exit 1
fi

go vet ./...
go test ./...
go test -race ./...
# Avoid leaving a binary in the Docker build context.
build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT
go build -o "$build_dir/submanager" .
