<!-- Entry for CHANGELOG.md, section Unreleased. It is folded into CHANGELOG.md when the next release is prepared. -->

### Security

- **`golang.org/x/net` 0.62.0.** The main module moves `golang.org/x/net` from 0.61.0 to 0.62.0, which fixes GO-2026-6860 and GO-2026-6866 reported by `govulncheck` at module level (the code does not call the affected functions). No other module changes.
