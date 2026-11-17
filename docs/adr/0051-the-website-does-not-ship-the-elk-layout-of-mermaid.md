# 0051. The website does not ship the ELK layout of Mermaid

- Status: Accepted
- Date: 2026-11-17
- Related: [ADR-0030](0030-reuse-compliance-and-third-party-notices.md), [ADR-0031](0031-website-with-hugo-and-docsy.md), [ADR-0038](0038-dependency-license-policy.md)

## Context

The website renders its diagrams with a self-hosted copy of Mermaid: `website/config/_default/module.yaml` publishes `mermaid.esm.min.mjs` and the chunks of `node_modules/mermaid/dist/chunks/mermaid.esm.min/` below `/vendor/mermaid/`. Mermaid's dependency `elkjs`, the JavaScript build of the Eclipse Layout Kernel, sits in one of these chunks, `elk-<hash>.mjs` (about 1.6 MB, with a source map of about 5.1 MB). Mermaid imports that chunk only when a diagram uses the ELK layout. The plugin options in `website/config/_default/params.yaml` pin the dagre layout, because the ELK layout shrinks the overview in `docs/architecture.md` below a readable size, so no page loads the chunk. The site still published it, and with it the code of `elkjs`, which the dependency graph reports as EPL-2.0 for 0.9.3 and as `EPL-2.0 OR GPL-3.0-or-later` for 0.12.1. EPL-2.0 is a weak copyleft license, and the notices page of the website does not name `elkjs`.

Decision 5 of [ADR-0038](0038-dependency-license-policy.md) left open whether the website owes a notice for `elkjs`, and made the dependency review fail when a change moves `elkjs` to another version until the question is answered. The update of the website to Mermaid 12.3.0 is the first such change: it moves `elkjs` from 0.9.3 to 0.12.1.

## Decision

We do not ship the ELK layout. The mount of the Mermaid files leaves out every file of the chunks directory whose name starts with `elk-` (the chunk and its source map), so the website publishes no code of `elkjs` and owes no notice for it. A diagram that asks for the ELK layout does not render on the site; the diagrams use dagre.

`website/scripts/check-notices.py` fails when a script or a source map of the built site contains code of `elkjs`, recognized by the identifiers of the Eclipse Layout Kernel (`org.eclipse.elk`). A Mermaid release that renames, moves or splits the chunk therefore stops the website gate instead of bringing the code back without a notice.

The dependency review gets an exception for `pkg:npm/elkjs`, with this record as the reason. This answers decision 5 of ADR-0038 and supersedes it. The ELK layout comes back only together with a notice (an entry in `website/data/third_party.yaml`, the license text below `/licenses/` and the place of the source) and a new record.

## Consequences

### Positive

- The website ships no copyleft code that no page runs, and no notice has to follow the versions of `elkjs`.
- Every deploy of the website is about 6.7 MB smaller.
- A Mermaid update no longer stops at the dependency review because `elkjs` moved.

### Negative

- The exception covers every version of `elkjs`, because the action ignores the version of a package URL: the dependency review no longer reports a license change of `elkjs`. The guard is `check-notices.py`, which runs on every build of the website gate.
- The guard recognizes the code by a string. The layout options of the Eclipse Layout Kernel carry the prefix `org.eclipse.elk`, and a build of `elkjs` without them would pass unnoticed.
- The published Mermaid files differ from the upstream distribution by one chunk, and the pattern of the mount has to match the name of the chunk; the check reports a mismatch.
- The diagrams of the documentation cannot use the ELK layout.

## Alternatives considered

- **Publish the chunk with a notice**: keeps the file set of the upstream distribution, but ships copyleft code that no page runs and adds a notice that has to follow every version of `elkjs`.
- **Keep Mermaid at 12.2.0**: leaves the question open, and the next Mermaid update that moves `elkjs` stops at the review again.
- **An exception without leaving the chunk out**: the site would ship `elkjs` without a notice.
