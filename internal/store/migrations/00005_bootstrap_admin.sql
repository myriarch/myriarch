-- Copyright The Myriarch Authors
-- SPDX-License-Identifier: Apache-2.0

-- The bootstrap admin: a user whose provider is 'bootstrap' (subject
-- 'bootstrap:<username>'), created from the server configuration
-- (auth.bootstrapAdmin) for the first sign-in and as a break-glass account.
--
-- Rolling update: expand only. Older replicas never create such users and still
-- reject disabled users; a session of a bootstrap user that they load is
-- rejected by the newer replicas as soon as the account is inactive there.

-- +goose Up

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_provider_check;
ALTER TABLE users ADD CONSTRAINT users_provider_check CHECK (provider IN ('oidc', 'local', 'bootstrap'));

-- +goose Down

UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, now())
WHERE created_by_user_id IN (SELECT id FROM users WHERE provider = 'bootstrap');
DELETE FROM users WHERE provider = 'bootstrap';
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_provider_check;
ALTER TABLE users ADD CONSTRAINT users_provider_check CHECK (provider IN ('oidc', 'local'));
