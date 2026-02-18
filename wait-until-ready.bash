#!/usr/bin/env bash

log() {
    echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] $*"
}

while test -f wait-for-build; do
    sleep 1
done

_ready() {
    curl --insecure --silent --fail "https://localhost:6443/readyz"
}

log starting ready check
while [[ "$(_ready)" != "ok" ]]; do
    echo "Waiting for API server to be ready..."
    sleep 1
done
log readyz returned ok

while ! KUBECONFIG=.kcp/admin.kubeconfig kubectl ws tree; do
    echo "Waiting for API server to be responsive..."
    sleep 1
done
log API server is responsive
