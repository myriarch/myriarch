# AGENTS.md

Guidance for AI coding agents and for humans who work in this repository. Human contributors should also read
[CONTRIBUTING.md](CONTRIBUTING.md) and the [development guide](docs/development.md).

## What this project is

Myriarch is a fleet manager for OpenTelemetry Collectors, built on OpAMP. Git is the source of truth for blueprints, fleets,
rollout policies and allowed collector versions; the web UI and the REST API operate within that frame, and every operator
action is audited. Collectors run under the upstream OpAMP Supervisor, which cannot replace the collector binary, so version
updates go through exchangeable executors (the host agent `myriarch-updater`, a webhook or an AWX call).

| Binary | Purpose |
|---|---|
| `myriarchd` | Server: OpAMP endpoint, REST API, embedded web UI, GitOps syncer, rollout engine, release tracker, artifact cache |
| `myrctl` | CLI for GitOps repositories: `validate` and `render` (a short alias `myr` is documented, not shipped) |
| `myriarch-updater` | Host agent for collector binary updates (systemd, Windows service, container mode) |
| `myriarch-sim` | Load simulator |

## Layout

```text
api/openapi.yaml          REST contract (OpenAPI 3.1)
cmd/                      main packages of the four binaries
internal/                 Go packages; internal/brand holds every product identifier
web/                      React + TypeScript web UI (Vite, pnpm)
deploy/                   Helm chart (deploy/helm/myriarch), Compose, Kubernetes manifests, installers
examples/quickstart/      example GitOps repository; examples/sim/ holds simulator profiles
gitops-template/          template for a GitOps repository
hack/kind/                Kubernetes probe on kind
scripts/, ci/             end-to-end tests, image scripts, gates and pinned tool versions
docs/                     documentation, design contracts (docs/design), decision records (docs/adr)
website/                  project website (Hugo + Docsy), built from docs/ and the root Markdown files; see website/README.md
```

## Toolchain

Go (the version in `go.mod`), Node.js (the version in `.node-version`; 22.22.2, 24.15 or newer works) and pnpm (the version in `web/package.json`, through corepack). No container runtime is
needed for development or tests. `make tools` installs the pinned CI tools into `.cache/bin`.

## Commands and gates

Run these before you propose a change that touches code:

```bash
go build ./... && go vet ./... && test -z "$(gofmt -l cmd internal)"
(ulimit -n 10240; go test -race -count=1 ./...)    # database tests use an embedded PostgreSQL, no Docker
cd web && pnpm install --frozen-lockfile && pnpm gen:api && pnpm typecheck && pnpm lint && pnpm test && pnpm build
make build && make validate-examples
make docs-check                                    # when you touch docs/ or change commands and flags
make website-check                                 # when you touch website/ or docs/ (builds the site, checks links and privacy rules)
make licenses-check                                # SPDX headers, reuse lint and THIRD_PARTY_NOTICES drift (gate `licenses`)
make license-headers                               # insert missing SPDX headers into new source files
make notices                                       # regenerate THIRD_PARTY_NOTICES after a dependency change
make vulncheck && make secrets-scan               # govulncheck (needs network; SKIP=vulncheck offline) and gitleaks (gates `vulncheck`, `secrets`)
make check-local                                   # all gates in one run (SKIP=e2e leaves out the end-to-end test); CI runs the same gates (required check: `ci-ok`)
scripts/e2e.sh                                     # end-to-end test (fixed ports, about 6 minutes)
```

`make dev` starts the server in development mode with an embedded PostgreSQL on <http://localhost:8080>, `make sim` connects
1,000 simulated collectors. Development mode has well-known logins and tokens and is never a production setup.

Some tests and scripts use fixed ports or a shared local cluster (`scripts/e2e.sh`, `make e2e-ui-updates`,
`make kind-probe`). Do not run two of them at the same time.

## Conventions

- **English everywhere** ([ADR-0020](docs/adr/0020-english-as-project-language.md)): code, comments, log and error messages,
  commit messages, documentation, issues. The language guard (`scripts/check-language.sh`, gate `language`) enforces it.
