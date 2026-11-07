#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Test of ci/check-commit-provenance.sh (ADR-0049). A temporary git repository gives the commits of --range, JSON files stand in
# for the GitHub API (PROVENANCE_FIXTURE_DIR), so no network is used. Every rule has a passing case and the cases that must fail:
# a missing or foreign sign-off, a sign-off of the machine account, a commit of the account that GitHub does not confirm, the
# wrong committer, a GPG signature, a pull request from a fork, a merge commit, a missing review, a commit of a human in a pull
# request of the account, a Dependabot commit in a pull request of someone else, a recorded exception and every missing piece
# of API data.
# shellcheck source=ci/tests/lib-test.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-test.sh"
set -u
have git || { echo "git is missing: test skipped"; exit 0; }
have python3 || { echo "python3 is missing: test skipped"; exit 0; }

if PROVENANCE_RESULT="$(python3 -I - "${CI_DIR}/check-commit-provenance.sh" "${TEST_TMP}" <<'PY'
import json
import os
import re
import subprocess
import sys

script, tmp = sys.argv[1], sys.argv[2]
work = os.path.join(tmp, 'work')
fx = os.path.join(tmp, 'fixtures')
os.makedirs(fx)
passed = failed = 0
SSH = '-----BEGIN SSH SIGNATURE-----\nabc\n-----END SSH SIGNATURE-----'
GPG = '-----BEGIN PGP SIGNATURE-----\nabc\n-----END PGP SIGNATURE-----'
ACCT = ('myriarch-bot', '1+myriarch-bot@users.noreply.github.com')
HUMAN = ('Ada Example', 'ada@example.test')
OTHER = ('Bob Example', 'bob@example.test')
DEP = ('dependabot[bot]', '49699333+dependabot[bot]@users.noreply.github.com')
BASE = {'PATH': os.environ.get('PATH', ''), 'HOME': tmp, 'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null'}


def git(*args, env=None):
    e = dict(BASE)
    e.update(env or {})
    return subprocess.run(['git', '-C', work] + list(args), check=True, capture_output=True, text=True, env=e).stdout.strip()


def commit(msg, author=HUMAN, committer=None, extra=None):
    committer = committer or author
    env = {'GIT_AUTHOR_NAME': author[0], 'GIT_AUTHOR_EMAIL': author[1], 'GIT_COMMITTER_NAME': committer[0],
           'GIT_COMMITTER_EMAIL': committer[1]}
    env.update(extra or {})
    git('commit', '-q', '--allow-empty', '-m', msg, env=env)
    return git('rev-parse', 'HEAD')


def put(path, data):
    name = re.sub(r'[^A-Za-z0-9._-]', '_', path.strip('/')) + '.json'
    with open(os.path.join(fx, name), 'w', encoding='utf-8') as fh:
        json.dump(data, fh)


def api_commit(sha, msg, author, committer, a_login, c_login, a_type='User', verified=True, reason='valid', sig=SSH, parents=1):
    return {'sha': sha, 'parents': [{'sha': 'p%d' % i} for i in range(parents)],
            'author': None if a_login is None else {'login': a_login, 'type': a_type},
            'committer': None if c_login is None else {'login': c_login, 'type': 'User'},
            'commit': {'message': msg, 'author': {'name': author[0], 'email': author[1]},
                       'committer': {'name': committer[0], 'email': committer[1]},
                       'verification': {'verified': verified, 'reason': reason, 'signature': sig}}}


def run(args, **extra):
    env = {'PATH': BASE['PATH'], 'HOME': tmp, 'GITHUB_REPOSITORY': 'o/r', 'PROVENANCE_FIXTURE_DIR': fx,
           'PROVENANCE_EXCEPTIONS': os.path.join(tmp, 'exceptions.txt')}
    env.update(extra)
    env = {k: v for k, v in env.items() if v is not None}
    r = subprocess.run(['bash', script] + args, cwd=work, capture_output=True, text=True, env=env)
    return r.returncode, r.stdout + r.stderr


def expect(name, rc_want, want, args, **extra):
    global passed, failed
    rc, out = run(args, **extra)
    if rc == rc_want and re.search(want, out):
        passed += 1
        print('  ok    %s' % name)
    else:
        failed += 1
        print('  FAIL  %s (exit %s, wanted %s and /%s/)\n%s' % (name, rc, rc_want, want, re.sub(r'^', '        ', out, flags=re.M)))


