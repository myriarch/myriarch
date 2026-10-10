#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Fault injection of the E2E test (no product code): repeatedly kills every otelcol-contrib process whose binary lives under
# <INSTALL_DIR>/versions/<VERSION>/. The newly installed collector therefore never stays up during the health check,
# and the myriarch-updater rolls back automatically; the old collector (versions/<old version>/) is not touched.
#
#   killer.sh INSTALL_DIR VERSION [INTERVAL_SECONDS]      runs until it is terminated (SIGTERM)
#
# The binary of a process is found on macOS in `lsof -a -p PID -d txt -Fn` (line "n<path>"), on Linux in /proc/PID/exe.
set -u
install_dir=${1:?install dir}
version=${2:?version}
interval=${3:-0.3}
needle="${install_dir}/versions/${version}/"
# macOS/Linux show resolved paths (/var -> /private/var): compare the resolved install path as well.
# The version directory does not have to exist yet, so only the install directory is resolved.
real_install="$(cd "${install_dir}" 2>/dev/null && pwd -P)"
real_needle=""
[ -n "${real_install}" ] && real_needle="${real_install}/versions/${version}/"

binary_of() { # pid
  if [ -r "/proc/$1/exe" ]; then readlink "/proc/$1/exe" 2>/dev/null; return; fi
  lsof -a -p "$1" -d txt -Fn 2>/dev/null | sed -n 's/^n//p' | head -1
}

trap 'exit 0' TERM INT
while :; do
  for pid in $(pgrep -f otelcol-contrib 2>/dev/null); do
    bin=$(binary_of "$pid")
    [ -n "$bin" ] || continue
    # Staging directories (versions/<v>.tmp-*, pre-check) and other versions do not match: the needle ends with "/"
    if [[ "$bin" == "$needle"* ]] || { [ -n "$real_needle" ] && [[ "$bin" == "$real_needle"* ]]; }; then
      kill -KILL "$pid" 2>/dev/null && echo "killed otelcol-contrib pid $pid ($bin)"
    fi
  done
  sleep "$interval"
done
