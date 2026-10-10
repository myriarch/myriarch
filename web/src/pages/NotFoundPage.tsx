// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Button } from '@mantine/core';
import { IconCompass } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { EmptyState } from '@/components/states';

export default function NotFoundPage() {
  const { t } = useTranslation();
  return (
    <EmptyState
      icon={<IconCompass size={28} />}
      title={t('errors.notFoundTitle')}
      text={t('errors.notFoundHint')}
      action={
        <Button component={Link} to="/" variant="light" size="xs">
          {t('nav.dashboard')}
        </Button>
      }
    />
  );
}
