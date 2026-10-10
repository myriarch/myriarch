---
title: "myriarch-sim run"
weight: 280
description: "Run the simulation."
---

## myriarch-sim run

Run the simulation

```text
myriarch-sim run [flags]
```

### Options

```text
      --agents int              number of simulated collectors (default 1000)
      --ca-file string          CA bundle for wss:// endpoints
      --control-listen string   serve the local control API, e.g. 127.0.0.1:9465
      --duration duration       stop after this duration (0 = run until interrupted)
      --fake-downloads          package downloads return a dummy body (no real download, no hash check) (default true)
  -h, --help                    help for run
      --insecure-skip-verify    skip TLS verification (dev only)
      --log-level string        log level: debug|info|warn|error (default "info")
      --metrics-listen string   serve Prometheus metrics, e.g. :9464
      --profile string          simulation profile (examples/sim/mixed-fleet.yaml)
      --seed int                override the profile seed
      --server string           myriarchd OpAMP endpoint (default "ws://localhost:8080/v1/opamp")
      --token string            enrollment token (myre_...); prefer the environment variable MYRIARCH_ENROLLMENT_TOKEN: arguments are visible in process listings
      --updaters                also simulate myriarch-updaters for groups with updater: true (default true)
```

### SEE ALSO

- [myriarch-sim](myriarch-sim.md) - Simulate OpAMP supervisors and myriarch-updaters against myriarchd
