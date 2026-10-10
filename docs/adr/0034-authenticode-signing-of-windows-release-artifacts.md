# 0034. Authenticode signing of Windows release artifacts by the SignPath Foundation, with a defined fallback and an exception for v0.1.0

- Status: Accepted
- Date: 2026-10-10
- Related: [ADR-0021](0021-publication-gates-and-v0-1-0-scope.md), [ADR-0023](0023-keyless-release-signing.md), [ADR-0029](0029-host-under-the-myriarch-organization.md), [ADR-0032](0032-continuous-integration-on-github-actions.md)

## Context

Releases are signed keyless with Sigstore from the release workflow ([ADR-0023](0023-keyless-release-signing.md),
[ADR-0029](0029-host-under-the-myriarch-organization.md)). That signature is the trust anchor for the installers and for
`myriarch-updater`. Windows has a second, separate mechanism that Sigstore does not replace: Authenticode, the signature that
Windows itself evaluates before it lets a file run. The repository has a first Authenticode path (`deploy/release/authenticode.sh`),
but it needs a PFX file with a password in the release job. No public certificate authority can deliver such a file any more,
and a long-lived signing secret is exactly what [ADR-0023](0023-keyless-release-signing.md) avoids.

**Rules that apply to every option.**

- The CA/Browser Forum code signing requirements demand that the private key of every publicly trusted code signing certificate,
  organization-validated ones included, is generated, stored and used in a hardware crypto module (a token, an HSM, a cloud HSM
  whose subscription logs every access, operation and configuration change, or a signing service). Public CAs therefore issue
  no exportable PFX files. Certificates issued from 2026-03-01 are valid for at most 460 days, so a certificate is renewed
  roughly every year.
- Without an RFC 3161 timestamp a signature becomes invalid when the certificate expires.
- Where the project signs itself (the fallback), signing works on the existing Linux runner (`jsign` or `osslsigncode`).
  Verification has to run on a Windows runner as well, because Linux CA bundles do not contain all code signing roots.

**Why signing matters for Myriarch.** Installs are scripted and `myriarch-updater` runs as a service, so the SmartScreen
warning for a downloaded file matters little. Since 2024 an EV certificate gives no SmartScreen advantage over any other
publicly trusted certificate, so EV is not worth its price for a project. The real reasons are the following:

- Servers that enforce App Control for Business or AppLocker publisher rules can allow a program only by signer, or else by one
  hash rule per release.
- Smart App Control on Windows 11 admin workstations that run `myrctl` blocks unsigned files that have no positive reputation.
- Windows Server defaults to the execution policy `RemoteSigned`. A script that arrives in a downloaded archive carries the Mark
  of the Web and needs `Unblock-File`, and `AllSigned` refuses an unsigned script. Under App Control script enforcement, a
  script that is not allowed runs in Constrained Language Mode, and the installer does not work there. A script signature helps
  only where the signer is trusted (Trusted Publishers store, App Control publisher rule, root in the root store).
- Reputation carries over between releases only while the signing identity stays the same.

Windows is experimental in the first release ([ADR-0021](0021-publication-gates-and-v0-1-0-scope.md)).

## Decision

1. **SignPath Foundation is the target.** The SignPath Foundation signs releases of open source projects for free with a
   certificate issued to "SignPath Foundation" (organization-validated, key in SignPath's HSM). It is the only option that adds
   a signing gate outside GitHub: SignPath fetches the unsigned files itself from the GitHub Actions run, checks repository,
   commit, ref and the use of GitHub-hosted runners (origin verification), and a person approves every release request in a
   separate account with its own multi-factor authentication. A leaked token or a compromised CI step cannot produce a
   signature on its own.
2. **Our own client, no further third-party action.** The submission to SignPath is a short `curl` client in a job that is
   bound to the GitHub environment `release`. It does not use `signpath/github-action-submit-signing-request`: the action is
   not owned by GitHub, its repository holds only a bundled `index.js`, and using it would need an exception in the
   organization's action allowlist and a change of rule R1 of `ci/check-workflows.py` (only `actions/*` and `github/*`
   besides the one exception of [ADR-0033](0033-openssf-scorecard-as-the-only-third-party-action.md)), and rule R10
   forbids the way the action takes its token (a `with:` input). It would be a second exception next to the Scorecard
   action. A narrow exception for exactly one workflow and job is possible only by an amendment of this record.
