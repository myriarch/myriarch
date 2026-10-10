---
title: "Track releases and send notifications"
weight: 50
description: "Follow upstream collector releases, review breaking changes before raising a target version, and route events to Teams, Slack or a signed webhook."
---

The release tracker watches the upstream releases of the OpenTelemetry Collector, reads their changelogs and extracts
breaking changes and deprecations. It never changes a fleet by itself: it tells you that a new version exists and what to
check before you raise the target version in Git (or choose it in the UI, see
[Roll out a collector version](roll-out-a-collector-version.md)).

## What the tracker does

- It polls `open-telemetry/opentelemetry-collector-releases` through the GitHub API (`releases.releasesRepo`, once an hour
  by default) and reads the changelogs of the core and contrib repositories for the versions it finds.
- Without a token GitHub allows 60 requests per hour for all clients behind one address. Give the tracker a read-only
  token for public repositories with `releases.tokenFile` to raise the limit to 5,000 per hour.
- The UI shows the result on the page *Releases* (list, details with breaking changes, comparison between two versions,
  snippets for the fleet file) and as a banner "New collector version" on the overview. A collector is **outdated** when
  it runs a version below the newest stable release. The target version of a fleet is decided by Git
  (`spec.collector.version`), not by the tracker.
- `POST /api/v1/releases/actions/refresh` polls at once. `GET /api/v1/releases/compare?from=0.158.0&to=0.162.0` lists the
  breaking changes and deprecations of all versions in between. `POST /api/v1/releases/{distribution}/{version}/actions/acknowledge`
  marks a release as reviewed.

For offline experiments, `myriarchd dev --releases-fixtures internal/release/testdata/fixtures` reads recorded release
data instead of GitHub. Enterprise setups with GitHub Enterprise set `releases.githubApiUrl` and `releases.rawBaseUrl`;
see the [production checklist](../operations/production-checklist.md).

## Configure notification channels

Channels live in the `myriarchd` configuration under `notifications.channels`. In dev mode, put them into a file and pass
it with `-c`:

```yaml
# .dev/notify.yaml
notifications:
  channels:
    - name: ops-chat
      type: slack                       # webhook | teams | slack
      urlFile: .dev/slack-webhook-url   # an incoming-webhook URL is a secret: keep it in a file
      events: ["release.*", "rollout.waiting_approval", "rollout.failed"]
      minSeverity: info                 # info (default) | warning | critical
    - name: automation
      type: webhook
      url: https://hooks.example.com/myriarch
      secretFile: .dev/webhook-secret   # at least 32 bytes; required for type webhook in production
```

Create the two files before you start: `urlFile` holds the incoming-webhook URL of your chat workspace on one line, and
`openssl rand -hex 32 > .dev/webhook-secret` creates a secret of the required length. The server refuses to start when a
referenced file is missing or too short.

```bash
bin/myriarchd dev --gitops-dir .dev/gitops-work --data-dir .dev -c .dev/notify.yaml
```

`events` lists the event names a channel receives; a trailing `*` matches a group, and an empty list means all events.
The events are:

| Event | When |
|---|---|
| `release.new` | A new upstream release was detected. |
| `release.breaking` | A new release contains breaking changes. |
| `rollout.started` | A rollout was created. |
| `rollout.waiting_approval` | A wave waits for approval. |
| `rollout.paused` | A rollout paused, for example at its failure threshold. |
| `rollout.failed` | A rollout failed. |
| `rollout.completed` | A rollout finished. |
| `rollout.rolled_back` | A rollout was rolled back. |
| `gitops.sync_failed` | The GitOps repository could not be applied. |

Send a test message to check a channel (the role *operator* or *admin* is required):

```bash
api -X POST localhost:8080/api/v1/notifications/channels/ops-chat/actions/test
```

## Verify webhook deliveries

A `webhook` channel posts a JSON document (event, severity, title, summary, link, fields, time) and signs it so that the
receiver can trust the sender:

| Header | Content |
|---|---|
| `X-Myriarch-Signature` | `sha256=` followed by the hex HMAC-SHA-256 of `<timestamp>.<body>` with the shared secret. |
| `X-Myriarch-Timestamp` | Unix time of the delivery, in seconds. |
| `X-Myriarch-Delivery` | The ID of the delivery, to recognise repeats. |

A receiver should recompute the signature over the exact bytes it received, compare in constant time, reject deliveries
whose timestamp is more than a few minutes old, and drop deliveries whose `X-Myriarch-Delivery` it has seen before. The
helper `scripts/e2e/mock_receiver.py` contains a short reference implementation of the check. Delivery is
de-duplicated on the server side as well, so a notification is not sent twice for the same event.

Teams and Slack channels need only the incoming-webhook URL. Every URL of a channel must use https in production and must
not contain credentials; the destination is checked by the outbound policy like every other outbound connection (see
[Security model](../security/model.md)).

Related pages: [Rollouts](../concepts/rollouts.md) explains when each rollout event is raised, and the settings of the
tracker and of the channels are part of the [production checklist](../operations/production-checklist.md).
