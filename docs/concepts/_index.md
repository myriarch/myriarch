---
title: "Concepts"
weight: 30
description: "The ideas behind myriarch: Git as the source of truth, agents, blueprints, fleets, inventory, rollouts and executors."
---

These pages explain what the objects of a myriarch installation are and why they behave the way they do. They are
written to be read in order, but each page stands on its own. For the exact field lists, defaults and error codes see
the [GitOps schema reference](../reference/gitops-schema.md). For step-by-step instructions see the
[how-to guides](../tasks/_index.md). The [architecture](../architecture.md) shows how the components fit together, and the
[security model](../security/model.md) describes the controls behind them.

| Page | What it explains |
|---|---|
| [Git as the source of truth](gitops.md) | The repository layout, the object envelope, how `myriarchd` reads and applies a revision, and the rule "Git sets the frame, the UI operates within it". |
| [Agents](agents.md) | Collectors and updaters, how an agent is identified, the attributes that fleets and templates work on, server labels and the config sync states. |
| [Blueprints](blueprints.md) | Layered collector configurations: parameters, fragments, removal, the rendering algorithm, templates, the canonical form and the hash. |
| [Fleets](fleets.md) | Selectors, assignment and priority, bootstrap versus rollout, and choosing the target collector version in the UI within a frame set in Git. |
| [Inventory](inventory.md) | Server-side labels and parameter overrides for agents that match exact attribute values. |
| [Rollouts](rollouts.md) | Rollout policies, waves, targets and success, maintenance windows, failure handling, the planner and the live progress of an update. |
| [Executors](executors.md) | How collector version updates are started: `none`, `updater`, `webhook`, `awx` and `manual`, with payloads, signatures and secrets. |

The five kinds of GitOps objects fit together like this:

```text
Inventory ──labels, parameters──┐
                                ▼
Agent ──attributes──▶ Fleet ──▶ Blueprint (rendered per agent into one config)
                        │
                        ├──▶ RolloutPolicy (how a change reaches the fleet)
                        └──▶ Executor      (how a collector version is changed)
```

An agent reports its attributes. The fleet whose selector matches (the highest priority wins) decides which blueprint
is rendered, which parameter values apply, which collector version is wanted, and which policy and executor are used.
The inventory adds labels and parameter overrides for individual agents before the fleet is chosen.
