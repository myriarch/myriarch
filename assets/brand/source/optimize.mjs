// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Optimize SVGs with svgo (preset-default) while keeping what a logo file
// needs: <title>, viewBox, width/height, role and aria-label, and the
// prefers-color-scheme rule in favicon.svg.
//
// Usage: node optimize.mjs <in-dir> <out-dir> <file.svg>...
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

// make_assets.sh installs the packages into BRAND_NODE_DIR. svgo is ESM-first, so load its ESM entry
// from there by path.
const nodeDir = process.env.BRAND_NODE_DIR;
if (!nodeDir) {
  console.error("optimize.mjs: BRAND_NODE_DIR is not set; run make_assets.sh");
  process.exit(2);
}
const svgoDir = join(nodeDir, "node_modules", "svgo");
const { optimize } = await import(pathToFileURL(join(svgoDir, "lib", "svgo-node.js")).href);

const [inDir, outDir, ...files] = process.argv.slice(2);
if (!inDir || !outDir || files.length === 0) {
  console.error("usage: optimize.mjs <in-dir> <out-dir> <file.svg>...");
  process.exit(2);
}
mkdirSync(outDir, { recursive: true });

const config = {
  multipass: true,
  floatPrecision: 2,
  js2svg: { pretty: false, finalNewline: true, eol: "lf" },
  plugins: [
    {
      name: "preset-default",
      params: {
        overrides: {
          removeUnknownsAndDefaults: { keepRoleAttr: true, keepAriaAttrs: true, defaultAttrs: false },
        },
      },
    },
  ],
};

for (const f of files) {
  const src = readFileSync(join(inDir, f), "utf8");
  const { data } = optimize(src, { ...config, path: f });
  writeFileSync(join(outDir, f), data);
  console.log(`${f}\t${Buffer.byteLength(src)} -> ${Buffer.byteLength(data)} bytes`);
}
