# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Prepares the SARIF output of govulncheck for GitHub code scanning (ci/vulncheck.sh --sarif).

usage:
  vulncheck-sarif.py filter ALLOWLIST IN.sarif OUT.sarif

filter     Source scan. Writes the report without the findings that ci/vulncheck-allowlist.txt accepts (the same file and the same
           IDs as the gate of ci/vulncheck.sh), so code scanning shows what the project has not decided about. A finding that has no
           location or no fingerprint gets one, because code scanning needs both to keep an alert apart from the others.

The reports are data from a tool of this repository. A file that is not a SARIF 2.1.0 log with a tool driver in every run is refused.
Exit status: 0 = written, 1 = refused input or unreadable file, 2 = usage error.
"""
import hashlib
import importlib.util
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ANCHOR_URI = "go.mod"
SOURCE_KEY = "govulncheck/v1"


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


def main(argv):
    if len(argv) == 5 and argv[1] == "filter":
        cmd_filter(argv[2], argv[3], argv[4])
    else:
        print("usage: vulncheck-sarif.py filter ALLOWLIST IN.sarif OUT.sarif", file=sys.stderr)
        sys.exit(2)


if __name__ == "__main__":
    main(sys.argv)
