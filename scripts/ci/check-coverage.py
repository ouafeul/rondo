#!/usr/bin/env python3

from __future__ import annotations

import subprocess
import sys
import tempfile
from collections import defaultdict
from pathlib import Path

FLOORS: dict[str, float] = {
    "rondo/internal/controllers": 78.0,
    "rondo/internal/jobs": 75.0,
    "rondo/internal/exec": 78.0,
    "rondo/internal/probe": 85.0,
    "rondo/internal/platform": 88.0,
    "rondo/internal/bootstrap": 90.0,
    "rondo/internal/kobj": 90.0,
    "rondo/api/v1alpha1": 85.0,
}

TOTAL_FLOOR = 80.0


def parse_profile(text: str) -> dict[str, tuple[int, int]]:
    per_package: dict[str, list[int]] = defaultdict(lambda: [0, 0])
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("mode:"):
            continue
        try:
            location, statements, count = line.rsplit(" ", 2)
            n_statements = int(statements)
            n_count = int(count)
        except ValueError:
            raise SystemExit(f"unreadable profile line: {line!r}")
        package = location.rsplit("/", 1)[0]
        entry = per_package[package]
        entry[1] += n_statements
        if n_count > 0:
            entry[0] += n_statements
    return {pkg: (covered, total) for pkg, (covered, total) in per_package.items()}


def percentage(covered: int, total: int) -> float:
    return 100.0 * covered / total if total else 0.0


def generate_profile(path: Path) -> None:
    print("==> generating the coverage profile (go test ./...)", flush=True)
    result = subprocess.run(
        ["go", "test", "./...", f"-coverprofile={path}", "-covermode=set"],
        check=False,
    )
    if result.returncode != 0:
        raise SystemExit(
            "tests failed: coverage cannot be measured on a red tree"
        )


def main(argv: list[str]) -> int:
    if len(argv) > 1:
        profile = Path(argv[1])
        if not profile.exists():
            raise SystemExit(f"profile not found: {profile}")
        text = profile.read_text(encoding="utf-8")
    else:
        with tempfile.TemporaryDirectory() as tmp:
            profile = Path(tmp) / "cover.out"
            generate_profile(profile)
            text = profile.read_text(encoding="utf-8")

    per_package = parse_profile(text)
    if not per_package:
        raise SystemExit("EMPTY coverage profile: the guard checks nothing")

    failures: list[str] = []
    print("==> coverage per watched package")
    for package, floor in sorted(FLOORS.items()):
        if package not in per_package:
            failures.append(
                f"{package}: missing from the profile; was the package renamed or "
                f"dropped from the tests? A floor that measures nothing guards nothing."
            )
            continue
        covered, total = per_package[package]
        pct = percentage(covered, total)
        status = "OK " if pct >= floor else "KO "
        print(f"    {status} {package:<34} {pct:5.1f} %  (floor {floor:.1f} %)")
        if pct < floor:
            failures.append(
                f"{package}: {pct:.1f} % < {floor:.1f} %; a package that makes decisions "
                f"(ordering, mutual exclusion, health, schema) must not lose "
                f"coverage silently."
            )

    covered_total = sum(c for c, _ in per_package.values())
    statements_total = sum(t for _, t in per_package.values())
    total_pct = percentage(covered_total, statements_total)
    print(f"    {'OK ' if total_pct >= TOTAL_FLOOR else 'KO '} {'TOTAL':<34} "
          f"{total_pct:5.1f} %  (floor {TOTAL_FLOOR:.1f} %)")
    if total_pct < TOTAL_FLOOR:
        failures.append(
            f"TOTAL: {total_pct:.1f} % < {TOTAL_FLOOR:.1f} %, the project's stated "
            f"threshold."
        )

    if failures:
        print("\n==> FAILED:", file=sys.stderr)
        for f in failures:
            print(f"    - {f}", file=sys.stderr)
        print(
            "\nA total is an AVERAGE: it rises when the easy code is tested. "
            "That is why the floor is per package.",
            file=sys.stderr,
        )
        return 1
    print("==> OK: every coverage floor holds.")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