def rng(sha):
    return ['--range', sha + '^!']


with open(os.path.join(tmp, 'exceptions.txt'), 'w', encoding='utf-8') as fh:
    fh.write('# none\n')
subprocess.run(['git', 'init', '-q', '-b', 'main', work], check=True, env=dict(BASE))
root = commit('root\n\nSigned-off-by: %s <%s>' % HUMAN)

# ---- sign-off rule --------------------------------------------------------------------------------------------------------
s = commit('A change\n\nSigned-off-by: %s <%s>' % HUMAN)
expect('sign-off that matches the author', 0, r'ok  .* sign-off', rng(s))
s = commit('A change\n\nSigned-off-by: %s <%s>' % (HUMAN[0], HUMAN[1].upper()))
expect('the email is compared without regard to case', 0, r'ok  .* sign-off', rng(s))
s = commit('No line')
put('commits/' + s, api_commit(s, 'No line', HUMAN, HUMAN, 'ada', 'ada', sig=''))
expect('no sign-off line, and GitHub knows a human author', 1, r'FAIL .*no Signed-off-by line that matches the author', rng(s))
s = commit('A change\n\nSigned-off-by: %s <%s>' % OTHER)
expect('sign-off of another person (GitHub data missing counts as a failure too)', 1, r'FAIL', rng(s))
s = commit('A change\n\nSigned-off-by: Ada Example <other@example.test>')
expect('right name, wrong email', 1, r'FAIL', rng(s))
s = commit('Rebase of a foreign commit\n\nSigned-off-by: %s <%s>' % OTHER, author=HUMAN, committer=OTHER)
expect('sign-off that matches only the committer', 1, r'FAIL', rng(s))
s = commit('Squash body\n\n* first\n\nSigned-off-by: %s <%s>\n\n* second\n\nSigned-off-by: %s <%s>' % (OTHER + HUMAN))
expect('a squash message with several lines, one of them the author', 0, r'ok  .* sign-off', rng(s))
s = commit('Pretend\n\nSigned-off-by: %s <%s>' % ACCT, author=HUMAN)
expect('sign-off in the name of the machine account on a human commit', 1, r'names the machine account', rng(s))
s = commit('Pretend\n\nSigned-off-by: %s <%s>' % ACCT, author=ACCT)
expect('sign-off of the machine account on its own commit', 1, r'names the machine account', rng(s))
s = commit('Pretend\n\nsigned-off-by: Some Name <1+MyRiArCh-Bot@users.noreply.github.com>', author=HUMAN)
expect('the account address in a sign-off, any case', 1, r'names the machine account', rng(s))
# A line that begins with "signed-off-by" counts, well formed or not: the human has a good line and a second one in the name of the account.
GOOD = 'Signed-off-by: %s <%s>' % HUMAN
for label, bad in (('without an address', 'Signed-off-by: myriarch-bot'),
                   ('with text after the address', 'Signed-off-by: Some Name <1+myriarch-bot@users.noreply.github.com> (agent)'),
                   ('with white space before the colon', 'Signed-off-by : myriarch-bot <x@example.test>'),
                   ('in capitals and without a space', 'SIGNED-OFF-BY:myriarch-bot'),
                   ('indented', '  Signed-off-by: MyRiArCh-Bot <>'),
                   ('with an empty address', 'signed-off-by: myriarch-bot <>')):
    s = commit('Pretend\n\n%s\n%s' % (GOOD, bad), author=HUMAN)
    expect('a human with a good line and one in the name of the account, %s' % label, 1, r'names the machine account', rng(s))

# ---- merge commits --------------------------------------------------------------------------------------------------------
git('checkout', '-q', '-b', 'side')
commit('side\n\nSigned-off-by: %s <%s>' % HUMAN)
git('checkout', '-q', 'main')
commit('main\n\nSigned-off-by: %s <%s>' % HUMAN)
git('merge', '-q', '--no-ff', '-m', 'Merge side\n\nSigned-off-by: %s <%s>' % HUMAN, 'side', env={
    'GIT_AUTHOR_NAME': HUMAN[0], 'GIT_AUTHOR_EMAIL': HUMAN[1], 'GIT_COMMITTER_NAME': HUMAN[0], 'GIT_COMMITTER_EMAIL': HUMAN[1]})
