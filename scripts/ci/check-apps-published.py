#!/usr/bin/env python3

from __future__ import annotations

import argparse
import base64
import json
import os
import re
import ssl
import sys
import urllib.error
import urllib.parse
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


CHALLENGE_PARAM = re.compile(r'(\w+)="([^"]*)"')


def credentials() -> tuple[str, str]:
    return os.environ.get("MIRROR_USER", ""), os.environ.get("MIRROR_PASSWORD", "")


def basic_header() -> str:
    user, password = credentials()
    pair = base64.b64encode(f"{user}:{password}".encode()).decode()
    return f"Basic {pair}"


def ssl_context() -> ssl.SSLContext | None:
    ca = os.environ.get("MIRROR_CA_CERT", "")
    return ssl.create_default_context(cafile=ca) if ca else None


def split_registry(registry: str) -> tuple[str, str]:
    """(scheme://host, prefix) from host[/prefix]; an explicit scheme is kept (plain-HTTP sites)."""
    match = re.match(r"^(?:(https?)://)?([^/]+)/?(.*)$", registry.strip())
    if not match:
        raise ProbeError(f"invalid registry {registry!r}, want host[/prefix]")
    scheme, host, prefix = match.groups()
    return f"{scheme or 'https'}://{host}", prefix.strip("/")


def fetch(url: str, authorization: str, timeout: int, accept: str = ""):
    """(status, headers, body) of a GET; HTTP errors are returned as data, not raised."""
    request = urllib.request.Request(url)
    if accept:
        request.add_header("Accept", accept)
    if authorization:
        request.add_header("Authorization", authorization)
    try:
        with urllib.request.urlopen(request, timeout=timeout, context=ssl_context()) as response:
            return response.status, response.headers, response.read()
    except urllib.error.HTTPError as err:
        return err.code, err.headers, b""
    except OSError as err:
        raise ProbeError(f"{url} unreachable: {err}") from err


def bearer_token(challenge: str, timeout: int) -> str:
    """Token for a `Bearer realm=...,service=...,scope=...` challenge (Docker token auth).

    The credentials, when there are some, go to the token endpoint as Basic auth; without
    them the request is anonymous, which public repositories answer with a pull token.
    """
    params = dict(CHALLENGE_PARAM.findall(challenge))
    realm = params.get("realm")
    if not realm:
        raise ProbeError(f"Bearer challenge without a realm: {challenge}")
    query = {k: params[k] for k in ("service", "scope") if k in params}
    url = realm + ("&" if "?" in realm else "?") + urllib.parse.urlencode(query)
    user, _ = credentials()
    status, _, body = fetch(url, basic_header() if user else "", timeout)
    if status != 200:
        raise ProbeError(f"HTTP {status} on token endpoint {realm}"
                         + ("" if user else " (anonymous: set MIRROR_USER and MIRROR_PASSWORD)"))
    try:
        payload = json.loads(body)
    except ValueError as err:
        raise ProbeError(f"token endpoint {realm} did not answer JSON") from err
    token = payload.get("token") or payload.get("access_token")
    if not token:
        raise ProbeError(f"token endpoint {realm} returned no token")
    return token


def published(registry: str, repo: str, chart: str, version: str, timeout: int = 30) -> bool:
    """Is repo/chart:version in the registry? Anonymous first, then the registry's challenge:
    Bearer (token endpoint: GHCR, Harbor, distribution) or Basic."""
    base, prefix = split_registry(registry)
    path = "/".join(p for p in (prefix, repo.strip("/"), chart) if p)
    url = f"{base}/v2/{path}/manifests/{version}"
    status, headers, _ = fetch(url, "", timeout, ACCEPT)
    if status == 401:
        challenge = headers.get("WWW-Authenticate", "")
        user, password = credentials()
        if challenge.lower().startswith("bearer"):
            token = bearer_token(challenge, timeout)
            status, headers, _ = fetch(url, f"Bearer {token}", timeout, ACCEPT)
        elif user and password:
            status, headers, _ = fetch(url, basic_header(), timeout, ACCEPT)
        else:
            raise ProbeError(f"HTTP 401 on {url}: the registry is not anonymous, set "
                             "MIRROR_USER and MIRROR_PASSWORD")
    if status == 200:
        return True
    if status == 404:
        return False
    raise ProbeError(f"HTTP {status} on {url}")


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Refuse a Component pin on an unpublished apps/ chart version")
    parser.add_argument("trees", nargs="*", default=["platform"],
                        help="Desired-state trees to scan (default: platform)")
    parser.add_argument("--apps", default="apps", help="Directory of first-party charts")
    parser.add_argument("--oci-registry", default=os.environ.get("MIRROR_REGISTRY", ""),
                        help="OCI registry, host[/prefix] (env MIRROR_REGISTRY); a scheme is "
                             "kept, https:// is assumed otherwise")
    parser.add_argument("--charts-path", default="charts",
                        help="path under the registry prefix that holds the charts "
                             "(default: charts, where download-deps.py and "
                             "mirror-release.sh push them)")
    args = parser.parse_args()
    if not args.oci_registry:
        print("==> FAILED: --oci-registry (MIRROR_REGISTRY) is required", file=sys.stderr)
        sys.exit(1)

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
        for pin in pins:
            key = (pin.chart, pin.version)
            if key not in seen:
                seen[key] = published(args.oci_registry, args.charts_path, pin.chart,
                                       pin.version)
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
