# 0035. Release workflow: tag-only, isolated jobs, keyless signing and provenance, verification before an immutable publication, and a frozen signing identity

- Status: Accepted
- Date: 2026-10-11
- Related: [ADR-0021](0021-publication-gates-and-v0-1-0-scope.md), [ADR-0023](0023-keyless-release-signing.md), [ADR-0024](0024-container-registry-and-image-set.md), [ADR-0029](0029-host-under-the-myriarch-organization.md), [ADR-0032](0032-continuous-integration-on-github-actions.md), [ADR-0033](0033-openssf-scorecard-as-the-only-third-party-action.md), [ADR-0034](0034-authenticode-signing-of-windows-release-artifacts.md)

## Context

The decisions that a release depends on exist, but nothing joins them yet.

- [ADR-0023](0023-keyless-release-signing.md) decides that releases are signed keyless with Sigstore from the release workflow
  and lists the signed objects. [ADR-0029](0029-host-under-the-myriarch-organization.md) fixes the identity that every verifier
  checks: `https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v*`, issuer
  `https://token.actions.githubusercontent.com`, and public packages under `ghcr.io/myriarch`.
- [ADR-0033](0033-openssf-scorecard-as-the-only-third-party-action.md) made the policy rules for the release workflow (R13) exist
  before the workflow does: build code never runs in a job that holds a write scope, `id-token`, `attestations` or an
  environment, and a `guard` job comes first.
- [ADR-0034](0034-authenticode-signing-of-windows-release-artifacts.md) defines the Windows signing hook and its acceptance check
  and allows `v0.1.0` and `v0.2.0` to ship unsigned Windows files, each under a reviewed exception for exactly that tag.
- The release build (`deploy/release/goreleaser.yaml`) holds no key and publishes nothing. GoReleaser OSS cannot import prebuilt
  binaries, so the Windows files cannot be signed in the job that packages them without a second build.

Facts about the platform shape the design:

- Events that `GITHUB_TOKEN` causes start no workflow. A separate smoke workflow on `release: published` would never run, so the
  smoke tests have to be jobs of the release workflow.
- ghcr.io has no referrers API. cosign v3 and `actions/attest` therefore keep signatures and attestations in `sha256-<hex>`
  fallback tags next to the images, and these tags are part of the release. ghcr.io also makes a package private on its first
  publish, and a package cannot be made private again after it is public.
- `actions/attest` reads registry credentials only from `$HOME/.docker/config.json`, and it updates the fallback tag of a digest
  by read-modify-write, which is not atomic.
- Immutable releases lock the assets and the tag when a release is published. A draft stays editable. The job token cannot read
  the repository setting that turns immutability on, so the workflow cannot check it before it publishes.
- One maintainer tags and approves. A second organization owner exists for emergencies, not as a regular reviewer.

## Decision

1. **Trigger and guard.** `.github/workflows/release.yml` runs on the push of a tag `v*` and on nothing else: no
   `workflow_dispatch`, no pull request. The job `guard` runs first and every other job needs it. It runs the checks of the tag
   guard (`ci/release/guard.sh`: tag form, annotated tag, commit on `origin/main`, `VERSION` and its copies, `CHANGELOG.md`, the
   Windows signing rule, a successful `ci-ok` on the commit) and then `ci/release/meta.sh`, which also refuses a run whose
   `GITHUB_WORKFLOW_REF` is not exactly `myriarch/myriarch/.github/workflows/release.yml@refs/tags/<tag>`. A renamed workflow
   file or a call through a reusable workflow therefore fails before anything is built, signed or pushed. The whole run is skipped
   in a fork.
2. **Job isolation.** Code of the repository and of its dependencies (Go, Node.js, pnpm, goreleaser, make) runs only in jobs with
   `contents: read` and no environment. The release tools are built once per run from `ci/tools` (no tool cache, no action cache)
   and travel as two artifacts. The signing set (cosign, crane, gh) is built first, in the job `signing-tools` on a runner where
   nothing else is installed or run, and it is the only set that the privileged jobs execute; the build set (goreleaser, syft,
   yq, go-winres, osslsigncode, which needs apt packages and a C compiler) is built in `build-tools` and reaches only jobs with
   `contents: read`. Each consumer runs `ci/release/tools.sh check` right after the unpack, and both producers write VERSIONS and
   SHA256SUMS to the job summary. Privileged jobs call only reviewed scripts under `ci/release/`, cosign, crane, gh and
   `actions/attest`, and take only artifacts of the same run: each is one tar file, downloaded by artifact ID, checked against the
   digest that the artifact service recorded and against the SHA-256 that the producing job reported as a job output. Artifacts
   are kept one day. Rule R13 of `ci/check-workflows.py` and `ci/tests/release-workflow-test.sh` enforce the shape.
