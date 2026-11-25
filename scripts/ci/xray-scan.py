#!/usr/bin/env python3

import argparse
import json
import os
import re
import sys
import time
from xml.etree.ElementTree import Element, SubElement, ElementTree

import requests

_REGISTRY_PATTERN = re.compile(r"^[a-zA-Z0-9][-a-zA-Z0-9]*\.[a-zA-Z0-9][-a-zA-Z0-9.]*(/|$)")


def strip_source_registry(image: str) -> str:
    if _REGISTRY_PATTERN.match(image):
        return image.split("/", 1)[1]
    return image


def get_session() -> requests.Session:
    session = requests.Session()
    user = os.environ.get("ARTIFACTORY_USER", "")
    password = os.environ.get("ARTIFACTORY_PASSWORD", "")
    token = os.environ.get("ARTIFACTORY_TOKEN", "")

    if token:
        session.headers["Authorization"] = f"Bearer {token}"
    elif user and password:
        session.auth = (user, password)
    else:
        print("WARN: no Artifactory authentication configured", file=sys.stderr)

    return session


def check_artifact_summary(session: requests.Session, base_url: str,
                           repo: str, path: str) -> dict:
    xray_url = base_url.replace("/artifactory", "/xray")
    summary_url = f"{xray_url}/api/v2/summary/artifact"

    payload = {
        "paths": [f"{repo}/{path}"]
    }

    try:
        resp = session.post(summary_url, json=payload, timeout=60)
        resp.raise_for_status()
        return resp.json()
    except json.JSONDecodeError:
        return {"error": f"Non-JSON response from {summary_url}"}
    except requests.exceptions.RequestException as e:
        return {"error": str(e)}


def extract_violations(summary: dict) -> dict:
    counts = {"critical": 0, "high": 0, "medium": 0, "low": 0, "unknown": 0}
    details = []
    all_issues = []
    errors = []

    if summary.get("error"):
        errors.append(str(summary["error"]))
    for err in summary.get("errors") or []:
        errors.append(f"{err.get('identifier', '?')}: {err.get('error', 'unknown')}")

    artifacts = summary.get("artifacts", [])
    if not artifacts and not errors:
        errors.append("no Xray data (artifact not indexed or unknown path)")

    for artifact in artifacts:
        for issue in artifact.get("issues", []):
            severity = issue.get("severity", "Unknown").lower()
            if severity in counts:
                counts[severity] += 1
            else:
                counts["unknown"] += 1
            cve = issue.get("cve", "N/A")
            desc = issue.get("summary", issue.get("description", "N/A"))
            line = f"{severity.upper()}: {desc} ({cve})"
            all_issues.append(line)
            if severity in ("critical", "high"):
                details.append(line)

    total = sum(counts.values())

    summary_lines = [
        f"Vulnerabilities: {total} total",
        f"  Critical: {counts['critical']}",
        f"  High: {counts['high']}",
        f"  Medium: {counts['medium']}",
        f"  Low: {counts['low']}",
    ]
    if errors:
        summary_lines.append("")
        summary_lines.append("Errors:")
        for err in errors:
            summary_lines.append(f"  - {err}")
    if all_issues:
        summary_lines.append("")
        summary_lines.append("Issues:")
        for iss in all_issues:
            summary_lines.append(f"  - {iss}")
    elif not errors:
        summary_lines.append("")
        summary_lines.append("No vulnerabilities found.")

    return {
        "critical": counts["critical"],
        "high": counts["high"],
        "total": total,
        "details": details,
        "error": "; ".join(errors) if errors else None,
        "summary_text": "\n".join(summary_lines),
    }


def build_checks(deps: dict) -> list[tuple[str, str, str]]:
    checks = []

    for section in ("binaries", "tofu_providers", "grafana_plugins"):
        for dep in deps.get(section, []):
            path = dep.get("target_path")
            if path:
                checks.append((f"{dep['name']} {dep['version']}",
                               dep["target_repo"], path))

    for dep in deps.get("docker_images", []):
        image_path = strip_source_registry(dep["name"])
        checks.append((f"{dep['name']}:{dep['version']}",
                       dep["target_repo"],
                       f"{image_path}/{dep['version']}/manifest.json"))

    return checks


def run_checks(session: requests.Session, base_url: str,
               checks: list[tuple[str, str, str]],
               retries: int, retry_delay: int) -> list[dict]:
    results: dict[int, dict] = {}
    pending = list(range(len(checks)))

    for attempt in range(1, retries + 1):
        still_failing = []
        for i in pending:
            label, repo, path = checks[i]
            if attempt == 1:
                print(f"Check: {label} ({repo}/{path})...")
            violations = extract_violations(
                check_artifact_summary(session, base_url, repo, path))
            results[i] = violations
            if violations["error"]:
                still_failing.append(i)
        pending = still_failing
        if not pending or attempt == retries:
            break
        print(f"{len(pending)} failed lookup(s), pass {attempt + 1}/{retries} "
              f"in {retry_delay}s (possible Xray indexing lag)")
        time.sleep(retry_delay)

    return [results[i] for i in range(len(checks))]


