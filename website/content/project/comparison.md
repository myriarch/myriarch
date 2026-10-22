---
title: "OpAMP server options compared: Supervisor, DIY, Myriarch"
weight: 14
description: "A neutral comparison of the OpAMP Supervisor alone, a server built on opamp-go and Myriarch, with sources and dates, and when Myriarch is not the right choice."
---

You run OpenTelemetry Collectors at scale and want to manage them over OpAMP. This page compares three ways to do it, says when
each one fits, and names the cases in which Myriarch is **not** the right choice. Statements about other projects and products
are taken from their public documentation, were read on **22 October 2026** (they are "as of October 2026") and link to
their source. They can be out of date by the time you read this; check the source. The page makes no judgment about the
quality of any other product.

Myriarch is an independent project. OpenTelemetry, Kubernetes and CNCF are trademarks of The Linux Foundation, and myriarch is not
affiliated with or endorsed by The Linux Foundation or the OpenTelemetry project. The names are used only to describe
compatibility. Other product names belong to their owners and appear only to describe what they are.

## The three options in short

**The OpAMP Supervisor alone** is the upstream process that starts and monitors a collector and speaks OpAMP for it. It is a
client: it needs an OpAMP server to talk to, and it does not decide what a fleet should run.

**A server of your own on opamp-go** means you write the server side with the Go implementation of OpAMP and build whatever
your fleet needs on top.

**Myriarch** is a ready server and control plane for collectors that run under the upstream supervisor. It adds configuration
rendering from Git, rollouts in waves, collector version updates through executors, a web UI and an audit trail.

| | OpAMP Supervisor alone | Own server on opamp-go | Myriarch |
|---|---|---|---|
| What it is | Upstream process next to each collector (a client) | A Go library with client and server parts of the protocol | A server with web UI, REST API, GitOps syncer and rollout engine |
| Needs an OpAMP server | Yes, an existing OpAMP server that you run or buy elsewhere | You write it | Included (`myriarchd`, needs PostgreSQL) |
| Remote configuration | Accepts it from a server | You implement what the server sends | Renders it from blueprints in Git, one per collector |
| Rollouts in waves and approvals | Not among its documented features | You build them | Included |
| Rollback of a bad configuration | Optional automatic rollback to the last working remote configuration on the host (off by default) | You build it | Per policy, for the whole rollout: automatic rollback or pause; after a pause, an operator resumes, retries, aborts or rolls back |
| Collector binary updates | Not implemented upstream (see below) | You build them | Through executors: `myriarch-updater`, a webhook or AWX |
| Source of truth | Not part of its scope | Yours to decide | A Git repository |
| Audit of operator actions | Not among its documented features | You build it | Included |
| Maturity | Alpha, per the upstream README | README of opamp-go: "work-in-progress"; README of the supervisor: "production-ready client and server implementations in Go" | Alpha |
| Effort for you | One process per host, plus an OpAMP server from elsewhere; no fleet view of its own | You write and run the server | Run one server and a database |

The rows for the supervisor and opamp-go rest on the sources below; the rows for Myriarch rest on this site's
[documentation](/docs/).

## What the sources say

- **OpAMP Supervisor** ([README of `cmd/opampsupervisor`](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/cmd/opampsupervisor/README.md),
  read 22 October 2026): its stability is alpha. It runs a collector with a configuration from an OpAMP server and can fall back
  to the last working remote configuration when that is enabled (it is disabled by default). The feature "Updates the Collector
  binary" has no checkmark and points to
  [issue #33947](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/33947); the capabilities
  `AcceptsPackages` and `ReportsPackageStatuses` are listed as not implemented and point to
  [issue #47272](https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/47272). Our own review of the
  supervisor sources, with dates, is in the [upstream notes](/docs/design/upstream-notes/).
- **opamp-go** ([README](https://github.com/open-telemetry/opamp-go), read 22 October 2026): a Go implementation of OpAMP with
  `client` and `server` parts. The README of opamp-go describes the repository as "work-in-progress of an OpAMP implementation in Go", while the supervisor README
  (read 22 October 2026) points to it for "production-ready client and server implementations in Go". We quote both and do not rate either. A
  library implements the protocol; storage, rendering, rollouts, a user interface and access control above it are for you to build.
- **Bindplane** ([documentation](https://docs.bindplane.com/), read 22 October 2026): describes itself as an
  "OpenTelemetry-native telemetry pipeline" and uses OpAMP to send configuration to collectors. The documentation describes a
  cloud-hosted and a self-hosted option, and pricing plans that "range from Free to Enterprise". For what it does beyond that, read
  its documentation; this page does not compare features it has not verified.

These are not all options. Other products and open-source servers exist; the list above is the part this page can cite.

## When does what fit?

**The supervisor alone fits** when you manage few collectors, you already have configuration management for the hosts, and you are
content to update collector binaries with the tools you use today. You get no fleet view and no rollouts of its own, and you
still need an OpAMP server to point it at.

**An own server on opamp-go fits** when your requirements differ from any existing server, you can staff and maintain a server,
and you want to decide every behavior yourself. Expect to build and secure everything above the protocol.

**A hosted or commercial product fits** when you prefer a managed service or a vendor relationship over running a server
yourself. Read the vendor's documentation for what it offers; Myriarch is software you run yourself, and its documentation
describes no hosted service.

**Myriarch fits** when you run many collectors (it is designed for around 1,000, with headroom up to 5,000), want Git to be the
source of truth, need changes to reach hosts in waves with approvals and rollback, need collector version updates that the
supervisor cannot do, and want every operator action audited.

## When Myriarch is not the right choice

- **You want a managed service.** Myriarch is software you run, with PostgreSQL. The documentation describes no hosted offering.
- **You need stable interfaces today.** Myriarch is alpha: interfaces, configuration and the GitOps format
  (`myriarch.dev/v1alpha1`) can still change. See the [roadmap](/project/roadmap/#from-alpha-to-beta) for what has to hold before
  the project calls itself beta.
- **Your hosts are mostly Windows and you need more than experimental support.** In v0.1.0, Windows hosts are experimental; Linux
  hosts and container mode are beta and the Helm chart is experimental.
- **You manage only a few collectors.** A server, a database and a Git workflow are more than a handful of hosts need.
- **You need multi-tenancy.** It is on the [roadmap](/project/roadmap/#later) as a later item, not part of v0.1.0.
- **You want to edit configuration in a UI.** Myriarch keeps declared objects in Git on purpose; the UI operates inside that frame.
- **You need something other than OpenTelemetry Collectors.** Myriarch is documented for OpenTelemetry Collector Contrib instances
  under the upstream supervisor.

## Related

- [What is Myriarch?](/project/what-is-myriarch/)
- [Upgrade the OpenTelemetry Collector on many hosts](/docs/tasks/upgrade-collectors-on-many-hosts/), the case the supervisor does not cover
- [FAQ](/project/faq/)
