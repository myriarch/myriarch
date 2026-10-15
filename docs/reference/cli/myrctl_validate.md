---
title: "myrctl validate"
weight: 140
description: "Validate all objects of a GitOps repository (default dir: .)."
---

## myrctl validate

Validate all objects of a GitOps repository (default dir: .)

### Synopsis

Validates the repository exactly like myriarchd does before it applies a revision and
renders every fleet for the matching sample agents (samples/agents/*.yaml).

Exit codes: 0 valid (warnings allowed), 1 validation errors, 2 usage or internal error.

```text
myrctl validate [dir] [flags]
```

### Options

```text
      --components string   component list (JSON) enabling the unknown_component check for all sample renderings
  -h, --help                help for validate
  -o, --output string       output format: text or json (default "text")
      --strict              treat warnings as errors (exit 1)
```

### SEE ALSO

- [myrctl](myrctl.md) - Validate and render myriarch GitOps repositories
