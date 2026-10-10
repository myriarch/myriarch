---
title: "Agents"
weight: 20
description: "Collectors and updaters as seen by the server, how agents are identified, the attribute view that selectors and templates work on, server labels and the configuration sync states."
---

An **agent** is anything that connects to `myriarchd` over OpAMP. The server keeps its description, health, reported
configuration and the assignment that Git produces for it. Fleets, rollout waves and templates do not look at agents
directly. They work on the **attribute view** of an agent, described below.

## Kinds of agents

| Kind | What it is | How the server recognizes it |
|---|---|---|
| `collector` | An OpenTelemetry Collector run by the upstream OpAMP Supervisor. It accepts remote configuration. | It is not an updater and it announces the OpAMP capability "accepts remote config". |
| `updater` | The `myriarch-updater` host agent. It installs new collector binaries, because the upstream supervisor cannot do that. | Its identifying attribute `service.name` is `myriarch-updater`. |
| `unknown` | Any other OpAMP agent. It is listed, but never assigned to a fleet. | Everything else. |

Only collectors are assigned to fleets. An updater follows its collector: the two belong together when they present the
same credential (the same enrollment token, or the same client certificate when no token is used) and, if both report
one, the same host name. The updater links to its collector through the non-identifying attribute
`dev.myriarch.collector.instance_uid`, whose value is the instance UID of the supervisor. If more than one online updater
with the same credential claims the same collector, the link is marked ambiguous and no package offers are made until
that is resolved. See [Executors](executors.md#updater) and the
[updater protocol](../design/updater-protocol.md).

## Identity

An agent is identified by its OpAMP **instance UID**. The first registration binds the UID to the credential that was
used: the enrollment token, or the client certificate subject when mutual TLS is used. A second agent that presents the
same UID with a different credential cannot take it over, and the attempt is recorded as an identity conflict in the
agent's event history. The access control for agents is described in the
[security model](../security/model.md) and in
[ADR-0012](../adr/0012-agent-access-via-enrollment-tokens.md).

## The attribute view

Fleets, waves and templates work on the **attribute view** of an agent: a flat map from string to string. It is built in
this order, where later steps win on equal keys:

1. all *non-identifying attributes* of the agent (OpAMP `AgentDescription`),
2. all *identifying attributes*, which win over the non-identifying ones,
3. all **server labels**, added under the prefix `labels.` (for example `labels.wave`).

The namespace `labels.` belongs to the server. If an agent reports an attribute that starts with `labels.`, the server
drops it and records a warning in the agent's event history, so an agent can never set its own labels.

OpAMP attribute values (`AnyValue`) may be typed. They are converted to strings with fixed rules: a boolean becomes `true` or
`false`, an integer its decimal form, a floating point number its shortest form (`strconv.FormatFloat` with format `g`
and precision `-1`), bytes become Base64, and arrays and maps become JSON. Values are cleaned to valid UTF-8 without NUL
bytes. An agent can report at most 256 attributes per list, and each key or value is cut at 4096 bytes.

> **Trust assumption.** All attributes except `labels.*`, in particular `site.*`, `host.*` and `deployment.*`, are
> **self-reported** from the local configuration of the host. They are exactly as trustworthy as the host. A
> compromised host can place itself into another fleet by changing its attributes. Where fleets must be separated by
> force, for example production and test or different tenants, select on `labels.*` instead. Labels come from the
> enrollment token (one token per channel or region) or from the inventory, and you control who receives which token.
> Attribute selectors are a convenience for assignment, not an access boundary.

### Attributes the collector reports

The `opamp` extension of the collector reports, among others:

| Attribute | Meaning |
|---|---|
| `service.name` | The collector distribution, for example `otelcol-contrib`. |
| `service.version` | The collector version, for example `0.162.0`. The server stores it without a leading `v`. |
| `service.instance.id` | The instance ID of the collector. |
| `os.type` | `linux` or `windows`. |
| `host.name`, `host.arch` | Host name and CPU architecture. |
| `os.description` | A human readable description of the operating system. |

### The site attribute contract

The installation scripts write the following values into the supervisor configuration
(`agent.description.non_identifying_attributes`), and the supervisor reports them. They are the convention that the
examples and the default blueprints rely on. All values are strings.

| Attribute | Meaning | Example |
|---|---|---|
| `site.type` | Kind of site. Free text, lower case. | `edge`, `office`, `datacenter`, `cloud` |
| `site.id` | Site number or code. Always a string, leading zeros are kept. | `edge-001` |
| `site.name` | Human readable name. | `Edge site 1` |
| `site.country` | ISO 3166-1 alpha-2 country code. | `DE` |
| `site.region` | Region as your organization defines it. | `north` |
| `site.timezone` | IANA time zone of the site, used for maintenance windows. | `Europe/Amsterdam` |
| `deployment.environment.name` | The environment. | `production` |

An updater reports further attributes under the namespace `dev.myriarch.`: how it is installed (`binary` or
`container`), which server endpoint it uses, which supervisor service it restarts and, in container mode, the collector
version of its image. You do not need them in selectors.

## Server labels

Server labels are key-value pairs that the server attaches to an agent. They come from two places:

- the **enrollment token** that the agent registered with. Labels are copied to the agent when it registers. A token may
  carry at most 20 labels, with keys of 1 to 63 characters without whitespace or `=` and values of at most 253
  characters.
- the **inventory** objects in Git, see [Inventory](inventory.md). Inventory labels win over token labels on equal keys.

In selectors a label is addressed as `labels.<key>`, for example `labels.wave`.

## Assigned, desired and failed configuration

For the configuration the server keeps three hashes per agent. They are the basis of the sync states below and of the
difference between bootstrap and rollout described in [Fleets](fleets.md#when-does-the-offered-configuration-change).

| Hash | Meaning |
|---|---|
| assigned | What the current GitOps revision renders for this agent. |
| desired | What the server currently offers to the agent as remote configuration. |
| failed | A desired hash for which the agent reported `FAILED`. It is not offered again automatically. |

The agent reports back which hash it runs (`last_remote_config_hash`) and a status (`unset`, `applying`, `applied` or
`failed`). The server derives a **config sync state** for the agent:

| State | Meaning |
|---|---|
| `none` | The agent has no fleet or no assigned configuration yet. A render error on an agent that already has an assigned configuration keeps the previous hash and does not produce `none`. |
| `in_sync` | The reported hash equals the desired hash and the status is `applied`. |
| `pending` | The desired configuration differs from the reported one and nothing failed yet: it is offered, being applied, or the agent is offline. |
| `failed` | The agent reported `FAILED` for the desired hash. |
| `drift` | The desired hash differs from the assigned hash: a rollout has not reached the agent yet, or it was rolled back. |

The states are evaluated in this order: `none`, `failed`, `drift`, `in_sync`, then `pending`.

If the rendering for an agent fails, the agent keeps running the configuration it has. The reason is stored as the
assignment error of the agent (`assignment.error`) and shown in the UI. Next to it the assignment holds the fleet, the
three hashes and the effective target version of the fleet (`assignment.targetVersion`).
