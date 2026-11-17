<!-- Entry for CHANGELOG.md, section Unreleased. It is folded into CHANGELOG.md when the next release is prepared. -->

### Changed

- **Website: Mermaid without the ELK layout.** The website no longer publishes the chunk of Mermaid's ELK layout and its source map (about 6.7 MB), which no page loaded: the diagrams use the dagre layout. The chunk holds the code of `elkjs` (EPL-2.0), so the site now ships none of it and needs no notice for it. `website/scripts/check-notices.py` fails when ELK code reaches the built site, and the dependency review has an exception for `elkjs` with this reason. [ADR-0051](docs/adr/0051-the-website-does-not-ship-the-elk-layout-of-mermaid.md) records the decision and answers the open question of ADR-0038 (decision 5).