3. **Exactly two approvals** of the environment `release`. The job `sign-windows` asks first, before anything is signed or pushed
   to a registry; it is also the slot of the Windows signing provider. The job `finalize` asks second, after the draft release
   exists and every verification has passed. Administrators cannot bypass the environment. With one maintainer this is a review
   step and not four eyes; that is a residual risk of the project, not a property of the workflow. Two limits belong to the
   design and are not hidden:
   - Gate 2 is a technical boundary for `finalize` only. The job `draft` holds `contents: write` without the environment and runs
     before gate 2, so anything that executes in `draft` (the pinned `gh`, `ci/release/draft.sh`) could publish or change a release
     without the second approval. Only gate 1 and the code at the tag stand in front of it, which is why `draft` takes only the
     signing set and runs no other code.
   - A gate is passed once per run, not once per attempt. "Re-run failed jobs" re-runs only the failed jobs and the jobs that depend
     on them: a `sign-windows` that already passed is not asked again, so `sign-blobs`, `push-images` and `draft` (id-token,
     attestations, packages and contents write) run again without a new approval, by anyone with write access to Actions.
     Whether `finalize` asks again after an approved attempt is to be observed on the first candidate (the checklist in
     `docs/releasing.md`). The effect is bounded: a re-run executes the same code at the same tag on artifacts checked against
     their SHA-256.
4. **What is signed.**
   - `checksums.txt` carries a keyless cosign bundle, `checksums.txt.sigstore.json`. Because `checksums.txt` lists the SHA-256 of
     the 39 other files (archives, bare updater binaries, SBOMs, `LICENSE` and `THIRD_PARTY_NOTICES`), the one signature covers
     the files of the object list of [ADR-0023](0023-keyless-release-signing.md).
   - GitHub build provenance (SLSA v1, `actions/attest`) is created over the same 39 subjects, stored in GitHub's attestation API
     and published as the asset `myriarch_<version>_provenance.intoto.jsonl`.
   - Both images are pushed by digest only, so they carry no release tag until the release is published. They are signed with
     `cosign sign --recursive` (index and platform manifests) and get a provenance attestation (index) and an SPDX SBOM
     attestation (each platform manifest) stored in ghcr.io. The signatures and attestations live in `sha256-<hex>` fallback tags.
   - The OCI chart of ADR-0023 follows in v0.2.0.
   - A release has 42 assets: the 40 files of the build stage and the two signature files.
5. **Verification before publication.** The jobs `verify`, `verify-images` and `verify-windows` check the files and the private
   images from which the draft was created (the artifacts of this run and the pushed digests), with read scopes only. They do
   not read the draft, and `verify` does not need the job `draft`; `finalize` binds the draft to their result.
   - Files: the exact identity (repository, workflow, tag, commit, trigger `push`), the pattern of
     [ADR-0029](0029-host-under-the-myriarch-organization.md) that users run, and the version-bound pattern that the installers
     use; then every file against `checksums.txt`.
   - Provenance: `gh attestation verify` for each of the 39 files, against the provenance asset and for the updater binary and
     its archive against the API copy, with the signer workflow, source ref, source commit and `--deny-self-hosted-runners`.
   - Images: index and platform manifests (exactly linux/amd64 and linux/arm64), configuration (user 65532:65532, entry point,
     one layer, labels), signatures and attestations, the absence of a `latest` tag, and a run of both images on amd64 and arm64
     without a shell.
   - Windows: version resources and Authenticode state, as `signer.json` says.
   - Then `finalize`, after the second approval, binds the draft to what was verified: the draft must still hold exactly the
     verified files, and GitHub's own SHA-256 digest of every asset must equal the verified manifest. It refuses unless both
     packages can be read without credentials and `refs/tags/<tag>` still resolves to the tagged commit (an annotated tag is
     dereferenced; `draft.sh` also passes the commit as the target of the release), sets the image tags with `crane tag`, reads
     the draft by ID again and repeats the asset and tag checks directly before the publication, publishes the release, reads the
     published release once more and compares its assets with the verified manifest again (a difference is reported as an
     immutable, wrong release: `release_assets_changed`), and checks that the release is immutable and that GitHub's release
     attestation verifies.
