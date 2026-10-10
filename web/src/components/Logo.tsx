// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Group, Text } from '@mantine/core';
import { NAME } from '@/brand';

// The tetractys mark from assets/brand/myriarch-icon.svg (114 × 104). The fill comes from
// --app-logo-mark (theme.ts): Tyrian on light surfaces, Orchid on dark ones, as in the brand guide.
const MARK_PATH =
  'M30 40a12 12 0 1 1 24 0 12 12 0 1 1-24 0m30 0a12 12 0 1 1 24 0 12 12 0 1 1-24 0M15 66a12 12 0 1 1 24 0 12 12 0 1 1-24 0m30 0a12 12 0 1 1 24 0 12 12 0 1 1-24 0m30 0a12 12 0 1 1 24 0 12 12 0 1 1-24 0M0 92a12 12 0 1 1 24 0 12 12 0 1 1-24 0m30 0a12 12 0 1 1 24 0 12 12 0 1 1-24 0m30 0a12 12 0 1 1 24 0 12 12 0 1 1-24 0m30 0a12 12 0 1 1 24 0 12 12 0 1 1-24 0M44 13a13 13 0 1 1 26 0 13 13 0 1 1-26 0m7.5 0a5.5 5.5 0 1 0 11 0 5.5 5.5 0 1 0-11 0';

/** The logo mark; `size` is its height in pixels (the brand guide's minimum is 24). */
export function LogoMark({ size = 30 }: { size?: number }) {
  return (
    <svg
      width={Math.round((size * 114) / 104)}
      height={size}
      viewBox="0 0 114 104"
      aria-hidden
      focusable="false"
    >
      <path d={MARK_PATH} style={{ fill: 'var(--app-logo-mark)' }} />
    </svg>
  );
}

export function Logo() {
  return (
    <Group gap={8} wrap="nowrap">
      <LogoMark />
      <Text fw={700} fz="lg" style={{ letterSpacing: '-0.01em' }}>
        {NAME}
      </Text>
    </Group>
  );
}
