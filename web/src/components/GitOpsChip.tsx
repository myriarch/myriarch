// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Badge, Tooltip } from '@mantine/core';
import { IconGitBranch } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import type { GitOpsBrief } from '@/api/types';

const color = { ok: 'teal', error: 'red', syncing: 'blue', never: 'gray' } as const;

/** GitOps status chip (state, applied commit, issue count) linking to the GitOps page. */
export function GitOpsChip({ gitops }: { gitops: GitOpsBrief }) {
  const { t } = useTranslation();
  const warn = gitops.state === 'ok' && gitops.issueCount > 0;
  const c = warn ? 'yellow' : color[gitops.state];
  return (
    <Tooltip
      label={
        gitops.appliedCommit
          ? t('gitops.chipTooltip', {
              commit: gitops.appliedCommit.slice(0, 8),
              count: gitops.issueCount,
            })
          : t('enum.gitopsState.never')
      }
    >
      <Badge
        component={Link}
        to="/gitops"
        color={c}
        size="lg"
        variant="light"
        leftSection={<IconGitBranch size={14} />}
        style={{ cursor: 'pointer', textTransform: 'none' }}
        data-testid="gitops-chip"
      >
        {t(`enum.gitopsState.${gitops.state}`)}
        {gitops.appliedCommit ? ` · ${gitops.appliedCommit.slice(0, 7)}` : ''}
        {gitops.issueCount > 0 ? ` · ${t('gitops.issuesCount', { count: gitops.issueCount })}` : ''}
      </Badge>
    </Tooltip>
  );
}
