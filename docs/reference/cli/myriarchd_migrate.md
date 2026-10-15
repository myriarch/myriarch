---
title: "myriarchd migrate"
weight: 80
description: "Apply database migrations and exit."
---

## myriarchd migrate

Apply database migrations and exit

### Synopsis

Applies the pending database migrations and exits.

The command needs only the database settings: the section "database" of the configuration file (database.dsn or
database.dsnFile) and the MYRIARCH_DATABASE_* environment variables. Every other section of the file and every other setting
is ignored, so the command runs where the server configuration, the login path and the secrets of other features do not
exist, for example in a job that runs before the server is updated.

Without --check the command applies the migrations that this version has and the database lacks, and prints the schema
version before and after and the compatibility level of the database. Running it again changes nothing. A database whose
schema is newer than this version but compatible with it is not changed (a warning is logged). A database whose
compatibility level is higher than the schema level of this version is refused.

With --check the command changes nothing. It prints the schema version, the compatibility level, the schema level of this
version and a status: current, pending (migrations are missing), newer (compatible) or incompatible.

Results go to stdout, log lines and errors to stderr.
Exit codes: 0 done (with --check: no migration is pending and this version can run on the schema), 1 error (settings,
connection, a failed migration), 3 with --check: migrations are pending, 4 the schema is incompatible with this version.

```text
myriarchd migrate [--config FILE] [--check] [flags]
```

### Options

```text
      --check           change nothing: print the state of the schema and tell it by the exit code (0 ready, 3 migrations pending, 4 incompatible)
  -c, --config string   path to myriarchd.yaml; only its database section is used (environment MYRIARCH_DATABASE_* overrides apply)
  -h, --help            help for migrate
```

### SEE ALSO

- [myriarchd](myriarchd.md) - Myriarch server - OpAMP based management of OpenTelemetry Collectors
