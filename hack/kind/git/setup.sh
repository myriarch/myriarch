#!/bin/sh
# shellcheck shell=sh
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Starts the Git server of the probe (container alpine, root): installs git and openssh, creates the account "git" with
# git-shell, installs the host key and the authorized key from the mounted Secret, creates the bare repository and runs
# sshd in the foreground. Mounts: /keys (Secret: ssh_host_ed25519_key, authorized_keys), /scripts (this ConfigMap),
# /srv (emptyDir).
set -eu

i=0
# GIT_SERVER_GIT_SERIES and GIT_SERVER_OPENSSH_SERIES (versions.env) pin the upstream series: "git~2.49" is any 2.49.x build
: "${GIT_SERVER_GIT_SERIES:?}" "${GIT_SERVER_OPENSSH_SERIES:?}"
until apk add --no-cache "git~${GIT_SERVER_GIT_SERIES}" "openssh-server~${GIT_SERVER_OPENSSH_SERIES}" >/tmp/apk.log 2>&1; do
  i=$((i + 1))
  echo "apk add failed (attempt $i):" >&2
  tail -n 3 /tmp/apk.log >&2
  [ "$i" -lt 8 ] || exit 1
  sleep 5
done

echo "installed: $(apk list -I 2>/dev/null | grep -E '^(git|openssh-server)-[0-9]' | cut -d' ' -f1 | tr '\n' ' ')"

adduser -D -h /srv/git -s /usr/bin/git-shell git
# an account locked with "!" is refused by sshd even for key logins: "*" = no password, not locked
sed -i 's/^git:!:/git:*:/' /etc/shadow

install -d -m 0755 /etc/ssh/authorized
install -m 0600 /keys/ssh_host_ed25519_key /etc/ssh/ssh_host_ed25519_key
install -m 0644 /keys/authorized_keys /etc/ssh/authorized/git
install -m 0644 /scripts/sshd_config /etc/ssh/sshd_config

mkdir -p /srv/git
git init -q --bare -b main /srv/git/fleet.git
git config --system --add safe.directory '*'
chown -R git:git /srv/git
echo "git server ready (ssh://git@git.probe-infra.svc:2222/srv/git/fleet.git)"
exec /usr/sbin/sshd -D -e -f /etc/ssh/sshd_config
