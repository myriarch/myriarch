#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Finds pinned tools that a shell text calls by their bare name (ADR-0035).

The release workflow never writes GITHUB_PATH, so the pinned tools are reached only through TOOLS_BIN. Two tests use this one
tokenizer, so that they cannot drift apart: ci/tests/release-scripts-test.sh (the release scripts and deploy/release/*.sh) and
ci/tests/release-workflow-test.sh (the run texts of .github/workflows/release.yml).

    python3 -I ci/tests/bare-tools.py FILE...        prints "FILE:LINE: ..." per finding, exit 1 if there is any

The tokenizer is line based and knows the shell only as far as it matters here. A tool is called by its bare name when the word
is in command position, which is:

  - at the start of a line, after ; ;; && || | & ( $( ` and the group opener `{`, and after the keywords then do else elif if while
    until and !;
  - after any number of NAME=value words and of the prefixes exec, env (with its options and assignments), command, builtin, time,
    nohup, xargs, sudo and nice (with their options), because those start the tool just the same.

A lookup (have, need_cmd, command -v, which, type -P) of a pinned tool is a finding as well. Not code: comments, quoted text (a
command substitution inside double quotes is code), here documents, case labels (the name is followed by ) or |), a name that is
followed by == != in or =, and ${{ }} expressions.
"""

import re
import sys

NAMES = "syft yq goreleaser go-winres osslsigncode cosign crane gh kind kubectl helm".split()

OPENERS = {";", ";;", "&&", "||", "|", "&", "(", "$(", "`", "{"}
KEYWORDS = {"then", "do", "else", "elif", "if", "while", "until", "!"}
# words that start another command; the options of these (words starting with -) are skipped as well
WRAPPERS = {"exec", "env", "command", "builtin", "time", "nohup", "xargs", "sudo", "nice"}
ASSIGNMENT = re.compile(r"[A-Za-z_][A-Za-z0-9_]*\+?=")
TOKEN = re.compile(r"\$\(|;;|&&|\|\||[;&|()`]|[^\s;&|()`]+")
HEREDOC = re.compile(r"(?<!<)<<(?!<)-?\s*['\"]?([A-Za-z_][A-Za-z0-9_]*)['\"]?")


def code_of(line):
    """The line without quoted text, except the command substitutions inside double quotes, and without ${{ }} expressions."""
    line = re.sub(r"\$\{\{.*?\}\}", "X", line)
    line = re.sub(r"'[^']*'", "''", line)
    subs = []
    line = re.sub(r'"((?:[^"\\]|\\.)*)"', lambda m: (subs.extend(re.findall(r"\$\(([^()]*)\)", m.group(1))) or '""'), line)
    return line + " ; " + " ; ".join(subs) if subs else line


def bare_in(code, names=NAMES):
    """The pinned tools that the code of one logical line calls by their bare name."""
    found = []
    alt = "|".join(re.escape(n) for n in names)
    helper = re.compile(r"\b(?:have|need_cmd|command\s+-v|which|type\s+-P)\s+(?:-\w+\s+)*(%s)\b" % alt)
    found.extend(m.group(1) for m in helper.finditer(code))
    expect = True  # at the start of a line a command can begin
    in_wrapper = False
    for m in TOKEN.finditer(code):
        word = m.group(0)
        if word in OPENERS:
            expect, in_wrapper = True, False
            continue
        if not expect:
            continue
        if word in KEYWORDS:
            in_wrapper = False
            continue
        if ASSIGNMENT.match(word):
            continue
        if word in WRAPPERS:
            in_wrapper = True
            continue
        if in_wrapper and word.startswith("-"):
            continue
        if word in names:
            rest = code[m.end():]
            # a case label (cosign|crane) or cosign)) ends right after the name; a comparison or a loop head follows with a space
            if (not rest or rest[0].isspace()) and not re.match(r"\s*(?:==|!=|=|in\b)", rest):
                found.append(word)
        expect = False
    return found


def scan_text(text, names=NAMES):
    """(line number, tool) of every bare call in a shell text. Lines that end with a backslash are joined with the next one."""
    findings = []
    in_here = None
    pending = ""
    pending_no = 0
    for no, raw in enumerate(text.split("\n"), 1):
        line = raw.strip()
        if in_here is not None:
            if line == in_here:
                in_here = None
            continue
        if line.startswith("#") and not pending:
            continue
        if not pending:
            pending_no = no
        if line.endswith("\\"):
            pending += line[:-1] + " "
            continue
        logical, pending = pending + line, ""
        m = HEREDOC.search(logical)
        for name in dict.fromkeys(bare_in(code_of(logical), names)):
            findings.append((pending_no, name))
        if m:
            in_here = m.group(1)
    return findings


def main(paths):
    bad = 0
    for path in paths:
        with open(path, encoding="utf-8") as f:
            for no, name in scan_text(f.read()):
                print("%s:%d: the pinned tool %s is called by its bare name" % (path, no, name))
                bad += 1
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