expect('a merge commit, even with a sign-off line', 1, r'FAIL .*merge commit', rng(git('rev-parse', 'HEAD')))

# ---- Dependabot -----------------------------------------------------------------------------------------------------------
dmsg = 'Build(deps): Bump x\n\nSigned-off-by: dependabot[bot] <support@github.com>'
GH = ('GitHub', 'noreply@github.com')
dep_seq = 0


def dep_pr(num, commits, user='dependabot[bot]', head='o/r'):
    put('pulls/%d' % num, {'number': num, 'user': {'login': user}, 'head': {'repo': {'full_name': head}, 'sha': 'h%d' % num}, 'commits': len(commits)})
    put('pulls/%d/commits' % num, commits)


def dep_squash(label, pr_user='dependabot[bot]', head='o/r', extra=None):
    """A Dependabot commit on the default branch, as a squash merge of a pull request."""
    global dep_seq
    dep_seq += 1
    num = 300 + dep_seq
    sha = commit(dmsg + ' ' + label, author=DEP, committer=GH)
    put('commits/' + sha, api_commit(sha, dmsg, DEP, GH, 'dependabot[bot]', 'web-flow', a_type='Bot', sig=GPG))
    put('commits/%s/pulls' % sha, [{'number': num, 'merge_commit_sha': sha, 'merged_at': '2026-11-07T00:00:00Z'}])
    dep_pr(num, [api_commit('d' * 40, dmsg, DEP, GH, 'dependabot[bot]', 'web-flow', a_type='Bot', sig=GPG)] + (extra or []), user=pr_user, head=head)
    return sha


d = dep_squash('s1')
expect('the squash merge of a Dependabot pull request', 0, r'ok  .* dependabot \(squash of pull request #', rng(d))
d = dep_squash('s2', pr_user='mallory')
expect('a Dependabot-shaped squash commit whose pull request someone else opened', 1, r'opened by mallory, not by dependabot', rng(d))
d = dep_squash('s3', head='fork/r')
expect('a Dependabot-shaped squash commit whose pull request comes from a fork', 1, r'comes from fork/r', rng(d))
d = dep_squash('s4', extra=[api_commit('e' * 40, 'No line', HUMAN, HUMAN, 'ada', 'ada', sig='')])
expect('a Dependabot pull request with a commit of a human without a sign-off', 1, r'commits of pull request #.* fail', rng(d))
d = dep_squash('s6', extra=[api_commit('c' * 40, 'Account work', ACCT, ACCT, 'myriarch-bot', 'myriarch-bot')])
expect('a Dependabot pull request that holds a commit of the account', 1, r'commit of the machine account in a Dependabot pull request', rng(d))
d = dep_squash('s5', extra=[api_commit('f' * 40, 'Fix\n\nSigned-off-by: %s <%s>' % HUMAN, HUMAN, HUMAN, 'ada', 'ada', sig='')])
expect('a Dependabot pull request that a human added a signed-off commit to', 0, r'ok  .* dependabot', rng(d))
d = commit(dmsg + ' two', author=DEP, committer=GH)
put('commits/' + d, api_commit(d, dmsg, DEP, GH, 'dependabot[bot]', 'web-flow', a_type='User', sig=GPG))
expect('Dependabot as author, but GitHub says the author is a user', 1, r'does not confirm', rng(d))
d = commit(dmsg + ' three', author=DEP, committer=('GitHub', 'noreply@github.com'))
put('commits/' + d, api_commit(d, dmsg, DEP, ('GitHub', 'noreply@github.com'), 'dependabot[bot]', 'web-flow', a_type='Bot', verified=False, reason='unsigned', sig=''))
expect('a Dependabot commit without a valid signature', 1, r'does not confirm', rng(d))
d = commit(dmsg + ' four', author=DEP, committer=('GitHub', 'noreply@github.com'))
expect('a Dependabot commit with no API data', 1, r'GitHub data missing', rng(d))
d = commit('Fake\n\nSigned-off-by: dependabot[bot] <support@github.com>', author=('dependabot[bot]', 'x@example.test'))
expect('the name dependabot[bot] with another address', 1, r'FAIL', rng(d))

