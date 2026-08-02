#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT_DIR="${ROOT_DIR}/dist"
TARGET_OS="${TARGET_OS:-linux}"
TARGET_ARCH="${TARGET_ARCH:-arm64}"
OUTPUT_NAME="${OUTPUT_NAME:-zipmanga-${TARGET_OS}-${TARGET_ARCH}}"
OUTPUT_PATH="${OUT_DIR}/${OUTPUT_NAME}"
STAGING_DIR="${OUT_DIR}/.${OUTPUT_NAME}.staging"
STAGING_BIN="${STAGING_DIR}/${OUTPUT_NAME}"
RUN_TESTS="${RUN_TESTS:-1}"

SOURCE_INPUT_PATHS=(
  "${ROOT_DIR}/cmd"
  "${ROOT_DIR}/internal"
  "${ROOT_DIR}/web"
  "${ROOT_DIR}/go.mod"
  "${ROOT_DIR}/go.sum"
)
WEB_ASSET_PATHS=(
  "${ROOT_DIR}/web/templates"
  "${ROOT_DIR}/web/static"
)

hash_stdin() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | awk '{print $1}'
    return
  fi
  shasum -a 256 | awk '{print $1}'
}

hash_file() {
  local file_path="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${file_path}" | awk '{print $1}'
    return
  fi
  shasum -a 256 "${file_path}" | awk '{print $1}'
}

build_tree_hash() {
  local target file
  while IFS= read -r target; do
    if [ -d "${target}" ]; then
      find "${target}" -type f ! -name '.DS_Store' ! -path '*/.DS_Store' | LC_ALL=C sort
    elif [ -f "${target}" ]; then
      if [ "$(basename "${target}")" = ".DS_Store" ]; then
        continue
      fi
      printf '%s\n' "${target}"
    fi
  done < <(printf '%s\n' "$@" | LC_ALL=C sort) | while IFS= read -r file; do
    [ -f "${file}" ] || continue
    printf '%s  %s\n' "$(hash_file "${file}")" "${file#${ROOT_DIR}/}"
  done | hash_stdin
}

detect_commit() {
  if command -v git >/dev/null 2>&1 && git -C "${ROOT_DIR}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    git -C "${ROOT_DIR}" rev-parse --short=12 HEAD
    return
  fi
  printf 'nogit\n'
}

host_goos() {
  go env GOHOSTOS
}

host_goarch() {
  go env GOHOSTARCH
}

verify_binary_metadata() {
  local binary_path="$1"
  local expected_source_sha="$2"
  local expected_web_sha="$3"

  if [ "${TARGET_OS}" = "$(host_goos)" ] && [ "${TARGET_ARCH}" = "$(host_goarch)" ]; then
    local version_output
    version_output="$("${binary_path}" version)"
    printf '%s\n' "${version_output}" | grep -F "source_input_sha=${expected_source_sha}" >/dev/null
    printf '%s\n' "${version_output}" | grep -F "web_asset_sha=${expected_web_sha}" >/dev/null
    return
  fi

  local binary_strings
  binary_strings="$(strings "${binary_path}")"
  printf '%s\n' "${binary_strings}" | grep -F "source_input_sha" >/dev/null
  printf '%s\n' "${binary_strings}" | grep -F "web_asset_sha" >/dev/null
  printf '%s\n' "${binary_strings}" | grep -F "${expected_source_sha}" >/dev/null
  printf '%s\n' "${binary_strings}" | grep -F "${expected_web_sha}" >/dev/null
}

mkdir -p "${OUT_DIR}"
rm -rf "${STAGING_DIR}"
mkdir -p "${STAGING_DIR}"

SOURCE_INPUT_SHA_BEFORE="$(build_tree_hash "${SOURCE_INPUT_PATHS[@]}")"
WEB_ASSET_SHA_BEFORE="$(build_tree_hash "${WEB_ASSET_PATHS[@]}")"
COMMIT_SHA="$(detect_commit)"
BUILD_TIME_UTC="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

echo "downloading modules..."
go mod download

echo "verifying modules..."
go mod verify

if [ "${RUN_TESTS}" = "1" ]; then
  echo "running tests..."
  go test ./...
fi

echo "building ${OUTPUT_PATH} ..."
CGO_ENABLED=0 \
GOOS="${TARGET_OS}" \
GOARCH="${TARGET_ARCH}" \
GOFLAGS="${GOFLAGS:-} -mod=readonly -trimpath -buildvcs=true" \
go build \
  -ldflags="-s -w -X zipmanga/internal/buildinfo.Version=${OUTPUT_NAME} -X zipmanga/internal/buildinfo.Commit=${COMMIT_SHA} -X zipmanga/internal/buildinfo.BuildTime=${BUILD_TIME_UTC} -X zipmanga/internal/buildinfo.SourceInputSHA=${SOURCE_INPUT_SHA_BEFORE} -X zipmanga/internal/buildinfo.WebAssetSHA=${WEB_ASSET_SHA_BEFORE}" \
  -o "${STAGING_BIN}" \
  ./cmd/zipmanga

SOURCE_INPUT_SHA_AFTER="$(build_tree_hash "${SOURCE_INPUT_PATHS[@]}")"
WEB_ASSET_SHA_AFTER="$(build_tree_hash "${WEB_ASSET_PATHS[@]}")"

if [ "${SOURCE_INPUT_SHA_BEFORE}" != "${SOURCE_INPUT_SHA_AFTER}" ] || [ "${WEB_ASSET_SHA_BEFORE}" != "${WEB_ASSET_SHA_AFTER}" ]; then
  echo "error: source tree changed during build; refusing to publish a non-deterministic artifact" >&2
  rm -rf "${STAGING_DIR}"
  exit 1
fi

verify_binary_metadata "${STAGING_BIN}" "${SOURCE_INPUT_SHA_BEFORE}" "${WEB_ASSET_SHA_BEFORE}"

if command -v file >/dev/null 2>&1; then
  file "${STAGING_BIN}" || true
fi

cat > "${STAGING_DIR}/${OUTPUT_NAME}.buildinfo" <<EOF
version=${OUTPUT_NAME}
commit=${COMMIT_SHA}
build_time=${BUILD_TIME_UTC}
source_input_sha=${SOURCE_INPUT_SHA_BEFORE}
web_asset_sha=${WEB_ASSET_SHA_BEFORE}
recommended_run=${OUTPUT_NAME} --web-dir=
EOF

cat > "${STAGING_DIR}/${OUTPUT_NAME}.run-embedded.sh" <<EOF
#!/usr/bin/env sh
set -eu
SELF_DIR=\$(CDPATH= cd -- "\$(dirname -- "\$0")" && pwd)
exec "\${SELF_DIR}/${OUTPUT_NAME}" --web-dir= "\$@"
EOF
chmod +x "${STAGING_DIR}/${OUTPUT_NAME}.run-embedded.sh"

mv -f "${STAGING_BIN}" "${OUTPUT_PATH}"
mv -f "${STAGING_DIR}/${OUTPUT_NAME}.buildinfo" "${OUTPUT_PATH}.buildinfo"
mv -f "${STAGING_DIR}/${OUTPUT_NAME}.run-embedded.sh" "${OUTPUT_PATH}.run-embedded.sh"
rm -rf "${STAGING_DIR}"

echo "build ok: ${OUTPUT_PATH}"
echo "metadata: ${OUTPUT_PATH}.buildinfo"
echo "embedded launcher: ${OUTPUT_PATH}.run-embedded.sh"