6. **Version and tag policy.** `VERSION` holds the next tag and every copy follows it. A release candidate is a GitHub
   pre-release and is never marked as the latest release. A final tag is marked latest when it is the highest final tag. Image
   tags are `X.Y.Z-rc.N` for a candidate and `X.Y.Z` plus the moving `X.Y` (only forward) for a final release; there is no
   `latest` tag while the major version is 0. A published tag is never moved: every attempt that needs a change is a new
   candidate, and a defect in a final release is fixed in the next patch release.
7. **Windows signing slot.** `sign-windows` runs `ci/release/windows-sign-hook.sh` with the mode of
   `deploy/release/windows-signing.env`; the contract, the acceptance check in the packaging job and the exceptions for `v0.1.0` and `v0.2.0`
   are those of [ADR-0034](0034-authenticode-signing-of-windows-release-artifacts.md). The release workflow holds no secret in
   v0.1.0. A provider adds a scope or a secret line to `ci/workflow-permissions.txt` or `ci/workflow-secrets.txt` for this one
   job; the job, its environment and its place in the graph do not change. A route that gives `sign-windows` `id-token: write` (for
   example a KMS route) makes that job a holder of the release identity: the job runs in `release.yml` at the tag and can mint OIDC
   tokens, Sigstore ones included, and with them certificates of the frozen identity. The acceptance check in `package` protects the
   Windows files, not that identity. Such a provider pull request therefore needs a review of the job equal to one for
   `sign-blobs`, no third-party code in it (R13) and no action that is not GitHub-owned; routes without `id-token` are preferred.
8. **Image contract** ([ADR-0024](0024-container-registry-and-image-set.md)). Both images are `FROM scratch` with one layer and
   user and group 65532, built from the released archives without a container runtime and with the same digest for the same
   inputs. The server image holds `myriarchd`, `myrctl`, `myriarch-sim` and the example repositories; the updater image holds the
   bare `myriarch-updater` binary.
9. **Smoke tests are jobs of the release workflow after `finalize`.** They use no registry login and no token beyond
   `contents: read` and `attestations: read`: anonymous downloads and registry reads, the verification commands of the guide,
   the installers on amd64 and arm64 with keyless verification and the real upstream collector, the staging of the Windows
   installer, `docker run` of both images by tag, and the chart from the tag installed on kind. A failure after publication
   cannot undo an immutable release; it is fixed forward and noted in the release notes, which stay editable.
10. **Frozen signing identity.** From `v0.1.0` on, `.github/workflows/release.yml` of `myriarch/myriarch` on `refs/tags/v*` is the
    identity that every installed verifier checks (the installers, `myriarch-updater`, the documented commands, the enterprise
    mirror toolkit). The file is never renamed, signing never moves into a reusable workflow or into another workflow, and SLSA
    Build L3 through a reusable workflow is not pursued, because it would change the signer identity. A change needs a new record
    with a transition for every installed verifier. The values live in `ci/release/identity.env`, and a test pins them to the
    installers, [ADR-0029](0029-host-under-the-myriarch-organization.md) and `internal/brand`.
11. **GHCR visibility.** The first push of the first release candidate creates `ghcr.io/myriarch/myriarch` and
    `ghcr.io/myriarch/myriarch-updater` as private packages. An organization owner switches both to public, once, after that push
    and before the second approval; this cannot be undone. Nobody creates the packages by hand, `finalize` fails with
    `ghcr_not_public` while one of them is private, and no `sha256-<hex>` tag or package version of a released digest is ever
    deleted.

## Consequences

### Positive

- No long-lived signing key or registry secret exists anywhere: files, images and provenance are signed by the one identity of
  the workflow at the tag, with short-lived certificates and a transparency log.