# ---- the machine account, in a range --------------------------------------------------------------------------------------
am = 'Account change'


def acct_commit(label, **kw):
    msg = am + ' ' + label
    sha = commit(msg, author=ACCT, committer=kw.pop('committer', ACCT))
    put('commits/' + sha, api_commit(sha, msg, ACCT, kw.pop('c_ident', ACCT), kw.pop('a_login', 'myriarch-bot'),
                                       kw.pop('c_login', 'myriarch-bot'), **kw))
    return sha


a = acct_commit('ok')
expect('a commit of the account pushed to the default branch itself (not squash-merged)', 1, r'pushed or rebased', rng(a))
a = acct_commit('unverified', verified=False, reason='unknown_key')
expect('the account, signature not verified', 1, r'not verified as valid', rng(a))
a = acct_commit('gpg', sig=GPG)
expect('the account, a GPG signature', 1, r'not an SSH signature', rng(a))
a = acct_commit('webflow', c_login='web-flow', c_ident=('GitHub', 'noreply@github.com'), sig=GPG)
expect('a commit by the account that GitHub made itself, no merged pull request', 1, r'GitHub data missing', rng(a))
a = acct_commit('stranger', c_login='ada')
expect('the account as author, someone else as committer', 1, r'committer is ada', rng(a))
a = acct_commit('nologin', a_login=None)
expect('the account address, but GitHub knows no author', 1, r'does not map it to that login', rng(a))
a = acct_commit('other-login', a_login='someone-else')
expect('the account address mapped to another login', 1, r'does not map it to that login', rng(a))
msg = am + ' signed\n\nSigned-off-by: Ada Example <ada@example.test>'
a = commit(msg, author=ACCT)
put('commits/' + a, api_commit(a, msg, ACCT, ACCT, 'myriarch-bot', 'myriarch-bot'))
expect('the account with any sign-off line', 1, r'has a Signed-off-by line', rng(a))
for label, line in (('without an address', 'Signed-off-by: Ada Example'),
                    ('with text after the address', 'Signed-off-by: Ada Example <ada@example.test> (agent)'),
                    ('with an empty address', 'Signed-off-by: Ada Example <>'),
                    ('with white space before the colon', 'Signed-off-by : Ada Example <ada@example.test>'),
                    ('in capitals', 'SIGNED-OFF-BY: Ada Example <ada@example.test>'),
                    ('in lower case and without a space', 'signed-off-by:Ada Example'),
                    ('indented', '   Signed-off-by: Ada Example <ada@example.test>'),
                    ('in the name of the account, without an address', 'Signed-off-by: myriarch-bot')):
    msg = am + ' mangled\n\n' + line
    a = commit(msg, author=ACCT)
    put('commits/' + a, api_commit(a, msg, ACCT, ACCT, 'myriarch-bot', 'myriarch-bot'))
    expect('the account with a sign-off line %s' % label, 1, r'has a Signed-off-by line|names the machine account', rng(a))
msg = 'Human address, account login'
a = commit(msg + '\n\nSigned-off-by: %s <%s>' % HUMAN, author=HUMAN)
expect('a human commit passes without any API call', 0, r'ok  .* sign-off', rng(a), PROVENANCE_FIXTURE_DIR=os.path.join(tmp, 'empty'))

# ---- a squash merge of a pull request of the account ---------------------------------------------------------------------
pr_seq = 0


def review(user='ada', state='APPROVED', commit_id=None, assoc='MEMBER', utype='User'):
    return {'user': {'login': user, 'type': utype}, 'state': state, 'commit_id': commit_id, 'author_association': assoc}


