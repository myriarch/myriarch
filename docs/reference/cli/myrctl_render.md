---
title: "myrctl render"
weight: 120
description: "Render the collector config of a fleet for a (sample) agent."
---

## myrctl render

Render the collector config of a fleet for a (sample) agent

### Synopsis

Renders the collector configuration for a sample agent (--sample) or for attributes given
on the command line (--attr key=value, repeatable). Without --fleet the fleet is chosen like
myriarchd does (selectors and priority). Inventory entries of the repository are applied.

The configuration is printed to stdout (--output yaml) or as a JSON document with parameters,
hash and issues (--output json). Exit codes: 0 ok, 1 render errors, 2 usage or internal error.

```text
myrctl render [flags]
```

### Options

```text
      --attr stringArray    agent attribute key=value (repeatable; alternative to --sample)
      --components string   component list (JSON) enabling the unknown_component check
      --dir string          GitOps repository directory (default ".")
      --fleet string        fleet name (default: the fleet the agent is assigned to)
  -h, --help                help for render
      --label stringArray   server label key=value (repeatable)
  -o, --output string       output format: yaml or json (default "yaml")
      --param stringArray   parameter override key=value (repeatable, YAML scalar)
      --sample string       sample agent name (samples/agents/*.yaml)
```

### SEE ALSO

- [myrctl](myrctl.md) - Validate and render myriarch GitOps repositories
