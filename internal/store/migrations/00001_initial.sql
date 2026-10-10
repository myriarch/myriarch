-- Copyright The Myriarch Authors
-- SPDX-License-Identifier: Apache-2.0

-- Myriarch initial schema (PostgreSQL >= 15).
--
-- Conventions:
--   * ids are uuid (generated in Go; agents.id is the OpAMP instance_uid)
--   * hashes are bytea (raw SHA-256, 32 bytes); hex only at the API boundary
--   * all timestamps are timestamptz (UTC)
--   * attribute/label maps are jsonb objects with string values
--   * enum-like columns are text with CHECK constraints (values mirror
--     internal/domain constants)

-- +goose Up

-- ------------------------------------------------------------------ agents --
CREATE TABLE agents (
    id                          uuid PRIMARY KEY,
    kind                        text        NOT NULL DEFAULT 'unknown'
                                CHECK (kind IN ('collector', 'updater', 'unknown')),
    service_name                text        NOT NULL DEFAULT '',
    service_version             text        NOT NULL DEFAULT '',
    -- parsed semantic version of service_version (NULL if unparsable);
    -- maintained by the store on UpsertDescription, used for "outdated"
    -- filters and version sorting.
    version_major               integer,
    version_minor               integer,
    version_patch               integer,
    hostname                    text        NOT NULL DEFAULT '',
    os_type                     text        NOT NULL DEFAULT '',
    os_description              text        NOT NULL DEFAULT '',
    arch                        text        NOT NULL DEFAULT '',
    identifying_attributes      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    non_identifying_attributes  jsonb       NOT NULL DEFAULT '{}'::jsonb,
    labels                      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    capabilities                bigint      NOT NULL DEFAULT 0,

    -- connection (written by the OpAMP layer)
    status                      text        NOT NULL DEFAULT 'offline'
                                CHECK (status IN ('online', 'offline')),
    connected_replica           text,
    transport                   text        NOT NULL DEFAULT '',
    remote_addr                 text        NOT NULL DEFAULT '',
    connected_at                timestamptz,
    disconnected_at             timestamptz,
    last_seen_at                timestamptz,

    -- health (top level + full tree)
    healthy                     boolean,
    health_status               text        NOT NULL DEFAULT '',
    health_last_error           text        NOT NULL DEFAULT '',
    health_start_time           timestamptz,
    component_health            jsonb,
    health_updated_at           timestamptz,

    -- reported remote config status
    remote_config_status        text        NOT NULL DEFAULT 'unset'
                                CHECK (remote_config_status IN ('unset', 'applying', 'applied', 'failed')),
    remote_config_hash          bytea,
    remote_config_error         text        NOT NULL DEFAULT '',
    remote_config_updated_at    timestamptz,

    -- reported effective config / components / packages
    effective_config_hash       bytea,
    effective_config_updated_at timestamptz,
    available_components_hash   bytea,
    package_statuses            jsonb,

    -- desired state (written by assignment service and rollout engine)
    fleet_name                  text,
    assigned_config_hash        bytea,
    desired_config_hash         bytea,
    failed_config_hash          bytea,
    target_version              text        NOT NULL DEFAULT '',
    assignment_error            text        NOT NULL DEFAULT '',
    assigned_at                 timestamptz,

    -- updater rows: the collector agent this updater manages (see
    -- store.AgentStore.SetUpdaterLink; collectors find their updater via this column)
    linked_agent_id             uuid,
    enrollment_token_id         uuid,

    first_seen_at               timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now(),

    -- Server-side config sync state (see domain.ConfigSyncState).
    config_sync                 text GENERATED ALWAYS AS (
        CASE
            WHEN fleet_name IS NULL OR assigned_config_hash IS NULL THEN 'none'
            WHEN desired_config_hash IS NOT NULL AND failed_config_hash = desired_config_hash THEN 'failed'
            WHEN desired_config_hash IS DISTINCT FROM assigned_config_hash THEN 'drift'
            WHEN remote_config_hash = desired_config_hash AND remote_config_status = 'applied' THEN 'in_sync'
            ELSE 'pending'
        END
    ) STORED
);

CREATE INDEX agents_status_idx        ON agents (status);
CREATE INDEX agents_kind_idx          ON agents (kind);
CREATE INDEX agents_fleet_idx         ON agents (fleet_name);
CREATE INDEX agents_version_idx       ON agents (version_major, version_minor, version_patch);
CREATE INDEX agents_hostname_idx      ON agents (lower(hostname));
CREATE INDEX agents_last_seen_idx     ON agents (last_seen_at);
CREATE INDEX agents_config_sync_idx   ON agents (config_sync);
CREATE INDEX agents_replica_idx       ON agents (connected_replica) WHERE connected_replica IS NOT NULL;
CREATE INDEX agents_linked_idx        ON agents (linked_agent_id) WHERE linked_agent_id IS NOT NULL;
CREATE INDEX agents_nonid_attrs_idx   ON agents USING gin (non_identifying_attributes jsonb_path_ops);
CREATE INDEX agents_id_attrs_idx      ON agents USING gin (identifying_attributes jsonb_path_ops);
CREATE INDEX agents_labels_idx        ON agents USING gin (labels jsonb_path_ops);