- Everything is verified, with the commands that users run, while the release is still a draft and the images are private. The
  release becomes immutable only after that, so a bad release candidate can be deleted and replaced by a new one.
- `finalize` compares the draft with the verified files by GitHub's own digests, twice before it publishes (once early, once
  directly before the PATCH, after the image tags took their time), and compares the published, immutable release once more. An
  asset swapped in the draft after verification is refused; a swap in the short window between the last read and the PATCH is
  not prevented but is found afterwards and reported as an immutable, wrong release, which is then not used (a new tag follows).
- A re-run is safe. A push by digest changes nothing, extra signatures are harmless, stale drafts of the tag are replaced, and
  `finalize` resumes after a publication.
- The release carries signed artifacts (`*.sigstore.json`, `*.intoto.jsonl`) that the Signed-Releases check of OpenSSF Scorecard
  looks for, and the shape of the workflow is tested.

### Negative

- Runs are slow because nothing is cached: the tools are compiled in every run.
- One person tags and approves both gates. The second approval is a review step, not four eyes, until a second reviewer exists.
- Artifacts live one day. An approval later than 24 hours after the start of a run needs "Re-run all jobs", which builds a new
  draft and new signatures; the image digests stay the same.
- ghcr.io shows `sha256-<hex>` tags in the package view. They hold the signatures and attestations and must not be deleted.
- Before gate 2 the job `draft` holds `contents: write` (the Releases API accepts no narrower scope for a draft), so the second
  approval is a technical boundary for `finalize` only; see point 3. The trust base of that job is kept small on purpose: the
  signing tool set and reviewed scripts.
- Every run creates durable credentials for the identity at the tag before verification and before gate 2, also for failed and
  abandoned runs and for "Re-run all jobs" attempts: Rekor-logged cosign bundles of `checksums.txt`, signatures and attestations
  of the images in the `sha256-<hex>` fallback tags in ghcr.io, and build provenance in GitHub's attestation API. Deleting a draft
  revokes none of them. A check with the exact or the version-bound identity therefore does not prove that a file was released;
  only `gh release verify-asset` (membership in the published, immutable release) does.
- The resume path of `finalize` (an attempt above 1) accepts a release that is already published when its assets match, and it
  cannot tell a publication by `finalize` from one by hand between two attempts, which the runbook forbids.
- The job token cannot read the setting for immutable releases. If it is off, `finalize` publishes and then fails with
  `release_not_immutable`; that release stays mutable and the setting must be fixed before the next tag.
- The `Token-Permissions` check of OpenSSF Scorecard can lose points for the write scopes of the privileged jobs.
- A final tag can be used once. A bug found after `v0.1.0` is tagged is fixed in `v0.1.1`.
- The release depends on the availability of GitHub, ghcr.io and the Sigstore public-good instance during the run.

## Alternatives considered

- **goreleaser publishes the release**: it needs a write token in the job that runs the build, which is what R13 forbids.
- **GoReleaser Pro (prebuilt binaries or split builds)**: not open source, and it would make the signing flow depend on it.
- **A reusable workflow for SLSA Build L3**: the signer identity becomes the reusable workflow and not `release.yml`, which
  would change the identity of every installed verifier. See point 10.
- **Rehearsals by `workflow_dispatch`**: they sign with the identity of the main branch, outside the policy of the environment
  for `v*` tags. `make release-rehearsal` runs the path of a release in a snapshot on every pull request instead.
- **One approval**: the draft would not be reviewed before it becomes immutable.
- **`actions/attest-build-provenance`**: a wrapper around `actions/attest`, which is used directly.
- **`docker/build-push-action` and buildx**: third-party actions ([ADR-0032](0032-continuous-integration-on-github-actions.md))
  and a container daemon; the images are built from the archives without a runtime.
- **A separate smoke workflow on `release: published`**: `GITHUB_TOKEN` events start no workflow (see the context).
- **`cosign attest` for the provenance**: it creates no entry in GitHub's attestation API, so `gh attestation verify` would not
  work.
- **One bundle per file**: 39 bundles instead of one signed list of checksums; more to download and to verify for the same trust.
- **Key-based signing**: rejected in [ADR-0023](0023-keyless-release-signing.md). The key-based toolkit stays in
  `contrib/enterprise-mirror` for organizations that re-sign mirrored releases.