- **Identifiers come from `internal/brand`.** Do not hard-code binary names, cookie names, token prefixes, attribute keys or
  environment prefixes elsewhere.
- **No secrets in Git, logs or chat.** Secrets come from files (`*File` settings) or environment variables, and log and error
  messages name the setting, never the value.
- **License headers and REUSE** ([ADR-0030](docs/adr/0030-reuse-compliance-and-third-party-notices.md)): a new source file
  starts with the two-line header `Copyright The Myriarch Authors` and `SPDX-License-Identifier: Apache-2.0` in its comment
  syntax (`make license-headers` adds it). Data files and third-party content get an entry in `REUSE.toml`, with attribution
  for third-party content. Do not change `THIRD_PARTY_NOTICES` by hand.
- **Neutral examples** ([ADR-0022](docs/adr/0022-white-label-and-neutral-examples.md)): edge sites, data centers, cloud and
  Kubernetes; the example fleets `linux-hosts`, `windows-hosts` and `k8s-gateway`; hosts under `example.com` or `.test`;
  addresses from the documentation ranges (RFC 5737, RFC 3849). No names of real organizations, people or environments.
- **Design documents are normative.** `docs/design/` describes contracts (version selection, updater protocol, container
  mode, server behavior). Change the document in the same pull request as the behavior.
- **Architecture decisions are ADRs.** Add a record under `docs/adr/` (copy `0000-template.md`, next free number) for decisions
  about architecture, interfaces, security, license or governance. Do not rewrite accepted records; supersede them.
- **Keep generated and reference files in step.**
  - Run `make docs-gen` when you change commands, flags or help texts or `api/openapi.yaml`; `docs/reference/cli/` (from the
    commands) and `docs/reference/api/` (from the OpenAPI contract) are generated and not edited by hand.
  - Keep `docs/reference/gitops-schema.md` complete: every kind, field and option of the GitOps format is documented there,
    and a test fails otherwise.
  - Run `pnpm gen:api` in `web/` after a change of `api/openapi.yaml` (and `make docs-gen`, see above) and commit `web/src/api/schema.d.ts`.
- **Documentation rules** are in [docs/development.md](docs/development.md#documentation): front matter, relative `.md` links,
  sections that start at H2.
- **Security-relevant changes** (authentication, authorization, outbound connections, parsing of untrusted input) need tests for
  the failure cases and a note in `docs/security/model.md` when a control changes.
- **Changelog:** user-visible changes get a line under "Unreleased" in [CHANGELOG.md](CHANGELOG.md).
- **Tests:** new behavior has tests. Prefer the embedded PostgreSQL test helper `internal/testutil/pgtest` over mocks for
  database code.

## Commits and pull requests

- Commit messages are in English, imperative, with a short first line and a body that explains why.
- Make focused commits; keep unrelated changes out.
- The project uses the Developer Certificate of Origin ([ADR-0019](docs/adr/0019-developer-certificate-of-origin.md)). The
  human who submits the change adds the `Signed-off-by:` line (`git commit -s`) and is responsible for the whole change.
  **An agent never adds or forges a `Signed-off-by:` line.** Disclose substantial AI assistance in the pull request
  description; an optional `Assisted-by: <tool>` trailer is welcome.
- Reviewers and the submitter must understand every line. Do not submit output that has not been verified by running the
  gates above.

## What not to do

- Do not add a Git remote, push, force-push, tag or create releases unless the maintainer asks for it explicitly.
- Do not run commands against real infrastructure, and do not publish images or charts.
- Do not weaken a security control, a gate or a test to make a change pass. Fix the cause or ask.
- Do not use development mode (`myriarchd dev`) or its credentials in a production-like deployment, and do not remove the
  checks that keep it away from one.
- Do not run binaries that you did not build from this source tree.
- Do not commit local state: `.dev/`, `.e2e/`, `.cache/`, `bin/`, `dist/` and `web/node_modules/` are git-ignored.
