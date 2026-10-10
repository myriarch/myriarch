#!/bin/sh
# shellcheck shell=sh
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Publishes a tree to the bare repository of the probe. Reads a tar archive of the complete tree from standard input,
# replaces the contents of the work tree with it, commits as "probe" and pushes to main. Prints the commit SHA.
#
#   kubectl exec -i deploy/git -- sh /scripts/publish.sh "message" < tree.tar
set -eu
msg="${1:?commit message}"
work=/tmp/work
rm -rf "$work"
git clone -q /srv/git/fleet.git "$work" 2>/dev/null
cd "$work"
git checkout -q -B main
find . -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} +
tar -xf - -C "$work"
git add -A
if git diff --cached --quiet; then
  git rev-parse HEAD 2>/dev/null || echo none
  exit 0
fi
git -c user.name=probe -c user.email=probe@example.test commit -q -m "$msg"
git push -q origin HEAD:main
chown -R git:git /srv/git
git rev-parse HEAD
