// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { Skeleton } from '@mantine/core';
import { lazy, Suspense } from 'react';

// CodeMirror is heavy: load it only when a YAML view is actually rendered.
const YamlViewInner = lazy(() => import('./yaml/YamlViewInner'));
const YamlDiffInner = lazy(() => import('./yaml/YamlDiffInner'));

export function YamlView(props: {
  value: string;
  height?: string;
  maxHeight?: string;
  lineNumbers?: boolean;
  language?: 'yaml' | 'plain';
}) {
  return (
    <Suspense fallback={<Skeleton height={props.height ?? 160} />}>
      <YamlViewInner {...props} />
    </Suspense>
  );
}

export function YamlDiff(props: { a: string; b: string; maxHeight?: number }) {
  return (
    <Suspense fallback={<Skeleton height={240} />}>
      <YamlDiffInner {...props} />
    </Suspense>
  );
}
