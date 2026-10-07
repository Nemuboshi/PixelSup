#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${ROOT_DIR}/dist"
BIN_NAME="pixelsup"
ENTRY="./cmd/pixelsup"

TARGETS=(
  "linux amd64"
  "linux arm64"
  "darwin amd64"
  "darwin arm64"
  "windows amd64"
  "windows arm64"
)

mkdir -p "${DIST_DIR}"

echo "Building ${BIN_NAME} into ${DIST_DIR}"

for target in "${TARGETS[@]}"; do
  IFS=' ' read -r GOOS GOARCH <<< "${target}"

  ext=""
  if [[ "${GOOS}" == "windows" ]]; then
    ext=".exe"
  fi

  out="${DIST_DIR}/${BIN_NAME}-${GOOS}-${GOARCH}${ext}"
  echo "-> ${GOOS}/${GOARCH}"

  (
    cd "${ROOT_DIR}"
    env CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" \
      go build -trimpath -ldflags "-s -w" -o "${out}" "${ENTRY}"
  )
done

echo "Build finished."
