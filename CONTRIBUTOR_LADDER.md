# Contributor ladder

This document describes the roles in the myriarch community, what is expected in each role, and how people move up. It is
adapted from the CNCF project template
([CONTRIBUTOR_LADDER.md](https://github.com/cncf/project-template/blob/main/CONTRIBUTOR_LADDER.md)) and reduced to three roles
that fit a young project: **contributor**, **reviewer** and **maintainer**. Everyone who takes part in the community and has
not taken one of these roles is a community participant: someone who uses the project, asks questions, reports bugs, comments
on issues and tries out releases. Participants follow the [Code of Conduct](CODE_OF_CONDUCT.md).

The template is licensed under Apache-2.0; this document was adapted and changed by The Myriarch Authors and is licensed under
Apache-2.0, see [LICENSE](LICENSE).

Each role lists **responsibilities** (what you are expected to do), **requirements** (what you need to qualify) and
**privileges** (what you are entitled to). Our project members are happy to help you advance. How governance works is in
[GOVERNANCE.md](GOVERNANCE.md); the current maintainers are in [MAINTAINERS.md](MAINTAINERS.md).

## Contributor

A contributor adds value to the project directly. Contributions need not be code. Contributors may be new, or they may
contribute only occasionally.

* Responsibilities:
  * Follow the [Code of Conduct](CODE_OF_CONDUCT.md) and the [contributing guide](CONTRIBUTING.md), including the DCO sign-off.
* Requirements (one or several of the following):
  * Report and sometimes resolve issues.
  * Submit pull requests, even occasionally.
  * Improve the documentation or the examples.
  * Answer questions from other community members.
  * Give feedback on issues and pull requests.
  * Test releases and patches and report the results.
  * Add your organization to [ADOPTERS.md](ADOPTERS.md) or write about your use.
* Privileges:
  * Can be assigned issues.
  * Eligible to become a reviewer.

## Reviewer

A reviewer is responsible for the quality of specific areas of the project: for example a Go package, the web UI, the Helm
chart, the installers or a part of the documentation. Reviewers have a track record of contributions and reviews.

* Responsibilities:
  * Review most pull requests that touch their areas, following the [contributing guide](CONTRIBUTING.md).
  * Be supportive of new and occasional contributors and help them get useful pull requests into shape.
  * Help other contributors become reviewers.
* Requirements:
  * Have contributed to the project for at least three months and had several non-trivial pull requests merged.
  * Have reviewed or helped review at least ten pull requests.
  * Show in-depth knowledge of the area, including its tests, and commit to being responsible for it.
  * Use a GitHub account with two-factor authentication enabled.
* Privileges:
  * Listed as code owner for their areas in [.github/CODEOWNERS](.github/CODEOWNERS), so that reviews are requested from them.
  * Their approval counts towards merging a pull request in their areas.
  * Can recommend other contributors to become reviewers.

The process: a maintainer, or the contributor with the support of a maintainer, opens a public pull request that adds the
contributor to `.github/CODEOWNERS` for the areas. A maintainer approves the pull request after a discussion period of at
least seven days.

## Maintainer

Maintainers are established contributors who are responsible for the entire project. They can approve pull requests in every
area and take part in deciding about strategy and priorities. See [GOVERNANCE.md](GOVERNANCE.md) for the maintainer role.

* Responsibilities:
  * Review pull requests, especially those that cross several areas of the project.
  * Mentor new reviewers and contributors.
  * Help determine the strategy and the policies of the project and keep the roadmap current.
  * Take part in decisions and votes, and in handling security reports and Code of Conduct reports.
  * Represent the project in public.
* Requirements:
  * Have been a reviewer for at least six months (or show an equivalent record).
  * Demonstrate broad knowledge of the project across several areas.
  * Exercise judgment for the good of the project, independent of an employer, friends or a team.
  * Mentor other contributors.
  * Commit to spending time on the project regularly.
* Privileges:
  * Write access to the repository and the ability to merge pull requests in every area.
  * A vote in maintainer decisions.
  * Communicate on behalf of the project, including with foundations.

The process is described in [GOVERNANCE.md](GOVERNANCE.md#becoming-a-maintainer): an existing maintainer nominates a
reviewer in a public pull request that adds the nominee to [MAINTAINERS.md](MAINTAINERS.md); the nominee accepts the
responsibilities in the pull request; a majority of the current maintainers approves.

## Inactivity

Contributors who stay active set an example and show commitment. Long inactivity leads to delays and loss of trust.

* Inactivity is measured by a period of no contributions and no communication of six months or more.
* The consequence can be a move down the ladder or to emeritus status. Involuntary removal or demotion also happens when the
  requirements of a role are no longer met or when the Code of Conduct is violated. It is decided by the maintainers as
  described in [GOVERNANCE.md](GOVERNANCE.md#stepping-down-and-removal).

## Stepping down

When your available time changes, you can step down a level on the ladder or leave completely. A maintainer who steps away
may become an emeritus maintainer and is listed as such in [MAINTAINERS.md](MAINTAINERS.md). Talk to a maintainer or open a
pull request that updates the files above.

## Contact

Questions about the ladder go to <maintainers@myriarch.dev> or to an issue in the repository.
