// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Button, Group, Modal, Stack, Text, Textarea } from '@mantine/core';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { ErrorAlert } from './states';

export interface ConfirmDialogProps {
  opened: boolean;
  onClose: () => void;
  onConfirm: (reason?: string) => void;
  title: ReactNode;
  children?: ReactNode;
  confirmLabel?: string;
  color?: string;
  loading?: boolean;
  error?: unknown;
  reason?: { required?: boolean; label?: string };
}

/**
 * Confirmation modal. With `reason` an (optional or required) justification text is collected,
 * which rollout actions send to the API and the audit log.
 */
export function ConfirmDialog(props: ConfirmDialogProps) {
  return (
    <Modal
      opened={props.opened}
      onClose={props.onClose}
      title={props.title}
      size="md"
      trapFocus
      returnFocus
    >
      {/* The body mounts per open, so the reason text starts empty every time. */}
      <ConfirmBody {...props} />
    </Modal>
  );
}

function ConfirmBody({
  onClose,
  onConfirm,
  children,
  confirmLabel,
  color = 'brand',
  loading,
  error,
  reason,
}: ConfirmDialogProps) {
  const { t } = useTranslation();
  const [text, setText] = useState('');
  const invalid = !!reason?.required && text.trim() === '';

  return (
    <Stack gap="md">
      {children && (typeof children === 'string' ? <Text fz="sm">{children}</Text> : children)}
      {reason && (
        <Textarea
          label={reason.label ?? t('common.reason')}
          description={reason.required ? undefined : t('common.optional')}
          value={text}
          onChange={(e) => setText(e.currentTarget.value)}
          maxLength={500}
          autosize
          minRows={2}
          maxRows={6}
          data-autofocus
        />
      )}
      <ErrorAlert error={error} />
      <Group justify="flex-end" gap="xs">
        <Button variant="default" onClick={onClose} disabled={loading}>
          {t('common.cancel')}
        </Button>
        <Button
          color={color}
          loading={loading}
          disabled={invalid}
          onClick={() => onConfirm(reason ? text.trim() || undefined : undefined)}
        >
          {confirmLabel ?? t('common.confirm')}
        </Button>
      </Group>
    </Stack>
  );
}
