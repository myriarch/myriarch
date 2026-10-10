# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Myriarch Makefile
#
# Targets:
#   make build        Go binaries into ./bin (build tags: embedui if internal/webui/dist exists, plus devpg = embedded PostgreSQL for `myriarchd dev`)
#   make test         Go unit tests (embedded PostgreSQL for DB tests, no Docker needed) plus the tagged tests of internal/devpg
#   make lint         go vet + gofmt check (+ UI lint, format check and typecheck if web/node_modules exists)
#   make web-build    build the React UI into internal/webui/dist
#   make dev          myriarchd dev mode: embedded PostgreSQL + examples/quickstart (built with the devpg tag; first start downloads the PostgreSQL binaries)
#   make sim          myriarch-sim with SIM_AGENTS agents against the dev server
#   make validate-examples  myrctl validate --strict on examples/quickstart (and the gitops-template)
#   make e2e          end-to-end test (scripts/e2e.sh; E2E_REAL=1, load run: E2E_AGENTS=1000 scripts/e2e.sh, ... see docs/development.md)
#   make release      cross-compiled binaries for all target platforms into ./dist (build tags: embedui only, deliberately WITHOUT devpg; see docs/development.md)
#   make release-snapshot  goreleaser snapshot of the full release matrix (unsigned, nothing published; local test only; ci/build-release.sh snapshot)
#   make release-rehearsal  rehearsal of the release build: snapshot in stage collect, one signing pass with a throwaway test CA, snapshot in stage apply, dist and reproducibility checks (ci/release/rehearsal.sh; needs the built UI and a C compiler with the OpenSSL 3 headers for osslsigncode)
#   make ci-test      script tests of ci/ and contrib/enterprise-mirror/ (ci/tests/run-all.sh)
#   make image-crane  container image WITHOUT a container runtime (crane) and FROM scratch: build and push to IMAGE_REPO (required), print the digest (scripts/build-image-crane.sh; IMAGE_PLATFORM=linux/arm64 builds for arm64)
#   make collector-image-crane  collector image (upstream opampsupervisor + otelcol-contrib of the same version + myriarch-updater) WITHOUT a container runtime: build and push to IMAGE_REPO (required); OTELCOL_VERSION=0.162.0 (default: ci/versions.env)
#   make mirror-image-crane  in-cluster mirror of the collector release files (busybox httpd) WITHOUT a container runtime: build and push to IMAGE_REPO (required; scripts/build-mirror-image-crane.sh)
#   make docs-gen     regenerate the generated references: docs/reference/cli from the cobra command trees (cmd/myriarchd/clidoc_test.go; CLIDOC_UPDATE=1), docs/reference/api from api/openapi.yaml (internal/apidoc; APIDOC_UPDATE=1) and docs/reference/{configuration,environment-variables,metrics}.md from the configuration struct, the loader and the metric definitions (internal/refdoc; REFDOC_UPDATE=1)
#   make docs-check   documentation checks: internal links, front matter (scripts/check-docs.py) and drift of the generated reference (ci/check-docs-gen.sh)
#   make license-headers  write the missing SPDX header into the source files (scripts/license-headers.py --fix; check only: python3 -I scripts/license-headers.py --check)
#   make notices      regenerate THIRD_PARTY_NOTICES from the dependency trees of the binaries and the web UI (scripts/gen-third-party-notices.sh)
#   make licenses-check  license compliance checks: SPDX headers, REUSE (ci/reuse-lint.sh) and drift of THIRD_PARTY_NOTICES; needs make tools
#   make website      build the website (website/, ADR-0031) into website/public (ci/website.sh build; Hugo extended + Dart Sass + pnpm, first run needs network access)
#   make website-serve  Hugo development server on http://127.0.0.1:1313 (WEBSITE_PORT changes the port)
#   make website-preview  production build served as static files on http://127.0.0.1:1313 (for Lighthouse and the request check)
#   make website-check  website gate: build with --panicOnWarning, module verify, lychee (offline), markdownlint, front matter, the checks of website/scripts and their tests
#   make e2e-ui-updates  local end-to-end test of the UI-triggered version update (scripts/e2e-ui-updates.sh)
#   make tools        build/download the pinned CI tools into .cache/bin (ci/install-tools.sh; TOOLS="helm shellcheck" limits the set)
#   make kind-up      kind cluster with a local registry (hack/kind/up.sh)
#   make kind-down    delete the kind cluster (ALL=1 also removes the registry container and the work directory .cache/kind)
#   make kind-probe   Kubernetes probe of the whole system on kind from zero: cluster, images, install, numbered checks (hack/kind/probe.sh --fresh; see hack/kind/README.md)
#   make vulncheck    govulncheck over the release and the devpg build; fails on called vulnerabilities (ci/vulncheck.sh, allowlist ci/vulncheck-allowlist.txt; needs network access)
#   make secrets-scan gitleaks over an export of HEAD (ci/secrets-scan.sh, configuration .gitleaks.toml; SECRETS_SCAN_WORKTREE=1 scans the work tree)
#   make check-local  run every local gate (go, go-test, web, build, examples, shellcheck, workflows, helm, language, docs, website, licenses, vulncheck, secrets, e2e); SKIP=e2e,go-test ONLY=... LANGUAGE_GUARD=strict|warn|off (empty = strict: the strict scope fails on findings, warn downgrades, off skips); logs in .cache/check-local/

