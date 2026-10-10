// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  Badge,
  Button,
  Checkbox,
  Grid,
  Group,
  SegmentedControl,
  Select,
  Stack,
  Text,
} from '@mantine/core';
import { IconPlayerPlay } from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useFleets, useRenderBlueprint } from '@/api/hooks/fleets';
import type { AgentSummary, ParameterSpec } from '@/api/types';
import { AgentPicker } from '@/components/AgentPicker';
import { CopyIconButton } from '@/components/CopyButton';
import { IssueList } from '@/components/IssueList';
import { KeyValueEditor } from '@/components/KeyValueEditor';
import { newRow, rowsToRecord, type KVRow } from '@/components/kv';
import { ParametersTable } from '@/components/ParametersTable';
import { SectionCard } from '@/components/SectionCard';
import { ErrorAlert } from '@/components/states';
import { YamlView } from '@/components/Yaml';
import { parseLooseValue } from '@/lib/yaml';
import { shortHash } from '@/lib/format';

/**
 * Render preview: choose an existing agent or enter attributes, optionally a fleet and parameter
 * overrides; the server renders without side effects and returns YAML, hash, parameter sources
 * and issues (rendering problems arrive as issues, not as HTTP errors).
 */
export function RenderPreview({
  blueprint,
  parameters,
}: {
  blueprint: string;
  parameters: ParameterSpec[];
}) {
  const { t } = useTranslation();
  const fleets = useFleets();
  const render = useRenderBlueprint(blueprint);
  const [mode, setMode] = useState<'agent' | 'attributes'>('attributes');
  const [agent, setAgent] = useState<AgentSummary | null>(null);
  const [attrs, setAttrs] = useState<KVRow[]>([
    newRow('os.type', 'windows'),
    newRow('site.type', 'edge'),
    newRow('site.country', 'NL'),
  ]);
  const [fleet, setFleet] = useState<string | null>(null);
  const [overrides, setOverrides] = useState<KVRow[]>([]);
  const [validate, setValidate] = useState(true);

  const canRun = mode === 'agent' ? !!agent : true;
  const run = () => {
    const params: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(rowsToRecord(overrides))) params[k] = parseLooseValue(v);
    render.mutate({
      ...(mode === 'agent' ? { agentId: agent!.id } : { attributes: rowsToRecord(attrs) }),
      ...(fleet ? { fleet } : {}),
      ...(Object.keys(params).length ? { parameters: params } : {}),
      validate,
    });
  };

  const res = render.data;
  return (
    <Grid gap="md">
      <Grid.Col span={{ base: 12, lg: 5 }}>
        <SectionCard
          title={t('blueprints.preview.title')}
          description={t('blueprints.preview.subtitle')}
        >
          <Stack gap="md">
            <SegmentedControl
              size="xs"
              value={mode}
              onChange={(v) => setMode(v as 'agent' | 'attributes')}
              data={[
                { value: 'attributes', label: t('blueprints.preview.byAttributes') },
                { value: 'agent', label: t('blueprints.preview.byAgent') },
              ]}
            />
            {mode === 'agent' ? (
              <AgentPicker value={agent} onChange={setAgent} />
            ) : (
              <KeyValueEditor
                rows={attrs}
                onChange={setAttrs}
                keyLabel={t('fleets.match.attribute')}
              />
            )}
            <Select
              label={t('common.fleet')}
              description={
                mode === 'agent'
                  ? t('blueprints.preview.agentFleetHint')
                  : t('blueprints.preview.fleetHint')
              }
              placeholder={
                mode === 'agent'
                  ? t('blueprints.preview.agentFleet')
                  : t('blueprints.preview.noFleet')
              }
              clearable
              searchable
              data={(fleets.data?.items ?? []).map((f) => ({
                value: f.fleet.name,
                label: f.fleet.displayName || f.fleet.name,
              }))}
              value={fleet}
              onChange={setFleet}
            />
            <Stack gap={4}>
              <Text fz="sm" fw={500}>
                {t('blueprints.preview.overrides')}
              </Text>
              <Text fz="xs" c="dimmed">
                {t('blueprints.preview.overridesHint')}
              </Text>
              <KeyValueEditor
                rows={overrides}
                onChange={setOverrides}
                keyLabel={t('blueprints.parameter')}
                keyPlaceholder={parameters[0]?.name ?? 'gatewayEndpoint'}
                valuePlaceholder={t('blueprints.preview.valuePlaceholder')}
                datalistId="param-names"
                suggestions={parameters.map((p) => p.name)}
                addLabel={t('blueprints.preview.addOverride')}
              />
            </Stack>
            <Checkbox
              label={t('blueprints.preview.validate')}
              checked={validate}
              onChange={(e) => setValidate(e.currentTarget.checked)}
            />
            <Group>
              <Button
                leftSection={<IconPlayerPlay size={16} />}
                loading={render.isPending}
                disabled={!canRun}
                onClick={run}
              >
                {t('blueprints.preview.render')}
              </Button>
            </Group>
            <ErrorAlert error={render.error} />
          </Stack>
        </SectionCard>
      </Grid.Col>

      <Grid.Col span={{ base: 12, lg: 7 }}>
        <Stack gap="md">
          {!res && !render.isPending && (
            <SectionCard>
              <Text c="dimmed" fz="sm" ta="center" py="xl">
                {t('blueprints.preview.placeholder')}
              </Text>
            </SectionCard>
          )}
          {res && (
            <>
              <SectionCard
                title={t('blueprints.preview.result')}
                actions={
                  res.body ? (
                    <CopyIconButton text={res.body} label={t('blueprints.preview.copyYaml')} />
                  ) : undefined
                }
              >
                <Stack gap="sm" data-testid="render-result">
                  <Group gap="sm" wrap="wrap">
                    {res.hash && (
                      <Group gap={4}>
                        <Text fz="xs" c="dimmed">
                          {t('agentDetail.config.hash')}:
                        </Text>
                        <Text className="myr-mono" fz="xs" data-testid="render-hash">
                          {shortHash(res.hash, 16)}
                        </Text>
                        <CopyIconButton text={res.hash} size="xs" />
                      </Group>
                    )}
                    {res.fleet && (
                      <Badge variant="light" style={{ textTransform: 'none' }}>
                        {t('common.fleet')}: {res.fleet}
                      </Badge>
                    )}
                    {res.layers.map((l, i) => (
                      <Badge
                        key={`${l}-${i}`}
                        variant="outline"
                        color="gray"
                        style={{ textTransform: 'none' }}
                      >
                        {i + 1}. {l}
                      </Badge>
                    ))}
                  </Group>
                  {res.body ? (
                    <YamlView value={res.body} maxHeight="520px" />
                  ) : (
                    <Text c="dimmed" fz="sm">
                      {t('blueprints.preview.noBody')}
                    </Text>
                  )}
                </Stack>
              </SectionCard>
              <SectionCard title={t('blueprints.preview.parameters')}>
                <ParametersTable values={res.parameters} sources={res.parameterSources} />
              </SectionCard>
              <SectionCard title={t('common.issues')}>
                <IssueList issues={res.issues} emptyText={t('blueprints.preview.noIssues')} />
              </SectionCard>
            </>
          )}
        </Stack>
      </Grid.Col>
    </Grid>
  );
}
