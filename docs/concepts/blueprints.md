---
title: "Blueprints"
weight: 30
description: "Layered, parameterized collector configurations: parameters, fragments, rendering, templates, merge rules, the canonical form and the hash."
---

A **blueprint** is a layered, parameterized collector configuration. `myriarchd` renders a blueprint on the server into
exactly one configuration per agent. Many agents usually render to the same bytes, and the result is stored once,
addressed by its hash. This design follows
[ADR-0004 (server-side rendering to one content-addressed config)](../adr/0004-server-side-rendering-to-one-content-addressed-config.md)
and
[ADR-0005 (templating with double brackets and typed evaluation)](../adr/0005-templating-with-double-brackets-and-typed-evaluation.md).

```yaml
apiVersion: myriarch.dev/v1alpha1
kind: Blueprint
metadata:
  name: windows-host
spec:
  extends: base                  # optional parent blueprint (single inheritance, no cycles)
  parameters:                    # typed parameters
    - name: appLogPaths
      type: list
      default: ['C:\ProgramData\ExampleApp\logs\*.log']
    - name: collectEventLog
      type: bool
      default: true
  config:                        # the collector configuration tree; strings may contain [[ ... ]]
    receivers:
      file_log/app:
        include: "[[ .params.appLogPaths ]]"
  fragments:                     # conditional overlays
    - name: no-eventlog
      when: "[[ not .params.collectEventLog ]]"
      remove:
        - receivers::windows_event_log/application
  remove: []                     # confmap paths deleted after this layer
```