def squash_case(label, pr_user='myriarch-bot', head='o/r', pr_commit_kw=None, merged=True, n_prs=1, count=None, reviews='ok'):
    global pr_seq
    pr_seq += 1
    num = 100 + pr_seq
    sq = acct_commit(label, c_login='web-flow', c_ident=('GitHub', 'noreply@github.com'), sig=GPG)
    pc = 'pr commit ' + label
    pcs = commit(pc, author=ACCT)
    put('commits/%s/pulls' % sq, [{'number': num, 'merge_commit_sha': sq if merged else 'other', 'merged_at': '2026-11-07T00:00:00Z' if merged else None}] * n_prs)
    put('pulls/%d' % num, {'number': num, 'user': {'login': pr_user}, 'head': {'repo': {'full_name': head}, 'sha': 'h%d' % num}, 'commits': 1 if count is None else count})
    put('pulls/%d/reviews' % num, [review(commit_id='h%d' % num)] if reviews == 'ok' else reviews(num))
    kw = dict(a_login='myriarch-bot', c_login='myriarch-bot')
    kw.update(pr_commit_kw or {})
    put('pulls/%d/commits' % num, [api_commit(pcs, pc, ACCT, ACCT, kw['a_login'], kw['c_login'], **{k: v for k, v in kw.items() if k not in ('a_login', 'c_login')})])
    return sq


sq = squash_case('s1')
expect('a squash merge of a pull request of the account', 0, r'ok  .* account \(squash of pull request #', rng(sq))
sq = squash_case('s2', pr_user='ada')
expect('a squash merge of a pull request that someone else opened', 1, r'opened by ada', rng(sq))
sq = squash_case('s3', head='fork/r')
expect('a squash merge of a pull request from a fork', 1, r'comes from fork/r', rng(sq))
sq = squash_case('s4', pr_commit_kw={'sig': GPG})
expect('a squash merge with a GPG-signed commit in the pull request', 1, r'commits of pull request #.* fail', rng(sq))
sq = squash_case('s5', pr_commit_kw={'c_login': 'web-flow'})
expect('a squash merge with a commit in the pull request that GitHub made', 1, r'commits of pull request #.* fail', rng(sq))
sq = squash_case('s6', merged=False)
expect('no merged pull request points at the squash commit', 1, r'0 merged pull requests', rng(sq))
sq = squash_case('s7', n_prs=2)
expect('two pull requests point at the squash commit', 1, r'2 merged pull requests', rng(sq))
sq = squash_case('s8', count=2)
expect('fewer commits than the pull request has', 1, r'1 commits read, the pull request has 2', rng(sq))
sq = squash_case('s9', reviews=lambda n: [])
expect('a squash merge of a pull request nobody approved', 1, r'has no approval of the current head', rng(sq))
sq = squash_case('s10', reviews=lambda n: [review(commit_id='old')])
expect('a squash merge whose approval is for an older head', 1, r'has no approval of the current head', rng(sq))
sq = squash_case('s11', reviews=lambda n: [review('myriarch-bot', commit_id='h%d' % n, assoc='MEMBER')])
expect('a squash merge approved only by the account itself', 1, r'has no approval', rng(sq))
sq = squash_case('s12', reviews=lambda n: [review('mallory', commit_id='h%d' % n, assoc='NONE')])
expect('a squash merge approved only by a stranger', 1, r'has no approval', rng(sq))
sq = squash_case('s13', reviews=lambda n: [review('ada', commit_id='h%d' % n), review('ada', 'CHANGES_REQUESTED', 'h%d' % n)])
expect('a squash merge whose last review requests changes', 1, r'requests changes', rng(sq))
sq = squash_case('s14', reviews=lambda n: [review('ada', commit_id='h%d' % n), review('ada', 'COMMENTED', 'h%d' % n)])
expect('a later plain comment does not withdraw an approval', 0, r'ok  .* account \(squash', rng(sq))

# ---- a squash merge of a human pull request: GitHub sets the author from the profile -----------------------------------------
GHUB = ('GitHub', 'noreply@github.com')
hs_seq = 0


