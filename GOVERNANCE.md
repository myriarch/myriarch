# Myriarch project governance

Myriarch is a GitOps-governed fleet manager for OpenTelemetry Collectors. This document explains how the project is run.
It is adapted from the maintainer-model template of the CNCF project template
([GOVERNANCE-maintainer.md](https://github.com/cncf/project-template/blob/main/GOVERNANCE-maintainer.md)) and is meant to
stay compatible with foundation governance requirements
([ADR-0028](docs/adr/0028-relationship-to-opentelemetry-and-foundation-path.md)). The template is licensed under Apache-2.0;
this document was adapted and changed by The Myriarch Authors and is licensed under Apache-2.0, see [LICENSE](LICENSE).

- [Current state](#current-state)
- [Values](#values)
- [Maintainers](#maintainers)
- [Becoming a maintainer](#becoming-a-maintainer)
- [Stepping down and removal](#stepping-down-and-removal)
- [Decision making](#decision-making)
- [Architecture decisions](#architecture-decisions)
- [Code of conduct](#code-of-conduct)
- [Security response](#security-response)
- [Foundations](#foundations)
- [Changes to this document](#changes-to-this-document)

## Current state

The project starts with **a single maintainer** (see [MAINTAINERS.md](MAINTAINERS.md)). That is a risk for a project that
wants to be long-lived, and we say so openly. The goal is to grow a group of at least three maintainers from at least two
organizations, to publish adopters in [ADOPTERS.md](ADOPTERS.md), and to keep the governance ready for a foundation
([ADR-0028](docs/adr/0028-relationship-to-opentelemetry-and-foundation-path.md)). Until then, the rules below apply with one
simplification: where they ask for a vote of the maintainers, the sole maintainer decides, but follows the same open process
(public discussion, a recorded reason, a decision record where required).

The project is hosted in the GitHub organization `myriarch`
([ADR-0029](docs/adr/0029-host-under-the-myriarch-organization.md)).

## Values

Myriarch and its leadership embrace the following values:

- **Openness:** communication and decision-making happen in the open and are discoverable for future reference. As much as
  possible, all discussions and work take place in public forums and open repositories.
- **Fairness:** all stakeholders have the opportunity to provide feedback and submit contributions, which are considered on
  their merits.
- **Community over product or company:** sustaining and growing the community takes priority over shipping code or the
  organizational goals of sponsors. Each contributor participates in the project as an individual.
- **Vendor neutrality:** the direction and decisions of the project are not controlled by any single organization.
  Maintainer selection, roadmap prioritization and release decisions are made on project merit, not employer affiliation.
  The software carries no references to a specific operator
  ([ADR-0022](docs/adr/0022-white-label-and-neutral-examples.md)).
- **Inclusivity:** we innovate through different perspectives and skill sets, which can only be accomplished in a welcoming and
  respectful environment.
- **Participation:** responsibilities within the project are earned through participation, and there is a clear path up the
  contributor ladder ([CONTRIBUTOR_LADDER.md](CONTRIBUTOR_LADDER.md)).

## Maintainers

Maintainers have write access to the [project repository](https://github.com/myriarch/myriarch). They can merge their own
patches and the patches of others. The current maintainers are listed in [MAINTAINERS.md](MAINTAINERS.md), together with their
GitHub ID, their organization and their areas. Maintainers collectively manage the project's resources and contributors; the
collective team of all maintainers is the governing body of the project.

This privilege comes with responsibility. A maintainer is not just someone who can make changes, but someone who has
demonstrated the ability to collaborate with others, to get the most knowledgeable people to review code and documentation,
to contribute high-quality work and to follow through on issues. Maintainers

- review pull requests and triage issues,
- keep the project releasable and the gates green,
- uphold the [Code of Conduct](CODE_OF_CONDUCT.md) and the
  [DCO requirement](docs/adr/0019-developer-certificate-of-origin.md),
- respond to security reports ([SECURITY.md](SECURITY.md)),
- decide, in the open, about direction, scope and releases.

Maintainers sign off their own commits like everyone else.

## Becoming a maintainer

The requirements are in [CONTRIBUTOR_LADDER.md](CONTRIBUTOR_LADDER.md). In short, a candidate has contributed over a
sustained period, has reviewed and merged non-trivial work, understands the code base and the processes, and can exercise
judgment for the good of the project independent of an employer.

A new maintainer is proposed by an existing maintainer in a public pull request that adds the candidate to
[MAINTAINERS.md](MAINTAINERS.md). The candidate confirms in the pull request that they accept the responsibilities. A simple
majority of all existing maintainers approves. Nominations are evaluated without prejudice to employer or demographics and
consider the organizational diversity of the maintainer group. Maintainers who are selected are granted the necessary GitHub
rights.

Once the group has at least four members, the maintainers avoid nominating a maintainer from an organization that already
employs half or more of them.

## Stepping down and removal

Maintainers may resign at any time. Maintainers may also be removed after a long period of inactivity (six months or more
without activity and without a definite schedule to return), for failing to fulfill their responsibilities, for violating the
Code of Conduct, or for other serious reasons. Removal requires a two-thirds vote of the remaining maintainers.

Depending on the reason, a former maintainer can be listed as an **emeritus maintainer**: recognized for past contributions
and welcome to advise, but without voting rights or merge access. An emeritus maintainer can be reinstated by a simple
majority of the maintainers if they meet the current requirements and commit to ongoing participation.

## Decision making

Most decisions are made by **lazy consensus**: a proposal is made in public (an issue or a pull request), and if nobody with a
stake objects within a reasonable time (at least seven days for anything larger than a bug fix), the proposal is accepted.
Objections have to name a concern, and the discussion looks for a change that resolves it.

When consensus cannot be reached, or when any maintainer demands it, the maintainers **vote**. Each maintainer has one vote.
Most votes need a simple majority of all maintainers. Two-thirds majority votes mean at least two thirds of all existing
maintainers. Votes happen in the open on the relevant issue or pull request, except for security and conduct matters.

When a maintainer has a conflict of interest, they say so and do not vote on that matter. Employer affiliation is never a
reason for a decision.

## Architecture decisions

Significant decisions about the architecture, public interfaces, security model, license, release scope, governance and
project identity are recorded as architecture decision records (ADRs) in [docs/adr/](docs/adr/README.md), proposed by pull
request and decided by the rules above. Accepted records are not rewritten: a change of mind is a new record that supersedes
the old one.

## Code of conduct

The [Code of Conduct](CODE_OF_CONDUCT.md) applies to everyone in project spaces. Reports go to <conduct@myriarch.dev> and are
handled by the maintainers. If a maintainer is involved in a report, that maintainer does not take part in handling it. During the period with a single maintainer, nobody else is left to handle a report about that maintainer. In that case the maintainer tells the reporter about the conflict, offers to forward the report to a neutral third party whom the reporter agrees to, and documents the outcome. Once a second maintainer exists, reports about one maintainer go to the others.

## Security response

Security reports are handled according to [SECURITY.md](SECURITY.md). The maintainers act as the security response team. If
the project grows, they appoint a team of at least two contributors for this task and review its members at least once a year.

## Foundations

Myriarch is an independent project and is not hosted by a foundation. A later application to a foundation (for example the
CNCF Sandbox) is a goal, not a fact ([ADR-0028](docs/adr/0028-relationship-to-opentelemetry-and-foundation-path.md)). The
project does not describe itself as part of a foundation before that foundation has accepted it. If the project joins a
foundation, this document is updated to the foundation's requirements, and the escalation paths of that foundation (for
example its Code of Conduct committee) apply from that point on.

## Changes to this document

Changes to this document and to the documents it refers to ([CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md),
[CONTRIBUTOR_LADDER.md](CONTRIBUTOR_LADDER.md), [SECURITY.md](SECURITY.md)) are proposed by pull request and need a
two-thirds vote of all maintainers. Governance changes are recorded in an ADR.
