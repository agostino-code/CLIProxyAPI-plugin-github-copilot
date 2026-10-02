#!/bin/sh
# Copies an already-built, natively compiled plugin library into goreleaser's
# build output path.
#
# Open-source goreleaser has no way to import a prebuilt binary directly (that
# is a GoReleaser Pro feature); it always compiles a real Go package per
# target. .goreleaser.yml builds a tiny placeholder package instead and runs
# this script as a post-build hook to substitute the real cgo c-shared
# library produced separately by `make build` on each native CI runner.
set -eu
goos=$1 goarch=$2 destination=$3 extension=$4
source="dist/$goos/$goarch/github-copilot.$extension"
test -f "$source" || { echo "missing prebuilt library: $source" >&2; exit 1; }
go run ./cmd/checklib -path "$source" -goos "$goos" -goarch "$goarch"
cp "$source" "$destination"
