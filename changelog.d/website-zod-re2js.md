<!-- Entry for CHANGELOG.md, section Unreleased. It is folded into CHANGELOG.md when the next release is prepared. -->

### Security

- **Website: zod 4.6.6 and re2js 2.8.7.** The website lockfile moves the `zod` of `@vercel/cli-config` (under `@vercel/blob` and `@vercel/sandbox` of the Vercel CLI) from 4.1.11 to 4.6.6, past GHSA-mrhx-c7xc-m3w5, and takes `re2js` 2.8.7 instead of the 2.8.6 that markdownlint-cli2 0.24.0 brings (GHSA-gg6h-894w-4m9c), both through overrides. The update of markdownlint-cli2 also removes the advisories GHSA-gfr2-rv44-r23c, GHSA-r7fv-28h4-cvq7 and GHSA-vfgh-jcmw-g37q of `markdown-it`.