def main():
    parser = argparse.ArgumentParser(description="Check Xray scan results for uploaded artifacts")
    parser.add_argument("--deps", required=True, help="Resolved deps JSON file")
    parser.add_argument("--artifactory-url", required=True, help="Artifactory base URL")
    parser.add_argument("--fail-on-critical", action=argparse.BooleanOptionalAction,
                        default=True, help="Fail if critical vulnerabilities found")
    parser.add_argument("--fail-on-error", action=argparse.BooleanOptionalAction,
                        default=True,
                        help="Fail if an Xray lookup fails (inconclusive gate)")
    parser.add_argument("--wait", type=int, default=30,
                        help="Seconds to wait for Xray scan to complete before checking")
    parser.add_argument("--retries", type=int, default=3,
                        help="Attempts per artifact when the lookup fails")
    parser.add_argument("--retry-delay", type=int, default=20,
                        help="Seconds between two attempts")
    args = parser.parse_args()

    with open(args.deps) as f:
        deps = json.load(f)

    checks = build_checks(deps)
    print(f"{len(checks)} artifacts to check")

    if args.wait > 0 and checks:
        print(f"Waiting {args.wait}s for Xray to finish the scans...")
        time.sleep(args.wait)

    session = get_session()
    all_violations = run_checks(session, args.artifactory_url, checks,
                                args.retries, args.retry_delay)

    results = []
    total_critical = 0
    total_high = 0
    total_errors = 0
    for (label, repo, path), violations in zip(checks, all_violations):
        total_critical += violations["critical"]
        total_high += violations["high"]
        if violations["error"]:
            total_errors += 1
        results.append({"dep": label, "path": f"{repo}/{path}", **violations})

    report = {
        "scan_date": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "total_checked": len(results),
        "total_critical": total_critical,
        "total_high": total_high,
        "total_errors": total_errors,
        "results": results
    }

    with open("scan-report.json", "w") as f:
        json.dump(report, f, indent=2)

    testsuites = Element("testsuites")
    testsuite = SubElement(testsuites, "testsuite",
                           name="Xray Security Scan",
                           tests=str(len(results)),
                           failures=str(sum(1 for r in results if r["critical"] > 0)),
                           errors=str(sum(1 for r in results if r.get("error"))))

    for r in results:
        tc = SubElement(testsuite, "testcase",
                        name=r["dep"],
                        classname="xray.security")
        if r.get("error"):
            SubElement(tc, "error", message=r["error"])
        elif r["critical"] > 0:
            SubElement(tc, "failure",
                       message=f"{r['critical']} critical, {r['high']} high vulnerabilities").text = "\n".join(r["details"])

        so = SubElement(tc, "system-out")
        so.text = r.get("summary_text", "No data available")

    try:
        from xml.etree.ElementTree import indent
        indent(testsuites)
    except (ImportError, TypeError):
        pass

    ElementTree(testsuites).write("xray-junit.xml", encoding="unicode", xml_declaration=True)

    print(f"\n{'='*60}")
    print(f"Xray results: {len(results)} artifacts checked")
    print(f"  Critical: {total_critical}")
    print(f"  High: {total_high}")
    print(f"  Failed lookups: {total_errors}")
    print(f"{'='*60}")

    for r in results:
        if r.get("error"):
            print(f"  ERROR: {r['dep']}: {r['error']}", file=sys.stderr)
        for detail in r.get("details", []):
            print(f"  {r['dep']}: {detail}", file=sys.stderr)

    exit_code = 0
    if total_critical > 0 and args.fail_on_critical:
        print(f"\nERROR: {total_critical} critical vulnerability(ies) detected", file=sys.stderr)
        exit_code = 1
    if total_errors > 0:
        if any("not indexed" in (r.get("error") or "") for r in results):
            print("\nHINT: \"not indexed/cached in Xray\" on older artifacts means the "
                  "repo is probably NOT among the resources Xray indexes "
                  "(Artifactory > Xray Settings > Indexed Resources).", file=sys.stderr)
        if args.fail_on_error:
            print(f"\nERROR: {total_errors} failed Xray lookup(s), the gate cannot conclude",
                  file=sys.stderr)
            exit_code = 1
    if exit_code:
        sys.exit(exit_code)

    print("\nNo critical vulnerability detected")


if __name__ == "__main__":
    main()
