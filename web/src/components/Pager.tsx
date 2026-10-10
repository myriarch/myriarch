// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Group, Pagination, Select, Text } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { useLang } from '@/i18n/useLang';
import { formatNumber } from '@/lib/format';

const PAGE_SIZES = [50, 100, 200] as const;

export interface PageState {
  limit: number;
  offset: number;
}

/** Offset/limit pager for server-side paginated tables. */
export function Pager({
  total,
  limit,
  offset,
  onChange,
  pageSizes = PAGE_SIZES,
  loading,
}: PageState & {
  total: number;
  onChange: (next: PageState) => void;
  pageSizes?: readonly number[];
  loading?: boolean;
}) {
  const { t } = useTranslation();
  const lang = useLang();
  const pages = Math.max(1, Math.ceil(total / limit));
  const page = Math.min(pages, Math.floor(offset / limit) + 1);
  const from = total === 0 ? 0 : offset + 1;
  const to = Math.min(total, offset + limit);

  return (
    <Group justify="space-between" px="md" py="sm" wrap="wrap" gap="sm" aria-busy={loading}>
      <Text fz="sm" c="dimmed" className="myr-tabular">
        {total === 0
          ? t('pager.empty')
          : t('pager.range', {
              from: formatNumber(lang, from),
              to: formatNumber(lang, to),
              total: formatNumber(lang, total),
            })}
      </Text>
      <Group gap="sm" wrap="wrap">
        <Select
          size="xs"
          w={118}
          aria-label={t('pager.pageSize')}
          data={pageSizes.map((n) => ({
            value: String(n),
            label: t('pager.perPage', { count: n }),
          }))}
          value={String(limit)}
          onChange={(v) => v && onChange({ limit: Number(v), offset: 0 })}
          allowDeselect={false}
        />
        {pages > 1 && (
          <Pagination
            size="sm"
            total={pages}
            value={page}
            onChange={(p) => onChange({ limit, offset: (p - 1) * limit })}
            siblings={1}
            boundaries={1}
            withEdges
            getControlProps={(control) => ({ 'aria-label': t(`pager.${control}`) })}
            getItemProps={(p) => ({ 'aria-label': t('pager.goToPage', { page: p }) })}
          />
        )}
      </Group>
    </Group>
  );
}
