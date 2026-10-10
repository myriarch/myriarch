#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Go lint: go mod verify, go vet, gofmt check (equivalent to `make lint` without the UI; needs no web/node_modules).
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "${ROOT}"
need_cmd go gofmt

section_start modverify "go mod verify"
go mod verify
section_end modverify

section_start vet "go vet ./..."
go vet ./...
go vet -tags devpg ./internal/devpg/... ./cmd/myriarchd/...
section_end vet

section_start gofmt "gofmt"
if git ls-files '*.go' >/dev/null 2>&1 && [ -n "$(git ls-files '*.go')" ]; then
  files="$(git ls-files '*.go')"
else
  files="$(find . -name '*.go' -not -path './web/*' -not -path './.cache/*')"
fi
# shellcheck disable=SC2086
out="$(gofmt -l ${files})"
if [ -n "${out}" ]; then
  printf 'gofmt needed:\n%s\n' "${out}" >&2
  exit 1
fi
section_end gofmt
log "Go lint ok"
