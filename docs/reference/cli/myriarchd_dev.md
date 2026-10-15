---
title: "myriarchd dev"
weight: 70
description: "Run myriarchd for local development (embedded PostgreSQL, dev login, quickstart GitOps repo)."
---

## myriarchd dev

Run myriarchd for local development (embedded PostgreSQL, dev login, quickstart GitOps repo)

### Synopsis

Run myriarchd for local development (embedded PostgreSQL, dev login, quickstart GitOps repo).

The embedded PostgreSQL is only part of builds with the devpg build tag; --dsn is required when myriarchd was built without the devpg build tag.

```text
myriarchd dev [flags]
```

### Options

```text
      --allow-nonlocal              allow dev mode with a non-loopback listen address or database, OIDC or TLS (well-known logins and token: use with care)
  -c, --config string               optional myriarchd.yaml applied on top of the dev defaults
      --data-dir string             directory for embedded PostgreSQL data, clones and artifacts (default ".dev")
      --dsn string                  use this PostgreSQL DSN instead of starting embedded PostgreSQL (second dev replica; required when myriarchd was built without the devpg build tag)
      --gitops-dir string           GitOps directory (plain directory or Git work tree) (default "examples/quickstart")
  -h, --help                        help for dev
      --install-bundle-dir string   serve the installer bundles from this directory (myriarch-updater_<version>_<os>_<arch>.tar.gz or .zip, as in the release) instead of the bundles of a release; they are not signed, so only a development server takes them
      --listen string               listen address (dev mode only runs on loopback addresses unless --allow-nonlocal is given) (default "127.0.0.1:8080")
      --releases-fixtures string    read release data from recorded fixtures instead of GitHub
      --replica-id string           replica id (default: hostname + random suffix)
```

### SEE ALSO

- [myriarchd](myriarchd.md) - Myriarch server - OpAMP based management of OpenTelemetry Collectors
