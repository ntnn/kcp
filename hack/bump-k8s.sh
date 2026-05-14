#!/usr/bin/env bash

# Copyright 2021 The kcp Authors.
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

set -o errexit
set -o nounset
set -o pipefail

# The normal flow for updating a replaced dependency would look like:
# $ go mod edit -replace old=new@branch
# $ go mod tidy
# However, we don't know all of the specific packages that we pull in from the staging repos,
# nor do we want to update this script when that set changes. Therefore, we just look up the
# version of the k8s.io/kubernetes replacement and replace all modules at that version, since
# we know we will always want to use a self-consistent set of modules from our fork.
# Note: setting GOPROXY=direct allows us to bump very quickly after the fork has been committed to.
GITHUB_USER=${GITHUB_USER:-kcp-dev}
GITHUB_REPO=${GITHUB_REPO:-kubernetes}
BRANCH=${BRANCH:-kcp-1.31.0}

main() {
    local user="$1"
    local repo="$2"
    local ref="$3"

    local reporef="github.com/$user/$repo"

    # Resolve BRANCH to a go pseudo version once with GOPROXY=direct to get the latest
    # This version is always in the form <base>.<date>-<hash>; since
    # this is the base module the base version will be based off of the
    # nearest vcs tag.
    local version="$(GOPROXY=direct go list -m "$reporef@$ref" | cut -d' ' -f2)"

    # cut off the <base> to just get the <date> and <hash>
    local date_hash="${version##*.}"

    # Replace all current k8s.io replacementes with the new version
    go mod edit -json | jq -r '.Replace[] | .Old.Path' | grep k8s.io | while read module; do
        case "$module" in
            # k8s.io/kubernetes needs the pseudo version with the nearest tag
            (k8s.io/kubernetes) go mod edit -replace "$module=$reporef@$version";;
            # all other modules need a v0.0.0-* pseudo version
            (*) go mod edit -replace "$module=$reporef/staging/src/$module@v0.0.0-$date_hash";;
        esac
    done

    # Update go.sum
    go mod tidy
}

main "$GITHUB_USER" "$GITHUB_REPO" "$BRANCH"
