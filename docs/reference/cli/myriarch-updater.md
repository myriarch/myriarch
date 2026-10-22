---
title: "myriarch-updater"
weight: 160
description: "Myriarch host agent: installs collector updates offered via OpAMP."
---

## myriarch-updater

Myriarch host agent: installs collector updates offered via OpAMP

### Options

```text
  -h, --help   help for myriarch-updater
```

### SEE ALSO

- [myriarch-updater container](myriarch-updater_container.md) - Run as PID 1 of a container: supervise the OpAMP supervisor and update the collector binary on a volume
- [myriarch-updater copy](myriarch-updater_copy.md) - Copy a regular file atomically (for init containers without a shell)
- [myriarch-updater fetch-trusted-root](myriarch-updater_fetch-trusted-root.md) - Fetch the Sigstore trusted root via TUF and write it as trusted_root.json (diagnostics, mirror image build)
- [myriarch-updater install](myriarch-updater_install.md) - Install myriarch-updater as OS service (systemd unit / Windows service)
- [myriarch-updater install-self](myriarch-updater_install-self.md) - Copy this binary into a directory as myriarch-updater (for init containers)
- [myriarch-updater licenses](myriarch-updater_licenses.md) - Print the license of myriarch, or with --third-party the notices of the included software
- [myriarch-updater run](myriarch-updater_run.md) - Run the updater (foreground or as OS service)
- [myriarch-updater start](myriarch-updater_start.md) - Start the service
- [myriarch-updater status](myriarch-updater_status.md) - Show the service state (and, with --config, the installed collector version)
- [myriarch-updater stop](myriarch-updater_stop.md) - Stop the service
- [myriarch-updater uninstall](myriarch-updater_uninstall.md) - Stop and remove the myriarch-updater service
- [myriarch-updater verify-bundle](myriarch-updater_verify-bundle.md) - Verify an artifact against a Sigstore bundle offline (diagnostics)
- [myriarch-updater version](myriarch-updater_version.md) - Print the version
