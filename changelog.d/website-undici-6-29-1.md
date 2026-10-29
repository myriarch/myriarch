<!-- Entry for CHANGELOG.md, section Unreleased. It is folded into CHANGELOG.md when the next release is prepared. -->

### Security

- **Website: undici 6.29.1 and 7.30.1, DOMPurify 3.4.17.** The website lockfile moves `undici` (override for the Vercel CLI and `@vercel/node`, and the copy of `@vercel/sandbox`) past the advisories GHSA-2m5v-pch6-74hx, GHSA-qrxm-fqhp-jwvm, GHSA-9fm3-97px-rjf6, GHSA-xjj6-f33q-929c, GHSA-4qv8-2p9j-2685, GHSA-4w7w-5xrc-9h26, GHSA-55wc-3pg8-jfw6 and GHSA-f72v-6w45-q472, and `dompurify` (a dependency of Mermaid in the lockfile) to the release that fixes GHSA-cg2g-xh64-v4q7, GHSA-mrv2-3pqr-j5jr and GHSA-c6gr-qvv4-5v3f.