comma     := ,
GO        ?= go
PNPM      ?= pnpm
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
# Package that receives the build metadata (-X). Defined once; must equal the module path of go.mod plus /internal/version
# (internal/updater/deploy_consistency_test.go checks this and every other definition).
VERSION_PKG := github.com/myriarch/myriarch/internal/version
LDFLAGS   := -s -w \
	-X $(VERSION_PKG).Version=$(VERSION) \
	-X $(VERSION_PKG).Commit=$(COMMIT) \
	-X $(VERSION_PKG).BuildDate=$(DATE)
# Build tags (comma-separated, see docs/development.md): embedui = the real UI when it has been built; devpg = embedded PostgreSQL for
# `myriarchd dev` (pulls in embedded-postgres, which release builds must not link). Dev builds use DEV_TAGS, release builds RELEASE_TAGS.
UI_TAGS      := $(if $(wildcard internal/webui/dist/index.html),embedui,)
DEV_TAGS     := $(if $(UI_TAGS),$(UI_TAGS)$(comma))devpg
RELEASE_TAGS := $(UI_TAGS)
BINARIES  := myriarchd myriarch-updater myrctl myriarch-sim
PLATFORMS := linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/arm64

# Collector image (make collector-image-crane): version of supervisor AND collector, default = the pinned one of ci/versions.env
OTELCOL_VERSION ?= $(shell sed -n 's/^OTELCOL_VERSION=//p' ci/versions.env)

# Pinned CI tools (make tools): built from ci/tools/ or downloaded with a SHA-256 check, see ci/install-tools.sh
TOOLS ?= goreleaser cosign helm yq kustomize kubeconform gotestsum crane govulncheck go-licenses gitleaks reuse shellcheck pwsh kind kubectl hugo sass lychee actionlint syft gh go-winres zizmor

SIM_AGENTS   ?= 1000
SIM_UPDATERS ?= true
SIM_SERVER   ?= ws://localhost:8080/v1/opamp
SIM_TOKEN    ?= myre_devtoken_insecure_do_not_use_in_prod

.PHONY: all build test test-race lint fmt web-install web-build web-dev web-gen dev sim e2e docs-gen docs-check license-headers notices licenses-check validate-examples release release-snapshot release-rehearsal ci-test image-crane collector-image-crane mirror-image-crane e2e-ui-updates website website-serve website-preview website-check tools kind-up kind-down kind-probe check-local vulncheck secrets-scan clean

all: lint test build

build:
	@mkdir -p bin
	@for b in $(BINARIES); do \
		echo "go build $$b (tags: $(or $(DEV_TAGS),none))"; \
		CGO_ENABLED=0 $(GO) build -trimpath -tags "$(DEV_TAGS)" -ldflags "$(LDFLAGS)" -o bin/$$b ./cmd/$$b || exit 1; \
	done

# Untagged run covers the stub of internal/devpg; the second run covers the embedded PostgreSQL implementation.
test:
	$(GO) test -count=1 ./...
	$(GO) test -count=1 -tags devpg ./internal/devpg/...

test-race:
	$(GO) test -race -count=1 ./...
	$(GO) test -race -count=1 -tags devpg ./internal/devpg/...

lint:
	$(GO) vet ./...
	@out=$$(gofmt -l $$(git ls-files '*.go' 2>/dev/null || find . -name '*.go' -not -path './web/*')); \
		if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@if [ -d web/node_modules ]; then cd web && $(PNPM) lint && $(PNPM) format:check && $(PNPM) typecheck; fi

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './web/*')

web-install:
	cd web && $(PNPM) install --frozen-lockfile

web-gen:
	cd web && $(PNPM) gen:api

web-build:
	cd web && $(PNPM) install --frozen-lockfile && $(PNPM) build
	@test -f internal/webui/dist/index.html || (echo "UI build did not produce internal/webui/dist/index.html" && exit 1)

web-dev:
	cd web && $(PNPM) dev

dev:
	$(GO) run -tags "$(DEV_TAGS)" ./cmd/myriarchd dev --gitops-dir examples/quickstart --data-dir .dev