def human_squash_case(label, pr_commits_kw=None, merged=True, n_prs=1, count=None, verified=True):
    """The squash commit has the profile name and a noreply address, not the git identity of the commits in the pull request."""
    global hs_seq
    hs_seq += 1
    num = 500 + hs_seq
    prof = ('Ada E.', '7+ada@users.noreply.github.com')
    msg = 'Add a thing (#%d)\n\n* thing\n\nSigned-off-by: %s <%s>' % ((num,) + HUMAN)
    sq = commit(msg + ' ' + label, author=prof, committer=GHUB)
    put('commits/' + sq, api_commit(sq, msg, prof, GHUB, 'ada', 'web-flow', sig=GPG, verified=verified, reason='valid' if verified else 'unsigned'))
    put('commits/%s/pulls' % sq, [{'number': num, 'merge_commit_sha': sq if merged else 'other', 'merged_at': '2026-11-07T00:00:00Z'}] * n_prs)
    put('pulls/%d' % num, {'number': num, 'user': {'login': 'ada'}, 'head': {'repo': {'full_name': 'ada/r'}, 'sha': 'h%d' % num}, 'commits': 1 if count is None else count})
    pcm = 'thing\n\nSigned-off-by: %s <%s>' % HUMAN
    pc = api_commit('9' * 40, pcm, HUMAN, HUMAN, 'ada', 'ada', sig='')
    pc.update(pr_commits_kw or {})
    put('pulls/%d/commits' % num, [pc])
    return sq


sq = human_squash_case('h1')
expect('the squash of a human pull request from a fork, author is the profile', 0, r'ok  .* sign-off \(squash of pull request #', rng(sq))
sq = human_squash_case('h2', pr_commits_kw={'commit': {'message': 'thing', 'author': {'name': HUMAN[0], 'email': HUMAN[1]}, 'committer': {'name': HUMAN[0], 'email': HUMAN[1]}}})
expect('a human squash whose pull request holds a commit without a sign-off', 1, r'commits of pull request #.* fail', rng(sq))
sq = human_squash_case('h3', merged=False)
expect('a human squash that no merged pull request points at', 1, r'0 merged pull requests', rng(sq))
sq = human_squash_case('h4', n_prs=2)
expect('a human squash that two pull requests point at', 1, r'2 merged pull requests', rng(sq))
sq = human_squash_case('h5', verified=False)
expect('a human squash commit GitHub did not sign', 1, r'no Signed-off-by line that matches the author', rng(sq))
sq = human_squash_case('h6', count=2)
expect('a human squash whose pull request lists fewer commits than it has', 1, r'FAIL', rng(sq))

# ---- --pr ------------------------------------------------------------------------------------------------------------------
def pr_fixture(num, commits, user='myriarch-bot', head='o/r', count=None, reviews=True):
    put('pulls/%d' % num, {'number': num, 'user': {'login': user}, 'head': {'repo': {'full_name': head}, 'sha': 'h%d' % num}, 'commits': len(commits) if count is None else count})
    put('pulls/%d/commits' % num, commits)
    put('pulls/%d/reviews' % num, [review(commit_id='h%d' % num)] if reviews is True else (reviews or []))


