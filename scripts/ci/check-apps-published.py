#!/usr/bin/env python3

from __future__ import annotations

import argparse
import base64
import os
import sys
import urllib.error
import urllib.request
from pathlib import Path
from typing import NamedTuple

import yaml

ACCEPT = ", ".join((
    "application/vnd.oci.image.manifest.v1+json",
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.v2+json",
))


class ProbeError(Exception):
    """The registry did not answer well enough to conclude anything."""


class TreeError(Exception):
    """A file of the desired-state tree cannot be read."""


class Pin(NamedTuple):
    path: str
    component: str
    chart: str
    version: str


def chart_sources(apps_dir: Path) -> dict[str, str]:
    charts: dict[str, str] = {}
    for chart_yaml in sorted(apps_dir.glob("*/Chart.yaml")):
        try:
            doc = yaml.safe_load(chart_yaml.read_text()) or {}
        except yaml.YAMLError as err:
            raise TreeError(f"{chart_yaml} is unreadable: {err}") from err
        version = doc.get("version")
        if not version:
            raise TreeError(f"{chart_yaml} declares no `version`")
        charts[str(doc.get("name") or chart_yaml.parent.name)] = str(version)
    return charts


def chart_name_from_oci(oci: str) -> str:
    return oci.rstrip("/").rsplit("/", 1)[-1] if oci else ""


def first_party_pins(trees: list[str], charts: dict[str, str]) -> list[Pin]:
    pins: list[Pin] = []
    for tree in trees:
        root = Path(tree)
        if not root.is_dir():
            continue
        for path in sorted(root.rglob("*.y*ml")):
            try:
                docs = list(yaml.safe_load_all(path.read_text()))
            except yaml.YAMLError as err:
                raise TreeError(f"{path} is unreadable: {err}") from err
            for doc in docs:
                if not isinstance(doc, dict) or doc.get("kind") != "Component":
                    continue
                chart = (doc.get("spec") or {}).get("chart")
                if not isinstance(chart, dict):
                    continue
                name = chart_name_from_oci(str(chart.get("oci", "")))
                if name not in charts:
                    continue
                pins.append(Pin(
                    str(path),
                    str((doc.get("metadata") or {}).get("name", "?")),
                    name,
                    str(chart.get("version", "")),
                ))
    return pins


def authorization() -> str:
    user = os.environ.get("ARTIFACTORY_DOCKER_USER") or os.environ.get("ARTIFACTORY_USER", "")
    password = (os.environ.get("ARTIFACTORY_DOCKER_PASSWORD")
                or os.environ.get("ARTIFACTORY_PASSWORD", ""))
    if not (user and password):
        raise ProbeError(
            "no registry credentials: set ARTIFACTORY_DOCKER_USER and "
            "ARTIFACTORY_DOCKER_PASSWORD. The registry is not anonymous.")
    pair = base64.b64encode(f"{user}:{password}".encode()).decode()
    return f"Basic {pair}"


def published(registry: str, repo: str, chart: str, version: str, auth: str,
              timeout: int = 30) -> bool:
    url = f"{registry.rstrip('/')}/v2/{repo}/{chart}/manifests/{version}"
    request = urllib.request.Request(url)
    request.add_header("Accept", ACCEPT)
    request.add_header("Authorization", auth)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            if response.status == 200:
                return True
            raise ProbeError(f"HTTP {response.status} on {url}")
    except urllib.error.HTTPError as err:
        if err.code == 404:
            return False
        raise ProbeError(f"HTTP {err.code} on {url}") from err
    except OSError as err:
        raise ProbeError(f"{url} unreachable: {err}") from err


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Refuse a Component pin on an unpublished apps/ chart version")
    parser.add_argument("trees", nargs="*", default=["platform"],
                        help="Desired-state trees to scan (default: platform)")
    parser.add_argument("--apps", default="apps", help="Directory of first-party charts")
    parser.add_argument("--registry", required=True, help="Registry base URL")
    parser.add_argument("--repo", default="rondo-helm-local",
                        help="OCI repository deps-publish-apps pushes to")
    args = parser.parse_args()

    print("==> apps/ pin guard: is the pinned version in the registry")

    try:
        charts = chart_sources(Path(args.apps))
        pins = first_party_pins(args.trees, charts)
    except TreeError as err:
        print(f"==> FAILED: {err}", file=sys.stderr)
        sys.exit(1)

    if not charts:
        print(f"==> FAILED: no chart under {args.apps}/, nothing to examine.",
              file=sys.stderr)
        sys.exit(1)
    if not pins:
        print("==> FAILED: no Component pins an apps/ chart in "
              f"{', '.join(args.trees)}, nothing to examine.", file=sys.stderr)
        sys.exit(1)

    missing: list[Pin] = []
    pending: list[Pin] = []
    seen: dict[tuple[str, str], bool] = {}
    try:
        auth = authorization()
        for pin in pins:
            key = (pin.chart, pin.version)
            if key not in seen:
                seen[key] = published(args.registry, args.repo, pin.chart, pin.version, auth)
            if seen[key]:
                print(f"    OK      {pin.chart} {pin.version}, published")
            elif pin.version == charts[pin.chart]:
                pending.append(pin)
            else:
                missing.append(pin)
    except ProbeError as err:
        print(f"==> FAILED: the registry answer is inconclusive, {err}", file=sys.stderr)
        print("    Neither published nor missing: a guard that cannot conclude is "
              "not green.", file=sys.stderr)
        sys.exit(1)

    print(f"    {len(pins)} pins examined, {len(charts)} charts under {args.apps}/.")

    for pin in pending:
        print(f"    PENDING {pin.chart} {pin.version}, not in the registry, published on merge")

    if pending:
        print("")
        print("==> What this pipeline CANNOT prove")
        print("    No branch publishes an apps/ chart: the OCI push only happens on "
              "merge to the")
        print("    default branch, or on a tag (deps-publish-apps). No cluster can "
              "pull the versions")
        print("    above before the merge, so testing a branch chart on a cluster "
              "is out of")
        print("    this CI's reach. Before the merge, the chart renders locally")
        print("    (helm template apps/<chart>); it does not deploy from the registry.")

    if missing:
        print("", file=sys.stderr)
        print("==> FAILED: these Components pin a version the registry does not have, "
              "and that", file=sys.stderr)
        print("    this commit will not publish:", file=sys.stderr)
        for pin in missing:
            print(f"      {pin.path}  Component/{pin.component} pins {pin.chart} "
                  f"{pin.version}, apps/ has {charts[pin.chart]}", file=sys.stderr)
        print("    `helm pull` will fail with a 404 on the cluster. Fix: align "
              "spec.chart.version", file=sys.stderr)
        print("    with apps/<chart>/Chart.yaml, or bump the Chart.yaml in the same pull request.",
              file=sys.stderr)
        sys.exit(1)

    print("==> OK: every apps/ pin is published, or will be when this commit merges.")


if __name__ == "__main__":
    main()
