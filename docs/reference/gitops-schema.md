---
title: "GitOps schema reference"
weight: 10
description: "Every field of every GitOps object kind with its type and default, the template functions, selector operators, limits, myrctl exit codes and the table of issue codes."
---

This page lists every field of the five object kinds that `myriarchd` reads from a Git repository, the template
functions, the limits, and the codes under which validation reports problems. For the ideas behind the objects see the
[concepts](../concepts/_index.md). For the layout of a repository and the rules that apply to all objects see
[Git as the source of truth](../concepts/gitops.md).

A test (`TestGitOpsSchemaReference` in `internal/gitops`) keeps this page complete: it fails if a field of the Go schema
or an issue code of the validation packages is missing from the tables below, or if a table names something that no longer
exists.

How to read the tables:

- The first column is the full YAML path, relative to the object. A list is written with `[]`, so
  `spec.waves[].percent` is the field `percent` of every element of `spec.waves`.
- **Type** uses these names: `string`, `int`, `bool`, `duration` (a string in Go syntax such as `30s`, `5m` or `1h30m`),
  `list`, `map` and `object`. A `map` of `any` is free-form: the contents are not checked against a schema.
- **Required** means the object is invalid without the field. "conditional" is explained in the description.
- Unknown fields are errors (`unknown_field`), except inside free-form maps.

## Common fields

Every object has the same envelope, followed by the `spec` of its kind.

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `apiVersion` | string | yes | none | Exactly `myriarch.dev/v1alpha1`. Anything else is `unknown_api_version`. |
| `kind` | string | yes | none | One of `Fleet`, `Blueprint`, `RolloutPolicy`, `Executor`, `Inventory`. Anything else is `unknown_kind`. |
| `metadata.name` | string | yes | none | Unique per kind. A DNS label: lower-case letters, digits and `-`, 1 to 63 characters, starting and ending with a letter or digit (`invalid_name`). Two objects of one kind with the same name are `duplicate_object`. |
| `metadata.description` | string | no | empty | Free text, shown in the UI. |
| `metadata.labels` | map of strings | no | none | Free labels of the object. They are not used for matching. Templates can read the labels of the fleet as `.fleet.labels`. |

## Fleet

