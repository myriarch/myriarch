#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Reads Lighthouse JSON reports and fails when a category score is below a minimum (acceptance check of the website, ADR-0031).

    python3 -I website/scripts/lighthouse-scores.py [--min N] [--categories a,b,...] REPORT.json|DIR ...

Lighthouse is not part of the lockfile (it needs Node.js 22.19 or newer); run it on the production build, for example

    make website-preview &      # serves website/public on 127.0.0.1:1313
    pnpm dlx lighthouse@13.5.0 http://127.0.0.1:1313/ --output=json --output-path=lh-home.json \\
        --chrome-flags="--headless=new" --only-categories=performance,accessibility,best-practices,seo

A directory argument stands for every *.json file in it. --min is the lowest accepted score on the scale 0 to 100 (default 90),
--categories limits the categories (default: performance, accessibility, best-practices, seo). Output: one line per report with
the scores; exit status 0 = all scores at least the minimum, 1 = a score below it or a missing category, 2 = usage error.
Python 3.9 compatible, standard library only.
"""

import json
import os
import sys

DEFAULT_CATEGORIES = ("performance", "accessibility", "best-practices", "seo")


def collect(paths):
    files = []
    for p in paths:
        if os.path.isdir(p):
            files.extend(os.path.join(p, n) for n in sorted(os.listdir(p)) if n.endswith(".json"))
        else:
            files.append(p)
    return files


def scores_of(report, categories):
    """Returns {category: score 0-100 or None} of a parsed Lighthouse report."""
    cats = report.get("categories", {})
    out = {}
    for c in categories:
        score = cats.get(c, {}).get("score")
        out[c] = None if score is None else round(score * 100)
    return out


def check(files, minimum, categories):
    """Returns (lines to print, number of failures)."""
    lines, failures = [], 0
    for f in files:
        try:
            with open(f, "r", encoding="utf-8") as fh:
                report = json.load(fh)
        except (OSError, ValueError) as exc:
            lines.append("%s: cannot read the report (%s)" % (os.path.basename(f), type(exc).__name__))
            failures += 1
            continue
        url = report.get("finalDisplayedUrl") or report.get("finalUrl") or report.get("requestedUrl") or "?"
        scores = scores_of(report, categories)
        bad = [c for c, s in scores.items() if s is None or s < minimum]
        failures += len(bad)
        text = " ".join("%s=%s" % (c, "missing" if s is None else s) for c, s in scores.items())
        lines.append("%s %s: %s%s" % (os.path.basename(f), url, text, "  BELOW %d: %s" % (minimum, ", ".join(bad)) if bad else ""))
    return lines, failures


def main(argv):
    minimum, categories, paths = 90, DEFAULT_CATEGORIES, []
    i = 0
    while i < len(argv):
        a = argv[i]
        if a == "--min" and i + 1 < len(argv):
            try:
                minimum = int(argv[i + 1])
            except ValueError:
                print("lighthouse-scores: --min needs a whole number", file=sys.stderr)
                return 2
            i += 2
        elif a == "--categories" and i + 1 < len(argv):
            categories = tuple(c for c in argv[i + 1].split(",") if c)
            i += 2
        elif a.startswith("-"):
            print("usage: python3 -I website/scripts/lighthouse-scores.py [--min N] [--categories a,b] REPORT.json|DIR ...", file=sys.stderr)
            return 2
        else:
            paths.append(a)
            i += 1
    if not paths or not categories:
        print("usage: python3 -I website/scripts/lighthouse-scores.py [--min N] [--categories a,b] REPORT.json|DIR ...", file=sys.stderr)
        return 2
    files = collect(paths)
    if not files:
        print("lighthouse-scores: no report found", file=sys.stderr)
        return 2
    lines, failures = check(files, minimum, categories)
    for line in lines:
        print(line)
    print("lighthouse-scores: %d reports, %d scores below %d" % (len(files), failures, minimum))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
