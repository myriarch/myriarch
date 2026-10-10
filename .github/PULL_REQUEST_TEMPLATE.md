## Summary

<!-- What does this change do, and why? Keep it short. -->

## Linked issue

<!-- Fixes #123, or "none" for small fixes. Larger changes should start with an issue. -->

## How was it tested?

<!-- Commands you ran, scenarios you tried. -->

## Checklist

- [ ] Every commit is signed off (`git commit -s`) by me with my real name (Developer Certificate of Origin, see CONTRIBUTING.md).
- [ ] `make check-local` passes (or I explain below which gates I could not run).
- [ ] Tests cover the new or changed behavior.
- [ ] Documentation is updated (`docs/`, including `docs/reference/gitops-schema.md` for GitOps format changes), and `CHANGELOG.md` has an entry for user-visible changes.
- [ ] Generated files are regenerated (`make docs-gen` for `docs/reference/cli`, `docs/reference/api` and the configuration, environment variable and metrics references, `pnpm gen:api` for `web/src/api/schema.d.ts`).
- [ ] No secrets, tokens or environment-specific data are included.
- [ ] If AI tools helped substantially, I disclose it below. I reviewed and understand every line of this change.

## AI assistance

<!-- "None", or which tool helped with what. Optional commit trailer: Assisted-by: <tool> -->

## Notes for reviewers

<!-- Anything that needs attention: trade-offs, follow-ups, gates you could not run. -->
