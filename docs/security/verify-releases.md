---
title: "Verify releases"
weight: 20
description: "Check the files of a myriarch release before you run them: checksums.txt, its keyless Sigstore bundle, the exact signing identity and the hashes of the files."
---

Every release of myriarch is built and signed by one GitHub Actions workflow, `release.yml` of the repository
`myriarch/myriarch`, from a `v<version>` tag. The workflow signs **keyless** with [Sigstore](https://www.sigstore.dev/)
([ADR-0023](../adr/0023-keyless-release-signing.md), [ADR-0029](../adr/0029-host-under-the-myriarch-organization.md)): there is no
long-lived signing key to steal or to distribute. A signature is bound to a short-lived certificate that Sigstore issued to
that workflow run, and the certificate names the workflow and the tag.

This page is part 1 of the verification guide and covers the **files of a release**. Verifying the container images and the
build provenance follows with the release workflow.

## What is signed

A release contains the archives and the bare `myriarch-updater` binaries, their SBOMs, `LICENSE`, `THIRD_PARTY_NOTICES` and two
files that carry the trust:

| File | Content |
|---|---|
| `checksums.txt` | the SHA-256 of every other file of the release, one line `<hex>  <file name>` per file |
| `checksums.txt.sigstore.json` | the Sigstore bundle: the signature over `checksums.txt`, the certificate and the transparency log proof |

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
VERSION=0.1.0     # the release you verify, without the "v"

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
$want = (Select-String -Path checksums.txt -Pattern '  myriarch-updater_0.1.0_windows_amd64.exe$').Line.Split(' ')[0]
$got  = (Get-FileHash -Algorithm SHA256 myriarch-updater_0.1.0_windows_amd64.exe).Hash.ToLowerInvariant()
if ($got -ne $want) { throw 'SHA-256 mismatch' }
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
download, so check the archive **before** you extract and run anything from it:

```bash
# on the host, or on an admin host and copy the archive over
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/myriarch/myriarch/.github/workflows/release.yml@refs/tags/v${VERSION}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
sha256sum --check --ignore-missing checksums.txt
tar -xzf myriarch-updater_${VERSION}_linux_amd64.tar.gz
```

On Windows, Windows marks files from the internet (the *Mark of the Web*) and refuses to run unsigned scripts under the usual
execution policy. Run `Unblock-File` **only after** the checks above have passed, never to get past the prompt first:

```powershell
Expand-Archive myriarch-updater_0.1.0_windows_amd64.zip -DestinationPath C:\Temp\myriarch
Unblock-File C:\Temp\myriarch\deploy\scripts\install-windows.ps1
```

## What the installers verify

You do not need to run these steps for a host that you install with the scripts: they verify the release themselves. See
[Connect a host](../tasks/connect-a-host.md) for the options.

- With `--updater-checksums-url` (`-UpdaterChecksumsUrl`) the installer downloads `checksums.txt` and its bundle
  (`<url>.sigstore.json`, or `--updater-bundle-url`), verifies the bundle against the identity above, and takes the SHA-256 of
  the updater from the line of the file name in `--updater-url`. The version of the identity is `--updater-version` or the
  version in that file name; if both are given, they must agree.
- The verifier is `cosign` 3.0.4 or newer, or an already installed `myriarch-updater` (`--verifier`). It is never the
  updater that is being installed, because that binary is only trusted through the file the verifier has to check.
- **Hosts without a verifier** (no `cosign`, no installed updater): verify `checksums.txt` on an admin host as shown above,
  take the SHA-256 of the updater from it and pass `--updater-sha256` (`-UpdaterSha256`). The installer then checks the
  downloaded updater against that hash and uses it to verify the collector and supervisor bundles.
- **Upgrades:** run the installer again with the new version. The updater that is already installed verifies the bundle of the
  new release, then the new updater replaces it.

## Mirrors

A mirror must carry `checksums.txt` and `checksums.txt.sigstore.json` unchanged. A modified `checksums.txt` fails the check,
whatever the mirror signed it with. See the [enterprise mirror toolkit](../../contrib/enterprise-mirror/README.md).
