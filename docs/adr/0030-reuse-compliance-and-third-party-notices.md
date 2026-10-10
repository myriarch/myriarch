# 0030. REUSE compliance and third-party notices

- Status: Accepted
- Date: 2026-10-09
- Related: [ADR-0018](0018-license-and-copyright.md), [ADR-0021](0021-publication-gates-and-v0-1-0-scope.md), [ADR-0022](0022-white-label-and-neutral-examples.md), [ADR-0024](0024-container-registry-and-image-set.md)

## Context

[ADR-0018](0018-license-and-copyright.md) decided the licenses (Apache-2.0 for code, CC-BY-4.0 for documentation), the copyright line, the headers and the absence of a project NOTICE file. It left the mechanics open: how a CI check proves that every file is covered, how files without comments and third-party content are licensed, and how the notices that the dependencies require reach the people who receive the binaries.

The facts that make the mechanics necessary:

- The binaries link 117 Go modules (Apache-2.0, BSD, MIT, ISC, Zlib and one MPL-2.0 module of which only BSD-3-Clause packages are linked). Twelve of them ship NOTICE files that Apache-2.0 section 4(d) requires to be reproduced. MIT, BSD, ISC and Zlib require their license texts in binary distributions. The Vite build removes the license comments of the web UI packages that are embedded into `myriarchd`, and four of them publish no license file.
- The repository holds content that is not original: OpenTelemetry release fixtures, Sigstore test data, component lists derived from OpenTelemetry, and community documents adapted from the CNCF project template and the CNCF Code of Conduct.
- Many files cannot carry a comment (JSON, images, lock files), and generated files would lose a hand-written header.
- The server image of the development variant contained OpenTelemetry release fixtures that production deployments do not need.

## Decision

1. **REUSE 3.3 is the compliance format, and `reuse lint` is the check.** The gate `licenses` of `make check-local` runs the header check, `ci/reuse-lint.sh` and the notices drift check. `reuse` is pinned (5.1.1, the last release for Python 3.9) and installed from hash-locked requirements. Only the licenses Apache-2.0 and CC-BY-4.0 are used, and their texts are in `LICENSES/`; the repository `LICENSE` is the unchanged Apache License 2.0 text.
2. **Source files carry the header of ADR-0018 in their comment syntax**, as two lines followed by one empty line:

   <!-- REUSE-IgnoreStart -->
   ```text
   Copyright The Myriarch Authors
   SPDX-License-Identifier: Apache-2.0
   ```
   <!-- REUSE-IgnoreEnd -->

   `scripts/license-headers.py` checks and inserts the header and knows where it goes (after a shebang, a Dockerfile parser directive or a leading doctype, before `//go:build` lines and package comments). Helm templates use a template comment, which keeps the rendered output identical. Generated files get the header from their generator (`pnpm gen:api`).
3. **`REUSE.toml` covers every file that has no header**: YAML, JSON, data, images and Markdown. Every Markdown file (documentation, decision records, root documents, READMEs) and the brand artwork are CC-BY-4.0, except the files of item 4 and the GitOps template (`gitops-template/`), which is Apache-2.0 so that the template that users copy carries a single license. Third-party content has its own annotation with the original license and holder, and a comment names the upstream source (URL, tag or commit). Annotations use `precedence = "override"`; a new data or third-party file needs an entry in the same pull request.
4. **Adapted community documents keep the license of their upstream.** `GOVERNANCE.md` and `CONTRIBUTOR_LADDER.md` come from the CNCF project template (Apache-2.0) and stay Apache-2.0; both state that they were adapted. `CODE_OF_CONDUCT.md` comes from texts under CC-BY-4.0 and stays CC-BY-4.0.
5. **`THIRD_PARTY_NOTICES` is generated, not written.** `scripts/gen-third-party-notices.sh` combines `go-licenses` reports for all four binaries and the three target operating systems (and the `devpg` build, marked as development only), the Vite license report of the bundled web UI packages, a shared override list (`ci/license-overrides.txt`) for modules whose license the tool cannot classify, and a small file of verified texts for components without a license file (`scripts/notices/third-party-texts.json`). It reproduces every distinct license text verbatim and prints identical texts once (section "License texts", each under a deterministic ID such as `[T1]` (numbered by first use), referenced by every component that ships it; texts that differ in any byte stay separate), keeps every NOTICE file verbatim with its component, fails when the file grows over 1,000,000 bytes (the limit of the publication gate), records the origin of the embedded Mozilla root bundle and of the time zone data, and notes the run-time download of PostgreSQL in development mode. The output is deterministic, and the drift check fails when it differs from the committed file.
6. **The notices travel with the software and are public.** `LICENSE` and `THIRD_PARTY_NOTICES` are in every release archive and every container image built by the project, and `myriarchd` serves them at `/licenses` without authentication. They are static information about the build, contain no user data and are served as `text/plain` with a restrictive Content-Security-Policy. Settings > System links them.
7. **Release fixtures only in the development image variant.** `scripts/build-image-crane.sh` builds the variants `dev` (default; `devpg` and `/opt/myriarch/release-fixtures`) and `release` (no `devpg`, no fixtures). The production image does not carry test data from third parties.
8. **Still no project NOTICE file**, as decided in ADR-0018. `THIRD_PARTY_NOTICES` is a notice about third-party software, not a NOTICE file in the sense of Apache-2.0 section 4(d) for the project itself.

## Consequences

### Positive

- A machine-checkable statement of the license of every file, with attribution for third-party content, that a foundation review or a downstream packager can verify with a standard tool.
- Distributions of the binaries meet the notice obligations of their dependencies without manual work, and the drift check keeps the file current when dependencies change.
- White-label builds can replace the header text and the notices generator input without changing the mechanism.
- Production images are free of third-party test data.

### Negative

- Every new file needs a header or a `REUSE.toml` entry, and a dependency change needs `make notices`.
- `reuse` 5.1.1 builds from source on most platforms and needs Python 3.9 or newer; the pin has to move when Python 3.9 support is dropped.
- A public `/licenses` route is a small addition to the unauthenticated surface (covered by the route enumeration test and the security model).
- The npm license texts for packages without a license file are stored in the repository and must be refreshed with the package version.

## Alternatives considered

- **`addlicense -check` only**: covers source headers, but not files without comments, third-party attribution or license texts, and does not check the license of data files.
- **A project NOTICE file**: rejected in ADR-0018, because it creates obligations for every downstream redistributor.
- **Writing `THIRD_PARTY_NOTICES` by hand**: not reproducible and out of date after the next dependency change.
- **`go-licenses save`**: refuses modules it cannot classify and copies whole source trees of MPL modules; `go-licenses report` with the shared override list gives full control over the output.
- **Serving the notices only from the documentation site**: a binary that is moved to an air-gapped host would not carry them.
- **Per-file headers for JSON and YAML fixtures**: changes upstream bytes (checksums and signatures of fixtures) and is not possible in JSON.
