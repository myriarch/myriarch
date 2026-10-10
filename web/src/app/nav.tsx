// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import {
  IconArchive,
  IconGitBranch,
  IconHierarchy2,
  IconHistory,
  IconLayoutDashboard,
  IconRocket,
  IconServer2,
  IconSettings,
  IconStack2,
  IconTag,
} from '@tabler/icons-react';
import type { ReactNode } from 'react';
import type { Permission } from '@/api/types';

export interface NavItem {
  to: string;
  labelKey: string;
  icon: ReactNode;
  /** Active when the location starts with one of these prefixes (default: `to`). */
  match?: string[];
  permission?: Permission;
  end?: boolean;
}

export interface NavGroup {
  labelKey: string;
  items: NavItem[];
}

const ic = 18;

export const NAV: NavGroup[] = [
  {
    labelKey: 'nav.groupOverview',
    items: [
      { to: '/', labelKey: 'nav.dashboard', icon: <IconLayoutDashboard size={ic} />, end: true },
    ],
  },
  {
    labelKey: 'nav.groupFleet',
    items: [
      { to: '/agents', labelKey: 'nav.agents', icon: <IconServer2 size={ic} /> },
      {
        to: '/fleets',
        labelKey: 'nav.fleets',
        icon: <IconHierarchy2 size={ic} />,
        match: ['/fleets', '/policies'],
      },
      { to: '/blueprints', labelKey: 'nav.blueprints', icon: <IconStack2 size={ic} /> },
    ],
  },
  {
    labelKey: 'nav.groupDelivery',
    items: [
      { to: '/rollouts', labelKey: 'nav.rollouts', icon: <IconRocket size={ic} /> },
      { to: '/releases', labelKey: 'nav.releases', icon: <IconTag size={ic} /> },
      { to: '/gitops', labelKey: 'nav.gitops', icon: <IconGitBranch size={ic} /> },
      { to: '/artifacts', labelKey: 'nav.artifacts', icon: <IconArchive size={ic} /> },
    ],
  },
  {
    labelKey: 'nav.groupAdmin',
    items: [
      {
        to: '/audit',
        labelKey: 'nav.audit',
        icon: <IconHistory size={ic} />,
        permission: 'audit:read',
      },
      { to: '/settings', labelKey: 'nav.settings', icon: <IconSettings size={ic} /> },
    ],
  },
];

export function isNavActive(item: NavItem, pathname: string): boolean {
  if (item.end) return pathname === item.to;
  const prefixes = item.match ?? [item.to];
  return prefixes.some((p) => pathname === p || pathname.startsWith(`${p}/`));
}
