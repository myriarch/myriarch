# 0004. Server-side rendering to one content-addressed configuration per agent

- Status: Accepted
- Date: 2026-10-07
- Related: [ADR-0003](0003-gitops-as-source-of-truth.md), [ADR-0005](0005-templating-with-double-brackets-and-typed-evaluation.md)

## Context

An agent's collector configuration is built from reusable parts: a blueprint with its parent blueprints, fragments, parameters from the fleet and the inventory, and attributes the agent reports. OpAMP lets the server send a remote configuration made of several named entries, and the supervisor merges them on the agent. The supervisor merges maps recursively and replaces lists and scalars with the later value ([upstream notes](../design/upstream-notes.md#collector-configuration-merge)).

A rollout needs a clear unit: which exact configuration does an agent run, what changed between two revisions, and has the agent applied it?

## Decision

The server renders each agent's configuration completely and sends exactly one configuration body per agent.

- Rendering resolves the blueprint chain, parameters and templates, validates the result and produces a canonical form ([blueprints](../concepts/blueprints.md)).
- The rendered body is stored content-addressed, identified by its SHA-256 hash. Agents with the same inputs share one stored body.
- The hash is the rollout unit: the assignment names an assigned hash, the rollout engine sets the desired hash, and the agent reports the hash it applied. The sync state of an agent (`none`, `in_sync`, `pending`, `failed`, `drift`) is derived from these hashes ([agents](../concepts/agents.md)).
- Differences between two revisions can be shown as a diff of two stored bodies.

## Consequences

### Positive

- A single, unambiguous configuration per agent, with a stable hash for comparison, diff and rollout tracking.
- Storage stays small because identical bodies are stored once.
- The server can validate the final result before any agent sees it.

### Negative

- Rendering runs on the server for every assignment change, and a change to a shared blueprint touches every agent that uses it.
- Per-agent configurations are possible and legal, but then each one is stored separately.

## Alternatives considered

- **Several `config_map` entries that the supervisor merges**: hard to follow, because the merge happens on the agent and the result differs from any single stored body.
