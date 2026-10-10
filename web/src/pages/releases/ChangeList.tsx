// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Accordion, Anchor, Badge, Group, Stack, Text, TextInput } from '@mantine/core';
import { IconSearch } from '@tabler/icons-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { ChangelogEntry } from '@/api/types';
import { EmptyState } from '@/components/states';
import { groupByComponent } from './changeGroups';

const ISSUE_BASE: Record<string, string | undefined> = {
  core: 'https://github.com/open-telemetry/opentelemetry-collector/issues/',
  contrib: 'https://github.com/open-telemetry/opentelemetry-collector-contrib/issues/',
};

export function ChangeList({
  entries,
  emptyTitle,
  showVersion = false,
}: {
  entries: ChangelogEntry[];
  emptyTitle: string;
  showVersion?: boolean;
}) {
  const { t } = useTranslation();
  const [q, setQ] = useState('');
  const groups = useMemo(() => groupByComponent(entries, q), [entries, q]);

  if (entries.length === 0) return <EmptyState compact title={emptyTitle} />;
  return (
    <Stack gap="sm">
      <Group justify="space-between">
        <TextInput
          size="xs"
          w={{ base: '100%', sm: 320 }}
          leftSection={<IconSearch size={14} />}
          placeholder={t('releases.searchChanges')}
          aria-label={t('releases.searchChanges')}
          value={q}
          onChange={(e) => setQ(e.currentTarget.value)}
        />
        <Text fz="xs" c="dimmed">
          {t('releases.changeCount', {
            count: groups.reduce((s, g) => s + g.entries.length, 0),
            components: groups.length,
          })}
        </Text>
      </Group>
      {groups.length === 0 ? (
        <EmptyState compact title={t('releases.noChangeMatch')} />
      ) : (
        <Accordion
          multiple
          variant="separated"
          radius="md"
          defaultValue={
            groups.length <= 6 || q ? groups.map((g) => g.component || '__general') : []
          }
        >
          {groups.map((g) => {
            const key = g.component || '__general';
            return (
              <Accordion.Item key={key} value={key}>
                <Accordion.Control>
                  <Group gap={8}>
                    <Text fw={600} className={g.component ? 'myr-mono' : undefined}>
                      {g.component || t('releases.general')}
                    </Text>
                    <Badge size="sm" variant="light" color="gray">
                      {g.entries.length}
                    </Badge>
                  </Group>
                </Accordion.Control>
                <Accordion.Panel>
                  <Stack gap="sm">
                    {g.entries.map((e, i) => (
                      <Stack
                        key={i}
                        gap={3}
                        style={{
                          borderLeft: '3px solid var(--mantine-color-orange-4)',
                          paddingLeft: 10,
                        }}
                      >
                        <Text fz="sm" style={{ wordBreak: 'break-word', whiteSpace: 'pre-wrap' }}>
                          {e.text}
                        </Text>
                        <Group gap={6} wrap="wrap">
                          {showVersion && (
                            <Badge size="xs" variant="outline" color="gray" className="myr-mono">
                              {e.version}
                            </Badge>
                          )}
                          <Badge size="xs" variant="light" color="gray">
                            {e.source}
                          </Badge>
                          {(e.issues ?? []).map((n) => {
                            const base = ISSUE_BASE[e.source];
                            return base ? (
                              <Anchor
                                key={n}
                                href={`${base}${n}`}
                                target="_blank"
                                rel="noopener noreferrer"
                                fz="xs"
                              >
                                #{n}
                              </Anchor>
                            ) : (
                              <Text key={n} span fz="xs" c="dimmed">
                                #{n}
                              </Text>
                            );
                          })}
                        </Group>
                      </Stack>
                    ))}
                  </Stack>
                </Accordion.Panel>
              </Accordion.Item>
            );
          })}
        </Accordion>
      )}
    </Stack>
  );
}
