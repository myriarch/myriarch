# 0037. OpAMP Bridge agents are recognised and locked until the Operator integration

- Status: Accepted
- Date: 2026-10-15
- Related: [ADR-0026](0026-collector-version-updates-on-kubernetes.md) (refined by this record), [ADR-0003](0003-gitops-as-source-of-truth.md), [ADR-0008](0008-kubernetes-supervisor-and-collector-in-one-pod.md), [ADR-0012](0012-agent-access-via-enrollment-tokens.md), [Upstream notes](../design/upstream-notes.md#opamp-bridge-identity-modes-and-remote-config), [Fleets](../concepts/fleets.md#bridge-agents)

## Context

[ADR-0026](0026-collector-version-updates-on-kubernetes.md) decides that a managing integration with the OpenTelemetry
Operator and its OpAMP Bridge comes later, and that until then "agents identified as Bridge-managed receive no remote
configuration unless explicitly opted in". It does not say how an agent is identified, what the opt-in is, or what else must
not reach a Bridge. The server did not implement the sentence: it classifies an agent as a collector when the agent accepts
remote config, so a Bridge is assigned a fleet by its selectors, is offered the rendered configuration, takes part in
configuration and version rollouts and can be sent a restart command.

What the Bridge does with such a message is the reason for the decision
([Upstream notes](../design/upstream-notes.md#opamp-bridge-identity-modes-and-remote-config), read from the source of the Operator at
the tags v0.160.0 and v0.161.0 and on main):

- The Bridge connects as one agent for many collector resources. In operator mode the keys of its configuration are
  `<namespace>/<name>`, the value is the complete resource, and a message that lacks the key of a resource that the Bridge
  manages deletes that resource. The single entry `myriarch.yaml` that the server sends is rejected as an entry, and the
  deletion of the resources whose keys are missing still runs.
- In standalone mode every client of the Bridge manages one workload: the key is the name of a configured config entry, the
  value a collector configuration, which the Bridge writes into an existing ConfigMap before it restarts the workload.
  Nothing is deleted.
- In operator mode a restart command restarts the workloads of all collector resources that the Bridge lists.
- The effective configuration of a Bridge holds the complete resources as written, with fields such as environment
  variables and arguments. The server redacts effective configurations by the names of keys and the shape of values; those
  rules were written for collector configurations, and the endpoint is readable with the permission `read`.
- The Bridge reports who it is itself: the `service.name` `io.opentelemetry.operator-opamp-bridge` in operator mode, the
  name of the workload in standalone mode, and the attribute `opentelemetry.io/opamp.bridge.mode` in both modes since
  Operator 0.157.0.

The server holds no Kubernetes credentials and does not ask the cluster who an agent is ([ADR-0026](0026-collector-version-updates-on-kubernetes.md)).

## Decision

- **Recognition.** An agent is a Bridge when it reports the `service.name` of operator mode or the attribute
  `opentelemetry.io/opamp.bridge.mode`, with any value. Either is enough. The mode is the value of the attribute when it
  is `operator` or `standalone`, and unknown otherwise. We will read the identifying facts again in the source of the
  Operator at its release tag before a release relies on them; a test with a real Bridge on a cluster follows later and
  does not block the release.
- **Agent kind `bridge`.** A Bridge is a new kind of agent next to `collector`, `updater` and `unknown`. A Bridge is not a
  member of a fleet for versions: it does not count for the reference of `allowDowngrade`, for the target version, for the
  fleet statistics or for the release views, and no executor is called for it. The UI marks it and offers no package or
  restart action.
- **No remote config without the opt-in, and the opt-in only for standalone mode.** A guard in front of every offer sends no
  remote config to a Bridge (it fails closed), whatever starts the offer: the first message, a poll, a push, a resync or a
  reconnect. The Git frame carries the opt-in: `spec.bridge.manage` of a fleet, default `false`. It takes effect for
  Bridges in standalone mode only. A Bridge in operator mode stays locked with the opt-in as well, and so does a Bridge
  whose mode is unknown, until a managing integration exists (an agent kind for the cluster, a map of the custom resources,
  `spec.image` as the way to change a version).
- **No restart.** A restart command to a Bridge is always refused, with the status 412 and an audit entry, in both modes and
  with the opt-in. Restarts of workloads run through Kubernetes.
- **No bodies.** The server stores no effective configuration body of a Bridge and delivers none, in both modes and also
  when the Bridge reports exactly one key. When an agent becomes a Bridge, the server deletes the effective
  configurations it stored before; rows of earlier releases are cleaned in the same way. The read-only inventory shows the
  keys and the health tree.
- **A warning in Git.** `myrctl validate` and the GitOps sync warn with `bridge_selected` when the selector of a fleet without
  the opt-in also matches a Bridge, a synthetic one in each mode or a sample agent of the repository. The warning is part
  of the strict check.
- **Limits.** Recognition rests on attributes that the agent reports itself, so it is a safeguard against mistakes and no
  authentication. A collector that reports the mode attribute is treated as a Bridge (it fails safe). A Bridge of a release
  before 0.157.0 in standalone mode reports neither the constant name nor the attribute and stays a collector (it fails
  open). The attributes that an agent cannot choose are the labels of its enrollment token
  ([ADR-0012](0012-agent-access-via-enrollment-tokens.md)): the documentation recommends a token of its own for Bridges and
  selectors on `labels.*` that keep them out of other fleets.

## Consequences

### Positive

- A fleet with a broad selector can no longer change or delete the custom resources that a Bridge manages, and a restart
  command can no longer reach the workloads of every collector resource that a Bridge lists.
- The inventory of a Bridge contains no resource specifications that the viewer role could read.
- The rules for the managing integration are written down before it exists: it is a new agent kind with a model of the
  custom resources, not a flag on a collector.
- The warning tells authors where a selector is too broad before the server runs into it.

### Negative

- Users of the Bridge cannot change their collector resources through the server in this release. The inventory and the
  health tree are read-only, and a version changes in the deployment repository.
- The opt-in for standalone mode needs the configuration of the Bridge to name a config entry `myriarch.yaml`, the key under
  which the server sends the rendered configuration.
- The agent kind adds a value that older replicas do not know. During a rolling update the replicas of an earlier release
  still classify a Bridge as a collector until all replicas run the new version; the upgrade notes say to scale to one
  replica for the update when Bridges are connected. There is no supported rollback to an earlier release.
- A Bridge that hides its identity, or is not recognised for the reasons under Limits, is treated like a collector.

## Alternatives considered

- **Recognise by `service.name` only**: a standalone client reports the name of its workload, and the mode attribute exists
  since 0.157.0, so neither feature covers every Bridge. Both are used.
- **Treat a Bridge like a collector and let a label opt it out**: this fails open, and a missing label would let a selector
  delete resources.
- **Let the opt-in manage operator-mode Bridges now**: the server would have to send one entry per custom resource, with
  the complete resource as value, without knowing the resources of the cluster; a missing entry deletes a resource. This
  belongs to the managing integration of [ADR-0026](0026-collector-version-updates-on-kubernetes.md).
- **Store the effective configuration of a Bridge with redaction**: no redaction concept exists for resources that no
  collector has redacted. It can be revisited with such a concept.
- **Allow the restart command with the opt-in**: in operator mode it restarts the workloads of every listed resource, and
  Kubernetes is the control for restarts.
- **Check the identity with the Kubernetes API**: the server holds no cluster credentials by decision of
  [ADR-0026](0026-collector-version-updates-on-kubernetes.md).
