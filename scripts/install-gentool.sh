#!/usr/bin/env bash
set -euo pipefail

VERSION="${1:-${GORM_GEN_TOOL_VERSION:-v0.0.2}}"
if [[ "${VERSION}" != v* ]]; then
  VERSION="v${VERSION}"
fi

echo "Installing gorm gentool ${VERSION}"
GO_BIN="${GO_BIN:-go}"
if [[ -n "${GOROOT:-}" && -x "${GOROOT}/bin/go" ]]; then
  GO_BIN="${GOROOT}/bin/go"
fi

"${GO_BIN}" install "gorm.io/gen/tools/gentool@${VERSION}"
