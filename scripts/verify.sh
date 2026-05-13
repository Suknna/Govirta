#!/bin/sh
set -eu

unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
  printf 'gofmt required for:\n%s\n' "$unformatted" >&2
  exit 1
fi

go test ./...
go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl
