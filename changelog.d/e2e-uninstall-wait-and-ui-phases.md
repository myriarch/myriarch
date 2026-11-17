<!-- Entry for CHANGELOG.md, section Unreleased. It is folded into CHANGELOG.md when the next release is prepared. -->

### Fixed

- **The nightly browser test of updates from the UI passes again.** The check of the job `ui-updates` (workflow `e2e`) asked the rollout page for the label of the host's health-check phase. That phase lasts only until the collector reports the new version, and the page often refreshes after that moment, so the check failed in many runs. It now reads the phase by its key from the step bar (a new `data-phase` attribute, independent of the UI language) and checks what the page shows reliably: a phase of the running update before the end, and the host's health check as a running step before it failed. This closes the known issue of v0.2.0 and v0.2.1.
