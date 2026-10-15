---
title: "Reference"
weight: 40
description: "Exact descriptions of the GitOps objects, the command line tools and the REST API."
---

The reference pages list facts: fields, defaults, flags, codes and limits. They describe the system and do not explain or
instruct. For explanations see the [concepts](../concepts/_index.md), for instructions the
[how-to guides](../tasks/_index.md).

| Page | Content |
|---|---|
| [GitOps schema](gitops-schema.md) | Every field of `Fleet`, `Blueprint`, `RolloutPolicy`, `Executor` and `Inventory`, the sample agent format, the template functions, selector operators, limits, the `myrctl` exit codes and JSON output, and the table of issue codes. |
| [Command line reference](cli/_index.md) | One page per command of `myriarchd`, `myrctl`, `myriarch-updater` and `myriarch-sim`, generated from the commands themselves. |
| [REST API](api/_index.md) | The management API under `/api/v1`, generated from the OpenAPI 3.1 contract `api/openapi.yaml`: authentication, roles, error format, one page per tag with every operation and its permission, and the data types. |
| [Helm chart and values](../../deploy/helm/myriarch/README.md) | Installation, profiles, network policy, storage and the configuration values of the chart `myriarch`. |
| [Server configuration](configuration.md) | Every key of the `myriarchd` configuration file with its type, default and effect, generated from the configuration struct and its doc comments. |
| [Environment variables](environment-variables.md) | The `MYRIARCH_*` overrides of the configuration keys, the other variables the programs read and the variables of the deployment templates. |
| [Metrics](metrics.md) | The Prometheus metrics of `myriarchd` and `myriarch-sim` with type, labels and help text, and how to reach the endpoint. |
| [Design contracts](../design/README.md) | The normative descriptions of version selection, the updater protocol, container mode and the OpAMP server. |

## Generated and checked references

The references are not written twice. Each one is generated from, or tested against, the source it describes, so that it cannot
drift:

- The command-line reference is generated from the command definitions with `make docs-gen`. A test and the `docs` gate fail
  when the committed pages differ from the generated output.
- The GitOps schema is maintained by hand, and a test fails when a kind, field or option of the format is missing from it.
- The REST API reference is generated from `api/openapi.yaml` with `make docs-gen`. A test and the `docs` gate fail when the
  committed pages differ from the generated output. The types of the web UI are generated from the same file and checked for
  drift.
- The server configuration reference is generated with `make docs-gen` from the configuration struct and its doc comments, the
  defaults and the loader. A test and the `docs` gate fail when the committed page differs from the generated output.
- The environment variable reference is generated with `make docs-gen` from the environment overrides of the loader, the
  documented environment reads of the programs and the deployment templates. A test and the `docs` gate fail on drift, and a
  test fails when the code reads an environment variable that the page does not list.
- The metrics reference is generated with `make docs-gen` from the metric definitions in the sources. A test and the `docs` gate
  fail on drift, and a server test compares the metrics that the running server exposes with the reference.
- The values of the Helm chart are checked against a strict schema, `deploy/helm/myriarch/values.schema.json`, which `make docs-gen`
  assembles from a hand-written source and the configuration struct. A test and the `docs` gate fail on drift, and the tests of the
  chart fail when `values.yaml` or a template uses a value that the schema lacks.
- The Helm chart page is the README of the chart. Generated reference tables for its values are planned.

`myriarchd config print` shows the effective configuration of a file with all defaults, and `myriarchd config validate` checks
it, see [`myriarchd config validate`](cli/myriarchd_config_validate.md).
