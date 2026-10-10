// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { yaml } from '@codemirror/lang-yaml';
import { defaultHighlightStyle, syntaxHighlighting } from '@codemirror/language';
import { MergeView } from '@codemirror/merge';
import { EditorState } from '@codemirror/state';
import { oneDark } from '@codemirror/theme-one-dark';
import { EditorView, lineNumbers } from '@codemirror/view';
import { useComputedColorScheme } from '@mantine/core';
import { useEffect, useRef } from 'react';

/** Side-by-side diff of two YAML documents (read-only), unchanged regions are collapsed. */
export default function YamlDiffInner({
  a,
  b,
  maxHeight = 560,
}: {
  a: string;
  b: string;
  maxHeight?: number;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const scheme = useComputedColorScheme('light');

  useEffect(() => {
    const parent = ref.current;
    if (!parent) return;
    const common = [
      lineNumbers(),
      yaml(),
      syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
      EditorState.readOnly.of(true),
      EditorView.editable.of(false),
      EditorView.lineWrapping,
      ...(scheme === 'dark' ? [oneDark] : []),
    ];
    const view = new MergeView({
      a: { doc: a, extensions: common },
      b: { doc: b, extensions: common },
      parent,
      highlightChanges: true,
      gutter: true,
      collapseUnchanged: { margin: 3, minSize: 6 },
    });
    return () => view.destroy();
  }, [a, b, scheme]);

  return <div ref={ref} className="myr-cm" style={{ maxHeight, overflow: 'auto' }} />;
}
