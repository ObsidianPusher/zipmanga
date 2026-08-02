#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CACHE_DIR="${ROOT_DIR}/.cache"
GO_CACHE_DIR="${CACHE_DIR}/go-build"
GO_MOD_CACHE_DIR="${CACHE_DIR}/go-mod"
GO_PATH_DIR="${CACHE_DIR}/gopath"
TARGET_OS="${TARGET_OS:-linux}"
TARGET_ARCH="${TARGET_ARCH:-arm64}"
OUTPUT_NAME="${OUTPUT_NAME:-zipmanga-${TARGET_OS}-${TARGET_ARCH}-musl}"

if [ "${TARGET_OS}" != "linux" ] || [ "${TARGET_ARCH}" != "arm64" ]; then
  echo "error: build-musl.sh only supports linux/arm64" >&2
  exit 1
fi

mkdir -p "${GO_CACHE_DIR}" "${GO_MOD_CACHE_DIR}" "${GO_PATH_DIR}"

export GOCACHE="${GO_CACHE_DIR}"
export GOMODCACHE="${GO_MOD_CACHE_DIR}"
export GOPATH="${GO_PATH_DIR}"
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export GOSUMDB="${GOSUMDB:-sum.golang.google.cn}"

TARGET_OS="${TARGET_OS}" \
TARGET_ARCH="${TARGET_ARCH}" \
OUTPUT_NAME="${OUTPUT_NAME}" \
RUN_TESTS="${RUN_TESTS:-1}" \
"${ROOT_DIR}/build-simple.sh"
