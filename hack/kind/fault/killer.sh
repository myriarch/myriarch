#!/bin/sh
# shellcheck shell=sh
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Fault injection of the kind probe (no product code): repeatedly kills every otelcol-contrib process whose binary lives under
# /var/lib/myriarch/collector/versions/<VERSION>/. The newly installed collector therefore never stays up during the health
# check and the myriarch-updater rolls back automatically; the old collector (another versions/ directory) and the staging
# directory of the preflight validate (versions/<VERSION>.tmp-*) are not touched.
#
#   kubectl exec -i kind-gw-0 -- sh -s -- 0.162.0 < hack/kind/fault/killer.sh
#
# Runs for at most 300 seconds, or until /tmp/killer.stop exists; every kill is logged to /tmp/killer.log.
set -u
version="${1:?version}"
needle="/var/lib/myriarch/collector/versions/${version}/"
rm -f /tmp/killer.stop /tmp/killer.log
: > /tmp/killer.log
end=$(($(date +%s) + 300))
while [ "$(date +%s)" -lt "$end" ] && [ ! -e /tmp/killer.stop ]; do
  for pid in $(pgrep -f otelcol-contrib 2>/dev/null); do
    exe="$(readlink "/proc/$pid/exe" 2>/dev/null)"
    case "$exe" in
      "$needle"*) kill -9 "$pid" 2>/dev/null && echo "killed otelcol-contrib pid $pid ($exe)" >> /tmp/killer.log ;;
    esac
  done
  sleep 0.3
done
