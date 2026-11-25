#!/usr/bin/env python3

import argparse
import os
import shutil
import subprocess
import sys
from datetime import datetime, timezone
from xml.etree.ElementTree import Element, SubElement, ElementTree


def find_clamscan() -> str:
    found = shutil.which("clamscan")
    if found:
        return found
    for p in ["/usr/bin/clamscan", "/usr/local/bin/clamscan", "/usr/sbin/clamscan"]:
        if os.path.exists(p):
            return p
    print("ERROR: clamscan not found", file=sys.stderr)
    sys.exit(2)


def check_db_age(clamscan_bin: str, max_days: int) -> None:
    if max_days <= 0:
        return
    out = subprocess.run([clamscan_bin, "--version"],
                         capture_output=True, text=True).stdout.strip()
    parts = out.split("/")
    if len(parts) < 3:
        print(f"ERROR: unreadable DB version ('{out}'), cannot guarantee "
              "signature freshness", file=sys.stderr)
        sys.exit(2)
    try:
        db_date = datetime.strptime(parts[2].strip(), "%a %b %d %H:%M:%S %Y")
    except ValueError:
        print(f"ERROR: unreadable DB date ('{parts[2].strip()}')", file=sys.stderr)
        sys.exit(2)
    age_days = (datetime.now() - db_date).days
    print(f"Virus DB: version {parts[1]}, dated {parts[2].strip()} ({age_days} days)")
    if age_days > max_days:
        print(f"ERROR: virus DB is {age_days} days old (> {max_days}). "
              "freshclam probably failed; the scan is not meaningful.", file=sys.stderr)
        sys.exit(2)


def scan_local_dir(scan_dir: str, clamscan_bin: str) -> tuple[list[dict], int]:
    expected = set()
    for root, _dirs, files in os.walk(scan_dir):
        for fname in files:
            expected.add(os.path.relpath(os.path.join(root, fname), scan_dir))

    print(f"{len(expected)} files to scan (recursive clamscan, single pass)")

    proc = subprocess.run(
        [clamscan_bin, "-r", "--stdout", "--no-summary", scan_dir],
        capture_output=True, text=True,
    )
    if proc.stderr.strip():
        print(proc.stderr, file=sys.stderr)

    results = []
    infected = 0
    seen = set()

    for line in proc.stdout.splitlines():
        path, sep, verdict = line.strip().rpartition(": ")
        if not sep:
            continue
        rel = os.path.relpath(path, scan_dir)
        if rel not in expected or rel in seen:
            continue
        seen.add(rel)

        if verdict == "OK":
            print(f"  OK: {rel}")
            results.append({"name": rel, "status": "clean",
                            "detail": "No threats found", "full_output": line.strip()})
        elif verdict.endswith("FOUND"):
            infected += 1
            print(f"  INFECTED: {rel}, {verdict}")
            results.append({"name": rel, "status": "infected",
                            "detail": f"{rel}: {verdict}", "full_output": line.strip()})
        else:
            print(f"  WARN: {rel}, {verdict}")
            results.append({"name": rel, "status": "error",
                            "detail": verdict, "full_output": line.strip()})

    for rel in sorted(expected - seen):
        print(f"  WARN: {rel}, missing from clamscan output (not scanned?)")
        results.append({"name": rel, "status": "error",
                        "detail": "file missing from clamscan output"})

    if proc.returncode not in (0, 1):
        results.append({"name": "clamscan", "status": "error",
                        "detail": f"clamscan rc={proc.returncode}: {proc.stderr.strip()[:500]}"})

    results.sort(key=lambda r: r["name"])
    return results, infected


def generate_junit(results: list[dict], infected: int, output_path: str) -> None:
    testsuites = Element("testsuites")
    testsuite = SubElement(
        testsuites, "testsuite",
        name="ClamAV Scan",
        tests=str(len(results)),
        failures=str(infected),
        errors=str(sum(1 for r in results if r["status"] == "error")),
    )

    for r in results:
        tc = SubElement(testsuite, "testcase", name=r["name"], classname="clamav.scan")
        if r["status"] == "infected":
            f = SubElement(tc, "failure", message=r["detail"])
            f.text = r.get("full_output", r["detail"])
        elif r["status"] == "error":
            SubElement(tc, "error", message=r["detail"])

        so = SubElement(tc, "system-out")
        so.text = r.get("full_output", r.get("detail", "clean"))

    try:
        from xml.etree.ElementTree import indent
        indent(testsuites)
    except (ImportError, TypeError):
        pass

    ElementTree(testsuites).write(output_path, encoding="unicode", xml_declaration=True)


