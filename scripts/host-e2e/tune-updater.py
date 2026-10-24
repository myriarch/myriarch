#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Shortens the waits of the updater for the host end-to-end test: health timeout 40 s instead of 120 s, a check every 2 s instead
of 5 s, a retry of a failed version after 15 s instead of 10 min, so that the rollback and the retry of a scenario take minutes and
not half an hour. It edits the `update:` block of one updater.yaml in place (a template of the checkout or the configuration of the
installed updater) and changes nothing else.

    python3 -I tune-updater.py PATH
"""

import re
import sys

path = sys.argv[1]
text = open(path).read()
if not re.search(r"^update:[ \t]*\n", text, re.M):
    sys.exit("no update: block in " + path)
for key, value in (("healthTimeout", "40s"), ("healthInterval", "2s"), ("failedRetryBackoff", "15s")):
    pattern = re.compile(r"^(  " + key + r":).*$", re.M)
    if pattern.search(text):
        text = pattern.sub(lambda m: m.group(1) + " " + value, text, count=1)
    else:
        text = re.sub(r"^(update:[ \t]*\n)", lambda m: m.group(1) + "  " + key + ": " + value + "\n", text, count=1, flags=re.M)
open(path, "w").write(text)