3. **No long-lived signing secret.** The only secret of the SignPath route is the API token of a CI user that can only submit
   signing requests. It is an environment secret of `release`, listed in `ci/workflow-secrets.txt` for exactly the signing job,
   and the job has read scopes only (`contents: read`, `actions: read`). For the fallback route (point 4) the job has
   `id-token: write` and no secret at all. `ci/workflow-permissions.txt` lists that scope when the route is switched on.
4. **Fallback with a trigger.** The fallback is an individual-validation (IV) certificate in the maintainer's name whose key is
   generated in a Google Cloud KMS HSM. The release environment uses it through GitHub OIDC and Workload Identity Federation
   with `jsign` and the public certificate chain; a condition on the token pins repository ID, environment `release`, the
   workflow file at `refs/tags/v*` and GitHub-hosted runners, signing rights apply to that one key only, and Cloud KMS data
   access audit logs with an alert per signature are switched on. The trigger is a SignPath rejection, or no decision four
   weeks after the application. The budget cap for the first year is US$700 (about US$660 are expected: US$129 for the
   certificate, a one-time US$500 for the key attestation and about US$30 for the HSM key). When the trigger fires, the
   maintainer decides between the fallback and waiting, and nothing is bought, ordered or paid before the maintainer decides.
   A variant, if SignPath refuses only its Foundation certificate, is SignPath with an own certificate.
5. **Privacy consequence of an IV certificate.** The legal name of the maintainer and the locality (city, state, country) appear
   in every signed file, and so in every signature that is downloaded. That is personal data. The maintainer accepts this
   explicitly before an IV certificate is ordered. The same applies to the SignPath variant with an own IV certificate. With the
   SignPath Foundation certificate the publisher is "SignPath Foundation" and no personal data appears in the signature.
6. **Authenticode adds to Sigstore and never replaces it.** The Sigstore bundles of `checksums.txt` and the archives stay the
   trust anchor of installers and updater. A file is accepted only if it is the pipeline's own reproducible build plus the
   provider's signature, and the pipeline verifies the signature (signer subject, product name, file version, RFC 3161
   timestamp) before it publishes, on Linux against the pinned chain and on a Windows runner against what Windows itself
   reports. The cosign signature covers the signed files, because `checksums.txt` is created after signing.
7. **Scope.** `myriarch-updater.exe` and `myrctl.exe` for amd64 and arm64, and the two PowerShell scripts that ship,
   `install-windows.ps1` and `Set-ServiceEnvironment.ps1`. Every signature uses SHA-256 and an RFC 3161 timestamp, with a
   second timestamp server as fallback. Both executables get a Windows version resource (product name "Myriarch", file and
   product version from the tag, original file name), because SignPath enforces product name and version and because App
   Control publisher rules can match product and file name. A signed script is never changed afterwards, not even its line endings. The test
   scripts below `ci/tests` are not signed. The Windows scripts ship only in the Windows archives, so that no unsigned copy
   lies next to a signed one. macOS binaries are not part of this decision.
8. **Exception for v0.1.0.** SignPath accepts only projects that are already released in the form that is to be signed and
   asks executables for a verifiable reputation, so signing cannot be in place for the first tag. `v0.1.0` ships unsigned
   Windows files under a reviewed exception for exactly that tag:
   - `deploy/release/windows-signing.env` holds `WINDOWS_SIGNING=off|signpath|kms` and `WINDOWS_UNSIGNED_FINAL_TAG`. The
     second is empty until the pull request "Prepare v0.1.0" sets it to `v0.1.0`.
   - A final tag with `WINDOWS_SIGNING=off` passes the release guard only if it is exactly the tag named in
     `WINDOWS_UNSIGNED_FINAL_TAG`. Every later final tag needs signing or a new, reviewed exception.
   - The release notes show an "unsigned" banner, and `docs/security/model.md` and the verification guide tell operators to
     verify the Windows files with cosign before they run them.
   - The pipeline is built ready for signing (signing hook, version resources, acceptance check, rehearsal with a throwaway
     test CA that exists only in the pull-request rehearsal), and the SignPath application goes out right after `v0.1.0`.
     Published files are never signed with a throwaway key.
   (The file and the release guard arrive with the release tag guard and the release build change; until then, the tree has no such setting.)