sim:
	$(GO) run ./cmd/myriarch-sim run --server $(SIM_SERVER) --token $(SIM_TOKEN) \
		--agents $(SIM_AGENTS) --updaters=$(SIM_UPDATERS) --profile examples/sim/mixed-fleet.yaml

e2e: build
	NO_BUILD=1 scripts/e2e.sh

# Generated references. The command line reference (docs/reference/cli) comes from the cobra trees of the four binaries by a test of
# cmd/myriarchd; the REST API reference (docs/reference/api) comes from api/openapi.yaml by a test of internal/apidoc; the configuration,
# environment and metrics references (docs/reference/{configuration,environment-variables,metrics}.md) come from the configuration struct,
# the loader, the environment reads and the metric definitions by a test of internal/refdoc. The tests also run in the go-test gate as a
# drift check. PGTEST_SKIP=1: the tests need no database.
docs-gen:
	PGTEST_SKIP=1 CLIDOC_UPDATE=1 $(GO) test -count=1 -run '^TestCLIReference$$' ./cmd/myriarchd
	PGTEST_SKIP=1 APIDOC_UPDATE=1 $(GO) test -count=1 -run '^TestAPIReference$$' ./internal/apidoc
	PGTEST_SKIP=1 REFDOC_UPDATE=1 $(GO) test -count=1 -run '^TestReferencePages$$' ./internal/refdoc

# Documentation checks: relative links and anchors, front matter, and the generated references are up to date (regenerates them and fails on a diff).
docs-check:
	python3 -I scripts/check-docs.py links && python3 -I scripts/check-docs.py front-matter && ci/check-docs-gen.sh

# License compliance (ADR-0018): license-headers writes the missing SPDX header of the source files and prints the changed paths;
# notices regenerates THIRD_PARTY_NOTICES; licenses-check is the read-only check of the licenses gate in ci/check-local.sh (SPDX headers,
# REUSE 3.3 and the notices, which are regenerated into a temporary file and compared). The tools come from `make tools`.
license-headers:
	python3 -I scripts/license-headers.py --fix

notices:
	scripts/gen-third-party-notices.sh

licenses-check:
	python3 -I scripts/license-headers.py --check && ci/reuse-lint.sh && scripts/gen-third-party-notices.sh --check

validate-examples:
	$(GO) run ./cmd/myrctl validate --strict --components examples/sim/components-otelcol-contrib-0.162.0.json examples/quickstart
	$(GO) run ./cmd/myrctl validate --strict --components examples/sim/components-otelcol-contrib-0.162.0.json gitops-template/config
	$(GO) run ./cmd/myrctl validate --strict --components examples/sim/components-otelcol-contrib-0.162.0.json hack/kind/gitops

release:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=$$( [ $$os = windows ] && echo .exe ); \
		for b in $(BINARIES); do \
			echo "build $$b $$os/$$arch"; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -tags "$(RELEASE_TAGS)" -ldflags "$(LDFLAGS)" \
				-o dist/$${b}_$(VERSION)_$${os}_$${arch}$$ext ./cmd/$$b || exit 1; \
		done; \
	done

# Snapshot of the complete release matrix (deploy/release/goreleaser.yaml): local test only, unsigned, never for releases. Needs the built
# UI (make web-build).
release-snapshot:
	ci/build-release.sh snapshot

# Rehearsal of the release build (deploy/release/README.md): a snapshot in stage collect, one signing pass with a throwaway test CA (never
# published), a snapshot in stage apply, then the dist and reproducibility checks. Nothing is published. Needs the built UI (make web-build)
# and, for osslsigncode, a C compiler with the OpenSSL 3 and zlib headers.
release-rehearsal:
	ci/release/rehearsal.sh

# Script tests of ci/ and contrib/enterprise-mirror/ (needs bash, openssl, python3; cosign/helm/crane/pwsh extend the scope)
ci-test:
	ci/tests/run-all.sh

# Container image without a container runtime (crane instead of Docker/Podman) and without a base image (FROM scratch, no OS packages):
# builds linux/amd64 (IMAGE_PLATFORM=linux/arm64 for arm64; its default tag gets the suffix -arm64), pushes to IMAGE_REPO:TAG (IMAGE_REPO is required,
# there is no default registry) and prints the digest.
# Controlled through the environment: IMAGE_REPO, IMAGE_PLATFORM, TAG (default: short git SHA), ALLOW_DIRTY=1, DRY_RUN=1, WEB_BUILD=always|never,
# BASE_IMAGE (default scratch; any other base must be pinned by digest; see the header of scripts/build-image-crane.sh).
# Example: `make image-crane IMAGE_REPO=registry.example.com/<org>/myriarch`. Login: `IMAGE_REPO=<registry>/<path>
# scripts/build-image-crane.sh login '<registry-user>' < token-file`.
image-crane:
	VERSION='$(VERSION)' COMMIT='$(COMMIT)' scripts/build-image-crane.sh

