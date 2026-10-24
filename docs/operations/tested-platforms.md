---
title: "Support levels and tested platforms"
weight: 27
description: "The support level of every component in v0.2.0 and the platforms of the automated tests: Linux and Windows hosts, and two Kubernetes versions on kind."
---

This page says what the project supports in v0.2.0 and on which platforms its automated tests run. A platform that is not
listed may work; the project has not tested it.

## Support levels

Myriarch as a whole is **alpha**. The level of a single component can be higher.

| Component | Level |
|---|---|
| `myriarchd` and the web UI | Alpha |
| Server Helm chart `deploy/helm/myriarch` | Beta |
| Collector Helm chart `deploy/helm/myriarch-collector` | Experimental |
| Linux hosts (systemd) | Beta |
| Windows hosts | Experimental |
| Container mode of `myriarch-updater` (Kubernetes pods) | Beta |

- **Alpha:** works end to end, but interfaces, configuration and behavior can change without notice.
- **Beta:** covered by automated tests and the end-to-end and cluster probe runs; breaking changes are announced in the
  [changelog](../../CHANGELOG.md).
- **Experimental:** limited testing; it can change incompatibly or be removed.

The levels of v0.1.0 were: container mode and Linux hosts beta, Windows hosts and the Helm chart experimental
([ADR-0021](../adr/0021-publication-gates-and-v0-1-0-scope.md)). v0.2.0 raises the server chart to beta.

## Tested platforms

### Hosts

The host test installs a host with the one-command installer of the server (on Linux also with the manual path), enrolls it, rolls out a
configuration and a collector version, and updates the host. It runs in GitHub Actions on every change of the installers and the
updater.

| Platform | Architecture | Shell | Level |
|---|---|---|---|
| Ubuntu 24.04 | amd64 | `install-linux.sh` as root, services under systemd | Beta |
| Ubuntu 24.04 | arm64 | `install-linux.sh` as root, services under systemd | Beta |
| Windows Server 2025 | amd64 | `install-windows.ps1` as administrator, in Windows PowerShell 5.1 | Experimental |
| Windows Server 2025 | amd64 | `install-windows.ps1` as administrator, in PowerShell 7 | Experimental |

Windows on ARM64 is not tested; the installer stops with `unsupported_platform` unless you pass the architecture `amd64`
explicitly. The Windows files of v0.2.0 are not Authenticode-signed ([Verify releases](../security/verify-releases.md)).

### Kubernetes

The kind test installs both charts from the packaged archives on a kind cluster, checks the installation and runs the upgrade
from the chart of the tag v0.1.0. It runs on two Kubernetes versions, whose node images are pinned in
[`hack/kind/versions.env`](../../hack/kind/versions.env):

| Kubernetes | Node image pin | Test |
|---|---|---|
| v1.37 (newest) | `KIND_NODE_IMAGE` | The kind probe and the chart test |
| v1.36 (previous) | `KIND_NODE_IMAGE_PREVIOUS` | The chart test |

The pins are the source of truth; the list changes when they change.

## What the results confirm

These lists describe the tests as they are configured. The runs in CI on the release candidate `v0.2.0-rc.1` confirm them
before the release; the release notes of a version state the levels that apply to it. A test result is a statement about the
tested platform, not a promise for others: for example, a Kubernetes version between the two, or another Linux distribution,
is expected to work but is not covered.

## Related

- [Connect a host](../tasks/connect-a-host.md): the installers and the platforms they support.
- [Install with Helm](install-helm.md) and [Install the collector with Helm](install-collector-helm.md).
- [Upgrade from v0.1.0 to v0.2.0](upgrade-to-v0-2-0.md).
- [Roadmap](../../ROADMAP.md): what comes next and what moves a component to beta.
