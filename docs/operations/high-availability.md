---
title: "High availability"
weight: 30
description: "How long a failover of the leader takes after a hard failure, the setting that bounds it, and how to size shutdownTimeout and terminationGracePeriodSeconds."
---

Run two or more replicas of `myriarchd` against one PostgreSQL database. Every replica serves agents, the REST API and the
UI. One replica, the **leader**, also runs the singleton jobs: GitOps polling, the rollout planner and engine, the release
tracker, artifact prefetch, the offline sweeper and housekeeping. The leader is the replica that holds a session advisory
lock in PostgreSQL ([design](../design/opamp-server.md#leader-election)). Without a leader there are no rollouts; agents keep
running with their last configuration.

## How long a failover takes

| Event | Leader role passes to another replica |
|---|---|
| Planned stop (rollout of the Deployment, `kubectl delete pod`, SIGTERM) | In seconds. The leader unlocks and closes its session; a follower asks for the lock every 5 seconds (with 20 percent jitter), so the wait is at most 6 seconds. |
| The leader's session ends cleanly (process crash, container killed) | The same: the operating system closes the connection and PostgreSQL releases the lock at once. |
| **Hard failure** of the leader's node, or a network failure that drops packets without closing the connection | At most `ha.leaderSessionTimeout` plus the 6 seconds of the retry, 36 seconds with the default of 30 seconds. |

A hard failure is the case that needs a bound. PostgreSQL releases a session lock only when it notices that the session is
gone, and for a client that went silent without a FIN or RST the operating system's TCP defaults need hours. `myriarchd`
therefore opens the lock session and the session of the event bus listener with TCP settings derived from
`ha.leaderSessionTimeout` (T) in the startup packet:

| Server parameter | Value |
|---|---|
| `tcp_keepalives_idle` | T / 3 seconds |
| `tcp_keepalives_interval` | T / 6 seconds |
| `tcp_keepalives_count` | 3 |
| `tcp_user_timeout` | T in milliseconds |

PostgreSQL probes an idle peer after T/3 and then three times at T/6, so it drops a silent peer after 5T/6 at the latest, and
the lock is free. With the default of 30 seconds that is 10 seconds of idle time, probes every 5 seconds and 25 seconds in
total. Only these two sessions get the settings; the pool of ordinary connections keeps the defaults of your server.

The leader does not wait for the server. It confirms its lock session with a short query every T/6 (timeout T/3) and
**gives up leadership itself, at the latest T/2 after the last answer** (15 seconds with the default): it cancels the
context of the leader jobs and reports `myriarch_leader 0`. This happens before the server can hand the lock to another
replica, so two replicas never act as leader at the same time. After it gave up, the old leader also asks the server, through
a regular connection, to end its lock session, which frees the lock at once when only the lock connection is affected.
It then asks for the lock again like every follower.

Notes:

- `ha.leaderSessionTimeout` accepts 5 seconds to 10 minutes. A shorter timeout fails over faster and ends leadership on a
  short network interruption between `myriarchd` and the database; a longer one is calmer and slower. The default of 30
  seconds suits a database in the same network.
- The parameters need a PostgreSQL server on Linux. A server that refuses `tcp_user_timeout` (for example PostgreSQL on macOS)
  is used without it and `myriarchd` logs a warning; the keepalive settings still apply.
- A connection pooler in transaction mode between `myriarchd` and PostgreSQL breaks the lock and the listener anyway (see the
  [design](../design/opamp-server.md#event-bus)); a pooler or firewall in session mode that keeps its own
  connection to the server can hide the death of the client from PostgreSQL. In that case the bound does not hold: the pooler's
  timeouts decide how long the lock stays.
- What is measured: the test suite freezes the TCP connection of the lock session (no FIN) and checks that a second replica
  becomes leader within `ha.leaderSessionTimeout` and that the old leader stops acting. A measurement of the failover in a
  cluster, with a real node failure, is not part of the tests.

## Size the shutdown

On a planned stop the HTTP servers wait for open requests for up to
`server.shutdownTimeout` (default 30 seconds). Kubernetes sends SIGKILL when `terminationGracePeriodSeconds` is over, so
the grace period must cover the whole sequence:

```text
terminationGracePeriodSeconds  >=  preStopSleepSeconds + server.shutdownTimeout + 5
```

The 5 seconds are for unlocking, closing the lock and listener sessions and the pool. The chart defaults (10 + 30 + 5 = 45,
grace period 60) satisfy this. If you raise `config.server.shutdownTimeout`, raise `terminationGracePeriodSeconds` with it;
otherwise a replica that is still draining is killed without having released the lock, and the others wait for the
failover time of a hard failure.

`ha.leaderSessionTimeout` does not enter the formula: a stopping leader releases the lock itself.

## Settings

| Setting | Default | Meaning |
|---|---|---|
| `ha.leaderSessionTimeout` | `30s` | Bound of the failover after a hard failure; see above. Environment variable `MYRIARCH_HA_LEADERSESSIONTIMEOUT`. |
| `ha.heartbeatInterval` | `10s` | Interval of the replica registry heartbeat. |
| `ha.leaderLockKey` | `7012024` | Advisory lock key; change it only if another application on the same database uses it. |
| `server.shutdownTimeout` | `30s` | Time to drain on a planned stop. |

The reference with all keys is [Configuration](../reference/configuration.md#ha). The metric `myriarch_leader` is 1 on the
leader and 0 elsewhere; alert when no replica reports 1 for longer than the failover time above.
