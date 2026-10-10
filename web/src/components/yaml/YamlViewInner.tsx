// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

import { yaml } from '@codemirror/lang-yaml';
import { EditorState } from '@codemirror/state';
import { oneDark } from '@codemirror/theme-one-dark';
import { EditorView } from '@codemirror/view';
import { useComputedColorScheme } from '@mantine/core';
import CodeMirror from '@uiw/react-codemirror';
import { useMemo } from 'react';

export default function YamlViewInner({
  value,
  height,
  maxHeight = '560px',
  lineNumbers = true,
  language = 'yaml',
}: {
  value: string;
  height?: string;
  maxHeight?: string;
  lineNumbers?: boolean;
  language?: 'yaml' | 'plain';
}) {
  const scheme = useComputedColorScheme('light');
  const extensions = useMemo(
    () => [
      ...(language === 'yaml' ? [yaml()] : []),
      EditorState.readOnly.of(true),
      EditorView.editable.of(false),
      EditorView.lineWrapping,
      EditorView.contentAttributes.of({ 'aria-label': 'YAML', 'aria-readonly': 'true' }),
      ...(scheme === 'dark' ? [oneDark] : []),
    ],
    [language, scheme],
  );
  return (
    <div className="myr-cm">
      <CodeMirror
        value={value}
        height={height}
        maxHeight={maxHeight}
        extensions={extensions}
        theme={scheme === 'dark' ? 'dark' : 'light'}
        basicSetup={{
          lineNumbers,
          foldGutter: true,
          highlightActiveLine: false,
          highlightActiveLineGutter: false,
          autocompletion: false,
          searchKeymap: true,
        }}
      />
    </div>
  );
}