9. **A change of publisher is announced.** The publisher shown by Windows is part of the identity that SmartScreen
   reputation and App Control rules refer to. Every change (unsigned to SignPath, IV certificate to SignPath, later to a
   foundation) resets reputation and breaks publisher rules that operators wrote, and is announced in the release notes of the
   first release that carries the new publisher. A certificate renewal at the same CA with the same subject is not a change of
   publisher, but it changes the thumbprint.
10. **Installer checks.** `-UpdaterSignerThumbprint` of `install-windows.ps1` stays for operators who re-sign with their own PKI.
    It pins the leaf certificate and so breaks at every renewal (at most every 460 days); the installer change documents this where the option is
    described. It is no check for upstream signatures. A check of the signer subject (exact subject, optionally the issuer, plus product
    name and file version) comes with the provider pull request. The PFX path leaves the release build: the release build change moves
    `authenticode.sh` to `contrib/enterprise-mirror` for private PKIs or deletes it, and `ci/build-release.sh` keeps only the
    snapshot mode.
11. **When to re-evaluate.** Azure Artifact Signing opens for individual developers in the EU; the project gets a legal entity;
    the project moves to a foundation. Azure Artifact Signing is then the best technical fit (about US$9.99 a month, OIDC, no
    secret), reached with `curl` and `jsign` instead of Microsoft's actions.

The policy page "Code signing policy" that SignPath requires (roles of author, reviewer and approver, a privacy statement and
the line that names SignPath and the SignPath Foundation) is published on the website together with the application and not
before it. Its privacy statement depends on the privacy notice of the website.

## Consequences

### Positive

- No long-lived signing secret in the repository, in the release workflow or in the maintainer's hands. A leaked SignPath token
  cannot produce a signature; a compromised CI step cannot either, unless a person approves the request.
- The pipeline is ready for signing when v0.1.0 is tagged, and the exception for the first tag is narrow, written down and
  checked by the release guard.
- Servers with App Control can move from one hash rule per release to one publisher rule once signing starts. Scripts can
  satisfy `AllSigned`.
- The trust anchor stays the Sigstore identity of the release workflow, so a Windows host without the Authenticode root still
  verifies with cosign.

### Negative

- `v0.1.0` has unsigned Windows files. Hosts with App Control need a hash rule per release until signing starts, and Smart App
  Control may block the files. Residual risk and the compensating check are in [the security model](../security/model.md).
- Dependence on one vendor: SignPath may pause or end the service, or revoke the certificate effective immediately or
  retroactively, and it needs a manual approval for every release, so a release waits for a person in a second system.
- The shared publisher "SignPath Foundation" is the same for every project in the program. A Trusted Publisher or App Control
  rule for it trusts all of them; rules for executables can add product and file name, rules for scripts cannot. Some
  enterprises prefer a publisher of their own; feedback is collected.
- Acceptance is not certain (reputation clause, no published review time), and the fallback costs money, a personal name in
  every signature and a yearly renewal with a new validation.
- A move between providers resets reputation and breaks publisher rules (point 9).
- The signing job waits for an approval in SignPath while the release run is open. Tags are immutable, so a failed run is
  repeated, not re-tagged.

## Alternatives considered

- **SignPath Foundation, with the SignPath action**: not chosen, see point 2. The action would be a second third-party action
  exception, and the token would be passed as a `with:` input, which the workflow rules forbid.
- **Azure Artifact Signing (formerly Trusted Signing)**: the best technical fit (no secret, OIDC), but individual developers
  are accepted only in the USA and Canada (quickstart updated 2026-10-08), and the organization route needs a legal entity with
  public records. It is not available to this project today; see point 11. It also costs a paid subscription.
