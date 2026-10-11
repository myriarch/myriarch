---
title: "Releasing"
weight: 110
description: "How to release Myriarch: the VERSION file, the release workflow and its two approvals, release candidates and final releases, re-runs, failures and the settings a maintainer has to make once."
---

This page is for maintainers who prepare and tag a release. It describes how the version is kept in one place, how the release
workflow turns a tag into a signed, verified and immutable release, what you do for a release candidate and for a final
release, and what to do when a job fails. The local gates are in the [development guide](development.md#gates). How users
check a release is in [Verify releases](security/verify-releases.md). The decision behind the workflow is
[ADR-0035](adr/0035-release-workflow-with-keyless-signing-and-verification-before-publication.md).

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

`ci/release/guard.sh` runs as the first step of the job `guard`, the first job of the release workflow. It reads the tag, the
commit, the repository, the API URL and the token from environment variables, matches each
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

The checks that need no network run first, and the API call comes last. After the guard, `ci/release/meta.sh` derives the outputs
that every later job uses (version, kind, pre-release flag, `latest` flag, image tags, the previous tag for the generated notes
and the Windows signing settings), and refuses a run whose `GITHUB_WORKFLOW_REF` is not
`myriarch/myriarch/.github/workflows/release.yml@refs/tags/<tag>`: a renamed workflow file or a call through a reusable workflow
would sign with another identity (see [Never](#never)).

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
per archive and one for the web UI, and `checksums.txt`. It holds no signing key and publishes nothing: the release workflow
signs keyless, and the Windows files get their Authenticode signature through a hook in stages (`MYRIARCH_WINDOWS_STAGE`), whose
mode comes from `deploy/release/windows-signing.env` (see [Windows signing rule](#windows-signing-rule) and
[ADR-0034](adr/0034-authenticode-signing-of-windows-release-artifacts.md)).

[`deploy/release/README.md`](../deploy/release/README.md) is the reference for the build: the assets and the contents of
`checksums.txt`, the stages `off`, `collect` and `apply`, the contract of the signing hook (`ci/release/windows-sign-hook.sh`),
the acceptance check of signed files, the release notes template and the commands. Two commands run the build locally:

```bash
make release-snapshot     # unsigned snapshot of the whole matrix
make release-rehearsal    # the path of a release in a snapshot, with a throwaway signing CA
```

## The release workflow

`.github/workflows/release.yml` runs when a tag `v*` is pushed, and only then. It builds the files, signs and attests them,
pushes the two images by digest, creates a **draft** release, verifies everything with the commands that users run, and publishes
only after you approved a second time. The release becomes immutable on publication. After that, smoke jobs download and install
the release the way a user does.

| Job | What it does | Approval |
|---|---|---|
| `guard` | Tag guard and `meta.sh` (above); the outputs of all later jobs. | |
| `tools` | Builds goreleaser, cosign, crane, syft, gh, go-winres, yq and osslsigncode (and cosign, crane for other platforms) once from `ci/tools`; no cache. | |
| `ui` | Builds the web UI (the only job with Node.js) and runs the release dependency and license gates. | |
| `build-windows` | `goreleaser build` of the Windows targets in stage `collect`. | |
| `sign-windows` | Runs the Windows signing hook (mode `off` in v0.1.0). | **Gate 1** |
| `package` | `goreleaser release --skip=publish` in stage `apply`, then stages the 40 release files. | |
| `images` | Builds the OCI layouts of both images from the release archives and their SBOMs. | |
| `sign-blobs` | Signs `checksums.txt` keyless and attests the build provenance of its 39 files. | |
| `push-images` | Pushes both images by digest (no release tags), signs them and attaches provenance and SBOM attestations. | |
| `draft` | Renders the release notes and creates the draft release with 42 assets. | |
| `verify` | Verifies signatures, provenance, images and the draft contents; writes the verified manifest and a summary. | |
| `verify-images` | Runs both images on amd64 and arm64. | |
| `verify-windows` | Checks version resources and Authenticode state of the Windows files on a Windows runner. | |
| `finalize` | Checks that the draft holds exactly the verified files, that both packages are public, tags the images, publishes the release and checks that it is immutable. | **Gate 2** |
| `smoke-assets`, `smoke-linux`, `smoke-windows`, `smoke-kind` | Anonymous downloads and verification, installers on amd64 and arm64, Windows staging, `docker run`, the Helm chart on kind. | |

Rules that hold for all of it:

- **The signing identity is frozen.** Every installed verifier checks `https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v*`.
  The file `release.yml` is never renamed, and signing never moves into a reusable workflow or another workflow
  ([ADR-0035](adr/0035-release-workflow-with-keyless-signing-and-verification-before-publication.md), point 10).
- **Build code never meets a privilege.** Rule R13 of `ci/check-workflows.py` keeps Go, Node.js, pnpm, goreleaser and make out of
  jobs with a write scope, `id-token`, `attestations` or an environment ([Security rules of the workflows](development.md#security-rules-of-the-workflows)).
  Privileged jobs run only the scripts in `ci/release/`.
- **Artifacts between jobs** are tar files, downloaded by artifact ID and checked against the SHA-256 that the producing job
  reported. They are kept **one day**. This matters for gate 2 (below).
- **Two approvals.** Exactly the jobs `sign-windows` and `finalize` use the environment `release`. Each asks for its own approval.
- **No secrets** exist in v0.1.0. The jobs use `GITHUB_TOKEN` and the OIDC token of the run.

## Before the first release

These settings are made by a maintainer in GitHub, once. The workflow cannot check most of them, and an agent never changes them.

1. **Release immutability.** Enable it for the organization (organization settings, Repository, General, Releases, "All
   repositories") or for the repository before you tag the first candidate. It applies **only to releases published after you
   enable it**, and the job token cannot read the setting, so `finalize` detects a missing setting only after it published
   (`release_not_immutable`).
2. **Environment `release`.** You are the required reviewer. Switch "Prevent self-review" off while you are the only reviewer.
   Administrators cannot bypass the protection rules. Deployment tags: `v*` only. No wait timer. The environment holds no secret
   in v0.1.0.
3. **Tag ruleset and Actions policy.** Only you create `v*` tags; they cannot be updated or deleted. `GITHUB_TOKEN` is read-only
   by default, actions must be pinned to a full SHA, the allowed actions are the GitHub-owned ones plus `ossf/scorecard-action@*`,
   and Actions cannot create or approve pull requests.
4. **Packages.** The organization allows public packages. Do not create `ghcr.io/myriarch/myriarch` or
   `ghcr.io/myriarch/myriarch-updater` by hand: the first push of the first candidate creates them, and linking them to the
   repository happens through the image label `org.opencontainers.image.source`.
5. **A second owner** of the organization exists for emergencies ([ADR-0029](adr/0029-host-under-the-myriarch-organization.md)).
   Until a second person can approve gate 2, the two gates are a review step by one person.

## Release a candidate

Every candidate, the first one included, goes through these steps. A candidate is a GitHub pre-release and never the latest
release.

1. **Prepare.** Open a pull request that runs `scripts/set-version.sh X.Y.Z-rc.N` (see [One version source](#one-version-source))
   and has the entries of the candidate under "Unreleased" in `CHANGELOG.md`. Squash-merge it.
2. **Wait for `ci-ok`** on the merge commit of `main`. The guard refuses a commit without it.
3. **Tag** the merge commit with an annotated tag and push it:

   ```bash
   git tag -a vX.Y.Z-rc.N -m vX.Y.Z-rc.N <merge-commit-sha>      # -s instead of -a when you have a signing key
   git push origin vX.Y.Z-rc.N
   ```

4. **Approve gate 1** (`sign-windows`) in the run of the workflow `release`. Nothing is signed or pushed to a registry before
   you do.
5. **Review the draft.** When `draft`, `verify`, `verify-images` and `verify-windows` are green, open the draft release and the
   summary of the job `verify`, and check:
   - the draft has 42 assets and the pre-release flag (a candidate) is set;
   - the notes show the expected banners (the release candidate banner; the unsigned banner while `WINDOWS_SIGNING=off`) and
     the right changes section;
   - the digests of both images in the notes equal the digests in the summary of `verify`;
   - the summary names the identity `https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/vX.Y.Z-rc.N`
     and the commit you tagged.
6. **First release only: make both packages public.** After `push-images` of the first candidate and before gate 2, an
   organization owner opens each package (organization `myriarch`, Packages, `myriarch` and then `myriarch-updater`), then
   Package settings, "Danger Zone", "Change visibility", Public. This **cannot be undone**. While you are there, check that the
   package is connected to the repository `myriarch/myriarch` and that the repository has Write access in "Manage Actions access".
   `finalize` fails with `ghcr_not_public` until both are public.
7. **Approve gate 2** (`finalize`) **within 24 hours of the start of the run**, because the artifacts expire after one day (see
   [Re-runs](#rc-numbering-and-re-runs)). `finalize` tags the images, publishes the release, and checks that GitHub made it
   immutable.
8. **Check the smoke jobs** (`smoke-assets`, `smoke-linux`, `smoke-windows`, `smoke-kind`). They run after publication. A failure
   here is fixed forward (see [When something fails](#when-something-fails)).

## Release a final version

A final release is a normal release and is marked as the latest release when it is the highest final tag. Its image tags are
`X.Y.Z` and the moving `X.Y` (which moves only forward; `latest` is never set while the major version is 0).

1. Open the pull request "Prepare vX.Y.Z": `scripts/set-version.sh X.Y.Z`, the heading `## [X.Y.Z] - YYYY-MM-DD` in
   `CHANGELOG.md` with an empty "Unreleased" section above it, and, **only for a reviewed exception**, `WINDOWS_UNSIGNED_FINAL_TAG`
   in `deploy/release/windows-signing.env` (see [Windows signing rule](#windows-signing-rule)). Remove the exception again in the
   next pull request after the release.
2. Follow steps 2 to 8 of [Release a candidate](#release-a-candidate) with the tag `vX.Y.Z`. Step 6 is not needed any more.
3. A final tag can be used **once**. If a problem shows up after publication, fix it and release `vX.Y.(Z+1)`.

## rc numbering and re-runs

- **Every attempt that needs a code or content change is a new candidate** (`rc.N+1`), prepared by a pull request that bumps
  `VERSION`. A tag is never moved, deleted or reused.
- **"Re-run failed jobs"** is the answer to a transient failure (a runner, a network or a registry error, an outage of GitHub or
  Sigstore). The approvals are asked again, stale drafts of the tag are deleted and replaced, a second push of the same digest
  changes nothing, duplicate signatures and attestations are harmless, and `finalize` resumes after a run in which it had
  already published.
- **Artifacts expire after one day.** If gate 2 waits longer than that, or a job has to be repeated after that, use "Re-run all
  jobs": it builds a new draft and new signatures. The image digests stay the same, because the images are reproducible.

## When something fails

| Job | Cause | What to do |
|---|---|---|
| `guard` | A guard check or `workflow_ref_mismatch` | The message names the cause. Fix it in a pull request and tag the next candidate, or wait for `ci-ok` and re-run. |
| `tools`, `ui`, `build-windows`, `package`, `images` | A build, a gate (dependencies, licenses, notices) or an artifact check failed | Fix it in a pull request and tag the next candidate. |
| `sign-windows` | The signing provider failed (v0.1.0: only the copy of the files) | Re-run. A provider issue is handled in the provider's change. |
| `sign-blobs`, `push-images` | Sigstore or ghcr.io unavailable, a token problem | Re-run failed jobs. |
| `draft` | `release_exists` or `draft_foreign` | A published release of the tag exists, or someone else made a draft of it: tag the next candidate, or delete the foreign draft by hand and re-run. |
| `verify`, `verify-images`, `verify-windows` | A verification failed | **Do not approve gate 2.** Fix forward with the next candidate. |
| `finalize`, `ghcr_not_public` | A package is still private | Make both packages public (step 6 above), then "Re-run failed jobs". |
| `finalize`, `image_tag_conflict` | An immutable image tag points to another digest | Investigate. Never move an immutable tag. |
| `finalize`, `release_not_immutable` | The release is published but not immutable | The release stays mutable. Enable release immutability before the next tag, and announce nothing until it is fixed. |
| `smoke-*` | A problem found after publication | The release is immutable: fix forward in the next version and describe the problem in the release notes, which stay editable. |

**Abandoned candidates.** Delete the draft of a candidate that never went through gate 2 with
`gh release delete <tag> --repo myriarch/myriarch --yes`, **without** `--cleanup-tag`. Untagged image versions left by a failed
candidate may stay. Delete a package version only after you checked that no published release uses it, and never delete the
`sha256-<hex>` tags of a released digest: they hold the signatures and attestations that verification needs.

## Never

- Publish the draft by hand. `finalize` binds the publication to the verified files.
- Move, re-create or delete a tag, or delete a published release.
- Rename `.github/workflows/release.yml`, call it as a reusable workflow, or move signing into another workflow.
- Add `workflow_dispatch` or any other trigger to the release workflow.
- Delete `sha256-<hex>` tags or package versions of a released image.
- Change the settings of [Before the first release](#before-the-first-release) from a script or an agent.
