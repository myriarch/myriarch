# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Evaluates the JSON output of govulncheck (ci/vulncheck.sh).

usage: vulncheck-report.py ALLOWLIST OUTPUT.json...

A finding is called when its first trace frame names a function. Called findings that are not in the allowlist make the
exit status 1. Findings at module or package level are printed. The allowlist has one entry per line:

    GO-2026-0000 | review 2027-01-31 | reason

Empty lines and lines that start with # are ignored. An entry whose review date has passed is a warning.
"""
import datetime
import json
import re
import sys


def read_stream(path):
    """Yields the JSON values of a govulncheck stream (pretty-printed objects, concatenated)."""
    text = open(path, encoding="utf-8").read()
    decoder = json.JSONDecoder()
    pos = 0
    while True:
        while pos < len(text) and text[pos].isspace():
            pos += 1
        if pos >= len(text):
            return
        value, pos = decoder.raw_decode(text, pos)
        yield value


def read_allowlist(path):
    entries = {}
    for number, raw in enumerate(open(path, encoding="utf-8"), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        parts = [p.strip() for p in line.split("|", 2)]
        m = re.fullmatch(r"review (\d{4}-\d{2}-\d{2})", parts[1]) if len(parts) == 3 else None
        if not re.fullmatch(r"GO-\d{4}-\d{4,}|CVE-\d{4}-\d+|GHSA-[0-9a-z-]+", parts[0]) or not m or not parts[2]:
            sys.exit(f"vulncheck: {path}:{number}: expected 'ID | review YYYY-MM-DD | reason'")
        entries[parts[0]] = (datetime.date.fromisoformat(m.group(1)), parts[2])
    return entries


def main():
    allow = read_allowlist(sys.argv[1])
    called, other = {}, {}
    osv_summary = {}
    for path in sys.argv[2:]:
        for msg in read_stream(path):
            if "osv" in msg:
                osv = msg["osv"]
                osv_summary[osv["id"]] = osv.get("summary", "")
            elif "finding" in msg:
                f = msg["finding"]
                trace = f.get("trace") or [{}]
                frame = trace[0]
                key = f["osv"]
                if frame.get("function"):
                    called.setdefault(key, (frame.get("package", ""), f.get("fixed_version", "")))
                else:
                    other.setdefault(key, (frame.get("module", ""), f.get("fixed_version", "")))
    today = datetime.date.today()
    failed = False
    for key, (pkg, fixed) in sorted(called.items()):
        if key in allow:
            print(f"vulncheck: {key} is called (package {pkg}) and accepted in the allowlist: {allow[key][1]}")
        else:
            failed = True
            print(f"vulncheck: ERROR {key} is called by the code (package {pkg}, fixed in {fixed or 'no fixed version'}): "
                  f"{osv_summary.get(key, '')}", file=sys.stderr)
    for key, (module, fixed) in sorted(other.items()):
        if key in called:
            continue
        note = allow[key][1] if key in allow else "not in the allowlist"
        print(f"vulncheck: {key} affects the required module {module}; the code does not call it ({note})")
    for key, (review, reason) in sorted(allow.items()):
        if review < today:
            print(f"vulncheck: WARNING the allowlist entry {key} is past its review date {review}: {reason}", file=sys.stderr)
        if key not in called and key not in other:
            print(f"vulncheck: note: the allowlist entry {key} matches no finding any more; remove it")
    if failed:
        sys.exit(1)
    print(f"vulncheck: no called vulnerabilities ({len(other)} module-level finding(s))")


main()
