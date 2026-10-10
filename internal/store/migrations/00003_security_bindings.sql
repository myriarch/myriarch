-- Copyright The Myriarch Authors
-- SPDX-License-Identifier: Apache-2.0

-- Credential binding of agents and creator of API tokens.
--
-- agents.client_cert_subject: subject of the client certificate an agent was
-- registered with. Together with enrollment_token_id it binds the instance uid
-- to the credential of its first registration; a connection that presents
-- another credential for the same uid is rejected (internal/opamp).
--
-- api_tokens.created_by_user_id: the user who created the token. A token never
-- has more rights than its creator and stops working when the creator is
-- disabled or removed (internal/authn).
--
-- users.issuer: OIDC issuer of the user. Users are identified by
-- (provider, issuer, subject) instead of the subject alone, so identical
-- subjects of different identity providers (or a reconfigured issuer) never
-- share an account. Existing OIDC users get the issuer at their next login.
--
-- Rolling update: this is the one change of this migration that is not
-- compatible with replicas that still run the previous version. Those
-- replicas upsert OIDC users with ON CONFLICT (subject), which needs the
-- unique constraint that is replaced here, so OIDC sign-ins on them fail until
-- every replica runs the new version (sessions that already exist, agents,
-- the API and all other functions are not affected). The previous constraint
-- cannot stay: it would defeat the identification by (provider, issuer,
-- subject). Upgrade notes: scale myriarchd to one replica for this upgrade (or
-- accept failing OIDC sign-ins during the rolling update), then scale up.

-- +goose Up

ALTER TABLE agents ADD COLUMN client_cert_subject text;
ALTER TABLE api_tokens ADD COLUMN created_by_user_id uuid REFERENCES users (id) ON DELETE SET NULL;
ALTER TABLE users ADD COLUMN issuer text NOT NULL DEFAULT '';
ALTER TABLE users DROP CONSTRAINT users_subject_key;
CREATE UNIQUE INDEX users_identity_idx ON users (provider, issuer, subject);

-- +goose Down

DROP INDEX IF EXISTS users_identity_idx;
ALTER TABLE users ADD CONSTRAINT users_subject_key UNIQUE (subject);
ALTER TABLE users DROP COLUMN IF EXISTS issuer;
ALTER TABLE api_tokens DROP COLUMN IF EXISTS created_by_user_id;
ALTER TABLE agents DROP COLUMN IF EXISTS client_cert_subject;