Assigns agents to a blueprint, parameters, a target collector version, an update executor and rollout policies.
See [Fleets](../concepts/fleets.md).

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `spec.displayName` | string | no | empty | Name shown in the UI. |
| `spec.priority` | int | no | `0` | The fleet with the highest priority wins when selectors overlap. Equal highest priorities leave the agent unassigned. |
| `spec.selector` | object | no | empty | Chooses the agents of the fleet. An empty selector matches every agent. |
| `spec.selector.matchAttributes` | map of strings | no | none | Exact equality for each key. All entries must hold. |
| `spec.selector.matchExpressions` | list | no | none | Set-based conditions. All must hold. See [Selector operators](#selector-operators). |
| `spec.selector.matchExpressions[].key` | string | yes | none | A key of the attribute view. Server labels are `labels.<key>`. |
| `spec.selector.matchExpressions[].operator` | string | yes | none | `In`, `NotIn`, `Exists`, `DoesNotExist` or `Glob`. |
| `spec.selector.matchExpressions[].values` | list of strings | conditional | none | Required for `In`, `NotIn` and `Glob`. Must be empty for `Exists` and `DoesNotExist`. |
| `spec.blueprint` | string | yes | none | Name of the leaf blueprint to render (`blueprint_not_found` if it does not exist). |
| `spec.parameters` | map of any | no | none | Values for the blueprint parameters. A name the blueprint does not declare is `unknown_parameter`. |
| `spec.collector` | object | no | see fields | The desired collector. |
| `spec.collector.distribution` | string | no | `otelcol-contrib` | The collector distribution. Only `otelcol-contrib` is supported (`invalid_field`). |
| `spec.collector.version` | string | no | empty | The Git target version, a semantic version without a leading `v`, for example `0.162.0`. Empty means versions are not managed for this fleet (unless the UI chooses one, see `spec.update.selection`). |
| `spec.update` | object | no | see fields | How collector versions are changed. |
| `spec.update.executor` | string | no | `none` | `none`, `updater`, `webhook`, `awx` or `manual`. See [Executors](../concepts/executors.md). Any other value is `executor_invalid`. |
| `spec.update.executorRef` | string | conditional | empty | Name of an `Executor` object. Required for `webhook` and `awx` (the type must match). For the other executors it is ignored with a warning. |
| `spec.update.params` | map of any | no | none | Free parameters passed to the executor, for example in the webhook payload. |
| `spec.update.selection` | object | no | absent | The frame within which the UI may choose the target version. Absent means disabled. See [Target version from the UI](../concepts/fleets.md#target-version-from-the-ui-within-the-git-frame). |
| `spec.update.selection.enabled` | bool | no | `false` | Allows UI and API updates. Requires an executor other than `none`. |
| `spec.update.selection.versionRange` | string | conditional | none | A semantic version constraint (Masterminds semver v3) over versions without `v`, for example `">=0.161.0, <=0.162.0"`. With `enabled: true` exactly one of `versionRange` and `versions` is required. |
| `spec.update.selection.versions` | list of strings | conditional | none | An explicit list of selectable versions, strict semantic versions without `v`, each once. |
| `spec.update.selection.allowDowngrade` | bool | no | `false` | `false` allows only versions greater than or equal to the current effective target. |
| `spec.update.selection.requireReason` | bool | no | `false` | A reason is mandatory when choosing and when taking a choice back. |
| `spec.update.selection.rolloutPolicy` | string | no | `spec.rollout.versionPolicy`, else `default` | The policy for rollouts that a UI choice causes. |
| `spec.rollout` | object | no | see fields | The rollout policies of the fleet. |
| `spec.rollout.configPolicy` | string | no | `default` | Name of the `RolloutPolicy` for configuration changes. |
| `spec.rollout.versionPolicy` | string | no | `default` | Name of the `RolloutPolicy` for version changes. |
| `spec.bridge` | object | no | see fields | How the fleet treats OpAMP Bridge agents of the OpenTelemetry Operator that its selector matches. See [Bridge agents](../concepts/fleets.md#bridge-agents). |
| `spec.bridge.manage` | bool | no | `false` | Available from v0.2.0. Opts the fleet in to configure the Bridge agents that its selector matches. Without it a Bridge never receives remote configuration from the fleet, and a selector that matches Bridges is the warning `bridge_selected`. The opt-in takes effect for Bridges in standalone mode only: a Bridge in operator mode stays locked with it. |

A policy name that is not `default` and does not exist is `policy_not_found`. If the policy `default` does not exist, the
built-in policy applies ([Rollouts](../concepts/rollouts.md#which-policy-a-fleet-uses)).

## Blueprint

A layered, parameterized collector configuration. See [Blueprints](../concepts/blueprints.md).

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `spec.extends` | string | no | empty | Name of the parent blueprint. Single inheritance. A cycle is `extends_cycle`, a missing parent `blueprint_not_found`. |
| `spec.parameters` | list | no | none | Typed parameters. A child may declare a parent's parameter again with the same type and replaces the declaration. |
| `spec.parameters[].name` | string | yes | none | `^[a-zA-Z][a-zA-Z0-9_]*$`, unique within the blueprint. Used as `.params.<name>`. |
| `spec.parameters[].type` | string | yes | none | `string`, `int`, `number`, `bool`, `duration`, `list` or `map`. |
| `spec.parameters[].description` | string | no | empty | Free text. |
| `spec.parameters[].default` | any | no | none | The value when no other source provides one. Must match the type. |
| `spec.parameters[].required` | bool | no | `false` | When `true`, rendering fails if no value results (`missing_parameter`). |
| `spec.parameters[].enum` | list | no | none | The allowed values. Each entry must match the type. |
| `spec.parameters[].pattern` | string | no | empty | An RE2 expression the whole value must match. Only for `string`. |
| `spec.parameters[].min` | number | no | none | Lower bound, inclusive. Only for `int` and `number`. Must not be greater than `max`. |
| `spec.parameters[].max` | number | no | none | Upper bound, inclusive. Only for `int` and `number`. |
| `spec.parameters[].fromAttribute` | string | no | empty | Takes the value from this key of the agent's attribute view if it exists, converted to the type. |
| `spec.config` | map of any | no | none | The collector configuration tree. Strings may contain `[[ ... ]]`. Keys must not contain `::`. |
| `spec.fragments` | list | no | none | Conditional overlays, merged in file order after `spec.config`. |
| `spec.fragments[].name` | string | yes | none | `^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`, unique within the blueprint. |
| `spec.fragments[].when` | string | no | empty | Exactly one `"[[ expression ]]"` that evaluates to a bool. Empty means always. |
| `spec.fragments[].config` | map of any | no | none | The overlay that is merged when the fragment is active. |
| `spec.fragments[].remove` | list of strings | no | none | Confmap paths removed after the overlay is merged. |
| `spec.remove` | list of strings | no | none | Confmap paths (`receivers::host_metrics`) removed after the blueprint's `config` and fragments are merged. No templates, no empty segments. |

The order of precedence for parameter values and the merge rules are explained in
[Blueprints](../concepts/blueprints.md#rendering).

## RolloutPolicy

Controls how changes reach the agents of a fleet. See [Rollouts](../concepts/rollouts.md).

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `spec.waves` | list | no | one wave `all` with 100 percent | The waves in order. If the last wave is a selector wave or below 100 percent, a final wave `rest` with 100 percent is appended. |
| `spec.waves[].name` | string | yes | none | Unique within the policy. |
| `spec.waves[].selector` | object | conditional | none | The wave takes all remaining targets that match. Exactly one of `selector` and `percent` is required. |
| `spec.waves[].selector.matchAttributes` | map of strings | no | none | Exact equality for each key, for example `labels.wave: canary`. |
| `spec.waves[].selector.matchExpressions` | list | no | none | Set-based conditions, as in a fleet selector. |
| `spec.waves[].selector.matchExpressions[].key` | string | yes | none | A key of the attribute view. |
| `spec.waves[].selector.matchExpressions[].operator` | string | yes | none | `In`, `NotIn`, `Exists`, `DoesNotExist` or `Glob`. |
| `spec.waves[].selector.matchExpressions[].values` | list of strings | conditional | none | As for a fleet selector. |
| `spec.waves[].percent` | int | conditional | none | 1 to 100. The wave grows the cumulative share of all targets to this percentage. Values must strictly increase from wave to wave. |
| `spec.waves[].requireApproval` | bool | no | `false` | The wave starts only after an operator approves it. |
| `spec.maxConcurrent` | int | no | `0` (unlimited) | Targets in progress at the same time across the rollout. Negative values are `policy_invalid`. The built-in policy uses 50. |
| `spec.healthWait` | duration | no | `60s` | How long a target must stay online and healthy after it reports success. |
| `spec.applyTimeout` | duration | no | `10m` for configuration, `30m` for versions | The longest time from the start of a target until success is reported. |
| `spec.offlineGracePeriod` | duration | no | `15m` | How long a wave waits for offline targets before it skips them. |
| `spec.failureThreshold` | object | no | `maxFailed: 1` | When a wave counts as failed. |
| `spec.failureThreshold.maxFailed` | int | no | `0` | Absolute number of failed targets that triggers `onFailure`. If both values are 0, `maxFailed` is 1. |
| `spec.failureThreshold.maxFailedPercent` | int | no | `0` | Percentage of the wave's targets, 0 to 100. If only this is set, only the percentage rule applies. |
| `spec.onFailure` | string | no | `pause` | `pause` or `rollback`. |
| `spec.maintenanceWindows` | list | no | none | Windows in which targets may start. Empty means any time. |
| `spec.maintenanceWindows[].days` | list of strings | no | every day | Weekdays on which the window begins: `Mon`, `Tue`, `Wed`, `Thu`, `Fri`, `Sat`, `Sun` (case-insensitive). |
| `spec.maintenanceWindows[].start` | string | yes | none | `HH:MM`, 24 hours. Quote it, so YAML does not read a number. |
| `spec.maintenanceWindows[].end` | string | yes | none | `HH:MM`. If `end` is not later than `start`, the window crosses midnight. `start` equal to `end` is 24 hours. |
| `spec.maintenanceWindows[].timezone` | string | no | `agent` | `agent` uses the attribute named by `timezoneAttribute`. Otherwise an IANA zone name such as `UTC` or `Europe/Amsterdam`. |
| `spec.timezoneAttribute` | string | no | `site.timezone` | The agent attribute that holds the IANA zone for windows with the timezone `agent`. |
| `spec.defaultTimezone` | string | no | the server setting `rollouts.defaultTimezone` (`UTC`) | The zone used when the agent has no valid zone attribute. |

Negative durations are `policy_invalid`. The built-in policy used when no `default` object exists is described in
[Rollouts](../concepts/rollouts.md#which-policy-a-fleet-uses).

## Executor

Describes an external update path for the executor types `webhook` and `awx`. The executors `updater` and `manual` need
no object. See [Executors](../concepts/executors.md).

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `spec.type` | string | yes | none | `webhook` or `awx`. The matching block must be present, the other must not. |
| `spec.webhook` | object | conditional | none | Required for the type `webhook`. |
| `spec.webhook.url` | string | yes | none | An absolute `http` or `https` URL without credentials. The host must be in `executors.allowedWebhookHosts` of the server configuration. |
| `spec.webhook.method` | string | no | `POST` | `POST`, `PUT` or `PATCH`. |
| `spec.webhook.headers` | map of strings | no | none | Extra headers. Not allowed: names with the prefix `X-Myriarch-`, and `Authorization`, `Cookie`, `Proxy-Authorization`, `Host`, `Content-Length`. |
| `spec.webhook.secretEnv` | string | conditional | none | Name of the environment variable on the server that holds the HMAC secret. Exactly one of `secretEnv` and `secretFile` is required. |
| `spec.webhook.secretFile` | string | conditional | none | File name below `executors.secretDir` with the HMAC secret. |
| `spec.webhook.timeout` | duration | no | `30s` | At most `5m`. The server may cap it further. |
| `spec.webhook.batchSize` | int | no | `100` | Targets per call. Must not be negative. |
| `spec.awx` | object | conditional | none | Required for the type `awx`. |
| `spec.awx.url` | string | yes | none | The AWX base URL, as for `spec.webhook.url`. |
| `spec.awx.jobTemplateId` | int | yes | none | The ID of the job template, a positive integer. |
| `spec.awx.apiPrefix` | string | no | `/api/v2/` | The path of the controller API. Automation Platform 2.5 and later uses `/api/controller/v2/`. An absolute path of plain segments. |
| `spec.awx.tokenEnv` | string | conditional | none | Name of the environment variable that holds the bearer token. Exactly one of `tokenEnv` and `tokenFile` is required. |
| `spec.awx.tokenFile` | string | conditional | none | File name below `executors.secretDir` with the token. |
| `spec.awx.limitAttribute` | string | no | `host.name` | The agent attribute used to build the AWX `limit`. Values must be plain host names. |
| `spec.awx.extraVars` | map of any | no | none | Variables passed to the job. Strings may use `[[ ... ]]` with the executor context (`.toVersion`, `.distribution`, `.fleet`, `.rolloutId`, `.wave`, `.hostnames`, `.params`). |
| `spec.awx.caFile` | string | no | empty | File name below `executors.secretDir` with an additional CA certificate for AWX. |
| `spec.awx.timeout` | duration | no | `30s` | At most `5m`. |
| `spec.awx.batchSize` | int | no | `100` | Hosts per job launch. Must not be negative. |

Violations of these rules are `missing_field` (a required field or block is absent) or `executor_invalid` (a value is
wrong). Which secret names the server may read is decided by its configuration, see
[Executors](../concepts/executors.md#secrets).

## Inventory

Adds server labels and parameter overrides to agents that match exact attribute values. See
[Inventory](../concepts/inventory.md).

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `spec.entries` | list | no | none | The entries, applied in file order. Later matches override earlier ones, key by key. |
| `spec.entries[].match` | map of strings | yes | none | Attribute keys and values. All must be equal. Must not be empty, and keys must not start with `labels.`. |
| `spec.entries[].labels` | map of strings | no | none | Server labels to attach, addressed in selectors as `labels.<key>`. Keys must be non-empty and contain no space. |
| `spec.entries[].parameters` | map of any | no | none | Parameter overrides for the blueprint. Undeclared names are ignored with a warning. |

## Sample agents

Files in `samples/agents/*.yaml` (or `.yml`) below the repository path describe example agents. They are **not**
objects of `myriarchd`. `myrctl validate` and the server's validation render every fleet for the sample agents that
match it, and `myrctl render --sample NAME` renders for one of them. A file may contain several documents.

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `name` | string | yes | none | Unique among the samples (`duplicate_object`). |
| `attributes` | map of strings | no | none | The attributes of the agent, as it would report them. Values are kept as written. |
| `labels` | map of strings | no | none | Server labels, as they would come from an enrollment token. Inventory labels override them on equal keys. |

```yaml
name: linux-edge-01
attributes:
  service.name: otelcol-contrib
  os.type: linux
  host.name: linux-edge-01.example.com
  site.type: edge
  site.id: edge-001
  site.timezone: Europe/Amsterdam
labels: {}
```

Unknown fields in a sample are `unknown_field`, a value that is not a scalar is `invalid_field`. A sample that matches no
fleet is the warning `sample_unassigned`. A sample that matches several fleets of equal highest priority is the warning
`fleet_ambiguous`.

From v0.2.0 on, a sample that is an OpAMP Bridge, because it has the `service.name` `io.opentelemetry.operator-opamp-bridge` or the
attribute `opentelemetry.io/opamp.bridge.mode`, is never rendered and does not have to match a fleet. For every matching
fleet without `spec.bridge.manage` it is the warning `bridge_selected`, with the name of the sample. The quickstart
examples contain no Bridge sample.

## Selector operators

Used by `spec.selector.matchExpressions` of a fleet and by wave selectors of a policy.

| Operator | Meaning | `values` |
|---|---|---|
| `In` | The attribute exists and its value is one of `values`. | required |
| `NotIn` | The attribute is missing, or its value is not one of `values`. | required |
| `Exists` | The attribute exists. | must be empty |
| `DoesNotExist` | The attribute is missing. | must be empty |
| `Glob` | The attribute exists and matches at least one pattern. The syntax is the one of Go's `path.Match`, for example `edge-0*`. | required, valid patterns |

All conditions of a selector, `matchAttributes` and `matchExpressions` together, must hold. An unknown operator, a missing
key or missing values are `selector_invalid`.

## Template functions

Templates use the delimiters `[[` and `]]`. In addition to the functions below, the Go builtins `and`, `or`, `not`, `eq`,
`ne`, `lt`, `le`, `gt`, `ge`, `len` and `index` are available. The argument that is processed comes **last**, so pipes
work: `.params.x | default "y"`. The rules for evaluation and the limits are in
[Blueprints](../concepts/blueprints.md#templates).

| Function | Result | Description |
|---|---|---|
| `attr KEY` | string | The attribute value. An error if the agent has no such attribute. |
| `attrOr KEY DEFAULT` | string | The attribute value, or `DEFAULT` if it does not exist. |
| `hasAttr KEY` | bool | Whether the attribute exists. |
| `param NAME` | any | The value of the parameter. An error if it has no value. |
| `default DEFAULT VALUE` | any | `VALUE`, unless it is empty (null, empty string, list or map, `false` or `0`), then `DEFAULT`. |
| `required MESSAGE VALUE` | any | `VALUE`, or an error with `MESSAGE` (shown for at most 200 characters) if it is empty. |
| `lower S`, `upper S`, `trim S` | string | Lower case, upper case, and trimmed white space. |
| `trimPrefix P S`, `trimSuffix X S` | string | Removes a prefix or a suffix. |
| `replace OLD NEW S` | string | Replaces every occurrence. `OLD` must not be empty. |
| `contains SUB S`, `hasPrefix P S`, `hasSuffix X S` | bool | String tests. Lists and maps are compared as compact JSON. |
| `join SEP LIST` | string | Joins the elements of a list. |
| `split SEP S` | list | Splits a string. At most 10,000 parts. |
| `list A B ...` | list | A list literal. At most 10,000 elements. |
| `dict K1 V1 K2 V2 ...` | map | A map literal. The keys must be strings. |
| `int V`, `float V`, `bool V` | int, number, bool | Conversions. `int` accepts an integer, a number (the fraction is cut off), a numeric string or a bool. |
| `str V` | string | Converts to a string. Lists and maps become compact JSON. |
| `add A B`, `sub A B`, `mul A B`, `div A B` | int | Integer arithmetic. `div` by zero is an error. |
| `toJson V` | string | The JSON encoding of a value. |
| `printf`, `print`, `println`, `html`, `js`, `urlquery` | string | The Go functions of the same name, with bounded output. `printf` accepts widths and precisions up to 256, no `*` and no argument indexes. |

The output of a function and of a template string is limited to 64 KiB. Further limits (range, compute time and the total
amount of text a template may produce) are described in [Blueprints](../concepts/blueprints.md#templates).

## Limits

| Limit | Value | Code when exceeded |
|---|---|---|
| Size of one YAML file | 4 MiB | `file_too_large` |
| Number of YAML files | `gitops.maxFiles`, default 2000 | `too_many_files` |
| Total size of all YAML files | `gitops.maxYamlBytes`, default 64 MiB | `repository_too_large` |
| Size of the checked-out repository | `gitops.maxRepoBytes`, default 256 MiB | the fetch fails (`sync_failed`) |
| YAML nodes visited by the strict checks of one object | 500,000 | `repository_too_complex` |
| Time to parse and validate one revision on the server | 2 minutes | `time_budget_exceeded` |
| Symbolic links and other non-regular files | not allowed | `not_regular_file` |
| Size of a rendered configuration | 1 MiB | `template_error` for the sample that renders it |

`myrctl` applies the same file, count and node limits. It has no time budget.

## Validation and issue codes

`myrctl validate [DIR]` (the default directory is `.`) validates the repository exactly as `myriarchd` does before it
applies a revision, and renders every fleet for the matching sample agents. `myrctl render` prints the rendered
configuration of one fleet for one agent. The flags are documented with the commands:
[`myrctl validate`](cli/myrctl_validate.md) and [`myrctl render`](cli/myrctl_render.md).

**Exit codes** of both commands:

| Exit code | Meaning |
|---|---|
| 0 | The repository is valid. Warnings are allowed (with `validate --strict` they are not). |
| 1 | Validation errors, or render errors. |
| 2 | Usage or internal error, for example an unknown flag, a directory that does not exist, or an unknown `--output`. |

**Text output.** Each finding is one line: `file:line: severity code: message (Object/name, path)`. A summary line
follows, for example `OK: 1 Blueprint, 3 Fleet, ...; 4 samples; 0 errors, 0 warnings`, or `FAILED: ...`.

**JSON output.** `myrctl validate --output json` prints one object with the keys

- `valid`: `true` when the exit code is 0,
- `errors` and `warnings`: the number of findings of each severity,
- `objects`: the number of objects per kind, for example `{"Fleet": 3}`,
- `samples`: the number of sample agents,
- `issues`: the list of findings (never `null`).

Each finding has `severity`, `code`, `message` and, where known, `file`, `line`, `path` and `object`. This output fits
annotations in CI systems. `myrctl render --output json` prints `fleet`, `blueprint`, `layers`, `parameters`,
`parameterSources` (per parameter one of `default`, `fleet`, `attribute`, `inventory` or `override`), `hash`, `config`
and `issues`.

`myrctl validate --components FILE` takes a component list (the format of
[`examples/sim/components-otelcol-contrib-0.162.0.json`](../../examples/sim/)) and enables the check `unknown_component`
for all sample renderings. Without it, component names are not checked against a collector.

**Issue codes.** The codes are stable. The server shows the same codes in the UI and the API.

| Code | Severity | Meaning |
|---|---|---|
| `blueprint_not_found` | error | A fleet names a blueprint that does not exist, or an `extends` parent is missing. |
| `bridge_selected` | warning | Available from v0.2.0. The selector of a fleet without `spec.bridge.manage` also matches an OpAMP Bridge agent: a synthetic Bridge in operator mode or in standalone mode (what the Bridge reports itself, with the inventory applied), or a sample agent that is a Bridge. The fleet would never configure it. Selectors on `labels.*` of an enrollment token cannot be evaluated here. With `validate --strict` the warning fails the check. |
| `config_invalid_toplevel` | error | The rendered configuration is not a mapping, has a top-level key other than `receivers`, `processors`, `exporters`, `connectors`, `extensions` and `service`, or a section is not a mapping. |
| `deprecated_component` | warning | A component uses an old type name (an alias), for example `hostmetrics`. |
| `duplicate_object` | error | Two objects of one kind have the same name, two fragments of one blueprint, or two sample agents. |
| `env_syntax` | warning | An unbalanced `${` or an unknown scheme in a `${...}` expression of the rendered configuration. |
| `executor_invalid` | error | An executor is wrong: URL, type, secret reference, header, method, timeout, block mismatch, or `update.executor` is not one of `none`, `updater`, `webhook`, `awx`, `manual`. As a warning: an `executorRef` that is ignored for `updater` and `manual`. |
| `executor_not_found` | error | `update.executorRef` names an executor that does not exist. |
| `extends_cycle` | error | The `extends` chain of blueprints is a cycle. |
| `file_too_large` | error | A YAML file is larger than 4 MiB. |
| `fleet_ambiguous` | warning | A sample agent matches several fleets with the same highest priority. |
| `invalid_component_id` | error | A component id is not `type[/name]`, or its configuration is not a mapping or null. |
| `invalid_config_key` | error | A key in `spec.config` contains `::`, the path separator of the collector's confmap. |
| `invalid_field` | error | A field has the wrong type or cannot be decoded, or a value is not allowed (distribution, collector version, remove path, label key, sample attribute). |
| `invalid_name` | error | An object name or a fragment name does not match its pattern. |
| `invalid_parameter` | error | A parameter declaration is wrong: name, unknown type, default, pattern, min or max, enum entry, or a duplicate. |
| `invalid_parameter_value` | error | A parameter value does not match its type or violates `enum`, `pattern`, `min` or `max`. |
| `invalid_pipeline` | error | `service.pipelines` is missing or wrong: no pipeline, an invalid id, an unknown key, a missing or duplicated component list. |
| `missing_field` | error | A required field or block is missing, or a document is not a mapping. As a warning: `update.executor` is set but `collector.version` is empty. |
| `missing_parameter` | error | A `required` parameter has no value. |
| `not_regular_file` | error | The repository contains a symbolic link or another non-regular file. |
| `parameter_type_conflict` | error | A child blueprint declares a parameter again with a different type. |
| `policy_invalid` | error | A rollout policy is wrong: a negative number or duration, a threshold out of range, an unknown `onFailure`, or an invalid `defaultTimezone`. |
| `policy_not_found` | error | A fleet or a selection frame names a rollout policy that does not exist. |
| `repository_too_complex` | error | An object has more YAML nodes than the strict checks accept. |
| `repository_too_large` | error | The YAML files exceed the total size limit. |
| `sample_unassigned` | warning | A sample agent matches no fleet. |
| `selection_invalid` | error | `spec.update.selection` is wrong: no or both of `versionRange` and `versions`, an invalid constraint or version, executor `none`, or `collector.version` outside the frame. As a warning: a disabled block that still has a frame. |
| `selector_invalid` | error | A selector has an empty key, an unknown operator, missing or unexpected values, or an invalid glob pattern. |
| `sync_failed` | error | The server could not fetch or apply the repository. Recorded by the syncer, not by `myrctl`. |
| `template_error` | error | Evaluating a template failed, or rendering a sample agent failed. |
| `template_syntax` | error | A template does not parse, a `when` is not exactly one expression, or a construct is not allowed. |
| `time_budget_exceeded` | error | Parsing or validating one revision took longer than the time budget. |
| `too_many_files` | error | The repository has more YAML files than `gitops.maxFiles`. |
| `undefined_component` | error | A pipeline or `service.extensions` refers to a component that is not defined. |
| `unknown_api_version` | error | `apiVersion` is missing or not `myriarch.dev/v1alpha1`. |
| `unknown_component` | error | A component type is not in the component list of the collector (after resolving aliases). |
| `unknown_field` | error | A field is not part of the schema. |
| `unknown_kind` | error | `kind` is missing or not one of the five kinds. |
| `unknown_parameter` | error | A fleet or a preview override sets a parameter that the blueprint does not declare. As a warning for inventory parameters, which are ignored. |
| `unquoted_template` | error | A value `[[ ... ]]` is not quoted, so YAML read a nested list. |
| `unused_component` | warning | A component is defined but used in no pipeline and not in `service.extensions`. |
| `wave_invalid` | error | A wave is wrong: no name, a duplicate name, both or neither of `selector` and `percent`, a percent out of range or not increasing. |
| `when_not_bool` | error | The `when` of a fragment does not evaluate to a boolean. |
| `window_invalid` | error | A maintenance window has an invalid time, weekday or time zone. |
| `yaml_syntax` | error | A YAML file cannot be read or parsed, or a rendered configuration is not valid YAML. |