- **Certum open source certificate**: a cheap certificate for individuals (from EUR 49 a year in the cloud version), but the
  cloud version needs a code from the mobile app for every session and has no supported non-interactive interface.
  Automation in CI needs the TOTP seed of the second factor as a long-lived secret, which can sign anywhere, plus an
  unofficial tool. Signing by hand breaks "releases only from GitHub Actions". The card cannot be used on GitHub-hosted
  runners. Rejected.
- **An IV or OV certificate with the CA's cloud signing (for example SSL.com eSigner, DigiCert KeyLocker)**: needs a user name,
  password and TOTP secret in CI, with no check where a request comes from. Worse than the fallback.
- **An own IV certificate with its key in Google Cloud KMS through GitHub OIDC and `jsign`**: kept as the fallback (point 4).
  It needs no third-party action and no secret, but it costs money, binds a personal name into every signature and adds a
  cloud account for a single maintainer.
- **A hardware token on a self-hosted runner**: a token that signs without a gate outside GitHub gives no protection against a
  compromised run, and a self-hosted runner contradicts the planned rule of the release workflow (ADR-0035) that release jobs
  run on GitHub-hosted runners only, which the provenance check will enforce with `--deny-self-hosted-runners` and which the
  SignPath policy will require. A hardware
  token also cannot be attached to a GitHub-hosted runner, and a machine of one person becomes a single point of custody and
  failure. Rejected.
- **An EV certificate**: only for organizations, at least about US$400 a year, and without a SmartScreen advantage since 2024.
- **No Authenticode signature at all**: leaves the App Control and script cases of the context unsolved. Kept only for the
  exception of `v0.1.0`.

## Sources

Checked in October 2026.

- CA/Browser Forum, [Code Signing Baseline Requirements](https://github.com/cabforum/code-signing/blob/main/docs/CSBR.md)
  v3.11.0, sections 6.1.5.2, 6.2.7.4 and 6.3.2, and
  [ballot CSC-31](https://cabforum.org/2025/11/17/ballot-csc-31-maximum-validity-reduction) (validity of at most 460 days).
- Microsoft Learn, [SmartScreen reputation](https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/smartscreen-reputation)
  and [code signing options](https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/code-signing-options)
  (EV, reputation, Smart App Control), [about execution policies](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_execution_policies),
  and [App Control script enforcement](https://learn.microsoft.com/en-us/windows/security/application-security/application-control/app-control-for-business/design/script-enforcement).
- SignPath: [terms](https://signpath.org/terms), [application](https://signpath.org/apply),
  [GitHub trusted build system](https://docs.signpath.io/trusted-build-systems/github),
  [origin verification](https://docs.signpath.io/origin-verification),
  [artifact configuration](https://docs.signpath.io/artifact-configuration/reference),
  [certificate management](https://docs.signpath.io/managing-certificates) and the
  [GitHub action](https://github.com/SignPath/github-action-submit-signing-request).
- Azure Artifact Signing: [quickstart](https://learn.microsoft.com/en-us/azure/artifact-signing/quickstart),
  [certificate management](https://learn.microsoft.com/en-us/azure/artifact-signing/concept-certificate-management) and
  [pricing](https://azure.microsoft.com/en-us/pricing/details/artifact-signing/).
- Certum, [open source code signing on SimplySign](https://shop.certum.eu/open-source-code-signing-on-simplysign.html).
- SSL.com, [IV code signing](https://www.ssl.com/products/software-integrity/code-signing/iv/),
  [eSigner pricing](https://ssl.com/guide/esigner-pricing-for-code-signing/) and
  [code signing with Google Cloud HSM](https://ssl.com/guide/implementing-code-signing-with-google-cloud-hsm/amp);
  Google, [Cloud KMS pricing](https://cloud.google.com/kms/pricing) and
  [workload identity federation with deployment pipelines](https://docs.cloud.google.com/iam/docs/workload-identity-federation-with-deployment-pipelines).
- Tools: [jsign](https://ebourg.github.io/jsign/) and [osslsigncode](https://github.com/mtrojnar/osslsigncode).
- GitHub, [OIDC reference](https://docs.github.com/en/actions/reference/security/oidc) and
  [deployments and environments](https://docs.github.com/en/actions/reference/workflows-and-actions/deployments-and-environments).
