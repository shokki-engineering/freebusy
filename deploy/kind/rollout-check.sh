#!/usr/bin/env bash
# Copyright 2026 Srikanth K.
# SPDX-License-Identifier: AGPL-3.0-or-later

# Rolling restart under traffic: a Pod calls a read API through the Service
# every 100 ms while the Deployment restarts, and the check fails if any call
# did not answer 200. It is what preStopSleepSeconds is for.
#
#   deploy/kind/rollout-check.sh [namespace] [release]
set -euo pipefail

ns=${1:-freebusy}
release=${2:-freebusy}
url="http://$release:8080/v1/resources?pageSize=1"
pod=rollout-probe

kubectl -n "$ns" delete pod "$pod" --ignore-not-found --wait=true >/dev/null
kubectl -n "$ns" run "$pod" --restart=Never --image=curlimages/curl:8.11.1 \
  --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":100}}}' \
  -- sh -c "while [ ! -f /tmp/stop ]; do
      code=\$(curl -s -o /dev/null -m 5 -w '%{http_code}' '$url')
      echo \"\$code\"
      sleep 0.1
    done"
kubectl -n "$ns" wait --for=condition=Ready "pod/$pod" --timeout=120s

sleep 3
kubectl -n "$ns" rollout restart "deploy/$release"
kubectl -n "$ns" rollout status "deploy/$release" --timeout=300s
sleep 3
kubectl -n "$ns" exec "$pod" -- touch /tmp/stop
sleep 1

codes=$(kubectl -n "$ns" logs "$pod")
kubectl -n "$ns" delete pod "$pod" --wait=false >/dev/null
total=$(grep -c . <<<"$codes" || true)
failed=$(grep -vc '^200$' <<<"$codes" || true)
echo "rolling restart: $total calls, $failed not 200"
if (( total == 0 || failed > 0 )); then
  sort <<<"$codes" | uniq -c
  exit 1
fi
