---
title: "Run two replicas locally"
weight: 80
description: "Start two myriarchd replicas on one database, see the leader election and test a failover on your machine."
---

`myriarchd` keeps all state in PostgreSQL, so several replicas can run next to each other. Every replica serves agents and
the REST API; exactly one replica is the **leader** and runs the singleton jobs: the rollout engine, the GitOps syncer, the
release tracker and housekeeping. Leadership is an advisory lock in PostgreSQL (`ha.leaderLockKey`), and changes reach the
agents of every replica through PostgreSQL `LISTEN`/`NOTIFY`. This page reproduces that setup in dev mode. The design is
described in the [architecture](../architecture.md).

## Start replica A

Use a working copy of the example repository as in [Roll out a configuration change](roll-out-a-config-change.md) and give
the first replica a name, so that you can find it later:

```bash
bin/myriarchd dev --gitops-dir .dev/gitops-work --data-dir .dev --replica-id replica-a
```

It starts the embedded PostgreSQL on port 54329. The dev database has fixed, well-known credentials, so its connection
string is:

```text
postgres://myriarch:myriarch@127.0.0.1:54329/myriarch?sslmode=disable
```

The server also logs it at start in a line that begins with `DEV ONLY: embedded postgres ready`. If you changed
`dev.postgresPort`, use that port.

## Start replica B

In a second terminal, start another dev instance that uses the same database instead of starting its own, on another port
and with its own data directory (each replica has its own artifact cache and Git clone):

```bash
DSN='postgres://myriarch:myriarch@127.0.0.1:54329/myriarch?sslmode=disable'
bin/myriarchd dev --dsn "$DSN" --listen 127.0.0.1:8081 --replica-id replica-b \
  --gitops-dir .dev/gitops-work --data-dir .dev/b
```

Connect simulated agents to replica B and sign in to replica A as before (the `api` function from
[Local development](../getting-started/local-dev.md)):

```bash
MYRIARCH_ENROLLMENT_TOKEN=myre_devtoken_insecure_do_not_use_in_prod \
  bin/myriarch-sim run --server ws://localhost:8081/v1/opamp --profile examples/sim/small.yaml --agents 200 --seed 99
api localhost:8080/api/v1/system/info | jq '{leaderId, replicas: [.replicas[] | {id, isLeader, connections}]}'
```

The answer lists both replicas, marks exactly one as leader and shows the number of agent connections of each. Change a
blueprint in the working copy and sync: both replicas deliver the new configuration to their agents, whichever replica is
the leader.

## Test a failover

Kill replica A hard, without a clean shutdown:

```bash
pkill -9 -f 'replica-id replica-a'
```

The embedded PostgreSQL keeps running after a `kill -9` (on a normal exit replica A would stop it). The database
connection of replica A closes, PostgreSQL releases the advisory lock, and replica B becomes the leader after a few
seconds. Check with `system/info` against port 8081. Running rollouts continue under the new leader, because the engine
keeps no state between ticks and every step is repeatable.

Then start replica A again as a client of the same database, like replica B:

```bash
bin/myriarchd dev --dsn "$DSN" --listen 127.0.0.1:8080 --replica-id replica-a --gitops-dir .dev/gitops-work --data-dir .dev
```

It joins as a follower, agents do not appear twice, and the leader stays where it is.

A dead leader keeps its last heartbeat row in the replica list for a short time. `system/info` names only the replica with the
newest heartbeat as leader. A replica counts as dead after 30 seconds without a heartbeat (`ha.heartbeatInterval`, at most
10 seconds).

## Stop everything

Stop the processes with `Ctrl+C`. If replica A was started with `--dsn`, it did not start the database, so stop that one
yourself:

```bash
kill $(head -1 .dev/postgres/data/postmaster.pid)
```

For a production deployment with several replicas, an external PostgreSQL and the Helm chart, read the
[production checklist](../operations/production-checklist.md).
