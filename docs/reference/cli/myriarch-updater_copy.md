---
title: "myriarch-updater copy"
weight: 180
description: "Copy a regular file atomically (for init containers without a shell)."
---

## myriarch-updater copy

Copy a regular file atomically (for init containers without a shell)

### Synopsis

Copies the regular file &lt;src> to &lt;dst>. Both paths must be absolute. &lt;dst> may be an existing directory, then the file keeps its name. The write is atomic (a temporary file next to &lt;dst>, then a rename) and the execute bit of &lt;src> is kept (mode 0755 with it, 0644 without). A directory, a device or a symlink as destination and a source that is not a regular file are refused; &lt;src> itself may be a symlink to a regular file. Missing directories are not created. It makes no network access.

```text
myriarch-updater copy <src> <dst> [flags]
```

### Options

```text
  -h, --help   help for copy
```

### SEE ALSO

- [myriarch-updater](myriarch-updater.md) - Myriarch host agent: installs collector updates offered via OpAMP
