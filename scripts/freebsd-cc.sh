#!/bin/sh
# Clang wrapper used only by the FreeBSD/amd64 cgo cross-build.
set -eu
: "${FREEBSD_SYSROOT:?FREEBSD_SYSROOT must point to the verified FreeBSD sysroot}"
exec "${FREEBSD_CLANG:-clang}" \
  --target=x86_64-unknown-freebsd14.4 \
  --sysroot="$FREEBSD_SYSROOT" \
  -fuse-ld=lld \
  -Wno-unused-command-line-argument \
  "$@"
