# 0049. Sign-off for the commits of the project's machine account

- Status: Accepted
- Date: 2026-11-07
- Related: [ADR-0019](0019-developer-certificate-of-origin.md), [ADR-0028](0028-relationship-to-opentelemetry-and-foundation-path.md), [ADR-0029](0029-host-under-the-myriarch-organization.md), [ADR-0035](0035-release-workflow-with-keyless-signing-and-verification-before-publication.md)

## Context

[ADR-0019](0019-developer-certificate-of-origin.md) requires a `Signed-off-by:` line that matches the author on every commit. The line is the statement of a person: that they wrote the change or have the right to submit it. [AGENTS.md](../../AGENTS.md) says that an agent never adds or forges that line, and this record does not change it.

Part of the work on the project is done by AI agents that the maintainer directs. They work under a GitHub account of their own, `myriarch-bot`, which is a machine account. The account is controlled by the maintainer. It is to be a public member of the `myriarch` organization with the write role, never an organization owner, and it is never named in `.github/CODEOWNERS`. With the rule of ADR-0019 alone, its commits are either blocked or need a line that somebody else did not write for it. A sign-off in the name of the account, or a line the maintainer adds without having made the commit, would be a statement that nobody made.

What the DCO check can do (the DCO-2 application, [cncf/dco2](https://github.com/cncf/dco2); read in its README, its sample configuration and its source at commit `7b01d3d`):

- With `require: members: false` in `.github/dco.yml`, the check skips a commit when GitHub reports its signature as verified and the commit author is a member of the organization. The default is `members: true`.
- The decision looks at the author only. It does not compare the author with the committer, and it does not ask who signed. Every verified commit whose author is a member passes, including a commit the account did not make and a commit of the maintainer.
- The application sees an author's membership only if the membership is public, or if the application is configured to see non-public members (README).
- The file is read from the default branch, not from the pull request, and the application caches it for up to an hour. A pull request therefore cannot exempt itself, and the pull request that adds the file is still checked under the old rule.
- Merge commits and commits that GitHub marks as authored by a bot are skipped in any case.
- `allowOverrideAction: false` hides the button "Set DCO to pass" and makes the application ignore such a request. The README says plainly that this is not a security boundary, because a maintainer with write access can change the configuration.

What GitHub reports as verified (GitHub documentation, "About commit signature verification", and the table of `verification.reason` values in the REST documentation for commits): GitHub verifies GPG, SSH and S/MIME signatures. For SSH, the signing key must be added to the account. The reason values tie the result to the account of the committer: `no_user` (no user is associated with the committer email address), `unverified_email` (the address is not verified on that account) and `unknown_key` (the key has not been registered with any user's account) all stop a commit from being verified, and `valid` means that none of these errors applied. GitHub signs commits made in the web interface itself, with GPG. A rebase merge adds commits without verification, because GitHub cannot sign them as the committer.

The DCO check alone is therefore wider than the rule the maintainer is willing to stand behind, and a check that the project controls has to close the gap.

## Decision

We will let commits of the machine account carry no `Signed-off-by:` line under the conditions below, and we will keep the rest of ADR-0019 as it is.

**Declaration of the maintainer.** The maintainer makes the following declaration once, in this record:

> I, the maintainer of this project (GitHub account `gilfy`), certify the Developer Certificate of Origin 1.1 for every change that meets all of these conditions, to the best of my knowledge:
>
> 1. The `myriarch-bot` account is both author and committer of each commit of a pull request. I control that account and am responsible for what it contributes.
> 2. Each of those commits is signed with the SSH signing key of that account, GitHub shows the signature as verified, and the commit has no `Signed-off-by:` line.
> 3. The change reaches the default branch only as the squash merge of that pull request, which the account opened from a branch of this repository, and which consists only of such commits. It is merged after the required checks passed, including the per-commit provenance check described below, and after a person other than the account, who is a member or collaborator of the repository, approved the head of the pull request.
>
> The commit that lands on the default branch is the squash commit. GitHub makes it (committer `web-flow`, GitHub's own signature) and the account is its author. The declaration covers that squash commit as the merged form of the commits in conditions 1 and 2: it has the same content, and the commits in the pull request are the ones I certify. I do not claim to have read every such commit line by line; the reviewer in condition 3 reviews the change, and I answer for the account. The declaration covers no other commits: not those of other contributors, not my own, and not commits that GitHub made on the account's behalf in any other way. Nobody adds a `Signed-off-by:` line in my name. It stands until this record is superseded or the exemption is switched off, and it does not apply to commits merged after that.

**Technical measures.**

- `.github/dco.yml` sets `require: members: false` and `allowOverrideAction: false`.
- The account signs with an SSH key that is registered as a signing key of the account, and the account's membership in the organization is public.
- `ci/check-commit-provenance.sh` checks every commit. The first matching rule decides:
  - a `Signed-off-by:` line that matches the author (the name exactly, the e-mail address without regard to case), where the author is not the machine account. The squash merge of a pull request of a person counts too when its author differs from the sign-off, because GitHub sets the author of a squash commit from the profile of whoever opened the pull request (profile name, noreply address) and not from the git identity of the commits. Then the committer must be GitHub with a valid signature, exactly one merged pull request must point at the commit, and every commit of that pull request must have a sign-off that matches its own author. The pull request may come from a fork;
  - a Dependabot commit that GitHub confirms: the author is the bot, the committer is GitHub and the signature is valid. Like the commits of the account, it counts only in a pull request that `dependabot[bot]` opened from a branch of this repository, or as the squash merge of such a pull request on the default branch;
  - a commit of the machine account as described in the declaration. GitHub must report valid verification for it, and the signature must be an SSH signature. In a pull request, the pull request must have been opened by the account from a branch of this repository, every commit of it must be a commit of the account (a commit of a human in that pull request fails, because the squash would not be covered by the declaration), and a person other than the account, who is a member or collaborator (or has write access to the repository), must have approved the current head of the pull request, and no member or collaborator may have requested changes. The approval comes after the first run of the check, so the reviewer re-runs the job `provenance` after approving. On the default branch only the squash merge of such a pull request counts, and the merged pull request must meet the same conditions. A commit of the account that sits on the default branch itself (pushed or rebased) fails.
- A line that begins with `Signed-off-by` (any case, white space before the colon allowed) counts as a sign-off line even when it is not well formed. A commit of the machine account with such a line fails, and so does a commit of a person that also carries such a line in the name of the machine account. A `Signed-off-by:` line in the name of the machine account fails in every case. Merge commits fail, although the DCO check skips them. A missing field or an API error fails the commit.
- The check runs in the CI job `provenance` for every pull request, and in the release guard (`ci/release/guard.sh`) for every commit since the previous published release. A tag without a GitHub release, such as one the guard refused, does not move the start of that range, because tags cannot be deleted. A commit that is already on the default branch and cannot be fixed is recorded, with a reason, in `ci/provenance-exceptions.txt` in a reviewed pull request. Both need no secret and use the token of the run: without a token the role of a reviewer whose membership of the organization is not public cannot be determined, and the check fails closed.
- The merge method for pull requests of the account and of Dependabot is squash. Rebase merges are not switched off in the repository settings. A rebased commit has no verified signature and fails the check on the default branch, so a rebase is caught at the release guard and not at the merge.
- [CONTRIBUTING.md](../../CONTRIBUTING.md) and [AGENTS.md](../../AGENTS.md) state the exception. Pull request descriptions keep disclosing AI assistance.

Humans keep signing their commits. ADR-0019 applies to every commit that does not meet the declaration.

## Consequences

### Positive

- The account's work no longer needs a statement that nobody made. The maintainer's statement is public, written once and limited in scope.
- The exemption is checked per commit by the project and does not rest on the wider rule of the DCO application.
- The same check guards releases.

### Negative

- Merged commits of the account stay without a `Signed-off-by:` line. The default branch is not rewritten, and switching the exemption off (`members: true`) affects only new commits.
- It is open whether a foundation accepts this. ADR-0019 and [ADR-0028](0028-relationship-to-opentelemetry-and-foundation-path.md) name the DCO as a foundation requirement, and no statement of a foundation's IP policy on commits of a machine account has been checked. A foundation may ask for a different form, for example a later declaration. This is decided with an application, not here.
- The exemption of the DCO check is wider than this rule. The provenance check closes the gap only where it runs, and a pull request that changes the check or its job is judged by its own version. This is a real gap and not only a matter of review: the account has the write role, the repository rules do not require an approving review, and a pull request of the account that changes the check or its job can remove the approval condition for itself and then be merged by the account. Nothing in the repository enforces the opposite. What stands against it is the maintainer's control of the account, and nothing else. The release guard runs the check again, but with the version of the tagged commit, so it does not close the gap either. Restricting merges to the default branch to the maintainer on the server would close it, and is open.
- The approval condition reads the role that GitHub shows to the token of the run. A membership that is not public may show up as no role at all, in which case the check falls back to the reviewer's permission on the repository (write or more), and fails closed if that is unavailable. This has to be measured with a test pull request of the account before this record is accepted. The same measurement has to look at one more case: the job `provenance` succeeds without checking anything for every event other than a pull request, among them a manual run (`workflow_dispatch`). A green `ci-ok` from such a run on the same commit could stand next to a red run of the pull request, and whether the repository rule counts the newer run is not known. The same measurement squash-merges one pull request of the account in the web interface, because the repository requires a sign-off on commits made there: if GitHub then adds a line in the name of the person who merges, that squash fails the check, and this record is changed before it is accepted.
- The DCO application caches the configuration for up to an hour, and it may not see a membership that is not public.
- Signing proves which key signed. It does not prove who gave the instruction. The declaration rests on the maintainer's control of the account and on the review before the merge, not on the signature.
- Dependabot commits stay as they are: the DCO check skips them, and the provenance check accepts them under the rule above.
- This record and `.github/dco.yml` are merged under the old rule, with the sign-off of the maintainer.
- When this record is accepted, the same pull request changes its status and its line in the index, the word "proposed" in the changelog, and the status of [ADR-0019](0019-developer-certificate-of-origin.md) and its index line ("Accepted, amended by ADR-0049"), because the rule "every commit" of that record is narrowed here.

## Alternatives considered

- **A `Signed-off-by:` line in the name of the machine account**: rejected. The line is the statement of a person, and a line that nobody made is not one.
- **The maintainer adds the line to the account's commits**: rejected. The maintainer did not make the commits and would sign a statement about them without a defined basis. It also stops the work whenever the maintainer is away.
- **The maintainer's own account and key for the agents' commits**: rejected. The commits would look like the maintainer's and would carry a statement in the maintainer's name that the maintainer did not make for each commit.
- **Using the button "Set DCO to pass"**: rejected. It overrides the result and certifies nothing. The option is switched off.
- **A GitHub App as the author**: rejected for now. The DCO check skips bot commits without any signature, which is weaker, and it needs a separate way to push.
- **`require: members: false` without the provenance check**: rejected, because it exempts every verified commit of every member.
- **Remediation commits per pull request**: rejected, because they add work for every pull request and the DCO application only allows them for a sign-off by the author or a third party who is authorized to give it.
- **Humans only, no contributions from the account**: possible, but it gives up work that is reviewed like any other and costs the maintainer's time.
