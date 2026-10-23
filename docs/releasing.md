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
| `deploy/helm/myriarch/Chart.yaml`, `version` | Version of the Helm chart in the repository. The release workflow packages the chart with `--version` and `--app-version` set to the tag without `v`, so the published OCI chart always has the version of the release. |
| `deploy/helm/myriarch/Chart.yaml`, `appVersion` | Default image tag of the chart (`image.tag` is empty by default). |
| `api/openapi.yaml`, `info.version` | Contract version of the REST API. It equals the release version. |
| `web/package.json`, `version` | Version of the web UI package. |
| `deploy/examples/production/myriarch-values.yaml`, `image.tag` | Image tag in the production example. |
| `gitops-template/.gitlab-ci.yml`, `MYRCTL_IMAGE` | Tag of the placeholder `myrctl` image in the GitOps template (the project publishes no `myrctl` image; you build it from the `myrctl` release archive). |
| `deploy/compose/quickstart.yaml`, `x-image` | Default image tag of the Compose quickstart (`QUICKSTART_IMAGE` replaces the whole image). |

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
pushes the two images and the two Helm charts (the server chart and the collector chart, both signed like the images) by digest, creates a **draft** release, verifies everything with the commands that users run, and publishes
only after you approved a second time. The release becomes immutable on publication. After that, smoke jobs download and install
the release the way a user does.

