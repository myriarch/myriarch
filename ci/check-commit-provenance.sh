#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Per-commit provenance check (ADR-0049). Every commit it looks at must pass one of three rules, and the first one that fits decides:
#
#   sign-off    the message has a "Signed-off-by: Name <email>" line that matches the commit author (name exactly, email
#               without regard to case), and the author is not the project's machine account. A sign-off in the name of the
#               machine account never counts, in any commit, and a line that begins with "Signed-off-by" (any case, white space
#               before the colon allowed) counts as a sign-off line even when it is not well formed. On the default branch, a
#               squash merge (committer web-flow, signature valid) whose author has no matching line qualifies when exactly one
#               merged pull request points at it and every commit of that pull request passes this rule, because GitHub takes the
#               author of a squash commit from the GitHub profile, not from the commits.
#   dependabot  Dependabot is the author (GitHub says: login dependabot[bot], type Bot), the committer is GitHub (web-flow) and
#               GitHub verified the signature. Dependabot's own sign-off line is allowed and not needed. In a pull request, the
#               pull request must also have been opened by dependabot[bot] from a branch of this repository. On the default
#               branch, the squash merge of such a pull request qualifies (the committer is web-flow), and every commit of that
#               pull request must pass.
#   account     the machine account is author and committer, the message has no Signed-off-by line, GitHub verified the
#               signature as valid, and the signature is an SSH signature. A commit that GitHub made itself (web-flow, GPG) does
#               not qualify as such. In a pull request, the pull request must also have been opened by the account from a branch
#               of this repository, every commit of it must be a commit of the account (the pull request is squash-merged, so a
#               commit of a human in it would be covered by no declaration), and a person other than the account who is a member
#               or collaborator (or has write access to the repository) must have approved the current head of the pull request. On the default branch only the squash
#               merge of such a pull request qualifies (the author is the account, the committer is web-flow): the merged pull
#               request must meet the same conditions and every one of its commits must pass. A commit of the account that sits on
#               the default branch itself (pushed or rebased) fails.
#
# Merge commits fail: the DCO check skips them, this check does not. A missing field, an incomplete answer and any API error
# fail the commit (fail closed). The DCO check (DCO-2) also accepts a sign-off line of the committer and skips bot commits; this
# check accepts only the line of the author, and it looks at the commits of organization members too.
# --range only: a commit named in ci/provenance-exceptions.txt (full SHA, then the reason) passes. That is the way to record that a
# commit already on the default branch is accepted, because history is not rewritten; the file changes only in a reviewed pull
# request. The check needs no secret, but it uses the token of the run (GITHUB_TOKEN): without a token the role of a reviewer whose
# organization membership is not public cannot be determined, and the check then fails closed.
#
#   ci/check-commit-provenance.sh --range <git revision range>   commits of a checkout, for example v0.2.0..HEAD or HEAD
#   ci/check-commit-provenance.sh --pr <number>                   the commits of one pull request, read from the API
#
#   GITHUB_REPOSITORY   owner/name (required)
#   GITHUB_API_URL      base URL of the REST API, https only (default https://api.github.com)
#   GITHUB_TOKEN        the token of the run; never printed (without it the role of a reviewer with a non-public membership is unknown)
#   PROVENANCE_ACCOUNT  login of the machine account (default myriarch-bot)
#   PROVENANCE_EXCEPTIONS  tests only (refused when GITHUB_ACTIONS is set): another exceptions file
#
# Exit 0 when every commit passes, 1 when at least one fails (one line per commit), 2 for a usage or environment error.
# Limit: with --range a commit that passes by sign-off is not asked for its GitHub login, so an author who uses a different
# address than the noreply address of the account is only caught with --pr, where the API list gives the login for free.
# PROVENANCE_FIXTURE_DIR (tests only, refused when GITHUB_ACTIONS is set) answers API reads from JSON files instead of the network.
set -euo pipefail
set +x
usage() {
  printf 'usage: ci/check-commit-provenance.sh --range <git revision range> | --pr <number>\n' >&2
  exit 2
}
[ "$#" -eq 2 ] || usage
case "$1" in --range|--pr) ;; *) usage ;; esac
command -v python3 >/dev/null 2>&1 || { echo "check-commit-provenance: python3 not found" >&2; exit 2; }
if [ "$1" = --range ]; then
  command -v git >/dev/null 2>&1 || { echo "check-commit-provenance: git not found" >&2; exit 2; }
fi

PROVENANCE_EXCEPTIONS_DEFAULT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/provenance-exceptions.txt"
export PROVENANCE_EXCEPTIONS_DEFAULT
exec python3 -I - "$@" <<'PY'
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request

