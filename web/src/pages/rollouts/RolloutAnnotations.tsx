// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Badge, Button, Code, Collapse, Group, Stack, Text } from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { useTranslation } from 'react-i18next';
import { splitAnnotations } from '@/lib/updates';

/**
 * Engine annotations of a rollout. Only executorRef, distribution and updateParams get their own
 * row (objects as `k=v` badges, never "[object Object]"); internal keys are folded into the
 * collapsible "technical details" as formatted JSON. The selection is shown in the page header.
 */
export function RolloutAnnotations({
  annotations,
}: {
  annotations: Record<string, unknown> | undefined;
}) {
  const { t } = useTranslation();
  const [open, { toggle }] = useDisclosure(false);
  const { visible, technical } = splitAnnotations(annotations);
  const hasTechnical = Object.keys(technical).length > 0;
  if (visible.length === 0 && !hasTechnical) return null;
  return (
    <Stack gap={6} mt="sm" data-testid="rollout-annotations">
      {visible.map((a) => (
        <Group key={a.key} gap={6} wrap="wrap" align="center">
          <Text fz="xs" c="dimmed">
            {t(`rollouts.annotation.${a.key}`)}:
          </Text>
          {a.items.map((item, i) => (
            <Badge
              key={`${item}-${i}`}
              variant="outline"
              color="gray"
              style={{ textTransform: 'none' }}
            >
              {item}
            </Badge>
          ))}
        </Group>
      ))}
      {hasTechnical && (
        <>
          <Button
            size="compact-xs"
            variant="subtle"
            color="gray"
            onClick={toggle}
            aria-expanded={open}
            style={{ alignSelf: 'flex-start' }}
          >
            {t('rollouts.technicalDetails')}
          </Button>
          <Collapse expanded={open}>
            <Code block fz="xs" data-testid="technical-details">
              {JSON.stringify(technical, null, 2)}
            </Code>
          </Collapse>
        </>
      )}
    </Stack>
  );
}