h1 = 'Human work\n\nSigned-off-by: %s <%s>' % HUMAN
c_h = api_commit('h' * 40, h1, HUMAN, HUMAN, 'ada', 'ada', sig='')
c_a = api_commit('a' * 40, 'Account work', ACCT, ACCT, 'myriarch-bot', 'myriarch-bot')
pr_fixture(1, [c_h], user='ada')
expect('--pr: a human pull request with a sign-off', 0, r'1 commits checked, 0 failed', ['--pr', '1'])
pr_fixture(2, [c_a])
expect('--pr: a pull request of the account', 0, r'ok  .* account', ['--pr', '2'])
pr_fixture(3, [c_a], user='ada')
expect('--pr: commits of the account in a pull request that someone else opened', 1, r'opened by ada', ['--pr', '3'])
pr_fixture(4, [c_a], head='fork/r')
expect('--pr: commits of the account in a pull request from a fork', 1, r'comes from fork/r', ['--pr', '4'])
pr_fixture(5, [c_h, c_a], user='ada')
expect('--pr: commits of a human and of the account in a pull request of a human', 1, r'FAIL .*not a commit of the account[\s\S]*FAIL .*opened by ada', ['--pr', '5'])
pr_fixture(11, [c_a, c_h])
expect('--pr: a pull request of the account with a signed-off commit of a human', 1, r'ok  .* account[\s\S]*FAIL .*not a commit of the account', ['--pr', '11'])
pr_fixture(12, [c_h])
expect('--pr: a pull request opened by the account, but with only a human commit', 1, r'FAIL .*not a commit of the account', ['--pr', '12'])
pr_fixture(13, [c_a], reviews=[])
expect('--pr: a pull request of the account without any review', 1, r'FAIL .*has no approval of the current head', ['--pr', '13'])
pr_fixture(14, [c_a], reviews=[review(commit_id='old')])
expect('--pr: a pull request of the account approved for an older head', 1, r'has no approval of the current head', ['--pr', '14'])
pr_fixture(15, [c_a], reviews=[review('myriarch-bot', commit_id='h15')])
expect('--pr: a pull request of the account approved by the account', 1, r'has no approval', ['--pr', '15'])
pr_fixture(16, [c_a], reviews=[review('mallory', commit_id='h16', assoc='CONTRIBUTOR')])
expect('--pr: a pull request of the account approved by a contributor without a role', 1, r'has no approval', ['--pr', '16'])
pr_fixture(17, [c_a], reviews=[review('ada', commit_id='h17'), review('bob', 'CHANGES_REQUESTED', 'h17')])
expect('--pr: a pull request of the account with a review that requests changes', 1, r'requests changes', ['--pr', '17'])
pr_fixture(31, [c_a], reviews=[review('ada', commit_id='h31'), review('mallory', 'CHANGES_REQUESTED', 'h31', assoc='NONE')])
expect('--pr: a request for changes from a stranger does not block', 0, r'ok  .* account', ['--pr', '31'])
pr_fixture(32, [c_a], reviews=[review('gil', commit_id='h32', assoc='CONTRIBUTOR')])
put('collaborators/gil/permission', {'permission': 'admin', 'role_name': 'admin'})
expect('--pr: an approval by a reviewer whose membership is not shown, but who has write access', 0, r'ok  .* account', ['--pr', '32'])
pr_fixture(33, [c_a], reviews=[review('reader', commit_id='h33', assoc='NONE')])
put('collaborators/reader/permission', {'permission': 'read', 'role_name': 'read'})
expect('--pr: an approval by a reviewer with read access only', 1, r'has no approval', ['--pr', '33'])
c_d = api_commit('d' * 40, dmsg, DEP, GH, 'dependabot[bot]', 'web-flow', a_type='Bot', sig=GPG)
pr_fixture(18, [c_d], user='dependabot[bot]', reviews=[])
expect('--pr: a Dependabot pull request needs no review', 0, r'ok  .* dependabot', ['--pr', '18'])
pr_fixture(19, [c_d], user='mallory', head='fork/r', reviews=[])
expect('--pr: Dependabot-shaped commits in a pull request of a stranger from a fork', 1, r'FAIL .*opened by mallory, not by dependabot\[bot\]', ['--pr', '19'])
pr_fixture(20, [c_d], user='dependabot[bot]', head='fork/r', reviews=[])
expect('--pr: Dependabot-shaped commits in a Dependabot pull request from a fork', 1, r'comes from fork/r', ['--pr', '20'])
pr_fixture(21, [c_d, c_h], user='dependabot[bot]', reviews=[])
expect('--pr: a Dependabot pull request with a signed-off commit of a human', 0, r'2 commits checked, 0 failed', ['--pr', '21'])
c_w = api_commit('w' * 40, 'Update branch', HUMAN, ('GitHub', 'noreply@github.com'), 'ada', 'web-flow', sig=GPG)
pr_fixture(6, [c_w], user='ada')
expect('--pr: a commit with no sign-off line, made on GitHub', 1, r'FAIL', ['--pr', '6'])
c_m = api_commit('m' * 40, h1, HUMAN, HUMAN, 'ada', 'ada', parents=2)
pr_fixture(7, [c_m], user='ada')
expect('--pr: a merge commit', 1, r'merge commit', ['--pr', '7'])
c_x = api_commit('x' * 40, h1, HUMAN, HUMAN, 'myriarch-bot', 'myriarch-bot')
pr_fixture(8, [c_x], user='ada')
expect('--pr: a human sign-off on a commit that GitHub attributes to the account', 1, r'Signed-off-by line', ['--pr', '8'])
pr_fixture(9, [c_h], user='ada', count=2)
expect('--pr: fewer commits read than the pull request has', 1, r'1 commits read, the pull request has 2', ['--pr', '9'])
expect('--pr: an unknown pull request', 1, r'GitHub data missing', ['--pr', '99'])
put('pulls/10', {'number': 10, 'user': {'login': 'ada'}, 'head': {'repo': None}, 'commits': 0})
put('pulls/10/commits', [])
expect('--pr: a pull request with no commits', 1, r'no commits', ['--pr', '10'])

