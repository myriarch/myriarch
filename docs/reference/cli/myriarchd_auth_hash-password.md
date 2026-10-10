---
title: "myriarchd auth hash-password"
weight: 30
description: "Print the bcrypt hash of a password read from standard input."
---

## myriarchd auth hash-password

Print the bcrypt hash of a password read from standard input

### Synopsis

Reads a password from standard input (the first line; the line break is removed), and prints its
bcrypt hash and a newline on standard output. The password is never echoed or printed. Use the output
as the file auth.bootstrapAdmin.passwordHashFile.

The password must have 12 to 72 bytes (bcrypt ignores everything after 72 bytes). The cost is 12 by
default; it must be between 10 and 14.

Example:
  printf '%s' "$PASSWORD" | myriarchd auth hash-password > password-hash

Exit codes: 0 hash printed, 1 invalid input, 2 wrong usage.

```text
myriarchd auth hash-password [--cost N] [flags]
```

### Options

```text
      --cost int   bcrypt cost (10 to 14) (default 12)
  -h, --help       help for hash-password
```

### SEE ALSO

- [myriarchd auth](myriarchd_auth.md) - Helpers for the credentials of the bootstrap admin
