#!/usr/bin/env bash
# Copyright 2026 Srikanth K.
# SPDX-License-Identifier: AGPL-3.0-or-later

# Bazel's workspace status command (--config=release, see .bazelrc): the
# values stamped into the server binary through the x_defs on //:freebusy.
# STABLE_ keys rebuild what uses them when they change.
#
#   STABLE_BUILD_VERSION  the release tag (RELEASE_TAG, else git describe)
#   STABLE_BUILD_COMMIT   the commit, with "-dirty" for uncommitted changes
set -euo pipefail

commit=$(git rev-parse HEAD 2>/dev/null || echo unknown)
if [[ $commit != unknown ]] && ! git diff --quiet HEAD -- 2>/dev/null; then
  commit="$commit-dirty"
fi
version=${RELEASE_TAG:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}

echo "STABLE_BUILD_VERSION $version"
echo "STABLE_BUILD_COMMIT $commit"