CREATE TABLE agent_events (
    id          bigserial PRIMARY KEY,
    agent_id    uuid        NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    time        timestamptz NOT NULL DEFAULT now(),
    type        text        NOT NULL,
    severity    text        NOT NULL DEFAULT 'info' CHECK (severity IN ('info', 'warning', 'error')),
    message     text        NOT NULL DEFAULT '',
    details     jsonb
);
CREATE INDEX agent_events_agent_idx ON agent_events (agent_id, id DESC);
CREATE INDEX agent_events_time_idx  ON agent_events (time);

-- ------------------------------------------- content-addressed config data --
CREATE TABLE rendered_configs (
    hash            bytea PRIMARY KEY,           -- sha256(body)
    body            bytea       NOT NULL,        -- canonical YAML
    content_type    text        NOT NULL DEFAULT 'text/yaml',
    fleet_name      text        NOT NULL DEFAULT '',
    blueprint_name  text        NOT NULL DEFAULT '',
    git_commit      text        NOT NULL DEFAULT '',
    size            integer     NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    -- refreshed by PutRendered on conflict; GC only deletes rows whose
    -- touched_at is older than the retention (store.ConfigStore.GC)
    touched_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE effective_configs (
    hash            bytea PRIMARY KEY,           -- sha256(body)
    body            bytea       NOT NULL,
    content_type    text        NOT NULL DEFAULT 'text/yaml',
    created_at      timestamptz NOT NULL DEFAULT now(),
    touched_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE component_sets (
    hash            bytea PRIMARY KEY,           -- agent-reported AvailableComponents.hash
    components      jsonb       NOT NULL,        -- domain.ComponentSet.Components
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- ------------------------------------------------------------------ gitops --
-- Active snapshot of declarative objects (replaced atomically per sync).
CREATE TABLE gitops_objects (
    kind            text        NOT NULL CHECK (kind IN ('Fleet', 'Blueprint', 'RolloutPolicy', 'Executor', 'Inventory')),
    name            text        NOT NULL,
    object          jsonb       NOT NULL,        -- JSON of the domain type (incl. source)
    source_path     text        NOT NULL,
    commit_sha      text        NOT NULL,
    content_hash    bytea       NOT NULL,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, name)
);

CREATE TABLE gitops_syncs (
    id              bigserial PRIMARY KEY,
    started_at      timestamptz NOT NULL,
    finished_at     timestamptz,
    trigger         text        NOT NULL CHECK (trigger IN ('poll', 'webhook', 'manual', 'startup')),
    commit_sha      text        NOT NULL DEFAULT '',
    commit          jsonb,                       -- domain.GitCommit
    result          text        NOT NULL CHECK (result IN ('applied', 'unchanged', 'invalid', 'error')),
    issues          jsonb       NOT NULL DEFAULT '[]'::jsonb,
    added           jsonb       NOT NULL DEFAULT '[]'::jsonb,
    changed         jsonb       NOT NULL DEFAULT '[]'::jsonb,
    removed         jsonb       NOT NULL DEFAULT '[]'::jsonb
);
CREATE INDEX gitops_syncs_started_idx ON gitops_syncs (started_at DESC);

-- Single-row status of the syncer (id is always 1).
CREATE TABLE gitops_status (
    id              smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    status          jsonb       NOT NULL,        -- domain.GitOpsSyncStatus
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------- rollouts --
CREATE TABLE rollouts (
    id              uuid PRIMARY KEY,
    kind            text        NOT NULL CHECK (kind IN ('config', 'version')),
    fleet_name      text        NOT NULL,
    policy_name     text        NOT NULL,
    policy          jsonb       NOT NULL,        -- domain.RolloutPolicy snapshot
    status          text        NOT NULL CHECK (status IN ('pending', 'running', 'waiting_approval', 'paused',
                                    'completed', 'failed', 'aborted', 'rolling_back', 'rolled_back', 'superseded')),
    status_reason   text        NOT NULL DEFAULT '',
    from_ref        text        NOT NULL DEFAULT '',
    to_ref          text        NOT NULL,        -- git commit (config) or version (version)
    git_commit      text        NOT NULL DEFAULT '',
    executor        text        NOT NULL DEFAULT '',
    current_wave    integer     NOT NULL DEFAULT 0,
    created_by      text        NOT NULL,
    annotations     jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz
);
CREATE INDEX rollouts_fleet_idx   ON rollouts (fleet_name, created_at DESC);
CREATE INDEX rollouts_active_idx  ON rollouts (status)
    WHERE status NOT IN ('completed', 'aborted', 'rolled_back', 'superseded');

CREATE TABLE rollout_waves (
    rollout_id       uuid        NOT NULL REFERENCES rollouts (id) ON DELETE CASCADE,
    idx              integer     NOT NULL,
    name             text        NOT NULL,
    selector         jsonb,                      -- domain.Selector (nil for percent waves)
    percent          integer     NOT NULL DEFAULT 0,
    require_approval boolean     NOT NULL DEFAULT false,
    status           text        NOT NULL CHECK (status IN ('pending', 'waiting_approval', 'running', 'completed', 'failed', 'skipped')),
    approved_by      text        NOT NULL DEFAULT '',
    approved_at      timestamptz,
    started_at       timestamptz,
    finished_at      timestamptz,
    PRIMARY KEY (rollout_id, idx)
);

CREATE TABLE rollout_targets (
    rollout_id      uuid        NOT NULL REFERENCES rollouts (id) ON DELETE CASCADE,
    agent_id        uuid        NOT NULL,        -- no FK: agents may be deleted, history stays
    hostname        text        NOT NULL DEFAULT '',
    wave            integer     NOT NULL,
    status          text        NOT NULL CHECK (status IN ('pending', 'waiting_window', 'in_progress', 'verifying',
                                    'succeeded', 'failed', 'skipped', 'cancelled', 'rolled_back')),
    from_ref        text        NOT NULL DEFAULT '',   -- config hash hex or version
    to_ref          text        NOT NULL,
    attempts        integer     NOT NULL DEFAULT 0,
    started_at      timestamptz,
    verified_at     timestamptz,
    finished_at     timestamptz,
    last_error      text        NOT NULL DEFAULT '',
    executor_ref    text        NOT NULL DEFAULT '',
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (rollout_id, agent_id)
);
CREATE INDEX rollout_targets_agent_idx  ON rollout_targets (agent_id, updated_at DESC);
CREATE INDEX rollout_targets_wave_idx   ON rollout_targets (rollout_id, wave, status);

-- Desired package set per myriarch-updater agent (OpAMP PackagesAvailable).
CREATE TABLE package_offers (
    agent_id        uuid        NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    name            text        NOT NULL,        -- '' = collector
    version         text        NOT NULL,        -- 'v0.163.0'
    download_url    text        NOT NULL,
    content_hash    bytea       NOT NULL,
    signature       bytea,
    headers         jsonb,
    rollout_id      uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, name)
);

-- ---------------------------------------------------------------- releases --
CREATE TABLE releases (
    distribution     text        NOT NULL,
    version          text        NOT NULL,       -- '0.162.0'
    version_major    integer     NOT NULL,
    version_minor    integer     NOT NULL,
    version_patch    integer     NOT NULL,
    tag              text        NOT NULL,
    published_at     timestamptz,
    prerelease       boolean     NOT NULL DEFAULT false,
    html_url         text        NOT NULL DEFAULT '',
    notes            text        NOT NULL DEFAULT '',
    breaking_changes jsonb       NOT NULL DEFAULT '[]'::jsonb,
    deprecations     jsonb       NOT NULL DEFAULT '[]'::jsonb,
    assets           jsonb       NOT NULL DEFAULT '[]'::jsonb,
    detected_at      timestamptz NOT NULL DEFAULT now(),
    acknowledged_by  text        NOT NULL DEFAULT '',
    acknowledged_at  timestamptz,
    PRIMARY KEY (distribution, version)
);
CREATE INDEX releases_order_idx ON releases (distribution, version_major DESC, version_minor DESC, version_patch DESC);

-- --------------------------------------------------------------- artifacts --
CREATE TABLE artifacts (
    id                  uuid PRIMARY KEY,
    distribution        text        NOT NULL,
    version             text        NOT NULL,
    os                  text        NOT NULL,
    arch                text        NOT NULL,
    format              text        NOT NULL,     -- tar.gz | zip
    filename            text        NOT NULL,
    size                bigint      NOT NULL DEFAULT 0,
    sha256              bytea,
    sigstore_bundle     bytea,
    signature_verified  boolean     NOT NULL DEFAULT false,
    signature_identity  text        NOT NULL DEFAULT '',
    verification_error  text        NOT NULL DEFAULT '',
    source_url          text        NOT NULL,
    status              text        NOT NULL CHECK (status IN ('pending', 'downloading', 'ready', 'failed')),
    error               text        NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (distribution, version, os, arch, format)
);

-- ---------------------------------------------------------------- identity --
CREATE TABLE users (
    id              uuid PRIMARY KEY,
    subject         text        NOT NULL UNIQUE, -- OIDC sub or 'local:<name>'
    provider        text        NOT NULL CHECK (provider IN ('oidc', 'local')),
    email           text        NOT NULL DEFAULT '',
    name            text        NOT NULL DEFAULT '',
    role            text        NOT NULL CHECK (role IN ('viewer', 'operator', 'admin')),
    role_source     text        NOT NULL DEFAULT 'mapping' CHECK (role_source IN ('mapping', 'manual')),
    disabled        boolean     NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_login_at   timestamptz
);

CREATE TABLE sessions (
    id              text PRIMARY KEY,            -- sha256 hex of the cookie secret
    user_id         uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    csrf_token      text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    last_seen_at    timestamptz NOT NULL DEFAULT now(),
    user_agent      text        NOT NULL DEFAULT '',
    remote_addr     text        NOT NULL DEFAULT ''
);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE api_tokens (
    id              uuid PRIMARY KEY,
    name            text        NOT NULL,
    display_prefix  text        NOT NULL,
    hash            bytea       NOT NULL UNIQUE, -- sha256(token)
    role            text        NOT NULL CHECK (role IN ('viewer', 'operator', 'admin')),
    created_by      text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz,
    last_used_at    timestamptz,
    revoked_at      timestamptz
);

CREATE TABLE enrollment_tokens (
    id              uuid PRIMARY KEY,
    name            text        NOT NULL,
    display_prefix  text        NOT NULL,
    hash            bytea       NOT NULL UNIQUE, -- sha256(token)
    labels          jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_by      text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz,
    last_used_at    timestamptz,
    revoked_at      timestamptz,
    use_count       bigint      NOT NULL DEFAULT 0
);

CREATE TABLE saved_filters (
    id              uuid PRIMARY KEY,
    user_id         uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name            text        NOT NULL,
    page            text        NOT NULL DEFAULT 'agents',
    query           jsonb       NOT NULL,
    shared          boolean     NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, page, name)
);

-- ------------------------------------------------------------------- audit --
CREATE TABLE audit_events (
    id              bigserial PRIMARY KEY,
    time            timestamptz NOT NULL DEFAULT now(),
    actor_type      text        NOT NULL CHECK (actor_type IN ('user', 'token', 'system', 'gitops', 'agent')),
    actor_id        text        NOT NULL,
    actor_name      text        NOT NULL DEFAULT '',
    action          text        NOT NULL,
    target_type     text        NOT NULL DEFAULT '',
    target_id       text        NOT NULL DEFAULT '',
    result          text        NOT NULL CHECK (result IN ('success', 'failure')),
    details         jsonb,
    source_ip       text        NOT NULL DEFAULT '',
    request_id      text        NOT NULL DEFAULT ''
);
CREATE INDEX audit_events_time_idx   ON audit_events (time DESC);
CREATE INDEX audit_events_action_idx ON audit_events (action, time DESC);
CREATE INDEX audit_events_target_idx ON audit_events (target_type, target_id, time DESC);
CREATE INDEX audit_events_actor_idx  ON audit_events (actor_id, time DESC);

-- ---------------------------------------------------------- replicas / kv --
CREATE TABLE replicas (
    id                  text PRIMARY KEY,
    hostname            text        NOT NULL DEFAULT '',
    version             text        NOT NULL DEFAULT '',
    started_at          timestamptz NOT NULL,
    last_heartbeat_at   timestamptz NOT NULL,
    is_leader           boolean     NOT NULL DEFAULT false,
    connections         integer     NOT NULL DEFAULT 0
);

CREATE TABLE kv (
    key             text PRIMARY KEY,
    value           bytea       NOT NULL,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS kv;
DROP TABLE IF EXISTS replicas;
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS saved_filters;
DROP TABLE IF EXISTS enrollment_tokens;
DROP TABLE IF EXISTS api_tokens;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS artifacts;
DROP TABLE IF EXISTS releases;
DROP TABLE IF EXISTS package_offers;
DROP TABLE IF EXISTS rollout_targets;
DROP TABLE IF EXISTS rollout_waves;
DROP TABLE IF EXISTS rollouts;
DROP TABLE IF EXISTS gitops_status;
DROP TABLE IF EXISTS gitops_syncs;
DROP TABLE IF EXISTS gitops_objects;
DROP TABLE IF EXISTS component_sets;
DROP TABLE IF EXISTS effective_configs;
DROP TABLE IF EXISTS rendered_configs;
DROP TABLE IF EXISTS agent_events;
DROP TABLE IF EXISTS agents;
