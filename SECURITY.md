# Security policy

## Reporting a vulnerability

Please report suspected vulnerabilities **privately**. Do not open a public issue, pull request or discussion for them.

1. **Preferred:** use GitHub private vulnerability reporting for this repository:
   <https://github.com/myriarch/myriarch/security/advisories/new>
2. **Fallback:** write to **<security@myriarch.dev>** if you cannot use GitHub. Do not put exploit details in the subject.

Please include:

- the affected component (`myriarchd`, `myrctl`, `myriarch-updater`, `myriarch-sim`, the web UI, the Helm chart, the
  installers) and the version or commit;
- a description of the problem and its impact (what an attacker needs and what they gain);
- how to reproduce it, ideally with a minimal configuration and the smallest input that triggers it;
- the configuration that matters (authentication mode, deployment type), with secrets and tokens removed;
- your name or handle if you want to be credited in the advisory.

Never send real credentials, tokens (`myra_`, `myre_`, `myrd_` prefixes) or private keys.

## What to expect

- We acknowledge your report and give a first response **within 14 days**; we aim to be faster.
- We confirm the problem, assess its severity and keep you informed about the progress.
- We develop and test a fix, then publish a release and a security advisory (with a CVE where appropriate). You are credited
  unless you prefer not to be.

## Coordinated disclosure

We follow coordinated disclosure with an embargo: the fix and the advisory are prepared first and published together.
The default maximum embargo is **90 days** from the report. We publish earlier when the problem is already being exploited
in the wild or when a fix is ready and released. We extend the period only by agreement with the reporter, for example when
a fix needs a coordinated change in an upstream project.

## Supported versions

myriarch is in alpha. Interfaces and configuration keys may still change. v0.1.0 is the first final release; release
candidates (`vX.Y.Z-rc.N`) are GitHub pre-releases.

| Version | Receives security fixes |
|---|---|
| `main` | yes |
| The latest minor release (`v0.N.x`) | yes, as a new patch release |
| Earlier minor releases | no; upgrade to the latest minor release |
| Release candidates (`-rc.N`) | no; a published candidate is never patched (fixes go into the next candidate or release) |

While the major version is 0, a minor release can contain breaking changes; the [changelog](CHANGELOG.md) lists them
and how to upgrade. The binaries, container images, the Helm chart and the installers of a release share its version
number and are fixed together. Vulnerabilities in the OpenTelemetry Collector or the OpAMP Supervisor are fixed
upstream; the collector versions that hosts run are chosen in your GitOps repository.

## Scope

In scope is the code in this repository and the artifacts built from it: the server, the CLI, the updater, the
simulator, the web UI, the Helm chart, the installers and the example deployment files. The development mode
(`myriarchd dev`) has well-known logins and tokens by design and must not be exposed; reports about the dev credentials
themselves are not vulnerabilities, but a way to start the dev mode in a production-like setup without noticing is.

Problems in third-party software (the OpAMP Supervisor, the OpenTelemetry Collector, PostgreSQL, Kubernetes) should be
reported to those projects. Tell us if myriarch makes such a problem worse.

## Security model and hardening

The controls of the system, the trust boundaries and the production settings are described in the
[security model](docs/security/model.md) and the
[production checklist](docs/operations/production-checklist.md). Limitations that remain are listed in
[Known limitations](docs/security/model.md#known-limitations); the [export control](docs/security/export-control.md)
assessment covers the cryptography in the software.