mode, arg = sys.argv[1], sys.argv[2]
repo = os.environ.get('GITHUB_REPOSITORY', '')
api = os.environ.get('GITHUB_API_URL', 'https://api.github.com').rstrip('/')
token = os.environ.get('GITHUB_TOKEN', '')
account = os.environ.get('PROVENANCE_ACCOUNT', 'myriarch-bot')
fixtures = os.environ.get('PROVENANCE_FIXTURE_DIR', '')


def die(msg):
    print('check-commit-provenance: %s' % msg, file=sys.stderr)
    sys.exit(2)


def has_control(s):
    return any(ord(c) < 32 or ord(c) == 127 for c in s)


if not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repo):
    die('GITHUB_REPOSITORY is not owner/name')
if not re.fullmatch(r'https://[A-Za-z0-9._:/-]+', api):
    die('GITHUB_API_URL is not an https URL')
if not re.fullmatch(r'[A-Za-z0-9-]{1,39}', account):
    die('PROVENANCE_ACCOUNT is not a GitHub login')
if token and (has_control(token) or not re.fullmatch(r'[A-Za-z0-9_.~+/=-]+', token)):
    die('GITHUB_TOKEN has an unexpected format')
if fixtures and os.environ.get('GITHUB_ACTIONS'):
    die('PROVENANCE_FIXTURE_DIR is for tests and is refused in GitHub Actions')
exc_file = os.environ.get('PROVENANCE_EXCEPTIONS', '')
if exc_file and os.environ.get('GITHUB_ACTIONS'):
    die('PROVENANCE_EXCEPTIONS is for tests and is refused in GitHub Actions')
exc_file = exc_file or os.environ.get('PROVENANCE_EXCEPTIONS_DEFAULT', '')
if mode == '--pr' and not re.fullmatch(r'[1-9][0-9]{0,8}', arg):
    die('the pull request number is not a number')
if mode == '--range' and (not arg or arg.startswith('-') or has_control(arg)):
    die('the revision range is empty or looks like an option')

account_l = account.lower()


class ApiError(Exception):
    pass


