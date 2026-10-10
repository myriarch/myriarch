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
- The Helm chart page is the README of the chart. Generated reference tables for its values are planned.

A reference for the server configuration (every key of the `myriarchd` configuration file), the environment variables and the
metrics of the server are being written for the first release. Until then, `myriarchd config print` shows the effective
configuration with all defaults, and `myriarchd config validate` checks a file, see
[`myriarchd config validate`](cli/myriarchd_config_validate.md).
