---
title: "myrctl"
weight: 120
description: "Validate and render myriarch GitOps repositories."
---

## myrctl

Validate and render myriarch GitOps repositories

### Synopsis

myrctl validates and renders the GitOps repository of a myriarch installation, for example in CI
pipelines.

The short name myr is an alias that is not installed by default: alias myr=myrctl (POSIX shells)
or Set-Alias myr myrctl (PowerShell).

### Options

```text
  -h, --help   help for myrctl
```

### SEE ALSO

- [myrctl render](myrctl_render.md) - Render the collector config of a fleet for a (sample) agent
- [myrctl validate](myrctl_validate.md) - Validate all objects of a GitOps repository (default dir: .)
- [myrctl version](myrctl_version.md) - Print the version
