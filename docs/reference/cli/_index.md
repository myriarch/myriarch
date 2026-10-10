---
title: "CLI reference"
weight: 20
description: "Reference of every command and option of the myriarch binaries, generated from the command line definitions."
---

This directory is generated from the command line definitions of the binaries by `make docs-gen`. Do not edit the files by hand: `make docs-check` fails when they differ from the code.

| Command | Description |
|---|---|
| [myriarchd](myriarchd.md) | Myriarch server - OpAMP based management of OpenTelemetry Collectors |
| [myrctl](myrctl.md) | Validate and render myriarch GitOps repositories |
| [myriarch-updater](myriarch-updater.md) | Myriarch host agent: installs collector updates offered via OpAMP |
| [myriarch-sim](myriarch-sim.md) | Simulate OpAMP supervisors and myriarch-updaters against myriarchd |

The short name `myr` is an optional alias of `myrctl`. It is not installed and not shipped as a second binary: create it yourself, for example with `alias myr=myrctl` in a POSIX shell or `Set-Alias myr myrctl` in PowerShell. Everything documented for `myrctl` applies to `myr`.
