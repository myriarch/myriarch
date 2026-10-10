// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Button, Center, Code, Stack, Text, ThemeIcon } from '@mantine/core';
import { IconBug } from '@tabler/icons-react';
import i18n from '@/i18n';

interface State {
  error: Error | null;
}

/** Catches render errors of a page so that the navigation stays usable. */
export class ErrorBoundary extends Component<{ children: ReactNode; resetKey?: string }, State> {
  override state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  override componentDidCatch(error: Error, info: ErrorInfo): void {
    console.error('UI render error', error, info.componentStack);
  }

  override componentDidUpdate(prev: { resetKey?: string }): void {
    if (this.state.error && prev.resetKey !== this.props.resetKey) this.setState({ error: null });
  }

  override render(): ReactNode {
    if (!this.state.error) return this.props.children;
    return (
      <Center py={64} role="alert">
        <Stack align="center" gap="xs" maw={560} ta="center">
          <ThemeIcon size={56} radius="xl" color="red" variant="light">
            <IconBug size={28} />
          </ThemeIcon>
          <Text fw={600}>{i18n.t('errors.pageCrashed')}</Text>
          <Text c="dimmed" fz="sm">
            {i18n.t('errors.pageCrashedHint')}
          </Text>
          <Code block>{this.state.error.message}</Code>
          <Button variant="light" onClick={() => window.location.reload()}>
            {i18n.t('common.refresh')}
          </Button>
        </Stack>
      </Center>
    );
  }
}
