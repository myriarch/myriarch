---
title: "Server configuration"
weight: 50
description: "Every key of the myriarchd configuration file, with its type, default and effect."
---

This page is generated from the mini configuration of the test by `make docs-gen`. Do not edit it by hand: `make docs-check` fails when it differs from the code.

## How the configuration is loaded

`myriarchd serve`, `myriarchd dev`, `myriarchd config validate` and `myriarchd config print` load the configuration the same way (`myriarchd migrate` needs only the database settings, see the last item of this list):

- The configuration file is YAML. An unknown key is an error.
- The loader starts from the defaults in the **Default** column. `myriarchd dev` starts from other defaults, shown as "(dev: ...)" where they differ.
- In string values, `${VAR}` and `${VAR:-default}` are replaced after the file is decoded, with the value of the environment variable of the process. An unset or empty variable gives the default, or the empty string when there is none.
- Then the environment variables `MYRIARCH_<PATH>` override single settings, see [environment variables](environment-variables.md#configuration-overrides). They win over the file.
- `database.dsnFile` and `notifications.channels[].urlFile` are always read at startup, `auth.oidc.clientSecretFile` only while `auth.oidc.enabled` is `true` and `gitops.webhookSecretFile` only while `gitops.enabled` is `true`. The content of the file (without surrounding white space) wins over the inline value next to it. A file that cannot be read or is empty is an error. The other `...File` settings name files (certificates, keys, tokens, CA bundles) that the components read themselves.
- Durations use Go syntax such as `30s`, `5m` or `720h`. A plain integer is rejected, and `d` is not a unit: write `720h` for 30 days.
- [`myriarchd config print`](cli/myriarchd_config_print.md) shows the effective configuration with secrets redacted, [`myriarchd config validate`](cli/myriarchd_config_validate.md) checks a file.
- [`myriarchd dev`](cli/myriarchd_dev.md) sets some keys from its flags: `--listen` sets `server.listenAddr` (and `server.publicUrl`, unless the file or the environment sets it), `--gitops-dir` sets `gitops.repoUrl` to `dir:<directory>`, `--data-dir` sets `dev.dataDir` (and with it `artifacts.dir` to `<data-dir>/artifacts` and `gitops.cloneDir` to `<data-dir>/gitops`), `--releases-fixtures` sets `releases.fixtureDir`, `--replica-id` sets `ha.replicaId` and `--dsn` sets `database.dsn`.
- [`myriarchd migrate`](cli/myriarchd_migrate.md) loads the database settings and nothing else, so that it runs where the server configuration does not exist: the `database` section of the file (an unknown key inside it is an error; every other section is ignored, whatever it holds), `${VAR}` references in its values, the `MYRIARCH_DATABASE_*` variables and `database.dsnFile`. It validates only the database settings.

How to read the tables: **Type** `duration` is a string in Go syntax. **Default** `empty` is the empty string, an empty list or an empty map, `-` marks a group of keys (the defaults are on its keys), `none` means that a list or map has no entries and its fields have no default of their own, and a size in bytes shows its binary unit in parentheses. A key such as `notifications.channels[].name` is a field of every entry of the list, and `<name>` in `executors.secrets.<name>.env` stands for the key of the map entry.

## server

`server` configures the listener of the \<mini\> server (HTTP \| HTTPS), see [docs/operations/authentication.md](../operations/authentication.md) and [docs/reference/gitops-schema.md](gitops-schema.md).

| Key | Type | Default | Description |
|---|---|---|---|
| `server.listenAddr` | string | `":8080"` (dev: empty) | `listenAddr` is the address of the listener, as host:port (default ":8080"). The value \*must\* not use a \[scheme\] and the setting\_name is escaped. |
| `server.tls` | object | - | `tls` configures the certificate. See `server.tls.certFile`. |
| `server.tls.certFile` | string | empty | `certFile` is the certificate file. |
| `server.tls.secrets` | map of objects | none | `secrets` are named secrets with their allowed hosts. Not settable through an environment variable. |
| `server.tls.secrets.<name>.env` | string | none | `env` is the variable that holds the secret. |
| `server.tls.secrets.<name>.allowedHosts` | list of strings | none | `allowedHosts` lists the destinations. |
| `server.timeout` | duration | `1m` | `timeout` limits one request. A second line of the comment joins the first. |
| `server.grace` | duration | `2h` | `grace` is a duration without trailing zero units. |
| `server.maxBodyBytes` | integer | `4194304` (4 MiB) | `maxBodyBytes` limits the body of a request. |
| `server.cacheSize` | integer | `1500` | `cacheSize` is a size that is not a multiple of 1024. |
| `server.workers` | integer | `4` (dev: `1`) | `workers` is the number of workers; the development default differs. |
| `server.ratio` | number | `0.5` | `ratio` is a floating point number. |
| `server.hosts` | list of strings | `["a.example.com", "b.example.com"]` (dev: empty) | `hosts` lists the allowed hosts as `*.example.com` or "a\|b". |
| `server.password` | string | empty | `password` is a secret that the loader keeps out of the output. Secret: `myriarchd config print` shows `<redacted>`. |
| `server.labels` | map of strings | empty | `labels` is a map of strings. Not settable through an environment variable. |
| `server.routes` | map of string lists | empty | `routes` maps names to lists of hosts. Not settable through an environment variable. |
| `server.mode` | string | `"fast"` | `mode` selects the mode. |
| `server.backup` | object | - | `backup` is the backup of the mini server; a field of this type relies on this comment. |
| `server.backup.enabled` | boolean | `false` | `enabled` switches the backup on. |
| `server.quiet` | boolean | `false` (dev: `true`) | `quiet` switches the output off. |
| `server.verbose` | boolean | `false` | `verbose` adds detail to the output (documented on the line of the field). |

## notify

`notify` configures the notifications.

| Key | Type | Default | Description |
|---|---|---|---|
| `notify.channels` | list of objects | none | `channels` lists the channels. Not settable through an environment variable. |
| `notify.channels[].name` | string | none | `name` identifies the channel. |
| `notify.channels[].url` | string | none | `url` is the target (a secret). Secret: `myriarchd config print` shows `<redacted>`. |

## dev

`dev` configures the development mode. Used only by `myriarchd dev`.

| Key | Type | Default | Description |
|---|---|---|---|
| `dev.dataDir` | string | `".dev"` | `dataDir` is the data directory. |