def get(path):
    """GET one API path below the repository; returns parsed JSON or raises ApiError."""
    if fixtures:
        name = re.sub(r'[^A-Za-z0-9._-]', '_', path.split('?')[0].strip('/')) + '.json'
        try:
            with open(os.path.join(fixtures, name), encoding='utf-8') as fh:
                return json.load(fh)
        except (OSError, ValueError) as exc:
            raise ApiError('%s: no usable fixture (%s)' % (path, name)) from exc
    url = '%s/repos/%s/%s' % (api, repo, path)
    headers = {'Accept': 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28',
               'User-Agent': 'myriarch-commit-provenance'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    last = None
    for attempt in range(3):
        try:
            req = urllib.request.Request(url, headers=headers)
            with urllib.request.urlopen(req, timeout=30) as resp:
                return json.load(resp)
        except urllib.error.HTTPError as exc:
            last = 'HTTP %d' % exc.code
            if exc.code < 500:
                break
        except Exception as exc:  # network, TLS, bad JSON
            last = type(exc).__name__
        time.sleep(1 + attempt)
    raise ApiError('%s: %s' % (path, last))


def get_all(path):
    """All pages of a list endpoint (100 per page)."""
    out = []
    page = 1
    while True:
        sep = '&' if '?' in path else '?'
        items = get('%s%sper_page=100&page=%d' % (path, sep, page))
        if not isinstance(items, list):
            raise ApiError('%s: not a list' % path)
        out.extend(items)
        if len(items) < 100:
            return out
        page += 1
        if page > 10:
            raise ApiError('%s: more than 1000 entries' % path)


SIGNOFF = re.compile(r'^signed-off-by:[ \t]*(.+?)[ \t]*<([^<>\r\n]+)>[ \t]*$', re.I)


def trailers(msg):
    found = []
    for line in msg.splitlines():
        m = SIGNOFF.match(line)
        if m:
            found.append((m.group(1), m.group(2)))
    return found


# Any line that begins with "signed-off-by" (any case, any white space before the colon) counts as a sign-off line, well formed or not.
# The rules about the machine account look at all of them, so a mangled line cannot slip past the strict pattern above.
LOOSE_SIGNOFF = re.compile(r'^[ \t]*signed-off-by[ \t]*:', re.I)


def signoff_lines(msg):
    return [line for line in msg.splitlines() if LOOSE_SIGNOFF.match(line)]


def names_account(line):
    return account_l in line.lower()


def account_identity(name, email):
    e = email.lower()
    return name.lower() == account_l or re.fullmatch(r'(\d+\+)?' + re.escape(account_l) + r'@users\.noreply\.github\.com', e) is not None


def from_api(c):
    """Normalize one commit object of the REST API."""
    cm = c.get('commit') or {}
    ca, cc = cm.get('author') or {}, cm.get('committer') or {}
    ver = cm.get('verification') or {}
    au, co = c.get('author') or {}, c.get('committer') or {}
    return {
        'sha': str(c.get('sha', '')), 'msg': str(cm.get('message', '')),
        'an': str(ca.get('name', '')), 'ae': str(ca.get('email', '')),
        'cn': str(cc.get('name', '')), 'ce': str(cc.get('email', '')),
        'merge': len(c.get('parents') or []) > 1, 'full': True,
        'a_login': au.get('login'), 'a_type': au.get('type'), 'c_login': co.get('login'),
        'verified': ver.get('verified') is True, 'reason': ver.get('reason'),
        'sig': ver.get('signature') or '',
    }


def from_git(sha):
    fmt = '%an%x00%ae%x00%cn%x00%ce%x00%P%x00%B'
    out = subprocess.run(['git', 'show', '-s', '--format=' + fmt, sha], check=True, capture_output=True, text=True).stdout
    an, ae, cn, ce, parents, msg = out.split('\x00', 5)
    return {'sha': sha, 'msg': msg, 'an': an, 'ae': ae, 'cn': cn, 'ce': ce, 'merge': len(parents.split()) > 1, 'full': False}


def load_exceptions():
    """ci/provenance-exceptions.txt: lines of "<40 hex characters> <reason>", # starts a comment. Only --range uses it."""
    found = {}
    if mode != '--range':
        return found
    try:
        with open(exc_file, encoding='utf-8') as fh:
            lines = fh.read().splitlines()
    except OSError as exc:
        die('cannot read the exceptions file: %s' % type(exc).__name__)
    for n, line in enumerate(lines, 1):
        line = line.strip()
        if not line or line.startswith('#'):
            continue
        m = re.fullmatch(r'([0-9a-f]{40})[ \t]+(\S.*)', line)
        if not m:
            die('exceptions file, line %d: expected "<full SHA> <reason>"' % n)
        found[m.group(1)] = m.group(2)
    return found


exceptions = load_exceptions()
SSH = '-----BEGIN SSH SIGNATURE-----'
BOT = 'dependabot[bot]'
pr_cache = {}


role_cache = {}


def has_role(login, association):
    """A member or collaborator. The association in a review is what GitHub shows to this token, and a private membership of an
    organization may show up as CONTRIBUTOR or NONE, so anything else is asked again as the permission on the repository (write
    or more). An API error counts as no role (fail closed)."""
    if association in ('OWNER', 'MEMBER', 'COLLABORATOR'):
        return True
    if login not in role_cache:
        try:
            perm = get('collaborators/%s/permission' % login)
            role_cache[login] = (perm.get('permission') in ('admin', 'write')
                                 or perm.get('role_name') in ('admin', 'maintain', 'write'))
        except (ApiError, AttributeError):
            role_cache[login] = False
    return role_cache[login]


def pr_conditions(num, who):
    """Conditions on a pull request: opened by `who` from a branch of this repository; for the account, also approved by a person
    other than the account who is a member or collaborator, for the current head. Returns a problem text or None."""
    pr = get('pulls/%s' % num)
    login = ((pr.get('user') or {}).get('login') or '')
    head_repo = (((pr.get('head') or {}).get('repo')) or {}).get('full_name') or ''
    if login.lower() != who.lower():
        return 'pull request #%s was opened by %s, not by %s' % (num, login or 'an unknown user', who)
    if head_repo.lower() != repo.lower():
        return 'pull request #%s comes from %s, not from a branch of %s' % (num, head_repo or 'an unknown repository', repo)
    if who.lower() == account_l:
        head_sha = str((pr.get('head') or {}).get('sha') or '')
        state = {}
        for rv in get_all('pulls/%s/reviews' % num):
            user = rv.get('user') or {}
            who_r = str(user.get('login') or '').lower()
            st = rv.get('state')
            if not who_r or st not in ('APPROVED', 'CHANGES_REQUESTED', 'DISMISSED'):
                continue  # a plain comment changes nobody's verdict
            trusted = who_r != account_l and user.get('type') == 'User' and has_role(who_r, rv.get('author_association'))
            if st == 'CHANGES_REQUESTED' and not trusted:
                continue  # a stranger's request for changes does not block the merge; the maintainer can dismiss it anyway
            ok_person = trusted and head_sha != '' and rv.get('commit_id') == head_sha
            state[who_r] = 'APPROVED' if st == 'APPROVED' and ok_person else ('CHANGES_REQUESTED' if st == 'CHANGES_REQUESTED' else 'OTHER')
        if 'CHANGES_REQUESTED' in state.values():
            return 'pull request #%s has a review that requests changes' % num
        if 'APPROVED' not in state.values():
            return ('pull request #%s has no approval of the current head from a person other than %s who is a member or collaborator'
                    % (num, account))
    return None


def pr_commits(num):
    pr = get('pulls/%s' % num)
    commits = get_all('pulls/%s/commits' % num)
    if len(commits) != pr.get('commits') or len(commits) >= 250:
        raise ApiError('pull request #%s: %d commits read, the pull request has %s (the API lists at most 250)'
                       % (num, len(commits), pr.get('commits')))
    return pr, [from_api(c) for c in commits]


def check(rec, ctx):
    """Returns (ok, text, rule). ctx is 'pr' or 'main'; rule is 'signoff', 'dependabot' or 'account' when ok."""
    if ctx == 'main' and rec['sha'] in exceptions:
        return True, 'recorded exception (ci/provenance-exceptions.txt): %s' % exceptions[rec['sha']], 'exception'
    if rec['merge']:
        return False, 'merge commit (the DCO check skips these, this check does not)', None
    tr = trailers(rec['msg'])
    lines = signoff_lines(rec['msg'])
    if any(names_account(line) for line in lines):
        return False, 'a Signed-off-by line names the machine account', None
    login = (rec.get('a_login') or '').lower()
    acct_author = account_identity(rec['an'], rec['ae']) or login == account_l
    if not acct_author and any(n == rec['an'] and e.lower() == rec['ae'].lower() for n, e in tr):
        return True, 'sign-off', 'signoff'
    if not rec['full']:
        try:
            rec = from_api(get('commits/%s' % rec['sha']))
        except ApiError as exc:
            return False, 'GitHub data missing (%s)' % exc, None
        login = (rec.get('a_login') or '').lower()
        acct_author = account_identity(rec['an'], rec['ae']) or login == account_l
    if rec['an'] == BOT and re.fullmatch(r'\d+\+dependabot\[bot\]@users\.noreply\.github\.com', rec['ae']):
        if (rec['a_login'] == BOT and rec['a_type'] == 'Bot' and rec['c_login'] == 'web-flow'
                and rec['verified'] and rec['reason'] == 'valid'):
            if ctx == 'main':
                return squash(rec, BOT, 'dependabot')
            return True, 'dependabot', 'dependabot'
        return False, 'looks like a Dependabot commit, but GitHub does not confirm bot author, web-flow committer and a valid signature', None
    if acct_author:
        if lines:
            return False, 'a commit of the machine account has a Signed-off-by line', None
        if login != account_l:
            return False, 'the author looks like the machine account, but GitHub does not map it to that login', None
        if not (rec['verified'] and rec['reason'] == 'valid'):
            return False, 'signature not verified as valid by GitHub (%s)' % rec['reason'], None
        c_login = (rec.get('c_login') or '').lower()
        if c_login == account_l:
            if not rec['sig'].startswith(SSH):
                return False, 'the signature is not an SSH signature', None
            if ctx == 'main':
                return False, 'a commit of the account on the default branch was pushed or rebased; only the squash merge of a pull request qualifies', None
            return True, 'account', 'account'
        if ctx == 'main' and c_login == 'web-flow':
            return squash(rec, account, 'account')
        return False, 'the committer is %s, not the machine account' % (rec.get('c_login') or 'unknown'), None
    if (ctx == 'main' and (rec.get('c_login') or '').lower() == 'web-flow' and rec['verified'] and rec['reason'] == 'valid'):
        return human_squash(rec)
    return False, 'no Signed-off-by line that matches the author, and not a commit of the machine account or Dependabot', None


def human_squash(rec):
    """A squash merge on the default branch whose author is not the one who signed off: GitHub sets the author of a squash commit from
    the profile of the person who opened the pull request (profile name, noreply address), not from the git identity of the commits.
    The squash counts when exactly one merged pull request points at it and every commit of that pull request has a sign-off that
    matches its own author (a fork is fine, contributors work from forks)."""
    miss = 'no Signed-off-by line that matches the author, and not a commit of the machine account or Dependabot'
    try:
        prs = get('commits/%s/pulls' % rec['sha'])
        merged = [p for p in prs if isinstance(p, dict) and p.get('merge_commit_sha') == rec['sha'] and p.get('merged_at')]
        if len(merged) != 1:
            return False, miss + ' (%d merged pull requests point at this squash commit, not one)' % len(merged), None
        num = merged[0].get('number')
        key = (num, 'signoff')
        if key not in pr_cache:
            bad = []
            for c in pr_commits(num)[1]:
                ok, text, r = check(c, 'pr')
                if not ok or r != 'signoff':
                    bad.append('%s %s' % (c['sha'][:12], text if not ok else 'is not covered by a sign-off'))
            pr_cache[key] = ('commits of pull request #%s fail: %s' % (num, '; '.join(bad))) if bad else None
        if pr_cache[key]:
            return False, miss + '; ' + pr_cache[key], None
        return True, 'sign-off (squash of pull request #%s)' % num, 'signoff'
    except ApiError as exc:
        return False, 'GitHub data missing (%s)' % exc, None


def squash(rec, who, rule):
    """A squash merge on the default branch whose author is `who`: look at the merged pull request and all of its commits."""
    try:
        prs = get('commits/%s/pulls' % rec['sha'])
        merged = [p for p in prs if isinstance(p, dict) and p.get('merge_commit_sha') == rec['sha'] and p.get('merged_at')]
        if len(merged) != 1:
            return False, 'squash merge by %s, but %d merged pull requests point at it, not one' % (who, len(merged)), None
        num = merged[0].get('number')
        key = (num, who)
        if key not in pr_cache:
            problem = pr_conditions(num, who)
            if problem is None:
                bad = []
                for c in pr_commits(num)[1]:
                    ok, text, r = check(c, 'pr')
                    if not ok:
                        bad.append('%s %s' % (c['sha'][:12], text))
                    elif rule == 'account' and r != 'account':
                        bad.append('%s is not a commit of %s' % (c['sha'][:12], who))
                    elif rule == 'dependabot' and r == 'account':
                        bad.append('%s is a commit of the machine account in a Dependabot pull request' % c['sha'][:12])
                problem = ('commits of pull request #%s fail: %s' % (num, '; '.join(bad))) if bad else None
            pr_cache[key] = problem
        if pr_cache[key]:
            return False, pr_cache[key], None
        return True, '%s (squash of pull request #%s)' % (rule, num), rule
    except ApiError as exc:
        return False, 'GitHub data missing (%s)' % exc, None


MIXED = 'this is not a commit of the account, but the pull request is the account\'s or holds commits of the account, and it is squash-merged: the declaration covers only commits of the account (push no commit of your own to it)'


def run():
    results = []  # (sha, ok, text)
    if mode == '--range':
        r = subprocess.run(['git', 'rev-list', '--reverse', arg, '--'], capture_output=True, text=True)
        if r.returncode != 0:
            die('git rev-list failed for the range %s' % arg)
        for sha in r.stdout.split():
            ok, text, _ = check(from_git(sha), 'main')
            results.append((sha, ok, text))
    else:
        try:
            pr, recs = pr_commits(arg)
        except ApiError as exc:
            print('FAIL pull request #%s: GitHub data missing (%s)' % (arg, exc))
            return [], 1
        if not recs:
            print('FAIL pull request #%s has no commits' % arg)
            return [], 1
        checked = [(rec,) + check(rec, 'pr') for rec in recs]
        rules = {r for _, ok, _, r in checked if ok}
        opened_by = str((pr.get('user') or {}).get('login') or '').lower()
        account_pr = opened_by == account_l or 'account' in rules
        problems = {}
        for rule, who in (('account', account), ('dependabot', BOT)):
            if rule in rules:
                try:
                    problems[rule] = pr_conditions(arg, who) or ''
                except ApiError as exc:
                    problems[rule] = 'GitHub data missing (%s)' % exc
        for rec, ok, text, rule in checked:
            if ok and problems.get(rule):
                ok, text = False, problems[rule]
            elif ok and account_pr and rule != 'account':
                ok, text = False, MIXED
            results.append((rec['sha'], ok, text))
    return results, 0


results, early = run()
bad = 0
for sha, ok, text in results:
    print('%s %s %s' % ('ok  ' if ok else 'FAIL', sha[:12], text))
    bad += 0 if ok else 1
print('check-commit-provenance: %d commits checked, %d failed' % (len(results), bad))
sys.exit(1 if bad or early else 0)
PY
