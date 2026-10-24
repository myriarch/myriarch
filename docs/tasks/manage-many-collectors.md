---
title: "Manage many OpenTelemetry Collectors with OpAMP and Git"
weight: 5
description: "Run a fleet of OpenTelemetry Collectors with Myriarch: Git holds the frame, an OpAMP server rolls changes out in waves, and every action is audited."
---

To manage many OpenTelemetry Collectors with OpAMP, you need three pieces: the upstream **OpAMP Supervisor** with a collector on
every host, an **OpAMP server** (`myriarchd`), and a **Git repository** that declares what the fleet should run. Operators change
the fleet by merging a change in Git; the server renders one configuration per collector, offers it over OpAMP and rolls it out in
waves, with approvals and rollback, and records every action in the audit log. This guide walks through the setup from an empty
repository to the first rollout. It is designed for around 1,000 collectors, with headroom up to 5,000 (design targets, see the
[architecture](../architecture.md#scaling)).

Myriarch is alpha; the [README](../../README.md#status) lists the support level of each component. Read
[Concepts](../concepts/_index.md) first if the terms blueprint, fleet and rollout are new to you.

## 1. Run the server

The server is one binary that needs PostgreSQL. To try it on your machine, follow [Local development](../getting-started/local-dev.md)
or [Docker Compose](../getting-started/docker-compose.md); both run the server in development mode with well-known logins, which is
not a production setup. For a real installation use [Install with Helm](../operations/install-helm.md) (the chart is beta from
v0.2.0) and go through the [production checklist](../operations/production-checklist.md).

## 2. Start a GitOps repository

Copy the starter in [gitops-template/](../../gitops-template/README.md) into an empty repository on your Git server, protect the
default branch so that changes go through merge requests, and give the server a read-only token. The repository holds
`blueprints/`, `fleets/`, `policies/`, `executors/` and `inventory/` below the path that you configure as `gitops.path`; the
server reads every YAML file there as an object, except `samples/` directly below that path and directories whose name starts
with a dot. The examples in steps 3 and 7 come from the complete example repository in
[examples/quickstart](../../examples/quickstart/README.md); the starter template uses other names and values for sites and
policies, so take the structure from the examples and the names from your own repository. See [Git as the source of truth](../concepts/gitops.md) for the layout and the rules.

## 3. Describe your fleets

A fleet selects agents by their attributes and says what they run. This is an excerpt of `examples/quickstart/fleets/linux-hosts.yaml`, shortened (it leaves out `description`, `displayName`,
`parameters` and the selection frame for the UI):

```yaml
apiVersion: myriarch.dev/v1alpha1
kind: Fleet
metadata:
  name: linux-hosts
spec:
  priority: 100                    # the higher priority wins when selectors overlap
  selector:
    matchAttributes: {os.type: linux}
    matchExpressions:
      - {key: site.type, operator: In, values: [edge, datacenter]}
  blueprint: linux-host            # required
  collector:
    distribution: otelcol-contrib
    version: 0.162.0               # the Git target, without "v"
  update:
    executor: updater
  rollout:
    configPolicy: config-canary
    versionPolicy: canary-then-all
```

The configuration itself comes from a [blueprint](../concepts/blueprints.md): layered, parameterized, rendered on the server. The
rollout policies define the waves, approvals and failure handling ([Rollouts](../concepts/rollouts.md)). Give fleets whose selectors
can overlap different priorities; two fleets with the same priority leave an agent unassigned. Every field is listed in the
[GitOps schema reference](../reference/gitops-schema.md).

## 4. Check every change in CI

Run `myrctl validate` on merge requests. It runs the same validation as the server, so a change that passes is accepted by it:

```bash
myrctl validate --strict --components ci/components-otelcol-contrib-0.162.0.json config
```

Render the configuration of a sample agent to review what a host would receive, not only the blueprint source. See
[Validate a GitOps repository](validate-a-gitops-repository.md) for the options and a CI pipeline.

## 5. Connect the server to the repository

Set the repository in the configuration of `myriarchd` (`gitops.*`; see the
[configuration reference](../reference/configuration.md)). The server polls every 60 seconds by default, and a webhook at
`POST /api/v1/gitops/webhook` syncs at once. It applies a revision only if the whole revision is valid. `GET /api/v1/gitops/status`
shows `state: ok` when it has read your repository, and the fleets appear in the UI.

## 6. Enroll the hosts

Create an enrollment token in the UI (*Settings*, *Tokens*) and run the installation script on each host. The script sets up the
upstream supervisor, the collector and optionally `myriarch-updater`. The `site.*` options become attributes that your fleet
selectors match on. [Connect a host](connect-a-host.md) has the inputs and the exact commands; Linux hosts with systemd are beta and
Windows hosts are experimental ([levels and tested platforms](../operations/tested-platforms.md)). Collectors in Kubernetes run as pods of the Helm chart `myriarch-collector` with
unchanged upstream images; see [Install the collector with Helm](../operations/install-collector-helm.md).

## 7. Change the fleet

Edit a blueprint or a fleet in Git and merge. The server renders the fleets again and creates a configuration rollout for each fleet
whose rendered configuration changed. With the quickstart policy `config-canary`, the canary hosts get the change first, the rollout
waits for their health check and stops for approval before the rest of the fleet follows. If the failure threshold is reached,
the policy decides between rolling back and pausing. [Roll out a configuration change](roll-out-a-config-change.md) shows the happy
path and the failure path, and the actions you have in the UI and the API.

Do not change the configuration and the collector version in the same merge request; the two kinds of rollout run independently.
To change versions, continue with [Upgrade the OpenTelemetry Collector on many hosts](upgrade-collectors-on-many-hosts.md).

## 8. Audit

The UI and the REST API only operate inside the frame that Git sets, and every action is audited: approvals with the user and the
reason, and a version chosen in the UI (`fleet.version.select`, with who, when and why). The audit log is part of the API reference ([REST API](../reference/api/_index.md)).
