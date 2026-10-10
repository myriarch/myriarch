---
title: "Inventory"
weight: 50
description: "Server-side labels and parameter overrides for agents that match exact attribute values."
---

The **inventory** adds information to agents that the agents do not report themselves. An entry matches agents by exact
attribute values and attaches **labels** and **parameter overrides** to them. Typical uses are putting the first host
of each kind into a canary wave, or giving one site its own gateway endpoint.

```yaml
apiVersion: myriarch.dev/v1alpha1
kind: Inventory
metadata:
  name: waves
spec:
  entries:
    - match: {site.id: edge-001}      # exact equality of all keys
      labels: {wave: canary}          # a server label, used in selectors as labels.wave
    - match: {site.id: edge-002}
      parameters:                     # parameter overrides for the blueprint
        gatewayEndpoint: gateway-2.example.com:4317
```

The quickstart inventory is [`examples/quickstart/inventory/waves.yaml`](../../examples/quickstart/inventory/waves.yaml).
The fields are listed in the [schema reference](../reference/gitops-schema.md#inventory).

## How entries are applied

- `match` is a map of attribute keys to values. An entry applies to an agent when **all** keys are present in the
  agent's attributes with exactly these values. An entry with an empty `match` is an error, because it would apply to
  every agent.
- `match` sees only the attributes the agent reports. It does **not** see `labels.*`, so entries never depend on each
  other, and a `match` key that starts with `labels.` is an error.
- `labels` become [server labels](agents.md#server-labels) of the agent. In selectors they are addressed as
  `labels.<key>`. Label values and `match` values are strings. The text as written in the file is used, and quoting values such
  as `"true"` or `"1001"` makes that explicit.
- `parameters` override blueprint parameters. They rank above the fleet's parameters and above values taken from
  attributes, and below preview overrides, see [Blueprints](blueprints.md#parameters). A parameter that the blueprint
  does not declare is ignored with a warning.

## Order

Inventory objects are applied sorted by name, and the entries of one object in file order. **Later matches override
earlier ones**, for labels and for parameters, key by key. Inventory labels also override the labels that came from the
enrollment token when the keys are equal.

## Where the inventory takes effect

The inventory is applied first when an agent is assigned. The labels it creates are already visible to the selectors of
fleets, and to the selectors of rollout waves ([Fleets](fleets.md#assignment-and-priority), [Rollouts](rollouts.md)).
