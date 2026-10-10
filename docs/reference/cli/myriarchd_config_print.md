---
title: "myriarchd config print"
weight: 50
description: "Print the effective configuration (defaults, file, environment) as YAML, secrets redacted."
---

## myriarchd config print

Print the effective configuration (defaults, file, environment) as YAML, secrets redacted

### Synopsis

Prints the configuration that "myriarchd serve" would run with: the defaults, the file and the
MYRIARCH_* environment overrides applied. Secrets (database.dsn, auth.oidc.clientSecret,
gitops.webhookSecret, the URLs of notification channels, dev passwords) are replaced by
"&lt;redacted>". The configuration is validated first; exit codes as for "config validate".

```text
myriarchd config print --config FILE [--skip-files] [flags]
```

### Options

```text
  -c, --config string   path to myriarchd.yaml (environment MYRIARCH_* overrides apply)
  -h, --help            help for print
      --skip-files      do not read the files the configuration refers to (secrets, CA files, certificates): their paths count as present
```

### SEE ALSO

- [myriarchd config](myriarchd_config.md) - Check and inspect a myriarchd configuration without starting myriarchd
