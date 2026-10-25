#!/usr/bin/env bash
# Copyright 2026 Srikanth K.
# SPDX-License-Identifier: AGPL-3.0-or-later

# Adds the version-dependent Artifact Hub annotations to the chart before the
# release workflow packages it:
#
#   artifacthub.io/images      the four images at this version (Artifact Hub
#                              scans them and lists them on the chart's page)
#   artifacthub.io/changes     this version's CHANGELOG.md entries, kind from
#                              the section they sit under
#   artifacthub.io/prerelease  "true" for a version with a pre-release part
#
# usage: chart-annotations.sh VERSION [CHART_DIR]   (VERSION without the v)
# Needs yq v4 (preinstalled on GitHub's Ubuntu runners).
set -euo pipefail

version="${1:?usage: chart-annotations.sh VERSION [CHART_DIR]}"
chart="${2:-deploy/helm/freebusy}/Chart.yaml"
registry="ghcr.io/shokki-engineering"

images="$(for part in freebusy freebusy-migrations freebusy-engine freebusy-connector; do
  printf -- '- name: %s\n  image: %s/%s:%s\n' "$part" "$registry" "$part" "$version"
done)"

# One change per top-level bullet of the version's section; a bullet's
# continuation lines join its description. Keep a Changelog headings map onto
# Artifact Hub kinds; anything else counts as "changed".
changes="$(awk -v h="## [$version]" '
  function flush() {
    if (d == "") return
    gsub(/\\/, "\\\\", d); gsub(/"/, "\\\"", d)
    printf "- kind: %s\n  description: \"%s\"\n", (kind == "" ? "changed" : kind), d
    d = ""
  }
  index($0, h) == 1 { on = 1; next }
  on && /^## \[/ { exit }
  !on { next }
  /^### / {
    flush()
    k = tolower($2)
    kind = (k == "added" || k == "changed" || k == "deprecated" || k == "removed" || k == "fixed" || k == "security") ? k : "changed"
    next
  }
  /^- / { flush(); d = substr($0, 3); next }
  /^[ \t]+[^ \t]/ && d != "" { line = $0; sub(/^[ \t]+/, "", line); d = d " " line; next }
  /^[ \t]*$/ { flush() }
  END { flush() }
' CHANGELOG.md)"

case "$version" in
  *-*) prerelease=true ;;
  *) prerelease=false ;;
esac

IMAGES="$images" CHANGES="$changes" PRERELEASE="$prerelease" yq -i '
  .annotations."artifacthub.io/images" = strenv(IMAGES) |
  .annotations."artifacthub.io/changes" = strenv(CHANGES) |
  .annotations."artifacthub.io/prerelease" = strenv(PRERELEASE)
' "$chart"
