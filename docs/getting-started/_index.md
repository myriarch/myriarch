---
title: "Getting started"
weight: 10
description: "Tutorials: start a complete Myriarch system on your own machine, from source or with Docker Compose."
---

These pages are the tutorials of the documentation. They teach by doing: you follow the steps in order and end with a
working system. Two paths lead to it. From source, Myriarch runs on a development machine without a container runtime
and without external services: `myriarchd dev` starts the server with an embedded PostgreSQL, the example GitOps repository
and well-known development logins, and `myriarch-sim` connects simulated collectors to it. With Docker Compose, one file
starts the server, PostgreSQL and the simulator from the release images (available from v0.1.0).

| Page | What you get |
|---|---|
| [Local development](local-dev.md) | From the source code to a running server with a sample fleet: build, start, sign in, change the GitOps repository, call the REST API, validate and render configuration with `myrctl`. |
| [Your first fleet and rollout](first-fleet-and-rollout.md) | A guided first session from source: write a fleet and a rollout policy, enroll ten simulated collectors with an enrollment token and roll out a change in two waves with an approval. |
| [Docker Compose](docker-compose.md) | The server, PostgreSQL and 200 simulated collectors on one machine from the release images, for an evaluation; your own GitOps directory, reset. |

The pages in [How-to guides](../tasks/_index.md) build on this setup. Start with [Simulate a fleet](../tasks/simulate-a-fleet.md)
to fill the system with agents, then roll out a [configuration change](../tasks/roll-out-a-config-change.md) or a
[collector version](../tasks/roll-out-a-collector-version.md).

To try the whole system on Kubernetes, with the Helm chart, a real server pod and a real collector, use the kind probe
described in [hack/kind/README.md](../../hack/kind/README.md). It builds a local cluster from zero and runs numbered
checks against it. The tutorial for Kubernetes on a local kind cluster is planned for v0.2.0. For a real installation read
[Install with Helm](../operations/install-helm.md) and the [production checklist](../operations/production-checklist.md).
