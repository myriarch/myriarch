-- Copyright The Myriarch Authors
-- SPDX-License-Identifier: Apache-2.0

-- The compatibility level of the schema (ADR-0036; docs/design/opamp-server.md, "Start, migrations and schema
-- compatibility").
--
-- schema_compat has exactly one row. min_binary_schema is the lowest schema level that a myriarchd binary has to have
-- to run on this database; the schema level of a binary is the highest migration it embeds (migrations.Latest).
-- myriarchd refuses to start when its schema level is below min_binary_schema. On a schema that is newer than itself
-- it starts with a warning, as long as it is not below. min_binary_schema never exceeds the highest applied migration.
--
-- From this migration on, the header of every migration states its effect on the level ("Rolling update:" and
-- "Compatibility level:" lines, see the package documentation of internal/store/migrations and docs/development.md),
-- and a migration that raises the level sets the value in Up and restores the previous value in Down.
-- conventions_test.go checks the headers and applies every migration upwards and downwards.
--
-- The initial value 3 is the level that every database has when it reaches this migration: 00003 is the last migration
-- that binaries before it cannot work with (see its header), and 00004 and 00005 are expand only.
--
-- Rolling update: expand only. Replicas of the previous version do not know the table and ignore it.
-- Compatibility level: unchanged. The table starts with the level 3 that the schema has had since 00003.

-- +goose Up

CREATE TABLE schema_compat (
    id                boolean     PRIMARY KEY DEFAULT true CHECK (id),
    min_binary_schema bigint      NOT NULL CHECK (min_binary_schema >= 1),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

INSERT INTO schema_compat (id, min_binary_schema) VALUES (true, 3);

-- +goose Down

DROP TABLE IF EXISTS schema_compat;
