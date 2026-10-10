// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Rasterize SVG files to PNG with resvg-js.
//
// Usage:
//   node render.mjs <in.svg> <out.png> [--width N | --height N | --zoom F]
//                   [--background CSSCOLOR] [--font file.ttf ...]
//
// resvg ignores CSS media queries, so prefers-color-scheme rules never apply:
// a favicon always renders in its light (default) colour here.
import { readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join } from "node:path";

// make_assets.sh installs the packages into BRAND_NODE_DIR.
const nodeDir = process.env.BRAND_NODE_DIR;
if (!nodeDir) {
  console.error("render.mjs: BRAND_NODE_DIR is not set; run make_assets.sh");
  process.exit(2);
}
const require = createRequire(join(nodeDir, "package.json"));
const { Resvg } = require("@resvg/resvg-js");

const args = process.argv.slice(2);
if (args.length < 2) {
  console.error("usage: render.mjs <in.svg> <out.png> [--width N|--height N|--zoom F] [--background C] [--font F]");
  process.exit(2);
}
const [input, output] = args;
const opts = { font: { loadSystemFonts: false, fontFiles: [] } };
for (let i = 2; i < args.length; i++) {
  const a = args[i];
  if (a === "--width") opts.fitTo = { mode: "width", value: Number(args[++i]) };
  else if (a === "--height") opts.fitTo = { mode: "height", value: Number(args[++i]) };
  else if (a === "--zoom") opts.fitTo = { mode: "zoom", value: Number(args[++i]) };
  else if (a === "--background") opts.background = args[++i];
  else if (a === "--font") opts.font.fontFiles.push(args[++i]);
  else throw new Error(`unknown argument ${a}`);
}
if (opts.font.fontFiles.length) opts.font.defaultFontFamily = "Inter";

const svg = readFileSync(input, "utf8");
const resvg = new Resvg(svg, opts);
const img = resvg.render();
writeFileSync(output, img.asPng());
console.log(`${output} ${img.width}x${img.height}`);
