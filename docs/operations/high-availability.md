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
| **Hard failure** of the leader's node, or a network failure that drops packets without closing the connection | Within `ha.leaderSessionTimeout` (T), 30 seconds with the default. The end-to-end budget below includes the retry of the follower. |

A hard failure is the case that needs a bound. PostgreSQL releases a session lock only when it notices that the session is
gone, and for a client that went silent without a FIN or RST the operating system's TCP defaults need hours. `myriarchd`
therefore opens the lock session and the session of the event bus listener with TCP settings in the startup packet. They
are derived from one budget: T is split into the time the server needs to drop the dead session and the time the follower
needs to take the lock afterwards.

| Part of T | Value | With the default of 30 seconds |
|---|---|---|
| Retry of a follower | every min(5 s, T/6), up to 20 percent more by jitter | at most 6 s |
| Margin for the acquisition | T/20 | 1.5 s |
| **Detection** D: the server drops the silent session within | T minus the two parts above, rounded down to whole seconds | 22 s |

The server parameters follow from D. With I = D/6 (rounded down, at least 1 second):

| Server parameter | Value |
|---|---|
| `tcp_keepalives_idle` | I seconds |
| `tcp_keepalives_interval` | I seconds |
| `tcp_keepalives_count` | 5 |
| `tcp_user_timeout` | (D − I) seconds, in milliseconds |

On Linux a non-zero `tcp_user_timeout` replaces the count as the criterion: PostgreSQL probes at I, 2I, 3I, ... after the
last packet it received and drops the connection at the first probe time that is at least D − I after it, so a silent peer
is dropped no earlier than D − I and before D (19 to 22 seconds with the default; D − I is above the 15 seconds after which
the leader gives up, see below). The keepalive count only matters on a server that does not know
`tcp_user_timeout`: it drops after 6I, which is again at most D. Because every timing is a whole number of
seconds, the steps are rounded in the safe direction for any T, including values that do not divide evenly. Only the two
sessions get the settings; the pool of ordinary connections keeps the defaults of your server.

The leader does not wait for the server. It confirms its lock session with a short query every T/6 (timeout T/3) and
**gives up leadership itself, at the latest T/2 after the last answer** (15 seconds with the default): it cancels the
context of the leader jobs and reports `myriarch_leader 0`. This happens before the server can hand the lock to another
replica, so two replicas never act as leader at the same time. After it gave up, the old leader also asks the server, through
a regular connection, to end its lock session, which frees the lock at once when only the lock connection is affected.
It then asks for the lock again like every follower.

Notes:

- `ha.leaderSessionTimeout` accepts 10 seconds to 10 minutes. A shorter timeout fails over faster and ends leadership on a
  short network interruption between `myriarchd` and the database; a longer one is calmer and slower. The default of 30
  seconds suits a database in the same network. Below 30 seconds the retry of the followers shortens to T/6.
- The parameters need a PostgreSQL server on Linux. A server that refuses `tcp_user_timeout` (for example PostgreSQL on macOS)
  is used without it and `myriarchd` logs a warning; the keepalive settings still apply.
- A connection pooler in transaction mode between `myriarchd` and PostgreSQL breaks the lock and the listener anyway (see the
  [design](../design/opamp-server.md#event-bus)); a pooler or firewall in session mode that keeps its own
  connection to the server can hide the death of the client from PostgreSQL. In that case the bound does not hold: the pooler's
  timeouts decide how long the lock stays.
- What the tests prove: (1) the budget: for every T from 10 seconds to 10 minutes, also values that are not whole seconds, a
  model of the Linux keepalive timer and of the count-based servers shows that detection plus the longest retry stays within
  T and that the earliest drop is after the leader gave up. (2) The server really receives the parameters: a session opened
  by `myriarchd` reports them with `SHOW`. (3) A test freezes the TCP connection of the lock session (no FIN) while the rest
  of the database stays reachable and checks that a second replica becomes leader within T and that the old leader stops
  acting; this covers the leader's own resignation and the explicit ending of the session, not the server's keepalive timer.
  A measurement of the failover in a cluster, with a real node failure that the server can only detect by its keepalives,
  is not part of the tests and is open; it needs a Linux network and is not possible with a TCP proxy on one host.

## Size the shutdown

On a planned stop `myriarchd` stops in sequence, and each phase has its own time:

1. The OpAMP server closes the agent connections for up to `server.shutdownTimeout` (default 30 seconds), then, with a
   deadline of its own of 15 seconds, flushes the last-seen times and events and marks the agents of the replica offline.
2. Only then the HTTP servers start and wait for open requests for a fresh `server.shutdownTimeout`.
3. Last, the leader unlocks and closes its session (up to 4 seconds) and the pool and the listener close (about 1 second).

Kubernetes sends SIGKILL when `terminationGracePeriodSeconds` is over, so the grace period must cover the slowest case:

```text
terminationGracePeriodSeconds  >=  preStopSleepSeconds + 2 * server.shutdownTimeout + 15 + 5
```

With the defaults that is 10 + 2 × 30 + 15 + 5 = **90 seconds**. The chart default of 60 seconds covers a shutdown in which
the drains finish well within their timeouts, which is the normal case (a quiet replica stops in a few seconds), but not
one in which both drains run to their limit: set `terminationGracePeriodSeconds: 90` (or, with a different
`config.server.shutdownTimeout`, the value of the formula) if you cannot accept that. A replica that is killed while it is
still draining has not released the lock, and the others wait for the failover time of a hard failure.

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
