# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Prepares the SARIF output of govulncheck for GitHub code scanning (ci/vulncheck.sh --sarif, ci/vulncheck-binaries.sh).

usage:
  vulncheck-sarif.py filter ALLOWLIST IN.sarif OUT.sarif
  vulncheck-sarif.py binaries ALLOWLIST OUT.sarif RELEASE [LABEL=IN.sarif ...]

filter     Source scan. Writes the report without the findings that ci/vulncheck-allowlist.txt accepts (the same file and the same
           IDs as the gate of ci/vulncheck.sh), so code scanning shows what the project has not decided about. A finding that has no
           location or no fingerprint gets one, because code scanning needs both to keep an alert apart from the others.

binaries   Scan of the released binaries. govulncheck's binary mode reports a finding without any file or line (code scanning refuses a
           result without a location) and one run per binary. This command merges the reports of all binaries (LABEL names the binary,
           IN.sarif is its report) into one run with one result per vulnerability that lists the affected binaries, and anchors the
           result at go.mod, the file in which the source scan puts a finding about a module. Accepted findings are left out as above.
           Without any LABEL=IN.sarif the report holds no result (nothing was found, or there is no release to scan).

The reports are data from a tool of this repository. A file that is not a SARIF 2.1.0 log with a tool driver in every run is refused.
Exit status: 0 = written, 1 = refused input or unreadable file, 2 = usage error.
"""
import hashlib
import importlib.util
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
LEVELS = ["none", "note", "warning", "error"]  # ascending
ANCHOR_URI = "go.mod"
MAX_TEXT = 1000  # code scanning shows a message as the title of an alert; keep it short
SOURCE_KEY = "govulncheck/v1"
BINARY_KEY = "govulncheck-binaries/v1"
SARIF_SCHEMA = "https://json.schemastore.org/sarif-2.1.0.json"


def die(message):
    sys.exit("vulncheck-sarif: " + message)


def read_allowlist(path):
    """The accepted IDs of ci/vulncheck-allowlist.txt, read by the code of the gate (ci/vulncheck-report.py)."""
    sys.dont_write_bytecode = True
    spec = importlib.util.spec_from_file_location("vulncheck_report", os.path.join(HERE, "vulncheck-report.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.read_allowlist(path)


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


def rank(level):
    return LEVELS.index(level) if level in LEVELS else LEVELS.index("warning")


def shorten(items, fits):
    """The longest prefix of items for which fits(prefix, rest) holds, as (prefix, rest count)."""
    for n in range(len(items), -1, -1):
        if fits(items[:n], len(items) - n):
            return items[:n], len(items) - n
    return [], len(items)


def cmd_filter(allowlist_path, src, dst):
    allow = read_allowlist(allowlist_path)
    doc = read_sarif(src)
    kept_count, accepted = 0, []
    for run in doc["runs"]:
        driver = run["tool"]["driver"]
        kept = []
        for result in run.get("results") or []:
            rid = result.get("ruleId", "")
            if rid in allow:
                accepted.append(rid)
                continue
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
    note = ""
    if accepted:
        note = "; accepted in the allowlist and left out: %s" % ", ".join(sorted(set(accepted)))
    print("vulncheck-sarif: %s: %d finding(s) for code scanning%s" % (os.path.basename(dst), kept_count, note), file=sys.stderr)


def modules_of(result):
    mods = set()
    for flow in result.get("codeFlows") or []:
        for thread in flow.get("threadFlows") or []:
            for loc in thread.get("locations") or []:
                if isinstance(loc.get("module"), str) and loc["module"]:
                    mods.add(loc["module"])
    return mods


def compose(rid, release, names, total, mods):
    """The message of a result of the binary scan, at most MAX_TEXT characters; long lists are cut with a count."""
    def build(shown_names, more_names, shown_mods, more_mods):
        listing = ", ".join(shown_names) + (", and %d more" % more_names if more_names else "")
        modules = ", ".join(shown_mods) + (", and %d more" % more_mods if more_mods else "")
        return (
            "%s affects %d of the %d binaries of the release %s (%s). Vulnerable modules: %s. govulncheck found the vulnerable symbols "
            "in the binaries; in binary mode it does not tell whether the program reaches them."
            % (rid, len(names), total, release, listing, modules or "none named")
        )

    shown_mods, more_mods = shorten(mods, lambda p, rest: len(build([], 0, p, rest)) <= MAX_TEXT // 2)
    shown_names, more_names = shorten(names, lambda p, rest: len(build(p, rest, shown_mods, more_mods)) <= MAX_TEXT)
    return build(shown_names, more_names, shown_mods, more_mods)


def cmd_binaries(allowlist_path, dst, release, labelled):
    allow = read_allowlist(allowlist_path)
    base_driver = None
    found = {}
    accepted = {}
    for label, path in labelled:
        for run in read_sarif(path)["runs"]:
            driver = run["tool"]["driver"]
            if base_driver is None:
                base_driver = driver
            rules = {rule.get("id"): rule for rule in driver.get("rules") or [] if isinstance(rule, dict)}
            for result in run.get("results") or []:
                rid = result.get("ruleId", "")
                if rid in allow:
                    accepted.setdefault(rid, set()).add(label)
                    continue
                entry = found.setdefault(rid, {"rule": rules.get(rid), "level": "none", "labels": set(), "modules": set()})
                entry["labels"].add(label)
                entry["modules"] |= modules_of(result)
                if entry["rule"] is None:
                    entry["rule"] = rules.get(rid)
                level = result.get("level") or "warning"
                if rank(level) > rank(entry["level"]):
                    entry["level"] = level
    results, rules_out = [], []
    for rid in sorted(found):
        entry = found[rid]
        names = sorted(entry["labels"])
        results.append({
            "ruleId": rid,
            "level": entry["level"] if entry["level"] != "none" else "warning",
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
    note = ""
    if accepted:
        note = "; accepted in the allowlist and left out: %s" % ", ".join(sorted(accepted))
    print("vulncheck-sarif: %s: %d finding(s) in %d binaries of %s%s" % (os.path.basename(dst), len(results), len(labelled), release, note), file=sys.stderr)


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
