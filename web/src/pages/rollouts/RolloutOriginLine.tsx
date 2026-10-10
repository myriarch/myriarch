// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Anchor, Group, Text } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import type { Rollout } from '@/api/types';
import { RolloutOriginBadge } from '@/components/VersionSourceBadge';
import { DateTime, TimeAgo } from '@/components/TimeAgo';
import { selectionAnnotation } from '@/lib/updates';
import { fleetLink } from '@/lib/links';

/**
 * Who started the rollout and why: "per UI gesetzt" with operator, time and reason for UI
 * rollouts, "aus Git" with the commit for GitOps rollouts.
 */
export function RolloutOriginLine({ rollout: r }: { rollout: Rollout }) {
  const { t } = useTranslation();
  const sel = selectionAnnotation(r.annotations);
  const reason = r.requestReason ?? sel.reason;
  return (
    <Group gap={8} wrap="wrap" mb="md" data-testid="rollout-origin-line">
      <RolloutOriginBadge origin={r.origin} size="md" />
      {r.origin === 'ui' && (
        <Text fz="sm">
          {t('versionUpdate.origin.by', { by: sel.selectedBy ?? r.createdBy })}{' '}
          <TimeAgo value={sel.selectedAt ?? r.createdAt} />
          {' · '}
          <DateTime value={sel.selectedAt ?? r.createdAt} />
          {reason ? (
            <>
              {' · '}
              {t('versionUpdate.section.reason')}: {reason}
            </>
          ) : null}
        </Text>
      )}
      {r.origin === 'gitops' && (
        <Text fz="sm" c="dimmed">
          {r.gitCommit
            ? t('versionUpdate.origin.gitCommit', { commit: r.gitCommit.slice(0, 8) })
            : t('versionUpdate.origin.git')}
        </Text>
      )}
      {r.origin === 'system' && (
        <Text fz="sm" c="dimmed">
          {t('versionUpdate.origin.system')}
        </Text>
      )}
      {r.origin === 'ui' && (
        <Anchor component={Link} to={fleetLink(r.fleetName)} fz="xs">
          {t('versionUpdate.origin.toFleet')}
        </Anchor>
      )}
    </Group>
  );
}
