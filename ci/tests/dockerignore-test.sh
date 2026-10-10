#!/usr/bin/env bash
# shellcheck disable=SC2015  # "A && ok || fail": ok/fail are counters and never fail
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of ci/check-dockerignore.sh: a Dockerfile with COPY . . needs an ignore file that covers .gitignore.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"

mk() { # name -> root with .gitignore and a Dockerfile that copies the whole context
  local d="${TEST_TMP}/$1"
  mkdir -p "${d}/deploy"
  printf '/bin/\n/.cache/\n*.test\n# comment\n\n.DS_Store\n' > "${d}/.gitignore"
  printf 'FROM scratch\nCOPY . .\n' > "${d}/deploy/app.Dockerfile"
  printf '%s' "${d}"
}
check() { "${CI_DIR}/check-dockerignore.sh" "$1"; }

r="$(mk missing)"
expect_fail "a missing ignore file is reported" 'app.Dockerfile.dockerignore does not exist' check "${r}"

r="$(mk partial)"; printf 'bin\n**/*.test\n' > "${r}/deploy/app.Dockerfile.dockerignore"
expect_fail "a pattern of .gitignore that is not covered is reported" 'does not cover the .gitignore pattern /.cache/' check "${r}"

r="$(mk floating)"; printf 'bin\n.cache\n*.test\n**/.DS_Store\n' > "${r}/deploy/app.Dockerfile.dockerignore"
expect_fail "a floating pattern needs the **/ form" 'does not cover the .gitignore pattern \*.test' check "${r}"

r="$(mk full)"; printf 'bin\n.cache\n**/*.test\n**/.DS_Store\n' > "${r}/deploy/app.Dockerfile.dockerignore"
expect_ok "an ignore file that covers .gitignore passes" check "${r}"

r="$(mk selective)"; printf 'FROM scratch\nCOPY go.mod go.sum ./\n' > "${r}/deploy/app.Dockerfile"
expect_ok "a Dockerfile that copies single paths needs no ignore file" check "${r}"

expect_ok "this repository passes" check "${ROOT}"
finish_tests "dockerignore-test"
