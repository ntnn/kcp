#!/usr/bin/env bash

# Copyright 2025 The KCP Authors.
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

# Run kubernetes conformance tests for kcp using kubetest2.
#
# The script takes two flags:
#   --skip-install  Skips installing kubetest2 et al
#   --skip-kcp      Skips starting kcp.
#
# Additionally the following environment variables affect the behavior of the script:
#   ARTIFACTS
#       Directory where kubetest2 stores test artifacts. Defaults to ./artifacts
#   KUBECONFIG
#       If kcp should not be started by the script, this variable must
#       be set to a kubeconfig that can access a kcp workspace.
#
# kubetest2: https://github.com/kubernetes-sigs/kubetest2

# go_install installs a Go binary into the tools directory.
#
# Since the kubetest2 project does not release binaries we cannot use
# uget and have to build from source.
go_install() {
    # TODO install into hack/tools

    # kubetest2
    go install sigs.k8s.io/kubetest2@latest
    # kubetest2-noop -> preexisting cluster
    go install sigs.k8s.io/kubetest2/kubetest2-noop@latest
    # kubetest2-tester-ginkgo -> run kube ginkgo tests
    go install sigs.k8s.io/kubetest2/kubetest2-tester-ginkgo@latest

    # TODO update PATH with hack/tools
}

kcp_pid=0
start_kcp() {
    go run ./cmd/kcp start --bind-address=127.0.0.1 2>&1 > kcp-output.log &
    kcp_pid=$!
    trap "kill -KILL $kcp_pid" EXIT

    while ! curl --insecure --fail --silent 'https://127.0.0.1:6443/readyz' > /dev/null; do
        echo "Waiting for kcp to be ready..."
        sleep 1
    done

    export KUBECONFIG="$(realpath .kcp/admin.kubeconfig)"
}

conformance() {
    if [[ -z "$KUBECONFIG" ]]; then
        echo "KUBECONFIG is not set. Please start kcp first and set the KUBECONFIG."
        return 1
    fi

    (
        cp .kcp/admin.kubeconfig conformance.kubeconfig
        export KUBECONFIG="$(realpath conformance.kubeconfig)"
        ( cd staging/src/github.com/kcp-dev/cli \
            && go run ./cmd/kubectl-create-workspace --enter --ignore-existing conformance \
        )

        export GOMOD="$(dirname $(realpath $0))/conformance.gomod"

        # TODO use a separate go.mod and go.sum to prevent errors between kcp deps and kubetest2 runner deps.
        # TODO specify test package version

        local flags=(
            # The ginkgo tester runs the kubernetes e2e tests.
            --test=ginkgo

            --artifacts="$(realpath "${ARTIFACTS:-./artifacts}")"
            --rundir-in-artifacts
        )

        # deployer flags should be empty kubetest2 does not deploy the instance
        local deployer_flags=()

        local tester_flags=(
            # Only run conformance tests.
            --focus-regex='\[Conformance\]'
            # conformance tests to skip
            #   sig-apps -> kcp does not do compute
            #   sig-autoscaling -> kcp does not do compute
            #   sig-storage -> kcp does not do storage
            --skip-regex='\[sig-apps\]|\[sig-autoscaling\]|\[sig-storage\]'

            # Arguments to pass to the kubernetes e2e test framework:
            #   https://godoc.org/k8s.io/kubernetes/test/e2e/framework#TestContextType
            #   https://github.com/kubernetes/kubernetes/blob/ab1a54911670bbc1792062c5ed5fbcc3df9dfcb1/test/e2e/framework/test_context.go#L320
            #
            #   allowed-not-ready-nodes=-1 to disable the node readiness check
            #   num-nodes
            #
            --test-args='-allowed-not-ready-nodes=-1 -num-nodes=0'
        )

        # TODO install a fake service CRD. the conformance tests grab
        # the cluster IP to cache it globally because /some/ tests need it.
        # https://github.com/kubernetes/kubernetes/blob/ab1a54911670bbc1792062c5ed5fbcc3df9dfcb1/test/e2e/e2e.go#L113-L129

        kubetest2 noop "${flags[@]}" "${deployer_flags[@]}" -- "${tester_flags[@]}"
    )
}

main() {
    local skip_install=false
    local skip_kcp=false

    while [[ "$#" -gt 0 ]]; do
        case "$1" in
            --skip-install) skip_install=true ;;
            --skip-kcp) skip_kcp=true ;;
            *) echo "Unknown parameter passed: $1"; exit 1 ;;
        esac
        shift
    done

    [[ "$skip_install" = false ]] && go_install
    [[ "$skip_kcp" = false ]] && start_kcp
    set -x
    conformance
}

main "$@"
