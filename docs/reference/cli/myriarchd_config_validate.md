---
title: "myriarchd config validate"
weight: 60
description: "Check a configuration exactly like `myriarchd serve` loads it, without starting anything."
---

## myriarchd config validate

Check a configuration exactly like `myriarchd serve` loads it, without starting anything

### Synopsis

Loads the configuration like "myriarchd serve" (defaults, YAML with unknown keys rejected,
${VAR} expansion, MYRIARCH_* overrides) and runs every check that serve makes before it touches the
database or the network. Nothing is started and no connection is made.

Without --skip-files the files that the configuration refers to are read and checked as well
(secrets not empty, CA file with a certificate, key pair matching, trusted root parsable).

Output: "OK: &lt;file> is valid" and one "WARNING: &lt;text>" line per warning on stdout; one
"ERROR: &lt;path>: &lt;message>" line per problem on stderr.
Exit codes: 0 valid (warnings allowed), 1 invalid, 2 wrong usage (no --config, file not readable).

```text
myriarchd config validate --config FILE [--skip-files] [flags]
```

### Options

```text
  -c, --config string   path to myriarchd.yaml (environment MYRIARCH_* overrides apply)
  -h, --help            help for validate
      --skip-files      do not read the files the configuration refers to (secrets, CA files, certificates): their paths count as present
```

### SEE ALSO

- [myriarchd config](myriarchd_config.md) - Check and inspect a myriarchd configuration without starting myriarchd
