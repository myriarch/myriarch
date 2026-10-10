// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { ActionIcon, Button, Tooltip } from '@mantine/core';
import { IconCheck, IconCopy } from '@tabler/icons-react';
import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { copyText } from '@/lib/clipboard';

function useCopy(text: string) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );
  const copy = async () => {
    if (await copyText(text)) {
      setCopied(true);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1800);
    }
  };
  return { copied, copy };
}

export function CopyIconButton({
  text,
  label,
  size = 'sm',
}: {
  text: string;
  label?: string;
  size?: 'xs' | 'sm' | 'md';
}) {
  const { t } = useTranslation();
  const { copied, copy } = useCopy(text);
  const tip = copied ? t('common.copied') : (label ?? t('common.copy'));
  return (
    <Tooltip label={tip} withArrow>
      <ActionIcon
        variant="subtle"
        color={copied ? 'teal' : 'gray'}
        size={size}
        onClick={() => void copy()}
        aria-label={tip}
      >
        {copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
      </ActionIcon>
    </Tooltip>
  );
}

export function CopyTextButton({ text, label }: { text: string; label?: string }) {
  const { t } = useTranslation();
  const { copied, copy } = useCopy(text);
  return (
    <Button
      variant="light"
      size="xs"
      color={copied ? 'teal' : 'brand'}
      leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
      onClick={() => void copy()}
    >
      {copied ? t('common.copied') : (label ?? t('common.copy'))}
    </Button>
  );
}
