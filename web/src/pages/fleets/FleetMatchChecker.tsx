// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Alert, Badge, Button, Group, SegmentedControl, Stack, Text } from '@mantine/core';
import { IconCircleCheck, IconTargetArrow } from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { useMatchFleets } from '@/api/hooks/fleets';
import type { AgentSummary } from '@/api/types';
import { AgentPicker } from '@/components/AgentPicker';
import { KeyValueEditor } from '@/components/KeyValueEditor';
import { newRow, rowsToRecord, type KVRow } from '@/components/kv';
import { KeyValue } from '@/components/misc';
import { SectionCard } from '@/components/SectionCard';
import { ErrorAlert } from '@/components/states';
import { fleetLink } from '@/lib/links';

const ATTRIBUTE_SUGGESTIONS = [
  'os.type',
  'host.name',
  'host.arch',
  'site.type',
  'site.id',
  'site.country',
  'site.region',
  'site.timezone',
  'service.name',
  'service.version',
];

/** "Which fleet?" checker: explains the fleet assignment of an existing or hypothetical agent. */
export function FleetMatchChecker() {
  const { t } = useTranslation();
  const [mode, setMode] = useState<'agent' | 'attributes'>('agent');
  const [agent, setAgent] = useState<AgentSummary | null>(null);
  const [rows, setRows] = useState<KVRow[]>([
    newRow('os.type', 'windows'),
    newRow('site.type', 'edge'),
    newRow('site.country', 'NL'),
  ]);
  const match = useMatchFleets();
  const result = match.data;

  const canRun = mode === 'agent' ? !!agent : Object.keys(rowsToRecord(rows)).length > 0;
  const run = () =>
    match.mutate(mode === 'agent' ? { agentId: agent!.id } : { attributes: rowsToRecord(rows) });

  return (
    <SectionCard title={t('fleets.match.title')} description={t('fleets.match.subtitle')}>
      <Stack gap="md" maw={760}>
        <SegmentedControl
          size="xs"
          w="fit-content"
          value={mode}
          onChange={(v) => {
            setMode(v as 'agent' | 'attributes');
            match.reset();
          }}
          data={[
            { value: 'agent', label: t('fleets.match.byAgent') },
            { value: 'attributes', label: t('fleets.match.byAttributes') },
          ]}
        />
        {mode === 'agent' ? (
          <AgentPicker value={agent} onChange={setAgent} />
        ) : (
          <KeyValueEditor
            rows={rows}
            onChange={setRows}
            datalistId="attr-keys"
            suggestions={ATTRIBUTE_SUGGESTIONS}
            keyLabel={t('fleets.match.attribute')}
          />
        )}
        <Group>
          <Button
            leftSection={<IconTargetArrow size={16} />}
            onClick={run}
            loading={match.isPending}
            disabled={!canRun}
          >
            {t('fleets.match.run')}
          </Button>
        </Group>
        <ErrorAlert error={match.error} />
        {result && (
          <Stack gap="sm" data-testid="match-result">
            {result.fleet ? (
              <Alert
                color="teal"
                variant="light"
                icon={<IconCircleCheck size={18} />}
                title={t('fleets.match.assignedTo')}
              >
                <Text component={Link} to={fleetLink(result.fleet)} fw={700} fz="lg">
                  {result.fleet}
                </Text>
              </Alert>
            ) : (
              <Alert color="yellow" variant="light" title={t('fleets.match.noFleet')}>
                {result.error ?? t('fleets.match.noFleetHint')}
              </Alert>
            )}
            {result.error && result.fleet && (
              <Alert color="orange" variant="light">
                {result.error}
              </Alert>
            )}
            <KeyValue
              labelWidth={160}
              items={[
                {
                  label: t('fleets.match.candidates'),
                  value:
                    result.candidates.length === 0 ? (
                      '–'
                    ) : (
                      <Group gap={6}>
                        {result.candidates.map((c, i) => (
                          <Badge
                            key={c}
                            variant={i === 0 ? 'filled' : 'light'}
                            component={Link}
                            to={fleetLink(c)}
                            style={{ textTransform: 'none', cursor: 'pointer' }}
                          >
                            {i + 1}. {c}
                          </Badge>
                        ))}
                      </Group>
                    ),
                },
                {
                  label: t('fleets.match.inventoryLabels'),
                  hidden:
                    !result.inventoryLabels || Object.keys(result.inventoryLabels).length === 0,
                  value: (
                    <Group gap={6}>
                      {Object.entries(result.inventoryLabels ?? {}).map(([k, v]) => (
                        <Badge
                          key={k}
                          variant="outline"
                          color="gray"
                          style={{ textTransform: 'none' }}
                        >
                          {k}={v}
                        </Badge>
                      ))}
                    </Group>
                  ),
                },
              ]}
            />
            <Text fz="xs" c="dimmed">
              {t('fleets.match.priorityHint')}
            </Text>
          </Stack>
        )}
      </Stack>
    </SectionCard>
  );
}
