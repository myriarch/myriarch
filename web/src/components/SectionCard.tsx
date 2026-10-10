// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Card, Group, Stack, Text, Title } from '@mantine/core';
import type { ReactNode } from 'react';

/** Card with a title row (title, optional description and right aligned actions). */
export function SectionCard({
  title,
  description,
  actions,
  children,
  padded = true,
  h,
  id,
}: {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  /** false: content spans the full card width (tables). */
  padded?: boolean;
  h?: string | number;
  id?: string;
}) {
  return (
    <Card padding={0} h={h} id={id}>
      {(title || actions) && (
        <Group
          justify="space-between"
          align="flex-start"
          px="md"
          pt="md"
          pb={padded ? 'xs' : 'sm'}
          wrap="nowrap"
          gap="sm"
        >
          <Stack gap={0} style={{ minWidth: 0 }}>
            {title && (
              <Title order={2} fz="md">
                {title}
              </Title>
            )}
            {description && (
              <Text fz="xs" c="dimmed">
                {description}
              </Text>
            )}
          </Stack>
          {actions && (
            <Group gap="xs" wrap="nowrap">
              {actions}
            </Group>
          )}
        </Group>
      )}
      <div
        style={
          padded
            ? { padding: '4px var(--mantine-spacing-md) var(--mantine-spacing-md)' }
            : undefined
        }
      >
        {children}
      </div>
    </Card>
  );
}
