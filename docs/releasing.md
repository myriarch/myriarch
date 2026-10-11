---
title: "Releasing"
weight: 110
description: "How a Myriarch release is versioned and which checks a release tag must pass: the VERSION file, release candidates, image tags, the tag guard and the release build."
---

This page is for maintainers who prepare and tag a release. It describes how the version is kept in one place, what the
tag guard refuses and where the release build is documented. The local gates are in the [development guide](development.md#gates).

## Versioning

Myriarch uses [semantic versioning](https://semver.org/) in two forms only: `X.Y.Z` for a final release and `X.Y.Z-rc.N` for a
release candidate. Numbers have no leading zeros, and the rc number starts at 1. Tags are the version with a leading `v`
(`v0.1.0-rc.1`, `v0.1.0`). Other forms (`-beta.1`, `-rc.0`, `+build`) are not release tags.

### One version source

The file `VERSION` at the repository root holds the **next tag** without the leading `v`, for example `0.1.0-rc.1`. It names the
release that the next tag will publish, not the last one. Each release candidate bumps it in the pull request that carries the
fixes for the previous candidate, and the pull request that prepares a final release sets it to `X.Y.Z`.

Every other place that states the product version is a copy of `VERSION`:

| Copy | What it is |
|---|---|
| `deploy/helm/myriarch/Chart.yaml`, `version` | Version of the Helm chart. |
| `deploy/helm/myriarch/Chart.yaml`, `appVersion` | Default image tag of the chart (`image.tag` is empty by default). |
| `api/openapi.yaml`, `info.version` | Contract version of the REST API. It equals the release version. |
| `web/package.json`, `version` | Version of the web UI package. |
| `deploy/examples/production/myriarch-values.yaml`, `image.tag` | Image tag in the production example. |
| `gitops-template/.gitlab-ci.yml`, `MYRCTL_IMAGE` | Tag of the placeholder `myrctl` image in the GitOps template (the project publishes no `myrctl` image; you build it from the `myrctl` release archive). |

The list lives in `ci/version-copies.py`. The binaries do not have a copy: they receive the version from the tag at build time.

Change the version with one command and commit the result:

```bash
scripts/set-version.sh 0.1.0-rc.2
```

The script accepts only `X.Y.Z` or `X.Y.Z-rc.N`. It writes `VERSION` and all copies, then runs `make docs-gen` and
`pnpm gen:api` in `web/`, so the generated REST reference (`docs/reference/api/`) and the web UI types stay in step. It needs
Go, Node.js and pnpm with the dependencies of `web/` installed.

`ci/check-versions.sh` fails when `VERSION` is not a valid version or a copy differs from it. It is part of `ci/check.sh`, so
every pull request runs it, and the tag guard runs it again on the tagged commit. A failure names the file and tells you to run
`scripts/set-version.sh`.

### Release candidates and the final release

- A release candidate is published as a GitHub **pre-release**. A final release is a normal release and is marked as the latest
  release; its notes carry the support statement for the alpha phase.
- A published tag is never moved or reused. A defect found in `v0.1.0-rc.1` is fixed in a pull request that also sets `VERSION`
  to `0.1.0-rc.2`, and the next tag is `v0.1.0-rc.2`. A defect in a final release is fixed in the next patch release.
- The pull request that prepares a final release sets `VERSION` to `X.Y.Z`, turns the "Unreleased" section of `CHANGELOG.md`
  into the heading `## [X.Y.Z] - YYYY-MM-DD` and checks `deploy/release/windows-signing.env` (see [Tag guard](#tag-guard)).

### Image tags

The container images carry the version without the `v`:

| Tag pushed | Image tags |
|---|---|
| `v0.1.0-rc.2` | `0.1.0-rc.2` |
| `v0.1.0` | `0.1.0` and `0.1` |

There is no `latest` tag while the major version is 0. Pin a version or, better, a digest.

## Tag guard

`ci/release/guard.sh` is written to run as the first job of the release workflow (the workflow itself is added in a later
change). It reads the tag, the commit, the repository, the API URL and the token from environment variables, matches each
value as a whole (a value with a newline or another control character is refused), never prints the token, and refuses a
release with one clear message per cause. It checks, in this order:

1. The tag name is `vX.Y.Z` or `vX.Y.Z-rc.N` without leading zeros, and the commit is a full SHA.
2. The tag is an annotated tag. The guard fetches the tag object from `origin` again, because a checkout can turn an annotated
   tag into a lightweight one.
3. The tag points at the commit that the run was started for, the checkout is at that commit and has no changes.
4. The commit is on `origin/main`: a release is built only from merged history.
5. `VERSION` equals the tag without the `v`, and every copy equals `VERSION` (`ci/check-versions.sh`).
6. A final tag has a heading `## [X.Y.Z] - YYYY-MM-DD` with a valid date in `CHANGELOG.md` (a heading inside a code block or
   an HTML comment does not count), the section under it has at least one list item, and the "Unreleased" section has none
   left. An rc tag needs at least one list item in the "Unreleased" section; prose and comments are no entries.
7. The Windows signing rule holds (below).
8. The check run `ci-ok` of GitHub Actions exists for the commit and has finished with `success`. The guard asks the GitHub REST
   API with `curl`; the token needs only `checks: read`. Any API error, an unexpected answer or a check run from another app
   refuses the release (fail closed).

The checks that need no network run first, and the API call comes last.

### Windows signing rule

`deploy/release/windows-signing.env` holds two settings that the guard reads as data:

- `WINDOWS_SIGNING` is `off`, `signpath` or `kms`. Any other value fails closed.
- `WINDOWS_UNSIGNED_FINAL_TAG` is empty or names one final tag, for example `v0.1.0`.

Release candidates may use `off`. A final tag with `WINDOWS_SIGNING=off` passes only when `WINDOWS_UNSIGNED_FINAL_TAG` is exactly
that tag; an empty value, another tag or a pattern fails. A final tag with `signpath` or `kms` needs no exception. The exception
is therefore a reviewed change in the pull request that prepares the release, it covers one tag, and the next final release
needs signing or a new reviewed exception. The release notes of a release under the exception show an "unsigned" banner.

### Run the guard locally

The guard needs a checkout of the tag with `origin` as the remote that has the tag and `main`, and a token. The test suite
`ci/tests/versions-test.sh` runs it against temporary repositories and a stub of the API:

```bash
ci/tests/versions-test.sh
```

## Release build

The release build (`deploy/release/goreleaser.yaml`) turns a tagged commit into the archives, the bare updater binaries, one SBOM
per archive and one for the web UI, and `checksums.txt`. It holds no signing key: the Sigstore signature is made by the release workflow, and the Windows
files get their Authenticode signature through a hook in stages (`MYRIARCH_WINDOWS_STAGE`), whose mode comes from
`deploy/release/windows-signing.env` (see [Windows signing rule](#windows-signing-rule) and
[ADR-0034](adr/0034-authenticode-signing-of-windows-release-artifacts.md)). Nothing in the repository publishes a release.

[`deploy/release/README.md`](../deploy/release/README.md) is the reference for the
build: the assets and the contents of `checksums.txt`, the stages `off`, `collect` and `apply`, the contract of the signing hook
(`ci/release/windows-sign-hook.sh`), the acceptance check of signed files and the commands. Two commands run the build locally:

```bash
make release-snapshot     # unsigned snapshot of the whole matrix
make release-rehearsal    # the path of a release in a snapshot, with a throwaway signing CA
```
