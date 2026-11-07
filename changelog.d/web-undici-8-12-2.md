<!-- Entry for CHANGELOG.md, section Unreleased. It is folded into CHANGELOG.md when the next release is prepared. -->

### Security

- **`undici` 8.12.2 in the web UI test tooling.** The lock file of `web/` moves the transitive `undici` (through `jsdom`, used only by the Vitest environment and not shipped) from 8.11.2 to 8.12.2, which fixes GHSA-4w7w-5xrc-9h26, GHSA-2m5v-pch6-74hx, GHSA-qrxm-fqhp-jwvm, GHSA-9fm3-97px-rjf6, GHSA-55wc-3pg8-jfw6, GHSA-xjj6-f33q-929c, GHSA-4qv8-2p9j-2685 and GHSA-f72v-6w45-q472. No other package changes.