# ---- usage and environment ------------------------------------------------------------------------------------------------
expect('no arguments', 2, r'usage', [])
expect('an unknown option', 2, r'usage', ['--all', 'x'])
expect('an option as range', 2, r'looks like an option', ['--range', '--output=x'])
expect('a pull request number that is not a number', 2, r'not a number', ['--pr', '1;2'])
expect('GITHUB_REPOSITORY missing', 2, r'not owner/name', ['--pr', '1'], GITHUB_REPOSITORY=None)
expect('GITHUB_API_URL that is not https', 2, r'not an https URL', ['--pr', '1'], GITHUB_API_URL='http://127.0.0.1:1')
expect('a token with a newline', 2, r'unexpected format', ['--pr', '1'], GITHUB_TOKEN='abc\nurl = x')
expect('the fixtures are refused in GitHub Actions', 2, r'refused in GitHub Actions', ['--pr', '1'], GITHUB_ACTIONS='true')
expect('an unknown revision range', 2, r'git rev-list failed', ['--range', 'no-such-ref..HEAD'])
expect('PROVENANCE_EXCEPTIONS is refused in GitHub Actions', 2, r'refused in GitHub Actions', ['--range', 'HEAD'], GITHUB_ACTIONS='true', PROVENANCE_FIXTURE_DIR=None)

# ---- recorded exceptions (--range only) ------------------------------------------------------------------------------------
bad = commit('Cannot be fixed, it is on the default branch')
put('commits/' + bad, api_commit(bad, 'x', HUMAN, HUMAN, 'ada', 'ada', sig=''))
expect('a commit without provenance fails', 1, r'FAIL', rng(bad))
exc = os.path.join(tmp, 'exceptions.txt')
with open(exc, 'w', encoding='utf-8') as fh:
    fh.write('# comment\n\n%s accepted by the maintainer, see the pull request\n' % bad)
expect('the same commit passes when it is named in the exceptions file', 0, r'ok  .* recorded exception.*accepted by the maintainer', rng(bad))
other_bad = commit('Another one')
expect('another commit is not covered by the entry', 1, r'FAIL', rng(other_bad))
with open(exc, 'w', encoding='utf-8') as fh:
    fh.write('%s\n' % bad)
expect('an entry without a reason is an error', 2, r'line 1: expected', rng(bad))
with open(exc, 'w', encoding='utf-8') as fh:
    fh.write('%s reason\n' % bad[:12])
expect('an entry with a short SHA is an error', 2, r'line 1: expected', rng(bad))
os.unlink(exc)
expect('a missing exceptions file is an error', 2, r'cannot read the exceptions file', rng(bad))
with open(exc, 'w', encoding='utf-8') as fh:
    fh.write('# none\n')
pr_fixture(30, [c_h], user='ada')
expect('the exceptions do not apply to --pr', 0, r'1 commits checked, 0 failed', ['--pr', '30'], PROVENANCE_EXCEPTIONS=os.path.join(tmp, 'missing.txt'))
expect('PROVENANCE_ACCOUNT overrides the account', 1, r'FAIL', ['--pr', '2'], PROVENANCE_ACCOUNT='other-bot')

print('RESULT %d %d' % (passed, failed))
sys.exit(1 if failed else 0)
PY
)"; then
  printf '%s\n' "${PROVENANCE_RESULT}" | grep -v '^RESULT '
  PASS=$((PASS + $(printf '%s\n' "${PROVENANCE_RESULT}" | sed -n 's/^RESULT \([0-9]*\) .*/\1/p')))
else
  printf '%s\n' "${PROVENANCE_RESULT}" | grep -v '^RESULT '
  PASS=$((PASS + $(printf '%s\n' "${PROVENANCE_RESULT}" | sed -n 's/^RESULT \([0-9]*\) .*/\1/p')))
  FAIL=$((FAIL + 1))
  fail "the test cases above failed"
fi

finish_tests commit-provenance-test
