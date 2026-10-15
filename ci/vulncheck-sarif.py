# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Prepares the SARIF output of govulncheck for GitHub code scanning (ci/vulncheck.sh --sarif, ci/vulncheck-binaries.sh).

usage:
  vulncheck-sarif.py filter ALLOWLIST IN.sarif OUT.sarif
  vulncheck-sarif.py binaries ALLOWLIST OUT.sarif RELEASE [LABEL=IN.sarif ...]

filter     Source scan. Writes the report without the findings that ci/vulncheck-allowlist.txt accepts (the same file and the same
           IDs as the gate of ci/vulncheck.sh), so code scanning shows what the project has not decided about. An entry accepts
           until its review date has passed: the day after, the finding is reported again, so a lapsed acceptance becomes an alert
           (the gate only warns). A finding that has no location or no fingerprint gets one, because code scanning needs both to
           keep an alert apart from the others.

binaries   Scan of the released binaries. govulncheck's binary mode reports a finding without any file or line (code scanning refuses a
           result without a location) and one run per binary. This command merges the reports of all binaries (LABEL names the binary,
           IN.sarif is its report) into one run with one result per vulnerability that lists the affected binaries, and anchors the
           result at go.mod, the file in which the source scan puts a finding about a module. Accepted findings are left out as above
           (while the review date of the entry has not passed).
           Without any LABEL=IN.sarif the report holds no result (nothing was found, or there is no release to scan).
           The reports must be those of a scan at module level (govulncheck -mode binary -scan module, as ci/vulncheck-binaries.sh runs
           it); a report of another mode or level is refused. The release binaries are stripped (-s -w), so govulncheck cannot read the
           symbols of a program, and a result means that a binary was built with a module or Go version that the advisory affects, not
           that it contains or calls the vulnerable code. govulncheck calls every finding of a module-level scan an error, because it is
           the strongest statement that level allows; the merged result is a warning.