The field list of `Blueprint` is in the [schema reference](../reference/gitops-schema.md#blueprint). The complete
quickstart blueprints are in [`examples/quickstart/blueprints/`](../../examples/quickstart/blueprints/).

## Layers and inheritance

A blueprint can `extends` one parent. A fleet names the **leaf** blueprint, and the chain from the root to the leaf is
the list of layers. In the quickstart, `windows-host` extends `base`, so the chain is `base`, `windows-host`. A missing
parent is the error `blueprint_not_found` and a cycle is `extends_cycle`.

## Parameters

Parameters let one blueprint serve many fleets. Each parameter has a `name` (letters, digits and `_`, starting with a
letter, used in templates as `.params.<name>`) and a `type`: `string`, `int`, `number`, `bool`, `duration`, `list` or
`map`. Optional constraints are `required`, `enum`, `pattern` (an RE2 expression that must match the whole string, only
for `string`), `min` and `max` (inclusive, only for `int` and `number`), and `fromAttribute`, which takes the value from
the agent's [attribute view](agents.md#the-attribute-view) when the key exists.

**Where a value comes from.** The sources are applied in this order, and a later source wins:

1. the `default` of the declaration,
2. `Fleet.spec.parameters`,
3. the agent attribute named by `fromAttribute`, if the agent has it,
4. the parameters of matching [inventory](inventory.md) entries,
5. preview overrides, which exist only in the UI preview and in `myrctl render --param`.

The winning source of each value is reported with the render result (`parameterSources`) as `default`, `fleet`,
`attribute`, `inventory` or `override`.

After that the type, `enum`, `pattern` and range are checked. A `required` parameter without a value is
`missing_parameter`. A fleet that sets a parameter the blueprint does not declare gets `unknown_parameter` as an error.
An undeclared inventory parameter is ignored with a warning of the same code.

**Types of values.** Values that come from YAML (defaults, fleet, inventory) must already have the right type. A
`number` also accepts an integer. A `duration` is a string in the Go format (`30s`, `5m`, `1h30m`) that is checked with
`time.ParseDuration` and rendered **as a string**. Values that come from attributes are strings and are converted: `int`
with `strconv.Atoi`, `number` with `ParseFloat`, `bool` with `ParseBool`, and `list` and `map` are read as JSON. A value
that does not fit is `invalid_parameter_value`.

**Redeclaring a parameter.** Parameters are collected from the root to the leaf. A child may declare a parameter of its
parent again **with the same type**. The declaration of the child then replaces the one of the parent completely: all
fields, such as `default`, `required`, `enum` and `fromAttribute`, are taken from the child, and fields it does not
state count as not set. A different type is the error `parameter_type_conflict`. The order of parameters is the order of
their first declaration.

## Fragments

`fragments` are conditional overlays inside one blueprint. They are evaluated **in file order**, right after the
`config` of the same blueprint. `when` is **exactly one** template expression `"[[ ... ]]"` that must evaluate to a
boolean (`when_not_bool` otherwise). An empty `when` means always. An active fragment is merged, and then its own
`remove` paths are applied. Fragment names are unique within the blueprint.

Fragments are the tool for optional features. The quickstart `linux-host` blueprint has one fragment per combination of
two switches, because a list such as the receivers of a pipeline is replaced as a whole when merging. The fragments are
deliberately disjoint.

## Removing configuration

`remove` lists paths in the style of the collector's confmap: map keys separated by `::`, for example
`receivers::host_metrics`, `service::pipelines::logs` or `exporters::otlp_grpc/gateway`. The paths are deleted after the
layer's `config` and fragments have been merged. A path that does not exist is not an error. Lists cannot be addressed:
to change a list, state the complete list in an overlay. Paths contain no templates.

The confmap reads `::` as the path separator, so a key in `config` that contains `::` is refused
(`invalid_config_key`).

## Rendering

Rendering is a function. Its inputs are the snapshot, the fleet, the attribute view of the agent, the inventory
parameters and, for previews, overrides. Its outputs are a canonical YAML document and its SHA-256 hash. It is
deterministic, and `myriarchd` and `myrctl` run the same code, so you can render locally exactly what the server will
offer.

1. **Resolve the chain** from the fleet's blueprint through `extends` up to the root. Missing blueprints and cycles are
   errors. The order of the layers is root to leaf.
2. **Declare parameters and resolve their values**, as described above.
3. **Merge the layers, without evaluating templates yet.** Start with an empty tree. For every blueprint of the chain
   in order, merge its `config`. Then, for every fragment whose `when` is true (evaluated with the resolved
   parameters), merge the fragment's `config` and apply its `remove`. Finally apply the blueprint's own `remove`. The
   layers that were applied are recorded, for example `base`, `base#gateway-auth`, `windows-host`,
   `windows-host#no-eventlog`.
4. **Evaluate templates** over the finished tree, in keys and string values.
5. **Validate the result.** The structural check always runs. The component check runs when the list of components of
   the agent's collector is known. Errors mean there is no result.
6. **Serialize canonically and hash.**

### Merge semantics

The merge is the same as the default merge of the collector's confmap:

- A map merged with a map is merged recursively, key by key.
- Everything else is **replaced completely**: lists, scalars and `null`. A `null` in an overlay sets the value to `null`.
  For a component, `null` means "default configuration". It does not delete the component. To delete, use `remove`.
- As a consequence, to extend a list such as `service.pipelines.logs.receivers` or `service.extensions`, write the
  **complete** list in the overlay.

The upstream supervisor merges the rendered configuration with its own local files. It concatenates
`service::extensions`, so the injected `opamp` extension is kept, and it merges `service::telemetry::resource::attributes`
by name. Blueprints therefore do not mention the `opamp` extension.

### Templates

Template expressions use the delimiters **`[[` and `]]`** and the Go `text/template` engine, with the option
`missingkey=error`. The collector's own syntax, `${env:...}`, `${file:...}`, `${VAR}` and `$$`, is not interpreted and
stays as written.

YAML reads a value that starts with `[[` as a nested list, so such values **must be quoted**. The validator reports
`unquoted_template` when it finds a flow list with exactly one flow list that holds one scalar, which is the typical
symptom.

Each string is evaluated in one of two ways:

- **Whole expression.** If the trimmed string is exactly one `[[ EXPRESSION ]]`, the expression is evaluated
  **typed**: the result keeps its type. Technically `[[ toJson (EXPRESSION) ]]` is evaluated and the JSON is decoded
  again (a number without fraction becomes an integer, any other number a floating point number). So
  `limit_percentage: "[[ .params.memoryLimitPercent ]]"` renders the number `75`, `include: "[[ .params.appLogPaths ]]"`
  renders a list, and `value: "[[ .params.siteId ]]"` renders the string `"edge-001"`.
- **Interpolation.** Otherwise the string is rendered as a template and stays a string, for example
  `endpoint: "https://[[ attr \"site.id\" ]].sites.example.com"`. Scalars are inserted directly. **Lists and maps are
  never converted to text implicitly.** `"http://[[ .params.hosts ]]/x"` is a `template_error`. Convert first with
  `join`, `toJson` or `str`, for example `[[ join "," .params.hosts ]]`.

Map keys are rendered as strings too. Two keys that become equal after rendering are an error. To write a literal `[[`,
use `"[[ \"[[\" ]]"`.

The data available as `.` in a template:

| Expression | Type | Content |
|---|---|---|
| `.params.<name>` | the parameter type | the resolved parameters |
| `.agent.id` | string | the instance UID of the agent (empty for hypothetical agents in previews) |
| `.agent.attributes` | map of strings | the attribute view, accessed with `index .agent.attributes "site.id"` or with `attr` |
| `.fleet.name`, `.fleet.labels` | string, map | the fleet (empty in pure blueprint previews) |
| `.blueprint.name` | string | the name of the rendered leaf blueprint |

The helper functions are listed with their arguments in the
[template functions](../reference/gitops-schema.md#template-functions) table. The argument that is processed comes
**last**, as in Sprig, so pipelines work: `.params.x | default "y"`.

**Limits.** Templates are bounded so that a single value cannot exhaust the server. A value whose text would exceed
64 KiB is rejected before the text is built. The output of one template string, the result of every helper and every
direct output are limited to 64 KiB, and all helpers together produce at most 8 MiB per template string. `list` and
`split` are limited to 10,000 elements. `printf` accepts widths and precisions up to 256, no `*`, no argument indexes
and no more verbs than arguments. `range` runs only over lists and maps, nests at most 3 deep and visits at most 10,000
elements per template string. The compute time of 5 seconds per template string is checked at every executed pipeline (output, assignment, `if`,
`with` and `range`), not only in loops.
`define`, `template` and `block` are not allowed, and templates have no access to the environment, to files or to the
network. The same limits apply to every template string: blueprint values, map keys and the `extraVars` of executors.

Error messages do not echo values from parameters, attributes or templates in full. Type errors name only the type,
values are quoted for at most 40 characters, and the message of `required` for at most 200.

**A tip for large fleets.** Template expressions that use agent-specific values, such as `site.id`, produce one
configuration per agent. That is allowed, because storage is content-addressed and thousands of small configurations
are cheap. Configurations that depend only on parameters are shared by many agents and are easier to review. Values that
the collector knows itself are better set with `resource_detection` or `${env:...}`.

### Validation of the rendered result

The structural check requires that the rendered tree has only the top-level keys `receivers`, `processors`, `exporters`,
`connectors`, `extensions` and `service`, that component ids have the form `type[/name]`, that pipelines are
`traces`, `metrics`, `logs` or `profiles` (optionally with `/name`) and list receivers, processors and exporters, and that
everything a pipeline or `service.extensions` references is defined. A definition that nothing uses is a warning
(`unused_component`), a deprecated component name is a warning (`deprecated_component`), and an unbalanced `${` or an
unknown scheme is a warning (`env_syntax`). If the component list of the collector is known, a component type that the
collector does not contain is `unknown_component`. `myriarchd` checks this when it renders for a real agent that has
reported its components. The check during GitOps sync does not know them, so use `myrctl validate --components` in CI.
The codes are listed in the [issue code table](../reference/gitops-schema.md#validation-and-issue-codes).

### Value types in the result

The result tree contains only maps with string keys, lists, strings, booleans, integers, floating point numbers and
`null`. YAML integers become integers and YAML floating point numbers become floating point numbers.

## Canonical form and hash

- The tree is serialized with `go.yaml.in/yaml/v3`: an encoder with an indent of 2, map keys sorted, no comments, no
  document separator, and the trailing newline the encoder produces.
- The **configuration hash** is the SHA-256 of exactly these bytes, written as hexadecimal text in the API and in the UI.
- A rendered configuration may be at most 1 MiB.
- The golden files of the test suite (`internal/blueprint/testdata/golden/`) pin the exact bytes and the metadata of
  the sample agents, so any change of the rendering is visible in review.

## Identity, storage and delivery of a rendered configuration

- **Identity.** The hash of the canonical YAML is the `config_hash`. It is the same value as the
  `AgentRemoteConfig.config_hash` in OpAMP and the primary key in the configuration store.
- **Storage.** Rendered configurations are stored once (the table `rendered_configs`), content-addressed, with their
  origin (fleet, blueprint and commit). One thousand agents with a handful of distinct configurations occupy a handful of rows. Agents refer to
  hashes. Configurations that no agent refers to any more are deleted after `retention.unreferencedConfig` (default 7
  days).
- **Delivery.** The server sends the OpAMP field `remote_config` with exactly **one** entry in the config map, the key
  `myriarch.yaml` with the content type `text/yaml`, together with the hash. The supervisor merges it as `$REMOTE_CONFIG`
  after the local files and injects the `opamp` extension.
- **Reconciliation.** The agent reports `remote_config_status.last_remote_config_hash` and a status. The server sends
  the configuration again only if the reported hash differs from the desired hash and the desired hash is not the failed
  one, or when an operator asks for a resync. If the hashes are equal, the status is enough.

## When rendering fails

If the rendering fails for an agent after a change, the agent keeps the configuration it already has. The reason is
recorded as the assignment error and shown in the UI, together with the file and the path of the failing value. If a
sample agent of the repository fails to render, the whole revision is rejected during sync. See
[Git as the source of truth](gitops.md#how-a-revision-is-read-and-applied).