def generate_html(results: list[dict], output_path: str) -> None:
    infected = sum(1 for r in results if r["status"] == "infected")
    errors = sum(1 for r in results if r["status"] == "error")
    clean = sum(1 for r in results if r["status"] == "clean")
    total = len(results)
    scan_date = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")

    if infected > 0:
        status_color = "#e74c3c"
        status_text = f"{infected} INFECTED"
    elif errors > 0:
        status_color = "#f39c12"
        status_text = f"{errors} ERRORS"
    else:
        status_color = "#27ae60"
        status_text = "ALL CLEAN"

    rows = ""
    for r in sorted(results, key=lambda x: (x["status"] != "infected", x["status"] != "error", x["name"])):
        if r["status"] == "infected":
            row_class = "infected"
            badge = '<span class="badge badge-infected">INFECTED</span>'
        elif r["status"] == "error":
            row_class = "error"
            badge = '<span class="badge badge-error">ERROR</span>'
        else:
            row_class = "clean"
            badge = '<span class="badge badge-clean">CLEAN</span>'

        detail = r.get("detail", "").replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
        full_output = r.get("full_output", r.get("detail", "")).replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")

        rows += f"""
        <tr class="{row_class}">
            <td>{r["name"]}</td>
            <td>{badge}</td>
            <td><pre>{detail}</pre></td>
            <td><details><summary>Full output</summary><pre>{full_output}</pre></details></td>
        </tr>"""

    html = f"""<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>ClamAV Scan Report, RONDO</title>
    <style>
        * {{ margin: 0; padding: 0; box-sizing: border-box; }}
        body {{ font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #f5f5f5; color: #333; padding: 20px; }}
        .container {{ max-width: 1200px; margin: 0 auto; }}
        h1 {{ font-size: 1.5rem; margin-bottom: 8px; }}
        .subtitle {{ font-size: 0.9rem; color: #666; margin-bottom: 20px; }}
        .summary {{ display: flex; gap: 15px; margin-bottom: 20px; flex-wrap: wrap; }}
        .card {{ background: white; border-radius: 8px; padding: 15px 20px; box-shadow: 0 1px 3px rgba(0,0,0,0.1); min-width: 140px; }}
        .card .label {{ font-size: 0.8rem; color: #666; text-transform: uppercase; letter-spacing: 0.5px; }}
        .card .value {{ font-size: 1.8rem; font-weight: 700; margin-top: 4px; }}
        .status-card {{ border-left: 4px solid {status_color}; }}
        .status-card .value {{ color: {status_color}; }}
        table {{ width: 100%; border-collapse: collapse; background: white; border-radius: 8px; overflow: hidden; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }}
        th {{ background: #2c3e50; color: white; padding: 12px 15px; text-align: left; font-size: 0.85rem; text-transform: uppercase; letter-spacing: 0.5px; }}
        td {{ padding: 10px 15px; border-bottom: 1px solid #eee; vertical-align: top; }}
        tr.infected {{ background: #fdf2f2; }}
        tr.error {{ background: #fef9e7; }}
        tr:hover {{ background: #f8f9fa; }}
        tr.infected:hover {{ background: #fbe4e4; }}
        .badge {{ padding: 3px 8px; border-radius: 4px; font-size: 0.75rem; font-weight: 600; }}
        .badge-clean {{ background: #d4edda; color: #155724; }}
        .badge-infected {{ background: #f8d7da; color: #721c24; }}
        .badge-error {{ background: #fff3cd; color: #856404; }}
        pre {{ font-size: 0.8rem; white-space: pre-wrap; word-break: break-all; max-width: 500px; margin: 0; }}
        details {{ cursor: pointer; }}
        details summary {{ color: #3498db; font-size: 0.85rem; }}
        .footer {{ margin-top: 20px; font-size: 0.8rem; color: #999; text-align: center; }}
    </style>
</head>
<body>
    <div class="container">
        <h1>ClamAV Scan Report</h1>
        <p class="subtitle">Scanned on {scan_date}</p>
        <div class="summary">
            <div class="card status-card">
                <div class="label">Status</div>
                <div class="value">{status_text}</div>
            </div>
            <div class="card">
                <div class="label">Total</div>
                <div class="value">{total}</div>
            </div>
            <div class="card">
                <div class="label">Clean</div>
                <div class="value" style="color: #27ae60">{clean}</div>
            </div>
            <div class="card">
                <div class="label">Infected</div>
                <div class="value" style="color: #e74c3c">{infected}</div>
            </div>
            <div class="card">
                <div class="label">Errors</div>
                <div class="value" style="color: #f39c12">{errors}</div>
            </div>
        </div>
        <table>
            <thead>
                <tr>
                    <th>Artifact</th>
                    <th>Status</th>
                    <th>Detail</th>
                    <th>Full Output</th>
                </tr>
            </thead>
            <tbody>
                {rows}
            </tbody>
        </table>
        <div class="footer">RONDO CI, ClamAV {scan_date}</div>
    </div>
</body>
</html>"""

    with open(output_path, "w") as f:
        f.write(html)


