#!/bin/sh
set -eu

unformatted="$(find . -name '*.go' -not -path './.lima/*' -not -path './.worktrees/*' -not -path './.tmp/*' -not -path './vendor/*' | xargs gofmt -l)"
if [ -n "$unformatted" ]; then
  printf 'gofmt required for:\n%s\n' "$unformatted" >&2
  exit 1
fi

go test ./...
go build ./cmd/govirtad ./cmd/govirtlet ./cmd/govirtctl