| Job | What it does | Approval |
|---|---|---|
| `guard` | Tag guard and `meta.sh` (above); the outputs of all later jobs. | |
| `signing-tools` | Builds cosign, crane, gh and helm (and cosign, crane for other platforms) once from `ci/tools` on a runner of its own, before anything else is installed or run there; no cache. The only tools the privileged jobs execute. | |
| `build-tools` | Builds goreleaser, syft, yq, go-winres and osslsigncode once from `ci/tools`; no cache. Only jobs with `contents: read` execute them. | |
| `ui` | Builds the web UI (the only job with Node.js) and runs the release dependency and license gates. | |
| `build-windows` | `goreleaser build` of the Windows targets in stage `collect`. | |
| `sign-windows` | Runs the Windows signing hook (mode `off` in v0.1.0). | **Gate 1** |
| `package` | `goreleaser release --skip=publish` in stage `apply`, then stages the 40 release files. | |
| `images` | Builds the OCI layouts of both images from the release archives and their SBOMs. | |
| `sign-blobs` | Signs `checksums.txt` keyless and attests the build provenance of its 39 files. | |
| `push-images` | Pushes both images by digest (no release tags), signs them and attaches provenance and SBOM attestations. | |
| `chart` | Packages the server chart with the digest of the server image written into `image.digest` and the collector chart with the digest of the updater image written into `images.updater.digest` (`ci/release/chart-package.sh`; chart version = app version = the tag without `v`; the two upstream images of the collector chart stay at the pins of `ci/versions.env`), checks the renderings, and turns each package into an OCI layout (exactly what `helm push` writes). It holds `contents: read` only and hands the layout on as an artifact. | |
| `push-chart` | Pushes the layouts to `ghcr.io/myriarch/charts/myriarch` and `ghcr.io/myriarch/charts/myriarch-collector` by digest, without a tag, signs each keyless and attaches the provenance attestation, one chart after the other. It runs after `push-images`. The version tags of the charts are set by `finalize`, like the tags of the images. | |
| `draft` | Renders the release notes and creates the draft release with 42 assets. It holds `contents: write` and runs before gate 2. | |
| `verify` | Verifies signatures, provenance and images, and the files from which the draft was created (not the draft itself); writes the verified manifest and a summary. | |
| `verify-images` | Runs both images on amd64 and arm64. | |
| `verify-chart` | Verifies the pushed chart by digest: that the version tag does not exist yet (or, in a re-run after `finalize`, points at this digest), the chart layer (every image of the `helm template` renderings is the image of this run that the chart references: the server image, or for the collector chart the updater image and the two upstream pins), the signature and the provenance, for both charts. | |
| `verify-windows` | Checks version resources and Authenticode state of the Windows files on a Windows runner. | |
| `finalize` | Binds the draft to the verified files (GitHub's asset digests), checks that the tag still points at the tagged commit and that all four packages (both images and both charts) are public, tags the images and the charts (`X.Y.Z[-rc.N]`), reads the draft again directly before it publishes, publishes the release, compares the published release with the verified files once more and checks that it is immutable. | **Gate 2** |
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
- **Two approvals.** In the release workflow, exactly the jobs `sign-windows` and `finalize` use the environment `release`. The only other job
  with that environment is `publish` of `.github/workflows/artifacthub.yml` ([Artifact Hub](#artifact-hub)). Each of the two asks for its own approval.
  A gate is passed once per run, not once per attempt (see [Re-runs](#rc-numbering-and-re-runs)), and only `finalize` is stopped
  by gate 2: the job `draft` holds `contents: write` without the environment and runs before it.
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
4. **Packages.** The organization allows public packages. Do not create `ghcr.io/myriarch/myriarch`,
   `ghcr.io/myriarch/myriarch-updater` or the charts below `ghcr.io/myriarch/charts/` by hand: the first push of the first candidate creates them, and linking them to the
   repository happens through the image label `org.opencontainers.image.source`.
5. **A second owner** of the organization exists for emergencies ([ADR-0029](adr/0029-host-under-the-myriarch-organization.md)).
   Until a second person can approve gate 2, the two gates are a review step by one person.

### First candidate: what to observe

Three things cannot be tested before the first candidate, and the first candidate is where they are settled. Record the result in
the pull request of [After every publication](#after-every-publication), or in a follow-up pull request if the re-run has not
been observed yet.

1. **Run the kind smoke test once before you tag.** `smoke-kind` runs only after publication, where a failure can only be fixed
   forward, and no job of `ci.yml` or of the release workflow runs the probe before the tag (`e2e-kind.yml` runs `make kind-probe`
   every night, outside `ci-ok`, and it is no gate of the release). Run `make kind-probe` on a machine with a container runtime (the
   same cluster configuration and chart) or read the last green run of `e2e-kind.yml` on the commit, and fix what it finds before the
   tag. `ci/release/smoke.sh kind` is not a substitute: it always installs the published image of the tag, which does not exist before
   the tag.
2. **Observe re-run approvals.** After the first candidate went through gate 2, re-run one failed or completed job on a throw-away
   candidate if you can, and write down whether "Re-run failed jobs" asks for gate 1 or gate 2 again. The runbook and the security
   model state the safe reading (no new approval for the privileged jobs before `finalize`) until you have observed it.
3. **Read the `smoke-windows` log for the ACL of `cosign.exe`.** The job sets the owner and the ACL of the directory that holds
   `cosign.exe` with `icacls` (best effort) so that `Test-AdminOnlyDirectory` of the installer accepts it. Neither the `icacls`
   calls nor that function have run on a Windows runner before the first publication, and no CI row tests them. If the log shows
   the warning `icacls could not set the owner and the ACL` or the installer stops with `directory_unsafe`, fix it forward and
   add a Windows CI step that creates an administrator-only and a user-writable directory and runs the real function on both.

The first published candidate is `v0.1.0-rc.3` (`v0.1.0-rc.1` and `v0.1.0-rc.2` stopped before the publication). Its results:

- **Kind:** `make kind-probe` passed before the first tag, and `smoke-kind` passed on `v0.1.0-rc.3`.
- **Re-run approvals:** "Re-run failed jobs" after gate 2 re-ran only the failed row of `smoke-linux` (HTTP 504 errors of
  downloads from github.com during a GitHub incident) and asked for no approval. No job of the environment `release` ran again,
  so whether `finalize` asks for gate 2 again after an approved attempt is still open, and the safe reading stays.
- **The directory of `cosign.exe`:** `smoke-windows` logged no `icacls` warning, and the installer accepted
  `C:\Program Files\cosign` on the Windows runner (`Test-AdminOnlyDirectory` runs on Windows in staging mode too). The refusal
  of a directory that unprivileged accounts can write is still tested only with test doubles.

## Release a candidate

Every candidate, the first one included, goes through these steps. A candidate is a GitHub pre-release and never the latest
release.

1. **Prepare.** Open a pull request that runs `scripts/set-version.sh X.Y.Z-rc.N` (see [One version source](#one-version-source))
   and has the entries of the candidate under "Unreleased" in `CHANGELOG.md`. In the same pull request, or one merged before it,
   do the [review of the texts](#review-the-texts-before-every-tag): the archives cannot be corrected after the tag. Squash-merge it.
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
   - the draft has 42 assets and the pre-release flag (a candidate) is set. `verify` checked the files and images from which the
     draft was created, not the draft: the count is yours to check, and `finalize` binds the draft to the verified files by
     their digests;
   - the notes show the expected banners (the release candidate banner; the unsigned banner while `WINDOWS_SIGNING=off`) and
     the right changes section;
   - the digests of both images in the notes equal the digests in the summary of `verify`;
   - the summary names the identity `https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/vX.Y.Z-rc.N`
     and the commit you tagged.
6. **First release only: make all four packages public.** After `push-images` and `push-chart` of the first candidate and before
   gate 2, an organization owner opens each package (organization `myriarch`, Packages, `myriarch`, `myriarch-updater` and then
   `charts/myriarch` and `charts/myriarch-collector`, the charts, which exist from v0.2.0-rc.1 on), then
   Package settings, "Danger Zone", "Change visibility", Public. This **cannot be undone**. While you are there, check that the
   package is connected to the repository `myriarch/myriarch` and that the repository has Write access in "Manage Actions access".
   `finalize` fails with `ghcr_not_public` until all are public.
7. **Approve gate 2** (`finalize`) **within 24 hours of the start of the run**, because the artifacts expire after one day (see
   [Re-runs](#rc-numbering-and-re-runs)). `finalize` tags the images and the charts, publishes the release, and checks that GitHub made it
   immutable.
8. **Check the smoke jobs** (`smoke-assets`, `smoke-linux`, `smoke-windows`, `smoke-kind`). They run after publication. A failure
   here is fixed forward (see [When something fails](#when-something-fails)).
9. **Update the status** in a pull request after the publication, see [After every publication](#after-every-publication).

## Release a final version

A final release is a normal release and is marked as the latest release when it is the highest final tag. Its image tags are
`X.Y.Z` and the moving `X.Y` (which moves only forward; `latest` is never set while the major version is 0).

1. Open the pull request "Prepare vX.Y.Z": `scripts/set-version.sh X.Y.Z`, the heading `## [X.Y.Z] - YYYY-MM-DD` in
   `CHANGELOG.md` with an empty "Unreleased" section above it, and, **only for a reviewed exception**, `WINDOWS_UNSIGNED_FINAL_TAG`
   in `deploy/release/windows-signing.env` (see [Windows signing rule](#windows-signing-rule)). Remove the exception again in the
   next pull request after the release.
   The same pull request turns the texts that describe the phase before the final release into texts for the final one: the
   "Status" section of `README.md`, "Supported versions" in `SECURITY.md` and the first paragraph of "Unreleased" in
   `CHANGELOG.md` (before v0.1.0, all three said that v0.1.0 is published first as release candidates). `README.md` and
   `CHANGELOG.md` are in the archives, so they change before the tag, not after it.
2. Follow steps 2 to 9 of [Release a candidate](#release-a-candidate) with the tag `vX.Y.Z`. Step 6 is not needed any more.
3. A final tag can be used **once**. If a problem shows up after publication, fix it and release `vX.Y.(Z+1)`.

## Review the texts before every tag

A release archive is immutable. Some repository files are copied into it, and the text in them is read for as long as the release
exists, long after the sentence "no release has been published yet" stopped being true. Read the files below against the
"Unreleased" section of `CHANGELOG.md` and the changes since the last tag before you tag. `deploy/release/goreleaser.yaml`
(`archives`, `files`) is the authority; this list is its state at the time of writing:

| Archive | Repository files in it |
|---|---|
| `myriarchd_*` (the server image is built from the `myriarchd`, `myrctl` and `myriarch-sim` archives) | `README.md`, `LICENSE`, `THIRD_PARTY_NOTICES`, `deploy/helm/`, `deploy/compose/`, `deploy/docker/`, `examples/quickstart/` |
| `myrctl_*` | `README.md`, `CHANGELOG.md`, `LICENSE`, `THIRD_PARTY_NOTICES` |
| `myriarch-updater_*` | `deploy/scripts/` (the installer of the platform), `deploy/supervisor/`, `deploy/systemd/`, `deploy/updater/`, `deploy/windows/` (`README.md`; on Windows also `Set-ServiceEnvironment.ps1`), `LICENSE`, `THIRD_PARTY_NOTICES` |
| `myriarch-sim_*` | `examples/sim/`, `LICENSE`, `THIRD_PARTY_NOTICES` |

The notes of a candidate are rendered from the "Unreleased" section of `CHANGELOG.md` (`ci/release/notes.sh`), so that section is
read as part of the release as well.

1. **Compare, do not trust.** Read `README.md`, the pages of `docs/` and the website against the changelog and the commits since
   the last tag: commands, flags, defaults, version numbers, platforms, support levels and what is signed. A sentence that claims
   more than the release delivers (a signed Windows file, a signed macOS binary, an OCI chart) is wrong, whatever the plan says.
2. **Files in the archives describe the process.** Every sentence must be true when the archive is created and still true after
   publication, until the next version. Write "release candidates are published as GitHub pre-releases", not "no release has
   been published yet", "available from v0.1.0" or "will be published". Search the files of the table:

   ```bash
   grep -rniE "no release|not (yet )?published|first release|available from|planned for|will be (published|released)|/v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?/|--branch v[0-9]" \
     README.md CHANGELOG.md deploy examples
   ```

   Every hit is read, not only the ones that look wrong. Commands that fetch a file or a branch of a tag must not name a tag that
   does not exist yet: use a placeholder such as `v<version>`.
3. **Other texts may name the state, but only if the update after publication changes them.** `SECURITY.md`, `docs/` and the
   website are not in the archives. Prefer wording that needs no edit; where a page must name the state, it is on the list of
   [After every publication](#after-every-publication).
4. **Run the gates for the texts:** `make docs-check` and `make website-check`.

## After every publication

When `finalize` is green and the smoke jobs have been checked, open a pull request that updates the status. These files are not in
the archives, so they can follow the release:

1. **`website/data/release.yaml`**, the single source of the release status. After a candidate set `candidate: "vX.Y.Z-rc.N"` (and
   `final: ""`); after a final release set `final: "vX.Y.Z"` and `candidate: ""`. The status chip of the landing page
   (`website/layouts/_partials/landing/hero.html`) is derived from it, and `website/scripts/check-landing.py` fails when the
   chip and the file differ. So is the `version` in the structured data of the home page (`website/layouts/_partials/seo/`), which
   `website/scripts/check-seo.py` compares with the file. A candidate of a later version does not replace a final release in the chip. The release workflow
   never writes this file (it has no write access to the repository), and `README.md` and `docs/` cannot read it: they are plain
   Markdown, which is why they describe the process.
2. **The pages that say whether images exist or name a release:** `docs/getting-started/_index.md`,
   `docs/getting-started/docker-compose.md`, `docs/operations/install-helm.md` and `docs/security/verify-releases.md`. Check that every tag they name exists and that
   their sentences about the images are true.
3. **The deferred observations:** after the first candidate, write the results of the three observations of
   [First candidate](#first-candidate-what-to-observe) in this pull request, the one on re-run approvals also in
   `docs/security/model.md`, and remove the wording that defers them. The kind result is known from before the tag and the
   result of the `cosign.exe` directory from the log of `smoke-windows`; the re-run observation may need a throw-away
   candidate, so it can follow in a pull request of its own.

Texts in the archives (the files in the table of [Review the texts before every tag](#review-the-texts-before-every-tag), among
them `README.md`, `CHANGELOG.md` and the chart) are not touched here: they are right already, or the next release corrects them.
Other files of the repository, such as `deploy/README.md`, are not in an archive and can follow the release like the pages above.

## Artifact Hub

Artifact Hub lists the server chart (`oci://ghcr.io/myriarch/charts/myriarch`) and shows it as "Verified publisher" when the chart
repository holds a metadata file under the tag `artifacthub.io`. The file carries the repository ID, which exists only after the board has
added the repository on Artifact Hub, and that needs a published chart. So the upload is not a job of the release workflow: a workflow of its
own, `.github/workflows/artifacthub.yml`, does it after the release, started by hand. Do the steps in this order:

1. **Publish the chart.** The release of a candidate or of a final version has finished (`finalize` is green), so the chart repository has a
   version tag. The workflow refuses to run without one (`chart_not_published`).
2. **Create the repository on Artifact Hub** (the board, once). Sign in at <https://artifacthub.io>, accept the terms of use and open the control
   panel. Add a repository of the kind "Helm charts" with the name `myriarch`, the display name `Myriarch` and the URL
   `oci://ghcr.io/myriarch/charts/myriarch`.
3. **Copy the repository ID** from the card of the repository in the control panel. It is a UUID in lower case.
4. **Run the workflow on the release tag.** On GitHub open Actions, "artifacthub", Run workflow, choose the release tag (for example `v0.2.0`)
   under "Use workflow from", and enter the ID. A branch, `main` included, and any other tag are refused: the environment `release` admits only
   tags `v*`, and the script checks the tag shape again. The workflow file has to be on `main` and in the tag; a fix to it takes effect with the
   next tag, because a tag is never moved. The job `tools` builds the pinned `crane`; the job `publish` waits for the approval in the
   environment `release` (three jobs use that environment: `sign-windows` and `finalize` of the release workflow and `publish` of this one) and
   shows the digest of the uploaded artifact in its job summary. The ID becomes part of a public file, so enter nothing else.
   The workflow writes the tag `artifacthub.io` of `ghcr.io/myriarch/charts/myriarch` and nothing else. It can run again (it overwrites only that
   tag) and works the same whether the repository was added after a candidate or after the final version.
5. **Check the result.** After the next scan of the repository, Artifact Hub shows "Verified publisher" on the repository card (the control
   panel shows the time of the last scan and its errors).

The file has exactly one key, `repositoryID`. It has no `owners`; an owners entry would put an e-mail address into a public file, and
the board has to ask for it explicitly, in a change of its own.

Artifact Hub lists every version tag of the repository, release candidates included. A repository that is added after a candidate therefore
shows the candidate (as a pre-release) until the final version is published.

## rc numbering and re-runs

- **Every attempt that needs a code or content change is a new candidate** (`rc.N+1`), prepared by a pull request that bumps
  `VERSION`. A tag is never moved, deleted or reused.
- **"Re-run failed jobs"** is the answer to a transient failure (a runner, a network or a registry error, an outage of GitHub or
  Sigstore). It re-runs only the failed jobs and the jobs that depend on them. **A gate is passed once per run:** a `sign-windows`
  that already passed is not asked again, so the privileged jobs `sign-blobs`, `push-images` and `draft` run again without a new
  approval, by anyone with write access to Actions. Whether `finalize` asks again after an approved attempt is not documented
  by GitHub in a form we could confirm, and the first published candidate, `v0.1.0-rc.3`, did not re-run it
  (see [First candidate](#first-candidate-what-to-observe)).
  Stale drafts of the tag are deleted and replaced, a second push of the same digest changes nothing, duplicate signatures and
  attestations are harmless, and `finalize` resumes after a run in which it had already published.
- **The resume path of `finalize`** (an attempt above 1) accepts a release that is already published when its assets are exactly
  the verified files. It cannot tell a publication by `finalize` from one made by hand between two attempts, which is forbidden
  (see [Never](#never)). After a re-run, check in the log of the failed attempt that `finalize` itself got as far as the PATCH.
- **Every run leaves valid credentials for the tag**, also a failed or abandoned one and each "Re-run all jobs" attempt: the
  signature bundle of `checksums.txt`, the signatures and attestations of the images and the build provenance. Deleting a draft
  does not revoke them. Proof that a file was released is `gh release verify-asset <tag> <file>`, not a valid signature.
- **Artifacts expire after one day.** If gate 2 waits longer than that, or a job has to be repeated after that, use "Re-run all
  jobs": it builds a new draft and new signatures. The image digests stay the same, because the images are reproducible.

## When something fails

| Job | Cause | What to do |
|---|---|---|
| `guard` | A guard check or `workflow_ref_mismatch` | The message names the cause. Fix it in a pull request and tag the next candidate, or wait for `ci-ok` and re-run. |
| `signing-tools`, `build-tools`, `ui`, `build-windows`, `package`, `images` | A build, a gate (dependencies, licenses, notices) or an artifact check failed | Fix it in a pull request and tag the next candidate. |
| `sign-windows` | The signing provider failed (v0.1.0: only the copy of the files) | Re-run. A provider issue is handled in the provider's change. |
| `sign-blobs`, `push-images` | Sigstore or ghcr.io unavailable, a token problem | Re-run failed jobs. |
| `push-chart` | Sigstore or ghcr.io unavailable, a token problem | Re-run failed jobs. The charts are pushed by digest and have no tag yet, so pushing the same digest again changes nothing and no manual deletion is needed. |
| `draft` | `release_exists` or `draft_foreign` | A published release of the tag exists, or someone else made a draft of it: tag the next candidate (the next patch version for a final tag), or delete the foreign draft by hand and re-run. |
| `verify`, `verify-images`, `verify-chart`, `verify-windows` | A verification failed | **Do not approve gate 2.** Fix forward with the next candidate. |
| `finalize`, `ghcr_not_public` | A package is still private | Make all packages public (step 6 above), then "Re-run failed jobs". |
| `finalize`, `image_tag_conflict` | An immutable image tag points to another digest | Investigate. Never move an immutable tag. |
| `finalize`, `release_not_immutable` | The release is published but not immutable | The release stays mutable. Enable release immutability before the next tag, and announce nothing until it is fixed. |
| `finalize`, `tag_moved` | `refs/tags/<tag>` does not exist (any more) or does not resolve to the tagged commit | Nothing was published. The tag was moved, re-created or deleted, against the ruleset: investigate, do not approve again, tag the next candidate. |
| `finalize`, `draft_foreign`, `draft_ambiguous`, `release_not_draft` | The release is not the draft that the workflow made, the tag has a second release, or the draft was published by hand; every re-read of the release repeats these checks | Nothing was published by this job (after the PATCH: see `release_assets_changed`). Investigate who changed the release, do not approve again, tag the next candidate. |
| `finalize`, `release_assets_changed` | After the PATCH the release (now immutable) differs from the verified files | **Do not use this release and do not announce it.** It cannot be changed or deleted into shape; say in its notes that it is void and publish the next version. |
| `finalize`, `release_state`, `release_attestation`, `release_asset_attestation` | The release is published, but GitHub does not (yet) show it as published or attested | "Re-run failed jobs": `finalize` resumes because the attempt is above 1, tags nothing twice and repeats the checks. |
| `smoke-*` | A problem found after publication | The release is immutable: fix forward in the next version and describe the problem in the release notes, which stay editable. A `[download_failed]` whose message says "retry window of 120 s used up" is an outage of github.com, not a problem of the release: use "Re-run failed jobs". Two kinds of step have a policy of their own and can fail during a shorter outage: the installation of kind, kubectl and helm in `smoke-kind` (`ci/install-tools.sh` retries the download of kubectl for up to 120 s and a `go build` of kind or helm up to 4 times, 5, 10 and 20 s apart, each only after a transient network error; "retry window of 120 s used up" or a `go build` that failed four times with a network error is an outage of dl.k8s.io or of the Go module proxy), and the service checks of `ci/release/smoke.sh` (`docker pull` from ghcr.io, `crane`, `cosign` and `gh`: `SMOKE_RETRIES` attempts, 3 by default, 10 s apart). Re-run the job in this case too. |

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
