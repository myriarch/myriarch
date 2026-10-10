# 0020. English as the language of documentation and project communication

- Status: Accepted
- Date: 2026-10-08
- Related: [ADR-0021](0021-publication-gates-and-v0-1-0-scope.md), [ADR-0001](0001-single-server-binary.md) to [ADR-0014](0014-binary-updates-in-the-pod-via-container-mode.md)

## Context

The user documentation under `docs/` is currently written in German. Code comments, log messages and commit messages are being moved to English, and the script `scripts/check-language.sh` guards against regressions. The web UI ships two locales, `web/src/i18n/en.json` and `web/src/i18n/de.json`, and currently defaults and falls back to German.

Reviews by foundation bodies, the OpenSSF Best Practices criteria and international contributors all expect English. Maintaining documentation in two languages doubles the effort and invites drift between the versions.

## Decision

- English is the only language for code, comments, log messages, commit messages, documentation, ADRs, the website, issues and discussions.
- The UI keeps German as a translation. `en.json` is the source of truth, `de.json` becomes the first community-maintained locale, and a missing key falls back to English. A later change makes English the default and fallback language, which differs from the current behavior.
- Transitional rule: until the English core documentation is complete, the existing German core documentation (neutralized, see [ADR-0021](0021-publication-gates-and-v0-1-0-scope.md)) may be public during the soft launch. English core documentation is required before the public announcement. After that, German documents are removed or clearly marked as unmaintained.
- The existing ADR-0001 to ADR-0014 (German, in the architecture document) are migrated to English ADR files with their original numbers ([ADR-0001](0001-single-server-binary.md) to [ADR-0014](0014-binary-updates-in-the-pod-via-container-mode.md)).

## Consequences

### Positive

- One language to maintain.
- Accessible to the widest contributor base and ready for foundation-style reviews.

### Negative

- Translation effort for the existing documentation.
- Non-English locales depend on community maintenance and may lag behind.
- German-speaking users lose native documentation.

## Alternatives considered

- **English plus German getting-started pages**: rejected, it still doubles part of the maintenance and the pages drift.
- **Fully bilingual documentation**: rejected, double maintenance and drift between the languages.
