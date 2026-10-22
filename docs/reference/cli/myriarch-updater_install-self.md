---
title: "myriarch-updater install-self"
weight: 210
description: "Copy this binary to <dir>/myriarch-updater (for init containers)."
---

## myriarch-updater install-self

Copy this binary to &lt;dir>/myriarch-updater (for init containers)

### Synopsis

Copies the running binary to &lt;dir>/myriarch-updater with mode 0755. The write is atomic (a temporary file in &lt;dir>, then a rename), &lt;dir> must be an existing absolute directory and an existing target must be a regular file. An init container uses it to put this static binary on a shared volume where the image of the collector has no shell. It makes no network access.

```text
myriarch-updater install-self <dir> [flags]
```

### Options

```text
  -h, --help   help for install-self
```

### SEE ALSO

- [myriarch-updater](myriarch-updater.md) - Myriarch host agent: installs collector updates offered via OpAMP
