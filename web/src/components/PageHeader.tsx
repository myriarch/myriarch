// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Anchor, Breadcrumbs, Group, Stack, Text, Title } from '@mantine/core';
import { Link } from 'react-router';
import type { ReactNode } from 'react';
import { useEffect } from 'react';
import { NAME } from '@/brand';

export interface Crumb {
  label: string;
  to?: string;
}

/** Page title block with optional breadcrumbs, subtitle and a right aligned actions slot. */
export function PageHeader({
  title,
  subtitle,
  actions,
  breadcrumbs,
  badge,
  documentTitle,
}: {
  title: ReactNode;
  subtitle?: ReactNode;
  actions?: ReactNode;
  breadcrumbs?: Crumb[];
  badge?: ReactNode;
  documentTitle?: string;
}) {
  const docTitle = documentTitle ?? (typeof title === 'string' ? title : undefined);
  useEffect(() => {
    if (!docTitle) return;
    const prev = document.title;
    document.title = `${docTitle} · ${NAME}`;
    return () => {
      document.title = prev;
    };
  }, [docTitle]);

  return (
    <Stack gap={4} mb="lg">
      {breadcrumbs && breadcrumbs.length > 0 && (
        <Breadcrumbs separatorMargin={6}>
          {breadcrumbs.map((c, i) =>
            c.to ? (
              <Anchor key={i} component={Link} to={c.to} fz="sm" c="dimmed">
                {c.label}
              </Anchor>
            ) : (
              <Text key={i} fz="sm" c="dimmed">
                {c.label}
              </Text>
            ),
          )}
        </Breadcrumbs>
      )}
      <Group justify="space-between" align="flex-start" wrap="wrap" gap="md">
        <Stack gap={2} style={{ minWidth: 0, flex: '1 1 320px' }}>
          <Group gap="sm" align="center" wrap="wrap">
            <Title order={1} style={{ wordBreak: 'break-word' }}>
              {title}
            </Title>
            {badge}
          </Group>
          {subtitle && (
            <Text c="dimmed" fz="sm">
              {subtitle}
            </Text>
          )}
        </Stack>
        {actions && (
          <Group gap="xs" wrap="wrap">
            {actions}
          </Group>
        )}
      </Group>
    </Stack>
  );
}
