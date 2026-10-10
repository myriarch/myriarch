---
title: "Design documents"
weight: 90
description: "Normative design contracts of the project and the upstream facts they rest on."
---

These documents hold the normative design contracts of the project and the upstream facts they rest on. They are
written for contributors and operators who need to know exactly how the components agree with each other. Code
comments, tests and configuration may cite them by file and heading, and a change to a described behaviour changes the
matching document in the same commit.

"Normative" means: when code and a document disagree, one of them is wrong, and the disagreement is a defect to fix, not
a matter of taste. Facts about third-party software in [upstream-notes.md](upstream-notes.md) are the exception. They are
observations with a date and a source, and they can age.

| Document | Purpose |
|---|---|
| [version-selection.md](version-selection.md) | How a fleet's target collector version is chosen: the frame declared in Git, the effective target and the conflict rules between Git and the UI, the REST operations, events, permission and audit trail, and the live progress per target. |
| [updater-protocol.md](updater-protocol.md) | The contract between myriarchd and `myriarch-updater`: identity attributes, package offers, the update health component, the phases and steps the server reads, the install, verification and rollback behaviour on the host, and the download base URLs. |
| [container-mode.md](container-mode.md) | `myriarch-updater` as the main process of a container that supervises the OpAMP Supervisor: process model, binary layout on a volume, seeding from the image, probes, signals and limits. |
| [opamp-server.md](opamp-server.md) | Server behaviour by component: persistence, event bus and leader lock, authentication, the OpAMP endpoint, the HTTP and live interfaces, the GitOps reader, blueprint rendering, assignment, rollout planner and engine, executors, artifact cache, release tracker, notifier, and how collectors are installed on hosts. |
| [upstream-notes.md](upstream-notes.md) | Verified facts about the OpAMP Supervisor, the collector release assets and their signatures, Kubernetes options and the OpAMP Bridge, configuration merge semantics, and database pitfalls, each with a public source and a dated status. |

Conventions:

- Names of API operations and schemas (`operationId`, schema names) refer to `api/openapi.yaml`, which stays the
  authoritative definition of the REST interface.
- Examples use reserved example domains and generic fleet and site names.
- Where a document gives a default value, it is the default the code ships with.