def generate_badge_svg(infected: int, output_path: str) -> None:
    if infected > 0:
        color = "#e05d44"
        text = f"{infected} infected"
    else:
        color = "#4c1"
        text = "clean"

    label = "ClamAV"
    label_width = len(label) * 7 + 10
    text_width = len(text) * 7 + 10
    total_width = label_width + text_width

    svg = f"""<svg xmlns="http://www.w3.org/2000/svg" width="{total_width}" height="20">
  <linearGradient id="b" x2="0" y2="100%">
    <stop offset="0" stop-color="#bbb" stop-opacity=".1"/>
    <stop offset="1" stop-opacity=".1"/>
  </linearGradient>
  <clipPath id="a">
    <rect width="{total_width}" height="20" rx="3" fill="#fff"/>
  </clipPath>
  <g clip-path="url(#a)">
    <rect width="{label_width}" height="20" fill="#555"/>
    <rect x="{label_width}" width="{text_width}" height="20" fill="{color}"/>
    <rect width="{total_width}" height="20" fill="url(#b)"/>
  </g>
  <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
    <text x="{label_width / 2}" y="15" fill="#010101" fill-opacity=".3">{label}</text>
    <text x="{label_width / 2}" y="14">{label}</text>
    <text x="{label_width + text_width / 2}" y="15" fill="#010101" fill-opacity=".3">{text}</text>
    <text x="{label_width + text_width / 2}" y="14">{text}</text>
  </g>
</svg>"""

    with open(output_path, "w") as f:
        f.write(svg)


def main():
    parser = argparse.ArgumentParser(description="ClamAV scanner with JUnit, HTML and badge output")
    parser.add_argument("--scan-dir", required=True,
                        help="Local directory to scan")
    parser.add_argument("--junit", default="clamav-junit.xml", help="Output JUnit XML file")
    parser.add_argument("--html", default="", help="Output HTML report file")
    parser.add_argument("--badge", default="", help="Output badge SVG file")
    parser.add_argument("--max-db-age-days", type=int, default=0,
                        help="Fail if the virus DB is older than N days (0 = disabled)")
    args = parser.parse_args()

    clamscan_bin = find_clamscan()
    print(f"Using clamscan: {clamscan_bin}\n")
    check_db_age(clamscan_bin, args.max_db_age_days)

    if not os.path.isdir(args.scan_dir):
        print(f"ERROR: directory not found: {args.scan_dir}", file=sys.stderr)
        sys.exit(2)
    print(f"Scanning local directory: {args.scan_dir}")
    results, infected = scan_local_dir(args.scan_dir, clamscan_bin)

    if not results:
        print("No artifact to scan")
        return

    generate_junit(results, infected, args.junit)
    print(f"\nJUnit: {args.junit}")

    if args.html:
        generate_html(results, args.html)
        print(f"HTML:  {args.html}")

    if args.badge:
        generate_badge_svg(infected, args.badge)
        print(f"Badge: {args.badge}")

    print(f"\n{'='*60}")
    print(f"  Scanned:  {len(results)}")
    print(f"  Clean:    {sum(1 for r in results if r['status'] == 'clean')}")
    print(f"  Infected: {infected}")
    print(f"  Errors:   {sum(1 for r in results if r['status'] == 'error')}")
    print(f"{'='*60}")

    if infected > 0:
        for r in results:
            if r["status"] == "infected":
                print(f"  INFECTED: {r['name']}, {r['detail']}")
        print(f"\nERROR: {infected} infected artifact(s)")
        sys.exit(1)

    print("\nNo malware detected")


if __name__ == "__main__":
    main()
