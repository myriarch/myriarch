<!-- Entry for CHANGELOG.md, section Unreleased. It is folded into CHANGELOG.md when the next release is prepared. -->

### Upgrade notes

- **The collector chart asks for more memory by default.** `resources.limits.memory` is now `1Gi` (was `512Mi`) and `resources.requests.memory` is `512Mi` (was `256Mi`). With the old limit the main container was OOM-killed in a chart installation test and did not become Ready. An installation that sets its own `resources` keeps them; if you set a limit below `768Mi`, expect the same OOM kills and raise it. The `memory_limiter` of the blueprints takes its limit as a percentage of the container limit, so it follows the new value without a change in Git. Check that the nodes have room for the higher requests before you upgrade a DaemonSet.

### Fixed

- **The collector chart installs with its default values.** The default memory limit of the main container was too low for the supervisor and the contrib collector together.