The reports are data from a tool of this repository. A file that is not a SARIF 2.1.0 log with a tool driver in every run is refused.
Exit status: 0 = written, 1 = refused input or unreadable file, 2 = usage error.
"""
import datetime
import hashlib
import importlib.util
import json
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
BINARY_LEVEL = "warning"  # the level of every result about the binaries: a scan at module level cannot show that a program reaches the code
ANCHOR_URI = "go.mod"
MAX_TEXT = 1000  # code scanning shows a message as the title of an alert; keep it short
SOURCE_KEY = "govulncheck/v1"
BINARY_KEY = "govulncheck-binaries/v1"
SARIF_SCHEMA = "https://json.schemastore.org/sarif-2.1.0.json"


def die(message):
    sys.exit("vulncheck-sarif: " + message)


def gate():
    """The code of the gate (ci/vulncheck-report.py): the reports read the allowlist, and decide when an entry has lapsed, as the gate does."""
    sys.dont_write_bytecode = True
    spec = importlib.util.spec_from_file_location("vulncheck_report", os.path.join(HERE, "vulncheck-report.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def read_acceptances(path, today=None):
    """(IDs in force, lapsed): the IDs of ci/vulncheck-allowlist.txt whose acceptance holds on today (default: the current day), and the
    IDs whose review date has passed with that date. The second kind accepts nothing: its finding stays in the report."""
    module = gate()
    today = today or datetime.date.today()
    in_force, lapsed = set(), {}
    for rid, (review, _reason) in module.read_allowlist(path).items():
        if module.past_review(review, today):
            lapsed[rid] = review
        else:
            in_force.add(rid)
    return in_force, lapsed


def describe(accepted, lapsed_seen):
    """The tail of the log line: what the allowlist left out, and what stayed in the report because its entry has lapsed."""
    note = ""
    if accepted:
        note += "; accepted in the allowlist and left out: %s" % ", ".join(sorted(accepted))
    if lapsed_seen:
        note += "; kept although the allowlist names them, because the review date has passed: %s" % ", ".join(
            "%s (%s)" % (rid, lapsed_seen[rid]) for rid in sorted(lapsed_seen))
    return note


def read_sarif(path):
    try:
        with open(path, encoding="utf-8") as f:
            doc = json.load(f)
    except (OSError, ValueError) as e:
        die("%s is not a readable SARIF file: %s" % (path, e))
    if not isinstance(doc, dict) or doc.get("version") != "2.1.0" or not isinstance(doc.get("runs"), list):
        die("%s is not a SARIF 2.1.0 log" % path)
    for run in doc["runs"]:
        driver = ((run.get("tool") or {}).get("driver")) if isinstance(run, dict) else None
        if not isinstance(driver, dict):
            die("%s has a run without tool.driver" % path)
    return doc


def write_sarif(path, doc):
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(doc, f, indent=2, ensure_ascii=False)
        f.write("\n")
    os.replace(tmp, path)


def anchor(text):
    return {
        "physicalLocation": {
            "artifactLocation": {"uri": ANCHOR_URI, "uriBaseId": "%SRCROOT%"},
            "region": {"startLine": 1},
        },
        "message": {"text": text},
    }


def location_key(result):
    """(uri, line) of the first location of a result, or None when it has none."""
    locations = result.get("locations")
    if not isinstance(locations, list) or not locations or not isinstance(locations[0], dict):
        return None
    physical = locations[0].get("physicalLocation") or {}
    uri = (physical.get("artifactLocation") or {}).get("uri")
    if not isinstance(uri, str) or not uri:
        return None
    return uri, (physical.get("region") or {}).get("startLine", 1)


def fingerprint(*parts):
    return hashlib.sha256("\0".join(str(p) for p in parts).encode("utf-8")).hexdigest()[:32]


def shorten(items, fits):
    """The longest prefix of items for which fits(prefix, rest) holds, as (prefix, rest count)."""
    for n in range(len(items), -1, -1):
        if fits(items[:n], len(items) - n):
            return items[:n], len(items) - n
    return [], len(items)


def cmd_filter(allowlist_path, src, dst):
    in_force, lapsed = read_acceptances(allowlist_path)
    doc = read_sarif(src)
    kept_count, accepted, lapsed_seen = 0, set(), {}
    for run in doc["runs"]:
        driver = run["tool"]["driver"]
        kept = []
        for result in run.get("results") or []:
            rid = result.get("ruleId", "")
            if rid in in_force:
                accepted.add(rid)
                continue
            if rid in lapsed:
                lapsed_seen[rid] = lapsed[rid]
            key = location_key(result)
            if key is None:
                result["locations"] = [anchor("Findings for vulnerability %s" % rid)]
                key = location_key(result)
            if "partialFingerprints" not in result:
                result["partialFingerprints"] = {SOURCE_KEY: fingerprint(rid, key[0], key[1])}
            kept.append(result)
        used = {r.get("ruleId") for r in kept}
        driver["rules"] = [rule for rule in driver.get("rules") or [] if rule.get("id") in used]
        run["results"] = kept
        kept_count += len(kept)
    write_sarif(dst, doc)
    print("vulncheck-sarif: %s: %d finding(s) for code scanning%s" % (os.path.basename(dst), kept_count, describe(accepted, lapsed_seen)), file=sys.stderr)


MODULES_RE = re.compile(r"depends on \d+ vulnerable modules? \(([^()]*)\)")


def modules_of(result):
    """The modules that govulncheck names in the message of a result of a module-level scan ("Your code depends on 2 vulnerable modules
    (a and b). ..."). A module path contains neither a space nor a comma, so the list is split at both and the word "and" is dropped.
    stdlib is the Go version of the build. A message in another form names no module."""
    text = (result.get("message") or {}).get("text")
    found = MODULES_RE.search(text) if isinstance(text, str) else None
    if not found:
        return set()
    return {word for word in re.split(r"[\s,]+", found.group(1)) if word and word != "and"}


def compose(rid, release, names, total, mods):
    """The message of a result of the binary scan, at most MAX_TEXT characters; long lists are cut with a count."""
    def build(shown_names, more_names, shown_mods, more_mods):
        listing = ", ".join(shown_names) + (", and %d more" % more_names if more_names else "")
        modules = ", ".join(shown_mods) + (", and %d more" % more_mods if more_mods else "")
        return (
            "%s affects %d of the %d binaries of the release %s (%s). Affected modules: %s (stdlib is the Go version of the build). "
            "The release binaries are stripped, so the scan works at module level: it shows that a binary was built with an affected "
            "version, not that it contains or calls the vulnerable code."
            % (rid, len(names), total, release, listing, modules or "not named by govulncheck")
        )

    shown_mods, more_mods = shorten(mods, lambda p, rest: len(build([], 0, p, rest)) <= MAX_TEXT // 2)
    shown_names, more_names = shorten(names, lambda p, rest: len(build(p, rest, shown_mods, more_mods)) <= MAX_TEXT)
    return build(shown_names, more_names, shown_mods, more_mods)


def cmd_binaries(allowlist_path, dst, release, labelled):
    in_force, lapsed = read_acceptances(allowlist_path)
    base_driver = None
    found = {}
    accepted = set()
    lapsed_seen = {}
    for label, path in labelled:
        for run in read_sarif(path)["runs"]:
            driver = run["tool"]["driver"]
            config = driver.get("properties") if isinstance(driver.get("properties"), dict) else {}
            if config.get("scan_mode") != "binary" or config.get("scan_level") != "module":
                die("%s is not the report of govulncheck -mode binary -scan module (scan_mode %r, scan_level %r)"
                    % (path, config.get("scan_mode"), config.get("scan_level")))
            if base_driver is None:
                base_driver = driver
            rules = {rule.get("id"): rule for rule in driver.get("rules") or [] if isinstance(rule, dict)}
            for result in run.get("results") or []:
                rid = result.get("ruleId", "")
                if rid in in_force:
                    accepted.add(rid)
                    continue
                if rid in lapsed:
                    lapsed_seen[rid] = lapsed[rid]
                entry = found.setdefault(rid, {"rule": rules.get(rid), "labels": set(), "modules": set()})
                entry["labels"].add(label)
                entry["modules"] |= modules_of(result)
                if entry["rule"] is None:
                    entry["rule"] = rules.get(rid)
    results, rules_out = [], []
    for rid in sorted(found):
        entry = found[rid]
        names = sorted(entry["labels"])
        results.append({
            "ruleId": rid,
            "level": BINARY_LEVEL,
            "message": {"text": compose(rid, release, names, len(labelled), sorted(entry["modules"]))},
            "locations": [anchor("Anchor of a finding in the released binaries of %s; the vulnerable code is in the binaries, not in this file" % release)],
            "partialFingerprints": {BINARY_KEY: fingerprint(rid)},
            "properties": {"release": release, "binaries": names},
        })
        rules_out.append(entry["rule"] or {"id": rid})
    driver = {k: v for k, v in (base_driver or {}).items() if k != "rules"}
    driver.setdefault("name", "govulncheck")
    driver.setdefault("informationUri", "https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck")
    properties = dict(driver.get("properties") or {})
    properties.update({"scan_mode": "binary", "release": release, "scanned_binaries": len(labelled)})
    driver["properties"] = properties
    driver["rules"] = rules_out
    write_sarif(dst, {"version": "2.1.0", "$schema": SARIF_SCHEMA, "runs": [{"tool": {"driver": driver}, "results": results}]})
    print("vulncheck-sarif: %s: %d finding(s) in %d binaries of %s%s"
          % (os.path.basename(dst), len(results), len(labelled), release, describe(accepted, lapsed_seen)), file=sys.stderr)


def main(argv):
    if len(argv) == 5 and argv[1] == "filter":
        cmd_filter(argv[2], argv[3], argv[4])
    elif len(argv) >= 5 and argv[1] == "binaries":
        labelled = []
        for arg in argv[5:]:
            label, sep, path = arg.partition("=")
            if not sep or not label or not path:
                die("expected LABEL=IN.sarif, got %r" % arg)
            labelled.append((label, path))
        cmd_binaries(argv[2], argv[3], argv[4], labelled)
    else:
        print("usage: vulncheck-sarif.py filter ALLOWLIST IN.sarif OUT.sarif | binaries ALLOWLIST OUT.sarif RELEASE [LABEL=IN.sarif ...]", file=sys.stderr)
        sys.exit(2)


if __name__ == "__main__":
    main(sys.argv)