# Collector image (supervisor + otelcol-contrib + myriarch-updater in one image; documented in the header of
# scripts/build-collector-image-crane.sh): pulls the two upstream images of OTELCOL_VERSION (pinned by digest), builds myriarch-updater
# from this repository (VERSION/COMMIT of the Makefile are linked into it), pushes IMAGE_REPO:TAG (IMAGE_REPO is required; default tag
# <collector version>-<git short sha>) and prints the digest. Environment: IMAGE_REPO, TAG, WITH_UPDATER=0 (image without
# myriarch-updater, tag <version>), IMAGE_PLATFORM=linux/arm64 (default linux/amd64; tag suffix -arm64), DRY_RUN=1, VERIFY=1,
# VERIFY_UPSTREAM=1 (signature check, needs cosign).
# Examples: `make collector-image-crane IMAGE_REPO=registry.example.com/<org>/myriarch-collector OTELCOL_VERSION=0.161.0`,
# `make collector-image-crane IMAGE_REPO=... DRY_RUN=1`.
# (OTELCOL_VERSION, not VERSION: VERSION is the version of myriarchd.)
collector-image-crane:
	@test -n '$(OTELCOL_VERSION)' || { echo "OTELCOL_VERSION is empty (ci/versions.env?)"; exit 1; }
	UPDATER_VERSION='$(VERSION)' UPDATER_COMMIT='$(COMMIT)' scripts/build-collector-image-crane.sh '$(OTELCOL_VERSION)'

# In-cluster mirror image (busybox httpd + the verified release files of the collector in the GitHub layout; see the header of
# scripts/build-mirror-image-crane.sh): VERSIONS="0.161.0 0.162.0" PLATFORMS="linux/amd64"; DRY_RUN=1 builds the layer without pushing;
# OUT_DIR=<dir> only writes the directory (no IMAGE_REPO needed). Pushes IMAGE_REPO:contrib-<versions>; IMAGE_REPO is required unless OUT_DIR is set.
# Example: `make mirror-image-crane IMAGE_REPO=registry.example.com/<org>/myriarch-mirror`.
mirror-image-crane:
	scripts/build-mirror-image-crane.sh

# Local end-to-end test of the UI-triggered version update on this machine (myriarchd dev + myriarch-updater container mode + real
# supervisor/collector; needs network once for the release files, see scripts/e2e-ui-updates.sh): E2E_KEEP=1 keeps the work dir.
e2e-ui-updates:
	scripts/e2e-ui-updates.sh

# Website (website/, ADR-0031): Hugo extended with the Docsy theme, built from docs/ and the root Markdown files. ci/website.sh installs
# the pinned tools (hugo, sass, lychee) and the Node dependencies of website/ first; the output is the static directory website/public.
website:
	ci/website.sh build

website-serve:
	ci/website.sh serve

website-preview:
	ci/website.sh preview

website-check:
	ci/website.sh check

# Pinned CI tools into .cache/bin (TOOLS="helm shellcheck" limits the set; a second run hits the cache).
tools:
	ci/install-tools.sh $(TOOLS)

# Kubernetes probe on kind (hack/kind): the cluster with its local registry, its removal, and the complete run from zero (needs Docker;
# docs/development.md, hack/kind/README.md). `make kind-down ALL=1` also removes the registry container and the work directory.
kind-up:
	hack/kind/up.sh

kind-down:
	hack/kind/down.sh $(if $(ALL),--all,)

kind-probe:
	hack/kind/probe.sh --fresh

# Vulnerability gate and secret scan (the two gates of check-local that need the tools govulncheck and gitleaks from `make tools`).
vulncheck:
	ci/install-tools.sh govulncheck
	ci/vulncheck.sh

secrets-scan:
	ci/install-tools.sh gitleaks
	ci/secrets-scan.sh

# All local gates (ci/check-local.sh): go, go-test, web, build, examples, shellcheck, workflows, helm, language, docs, website, licenses, vulncheck, secrets, e2e. The variables are passed
# explicitly, so both `make check-local SKIP=e2e` and an environment SKIP work. LANGUAGE_GUARD empty = strict: the strict scope fails on findings, warn downgrades, off skips.
check-local:
	SKIP='$(SKIP)' ONLY='$(ONLY)' LANGUAGE_GUARD='$(LANGUAGE_GUARD)' LANGUAGE_GUARD_PATHS='$(LANGUAGE_GUARD_PATHS)' ci/check-local.sh

clean:
	rm -rf bin dist .cache/release .cache/release-rehearsal cmd/myriarch-updater/rsrc_windows_*.syso cmd/myrctl/rsrc_windows_*.syso
