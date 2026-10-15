---
title: "Verify releases"
weight: 20
description: "Check a myriarch release before you run it: checksums.txt and its keyless Sigstore bundle, the build provenance, the immutable release, the container images and the macOS binaries."
---

Every release of myriarch is built and signed by one GitHub Actions workflow, `release.yml` of the repository
`myriarch/myriarch`, from a `v<version>` tag. The workflow signs **keyless** with [Sigstore](https://www.sigstore.dev/)
([ADR-0023](../adr/0023-keyless-release-signing.md), [ADR-0029](../adr/0029-host-under-the-myriarch-organization.md)): there is no
long-lived signing key to steal or to distribute. A signature is bound to a short-lived certificate that Sigstore issued to
that workflow run, and the certificate names the workflow and the tag.

This guide has two parts. Part 1 covers the **files of a release** and how the installers check them. Part 2 covers the
**build provenance**, the **immutable release**, the **container images** and the **macOS binaries**. Every command below uses
the identity of the workflow at the tag of the version you verify. The release notes of every release show the same commands
with the tag, the commit and the image digests filled in.

## What is signed

A release contains the archives and the bare `myriarch-updater` binaries, the SBOMs of the archives and of the web UI, `LICENSE`,
`THIRD_PARTY_NOTICES` and three files that carry the trust:

| File | Content |
|---|---|
| `checksums.txt` | the SHA-256 of the 39 other files of the release (archives, bare binaries, SBOMs and the two license files), one line `<hex>  <file name>` per file |
| `checksums.txt.sigstore.json` | the Sigstore bundle: the signature over `checksums.txt`, the certificate and the transparency log proof |
| `myriarch_<version>_provenance.intoto.jsonl` | the GitHub build provenance (SLSA) of the same 39 files, as a Sigstore bundle (part 2) |

You verify **one signed file** and then check every other file against it. The identity of a release is exactly this
certificate identity (the Subject Alternative Name of the certificate), with this issuer:

| Item | Value for the release `v<version>` |
|---|---|
| Certificate identity | `https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v<version>` |
| OIDC issuer | `https://token.actions.githubusercontent.com` |

Always give the **complete identity of the version you want**, not a pattern that accepts any tag. A signature of the same
workflow for an older tag is genuine, too, and would pass a pattern such as `.../release.yml@refs/tags/v.*`.

## Verify with cosign

You need [cosign](https://docs.sigstore.dev/cosign/) **3.0.4 or newer** (`cosign version`; older versions have known
verification flaws and the installers refuse them). Download `checksums.txt` and `checksums.txt.sigstore.json` and the files
you want from the release page of the tag, then:

```bash
VERSION=0.1.0        # the release you verify, without the "v"; the newest one is on https://github.com/myriarch/myriarch/releases

cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v${VERSION}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

`Verified OK` means that GitHub Actions ran `release.yml` of `myriarch/myriarch` for the tag `v0.1.0` and signed exactly this
`checksums.txt`. Then check the files you downloaded against it:

```bash
sha256sum --check --ignore-missing checksums.txt          # Linux
shasum -a 256 --check --ignore-missing checksums.txt      # macOS
```

Every file you downloaded must say `OK`. `--ignore-missing` skips the lines of files you did not download; a file that is not in
`checksums.txt` at all is not part of the release. On Windows compare one file at a time:

```powershell
$file  = 'myriarch-updater_0.1.0_windows_amd64.exe'
$lines = @(Get-Content checksums.txt | Where-Object { $_.EndsWith("  $file", [StringComparison]::Ordinal) })
if ($lines.Count -ne 1) { throw "expected exactly one line for $file in checksums.txt" }
$want = $lines[0].Split(' ')[0]
$got  = (Get-FileHash -Algorithm SHA256 $file).Hash.ToLowerInvariant()
if ($got -cne $want) { throw 'SHA-256 mismatch' }
```

If a check fails, do not run the file. A failure of the first command means that the file is not the one the workflow
signed, was signed by another identity or for another tag, or that the bundle does not belong to it. A failure of the second
means that the file was modified or corrupted after signing, or that you downloaded another version.

### Offline and behind a proxy

`cosign verify-blob` fetches the Sigstore trusted root over the network. For a host without access, fetch the trusted root once
on a connected machine and pass it with every check:

```bash
myriarch-updater fetch-trusted-root --out trusted_root.json
cosign verify-blob --trusted-root trusted_root.json --bundle checksums.txt.sigstore.json ... checksums.txt
```

The bundle contains the proof of the transparency log, so the check itself needs no further network access. Fetch a fresh
trusted root now and then: Sigstore rotates its keys.

## Verify the archive that contains the installer before you run it

The release archives of `myriarch-updater` (`.tar.gz` for Linux, `.zip` for Windows) also contain the installation scripts
`install-linux.sh` and `install-windows.ps1`. A script that runs as root or Administrator is only as trustworthy as its
download, so check the archive **before** you extract and run anything from it. On Linux do it in a directory that root owns and
no other account can write, such as one below `/root`, so that nobody can replace a file between the check and the run:

```bash
# in a root shell on the host, or on an admin host (then copy the archive over and repeat the sha256sum check on the host)
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v${VERSION}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
sha256sum --check --ignore-missing checksums.txt
tar -xzf myriarch-updater_${VERSION}_linux_amd64.tar.gz
```

On Windows, a browser marks the files it downloads (the *Mark of the Web*), and the usual execution policy of a client
(`Restricted`) refuses every script. Run `Unblock-File` **only after** the checks above have passed, never to get past the
prompt first, and unpack the archive into a folder that only administrators can write (a folder below `C:\Program Files`, not
`C:\Temp`, a user profile or a download folder), so that nobody can replace a file between the check and the run:

```powershell
Expand-Archive myriarch-updater_0.1.0_windows_amd64.zip -DestinationPath 'C:\Program Files\myriarch-setup\package'
Unblock-File 'C:\Program Files\myriarch-setup\package\deploy\scripts\install-windows.ps1'
```

Then start the installer in a new PowerShell process with the execution policy for that process only
(`powershell.exe -NoProfile -ExecutionPolicy RemoteSigned -File ...`) and do not change the policy of the machine; the
command is in [Connect a host](../tasks/connect-a-host.md#5-install-on-windows).

## What the installers verify

You do not need to run these steps for a host that you install with the scripts: they verify the release themselves. See
[Connect a host](../tasks/connect-a-host.md) for the options.

- With `--updater-checksums-url` (`-UpdaterChecksumsUrl`) the installer downloads `checksums.txt` and its bundle
  (`<url>.sigstore.json`, or `--updater-bundle-url`), verifies the bundle against the identity above, and takes the SHA-256 of
  the updater from the line of the file name in `--updater-url`. The version of the identity is `--updater-version` or the
  version in that file name; if both are given, they must agree.
- The verifier is the file given with `--verifier`, otherwise `cosign` 3.0.4 or newer if it is in `PATH`, otherwise an
  already installed `myriarch-updater`. It is never the updater that is being installed, because that binary is only trusted through the file the verifier has to check.
- **Hosts without a verifier** (no `cosign`, no installed updater): verify `checksums.txt` on an admin host as shown above,
  take the SHA-256 of the updater from it and pass `--updater-sha256` (`-UpdaterSha256`). The installer then checks the
  downloaded updater against that hash and uses it to verify the collector and supervisor bundles.
- **Upgrades:** run the installer again with the new version. On Linux a `cosign` 3.0.4 or newer in `PATH` verifies the bundle
  of the new release, and the updater that is already installed does so on a host without `cosign` (on Windows the installed
  updater comes before `cosign.exe`); then the new updater replaces it.

## Mirrors

A mirror must carry `checksums.txt` and `checksums.txt.sigstore.json` unchanged. A modified `checksums.txt` fails the check,
whatever the mirror signed it with. The [enterprise mirror toolkit](../../contrib/enterprise-mirror/README.md) verifies a
release keyless (`verify-release.sh --keyless`) and copies it unchanged (`publish-mirror.sh --keyless`).

## Part 2: provenance, immutability, images and macOS

Part 2 needs [gh](https://cli.github.com/) (the release workflow itself uses gh 2.102.0), cosign 3.0.4 or newer and, for the
images, [crane](https://github.com/google/go-containerregistry/blob/main/cmd/crane/README.md) or Docker. Set the version and the
tag once:

```bash
VERSION=0.1.0        # the release you verify, without the "v"; the newest one is on https://github.com/myriarch/myriarch/releases
TAG="v${VERSION}"
```

## Verify the build provenance

Each of the 39 files that `checksums.txt` lists has a build provenance attestation: a statement, signed by the same workflow
identity, that GitHub Actions built this exact file from this repository, this commit and this tag. The online check asks
GitHub's attestation API, and needs a login of gh (`gh auth login`, or `GH_TOKEN`):

```bash
gh release download "${TAG}" --repo myriarch/myriarch --pattern 'checksums.txt*' --pattern "myriarch-updater_${VERSION}_linux_amd64"
gh attestation verify "myriarch-updater_${VERSION}_linux_amd64" --repo myriarch/myriarch \
  --signer-workflow "myriarch/myriarch/.github/workflows/release.yml@refs/tags/${TAG}" \
  --source-ref "refs/tags/${TAG}" --deny-self-hosted-runners
```

`--signer-workflow` with `@refs/tags/${TAG}` pins the workflow **and** the tag from gh **2.102.0** on. Older versions match that
value only as a prefix (`@refs/tags/v0.1.0` would also accept `v0.1.0-rc.3`) or do not escape it at all, so use gh 2.102.0 or newer.
`--source-ref` pins the tag the build ran for and is an exact check in every version, so keep it. `--deny-self-hosted-runners`
refuses a build that did not run on a GitHub-hosted runner. To pin the commit as well, add
`--source-digest <commit>` (the commit of the tag, shown in the release notes).

The provenance is also a release asset, `myriarch_<version>_provenance.intoto.jsonl`. With `--bundle` the check needs no login
and no call to the attestation API. It still needs the Sigstore trusted root: gh caches it for **one day** and fetches it again
after that, so an offline host passes a trusted root file with `--custom-trusted-root trusted_root.json` (`gh attestation
trusted-root` prints the current one; `myriarch-updater fetch-trusted-root` writes the same kind of file, see
[Offline and behind a proxy](#offline-and-behind-a-proxy)):

```bash
gh release download "${TAG}" --repo myriarch/myriarch --pattern "myriarch_${VERSION}_provenance.intoto.jsonl"
gh attestation verify "myriarch-updater_${VERSION}_linux_amd64" --repo myriarch/myriarch \
  --bundle "myriarch_${VERSION}_provenance.intoto.jsonl" \
  --signer-workflow "myriarch/myriarch/.github/workflows/release.yml@refs/tags/${TAG}" \
  --source-ref "refs/tags/${TAG}" --deny-self-hosted-runners
```

The signature of `checksums.txt` and the provenance are two independent proofs of the same identity. Run both for a release you
depend on.

## Check that a release is immutable

Myriarch publishes releases as **immutable releases**: once a release is published, GitHub locks its assets and its tag: the assets cannot be
changed, added or removed, and the tag cannot be moved. GitHub signs a release attestation when it publishes. Ask for it:

```bash
gh release verify "${TAG}" --repo myriarch/myriarch
gh release verify-asset "${TAG}" checksums.txt --repo myriarch/myriarch
```

`gh release verify` checks that the release of the tag is immutable and that its attestation is valid. `gh release verify-asset`
checks that a file you downloaded is an asset of that release, so a swapped file fails. Both commands need a login of gh
(`gh auth login`, or `GH_TOKEN`) even though the release is public. If the first command reports that the release is not
immutable, do not rely on the contents of the assets; report it in the issue tracker.

**`gh release verify-asset` is the proof that a file was released.** A signature or an attestation of the release identity proves
that the workflow run for that tag created the file, but the workflow creates signatures, attestations and image signatures before
it verifies the release and before the second approval, also in runs that never published (a failed attempt, an abandoned
candidate, a "Re-run all jobs"). Those credentials stay valid for the tag; deleting a draft revokes none of them. Only the
membership in the published, immutable release shows that the file was released.

## Verify the container images

The images are `ghcr.io/myriarch/myriarch` (the server, with `myrctl` and `myriarch-sim`) and
`ghcr.io/myriarch/myriarch-updater`. They are signed by the same workflow identity as the files and carry a provenance
attestation (on the image index) and an SPDX SBOM attestation (on each platform image). A tag is only a name: **verify and pin
the digest**.

1. Resolve the tag of the version to its digest, or take the digest from the release notes:

   ```bash
   crane digest "ghcr.io/myriarch/myriarch:${VERSION}"
   docker buildx imagetools inspect "ghcr.io/myriarch/myriarch:${VERSION}"      # the line "Digest:" is the same value
   ```

2. Verify the signature by digest with the complete identity of the version:

   ```bash
   DIGEST=sha256:...           # the digest from step 1 or from the release notes
   cosign verify "ghcr.io/myriarch/myriarch@${DIGEST}" \
     --certificate-identity "https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/${TAG}" \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com
   ```

   Repeat it for `ghcr.io/myriarch/myriarch-updater` with its own digest. `cosign verify` also accepts the provenance and SBOM
   attestations of the same identity as a "signature" of the digest; the JSON it prints shows the type of each verified entry, and
   `https://sigstore.dev/cosign/sign/v1` is the signature made by `cosign sign`.

3. Verify the build provenance of the image index. The attestations are stored in the registry, next to the image:

   ```bash
   gh attestation verify "oci://ghcr.io/myriarch/myriarch@${DIGEST}" --repo myriarch/myriarch --bundle-from-oci \
     --signer-workflow "myriarch/myriarch/.github/workflows/release.yml@refs/tags/${TAG}"
   ```

4. Verify the SBOM. It is attached to each platform image, so use the digest of the platform image (the manifest of the index
   lists them):

   ```bash
   crane manifest "ghcr.io/myriarch/myriarch@${DIGEST}" | jq -r '.manifests[] | "\(.platform.os)/\(.platform.architecture) \(.digest)"'
   gh attestation verify "oci://ghcr.io/myriarch/myriarch@sha256:<platform digest>" --repo myriarch/myriarch --bundle-from-oci \
     --predicate-type https://spdx.dev/Document/v2.3 \
     --signer-workflow "myriarch/myriarch/.github/workflows/release.yml@refs/tags/${TAG}"
   ```

   `gh` needs a login here (`GH_TOKEN`), but it reads the attestations from the registry, anonymously.

**What the `sha256-<hex>` tags are.** The package view of ghcr.io shows tags named `sha256-` followed by 64 hex digits, about
three for each image. ghcr.io has no API for attached artifacts, so cosign and the attestation step keep the signatures and
attestations in these tags. They are part of the release: do not delete them, and do not delete untagged versions of a released
image, or the checks above fail. There is no `latest` tag while the major version is 0; the version tags are `X.Y.Z`, `X.Y`
(the newest patch release of that minor version) and `X.Y.Z-rc.N` for release candidates.

**Pin the digest in your deployment.** With the Helm chart from the tag, set the digest of the server image (the digest wins
over the tag):

```bash
helm install myriarch ./deploy/helm/myriarch --set image.digest="${DIGEST}"     # from a checkout of the tag
```

For Compose, use `QUICKSTART_IMAGE=ghcr.io/myriarch/myriarch@${DIGEST}` (see the
[Docker Compose quickstart](../getting-started/docker-compose.md)).

## macOS

The macOS binaries (`myriarchd`, `myrctl` and `myriarch-sim` for Apple silicon, `myrctl` also for Intel) are **not signed with
an Apple Developer ID and not notarized**. macOS Gatekeeper blocks an unsigned binary that carries the quarantine attribute, which
browsers set on downloads. Verify the file first (cosign and `shasum -a 256 --check --ignore-missing checksums.txt`, above), then
use one of these ways:

- download the archive with `curl` or `gh release download`, which do not set the quarantine attribute; or
- remove the attribute from a file you verified: `xattr -d com.apple.quarantine <file>` (for an archive, extract it first and run
  the command on the extracted binary).

Do not turn Gatekeeper off for the system. Whoever needs signed and notarized macOS binaries can build from source.
