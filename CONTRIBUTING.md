# Contributing to myriarch

Thank you for your interest in myriarch. This project welcomes contributions of all kinds. This document explains how to
get started and what we expect from a contribution. Please also read our [Code of Conduct](CODE_OF_CONDUCT.md).

## Ways to contribute

- **Report a bug or request a feature** in [GitHub Issues](https://github.com/myriarch/myriarch/issues). Use the issue
  forms; they ask for the details we need.
- **Improve the documentation**: fix a typo, clarify a step, add a task guide. The documentation is in [docs/](docs/_index.md).
- **Test and give feedback**: try the [quickstart](README.md#quickstart), run the Kubernetes probe, report what surprised you.
- **Fix a bug or implement a feature** with a pull request.
- **Tell us how you use myriarch** by adding your organization to [ADOPTERS.md](ADOPTERS.md).

**Security vulnerabilities** are never reported in public issues. Follow [SECURITY.md](SECURITY.md).

## Before you start

For anything larger than a small fix, open an issue first (or comment on an existing one) and describe the problem and
your idea. That avoids duplicate work and lets a maintainer say early whether the change fits the project.
Architecture-level changes need a decision record, see [Documentation and decision records](#documentation-and-decision-records).

Issues labeled `good first issue` or `help wanted` are good starting points once they exist.

## Development setup

The [development guide](docs/development.md) covers the toolchain, the repository layout, the build tags, the web UI,
the tests and the documentation tooling. In short:

```bash
git clone https://github.com/<your-account>/myriarch.git    # your fork
cd myriarch
make web-build && make build
make dev                                                    # http://localhost:8080, dev logins in the README
```

## Checks before you open a pull request

Run `make check-local`. It runs the gates that CI runs and the end-to-end test: Go build, vet and gofmt, `go test -race`,
the web checks, the example repositories, shellcheck, the workflow checks, the Helm chart checks, the language guard and the
documentation checks. `make check-local SKIP=e2e` leaves out the long end-to-end test, and `make tools` installs the pinned tools.
The details and the variables are in the [development guide](docs/development.md#gates); how CI runs the same gates, and how to
reproduce a job, is in [Continuous integration](docs/development.md#continuous-integration).

Please also make sure that:

- new behavior has tests;
- generated files are regenerated: `pnpm gen:api` in `web/` after a change of `api/openapi.yaml`, and `make docs-gen`
  after a change of commands, flags, help texts, `api/openapi.yaml`, configuration keys, environment reads, metrics or values of
  the Helm chart (`docs/reference/cli`, `docs/reference/api`, the configuration, environment variable and metrics pages and
  `deploy/helm/myriarch/values.schema.json` are generated);
- the documentation is updated: every new setting, field, flag or API path appears in the matching page under `docs/`
  (the GitOps field reference is [docs/reference/gitops-schema.md](docs/reference/gitops-schema.md));
- user-visible changes have a line under "Unreleased" in [CHANGELOG.md](CHANGELOG.md).

## Pull request flow

1. Fork the repository and create a branch from `main` in your fork.
2. Make small, focused changes. One pull request should do one thing; unrelated cleanups belong in their own pull request.
3. Open a pull request against `main`. Fill in the template: what changes and why, the linked issue, and the checklist.
4. A maintainer reviews it. Reviews are about correctness, security, maintainability and fit; expect questions and
   requests for changes. Push follow-up commits to the same branch.
5. The required checks must pass: the DCO check and `ci-ok`, the aggregate of all CI jobs. CI runs automatically for
   maintainers; the first workflow run of a pull request from a new contributor starts after a maintainer approves it.
6. A maintainer merges, by squash or rebase. [CODEOWNERS](.github/CODEOWNERS) lists who is requested for review.

## Commit messages

Commit messages are written in English, in the imperative mood. The first line is a short summary (about 72 characters
at most), followed by a blank line and, when it helps, a body that explains the why. Reference issues with `Fixes #123`.

English is the language of the whole project, including code comments, log messages, documentation, issues and discussions
([ADR-0020](docs/adr/0020-english-as-project-language.md)).

## Sign-off: Developer Certificate of Origin

Every commit must carry a `Signed-off-by:` line that matches the commit author. It certifies that you wrote the change or
have the right to submit it under the project license, according to the
[Developer Certificate of Origin 1.1](https://developercertificate.org/). The project uses the DCO and **no** Contributor
License Agreement ([ADR-0019](docs/adr/0019-developer-certificate-of-origin.md)).

Add the line with `-s`:

```bash
git commit -s -m "Fix the maintenance window check for negative offsets"
```

This produces `Signed-off-by: Your Real Name <your-email@example.com>`, using the name and e-mail address from your Git
configuration. Use your real, non-anonymous name; a GitHub noreply address is fine as the e-mail address.

If you forgot the sign-off, fix it before the merge:

```bash
git commit --amend -s --no-edit              # the last commit only
git rebase --signoff main                    # every commit of the branch (rebase onto the current main)
git push --force-with-lease                  # update your pull request
```

The DCO check is a required status check; a pull request with an unsigned commit cannot be merged.

## AI-assisted contributions

You may use AI coding tools. The rules are the same as for every contribution, and they put the responsibility on you:

- You review and understand every line you submit, and you can explain and defend it in review.
- You are responsible for the result: correctness, security, license compatibility and tests.
- You sign the DCO personally. An AI tool never adds a `Signed-off-by:` line, and nobody forges one on behalf of another
  person.
- Disclose substantial AI assistance in the pull request description (which tool, for what). Optionally add a trailer to
  the commit message, for example `Assisted-by: <tool name>`.
- Do not submit generated text or code that you have not verified, and do not paste confidential material into
  external tools.

Maintainers may ask for changes or close pull requests that look like unreviewed generated output.

[AGENTS.md](AGENTS.md) contains guidance written for coding agents.

## Documentation and decision records

The documentation under `docs/` follows the structure and rules in the
[development guide](docs/development.md#documentation): Hugo-compatible front matter, relative links, neutral examples,
English text. `make docs-check` verifies the mechanical rules.

Decisions that shape the architecture, the public interfaces, the security model, the license or the governance are recorded
as architecture decision records (ADRs). Copy [docs/adr/0000-template.md](docs/adr/0000-template.md), use the next free
number, and propose the record in a pull request. The process is described in [docs/adr/README.md](docs/adr/README.md).
Accepted records are not rewritten; a change of mind is a new record. Editorial fixes such as links and typos are fine.

## Licensing of contributions

By contributing you agree that your contribution is licensed under the license of the part of the project you change:

- **Source code** (Go, TypeScript, shell, Helm templates, build files), data and configuration files: Apache License 2.0.
- **Documentation** (all Markdown files: `docs/`, the decision records, website text, README and the other root documents):
  Creative Commons Attribution 4.0 (CC-BY-4.0). Code snippets inside documentation are Apache-2.0.
- **Brand artwork** (`assets/brand/`, `web/public/`): CC-BY-4.0.
- **Exceptions under Apache-2.0:** `GOVERNANCE.md` and `CONTRIBUTOR_LADDER.md` (adapted from an Apache-2.0 template) and the
  GitOps template (`gitops-template/`).

See [ADR-0018](docs/adr/0018-license-and-copyright.md). The project does not collect copyright assignments.

### Headers and REUSE

The repository follows the REUSE specification ([ADR-0030](docs/adr/0030-reuse-compliance-and-third-party-notices.md)).

- **New source files** (Go, TypeScript, JavaScript, shell, PowerShell, Python, SQL, CSS, HTML, Dockerfiles, Helm templates and
  the Makefile) start with exactly these two lines in the comment syntax of the file, followed by one empty line (HTML and Helm
  templates: the content follows the header directly):

  ```text
  Copyright The Myriarch Authors
  SPDX-License-Identifier: Apache-2.0
  ```

  The header goes at the top of the file. Only a shebang, a `shellcheck` directive, a Dockerfile parser directive such as
  `# syntax=...` or a leading `<!doctype>` line stays in front of it. Do not add years or personal names.
- `make license-headers` inserts the header where it is missing. `make licenses-check` (also the gate `licenses` of
  `make check-local`) reports files without a correct header.
- **Files without comments** (JSON, YAML, data files, images) and **third-party content** get an entry in
  [REUSE.toml](REUSE.toml). Third-party content needs an attributed entry: the upstream source (URL and tag or commit), the
  license and the copyright holder of the original, kept verbatim. Generated code and vendored assets keep their original
  headers.
- `ci/reuse-lint.sh` runs `reuse lint`; install the pinned tool with `ci/install-tools.sh reuse`. Details are in the
  [development guide](docs/development.md).

## Getting help

Open an [issue with the "Question" form](https://github.com/myriarch/myriarch/issues/new?template=question.yml) for questions, or write to
<maintainers@myriarch.dev> for matters that are not suited for public discussion (not for security reports, see
[SECURITY.md](SECURITY.md)).
