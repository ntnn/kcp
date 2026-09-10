#!/usr/bin/env bash

# Copyright 2026 The kcp Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Downloads the kcp binary of a released version for the host OS/arch into
# <dest>/kcp-<version>/kcp and maintains a <dest>/current symlink pointing at
# it. Used by `make test-upgrade` to obtain the version to upgrade from.
#
# Usage: download-kcp-release.sh [version|latest] [dest-dir]

set -euo pipefail

VERSION="${1:-latest}"
DEST="${2:-bin/upgrade-from}"

if [[ "${VERSION}" == "latest" ]]; then
  VERSION="$(curl -fsSL https://api.github.com/repos/kcp-dev/kcp/releases/latest | grep '"tag_name"' | head -n1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
  if [[ -z "${VERSION}" ]]; then
    echo "error: could not determine the latest kcp release" >&2
    exit 1
  fi
fi

OS="$(go env GOOS)"
ARCH="$(go env GOARCH)"
VERSION_DIR="${DEST}/kcp-${VERSION}"

if [[ ! -x "${VERSION_DIR}/kcp" ]]; then
  echo "Downloading kcp ${VERSION} (${OS}/${ARCH}) into ${VERSION_DIR}"
  mkdir -p "${VERSION_DIR}"
  TARBALL="kcp_${VERSION#v}_${OS}_${ARCH}.tar.gz"
  URL="https://github.com/kcp-dev/kcp/releases/download/${VERSION}/${TARBALL}"
  curl -fsSL "${URL}" | tar -xz -C "${VERSION_DIR}"

  # The binary lives either at the archive root or under bin/, depending on
  # the release tooling of the version.
  if [[ ! -x "${VERSION_DIR}/kcp" ]]; then
    if [[ -x "${VERSION_DIR}/bin/kcp" ]]; then
      cp "${VERSION_DIR}/bin/kcp" "${VERSION_DIR}/kcp"
    else
      echo "error: no kcp binary found in ${TARBALL}" >&2
      exit 1
    fi
  fi
else
  echo "Using cached kcp ${VERSION} from ${VERSION_DIR}"
fi

ln -sfn "kcp-${VERSION}" "${DEST}/current"
echo "kcp ${VERSION} available at ${DEST}/current/kcp"
