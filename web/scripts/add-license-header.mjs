// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Prepends the two-line license header to generated files (used by `pnpm gen:api`).
// Idempotent: a file that already starts with the header is left unchanged.
import { readFileSync, writeFileSync } from 'node:fs';

// REUSE-IgnoreStart
const header = '// Copyright The Myriarch Authors\n// SPDX-License-Identifier: Apache-2.0\n\n';
// REUSE-IgnoreEnd

const files = process.argv.slice(2);
if (files.length === 0) {
  console.error('usage: node scripts/add-license-header.mjs FILE...');
  process.exit(2);
}
for (const file of files) {
  const text = readFileSync(file, 'utf8');
  if (!text.startsWith(header)) {
    writeFileSync(file, header + text);
  }
}
