---
title: "myriarch-updater container"
weight: 170
description: "Run as PID 1 of a container: supervise the OpAMP supervisor and update the collector binary on a volume."
---

## myriarch-updater container

Run as PID 1 of a container: supervise the OpAMP supervisor and update the collector binary on a volume

### Synopsis

Container mode (Linux, macOS): myriarch-updater starts the OpAMP supervisor as a child process, keeps the collector binaries on a persistent volume (versions/&lt;v> + current symlink), seeds them from the container image and installs offered updates with the same verification, health check and rollback as the service mode.

```text
myriarch-updater container [flags]
```

### Options

```text
  -c, --config string   path to updater.yaml
  -h, --help            help for container
```

### SEE ALSO

- [myriarch-updater](myriarch-updater.md) - Myriarch host agent: installs collector updates offered via OpAMP
