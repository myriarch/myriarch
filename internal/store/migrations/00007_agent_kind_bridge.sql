-- Copyright The Myriarch Authors
-- SPDX-License-Identifier: Apache-2.0

-- The agent kind "bridge" (ADR-0037): the OpAMP Bridge of the OpenTelemetry Operator is recognised by the attributes it
-- reports and stored as its own kind, so that no query that selects collectors reaches it.
--
-- The check constraint of agents.kind lists the kinds. The new kind is written by the new binary only; a replica of the
-- previous version never writes it. During a rolling update the previous replicas still classify a Bridge as a collector
-- (docs/design/opamp-server.md, "Bridge agents"), and a row of kind "bridge" that they read is an agent of an unknown
-- kind to them.
--
-- Rolling update: expand only. The replicas of the previous version keep working on the wider constraint.
-- Compatibility level: unchanged. The level of the schema stays where it was.

-- +goose Up

ALTER TABLE agents DROP CONSTRAINT agents_kind_check;
ALTER TABLE agents ADD CONSTRAINT agents_kind_check CHECK (kind IN ('collector', 'updater', 'bridge', 'unknown'));

-- +goose Down

UPDATE agents SET kind = 'unknown' WHERE kind = 'bridge';
ALTER TABLE agents DROP CONSTRAINT agents_kind_check;
ALTER TABLE agents ADD CONSTRAINT agents_kind_check CHECK (kind IN ('collector', 'updater', 'unknown'));
