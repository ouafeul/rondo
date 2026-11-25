#!/usr/bin/env python3
"""Download third-party dependencies and push them to the mirror.

  download-deps.py --deps deps-resolved.json --type binaries --download-only --local-dir out
  download-deps.py --deps deps-resolved.json --type binaries --upload-only --local-dir out \
      --files-url https://files.site/mirror

Targets per `kind` (see manifests/dependencies.yaml; flags or MIRROR_* environment, see
mirror_common.py):
  file, grafana_plugin  PUT to <files-url>/<path>, with a .sha256 sidecar
  provider              a static OpenTofu network_mirror tree under --provider-mirror
                        (default <files-url>/providers)
  chart                 helm push to oci://<oci-registry>/charts
"""

import argparse
import glob
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

import mirror_common as mc
import requests

DEFAULT_PROVIDER_HOST = "registry.opentofu.org"
PLATFORM = re.compile(r"_([a-z0-9]+_[a-z0-9]+)\.zip$")


def get_local_path(local_dir: str, dep_type: str, dep: dict) -> str:
    if dep_type == "helm_charts":
        return os.path.join(local_dir, "helm_charts", f"{dep['name']}-{dep['version']}.tgz")
    safe_name = dep["name"].replace("/", "_")
    return os.path.join(local_dir, dep_type, safe_name, os.path.basename(dep["path"]))


def download_to_local(source_url: str, local_path: str) -> bool:
    print(f"  Downloading: {source_url}")
    try:
        os.makedirs(os.path.dirname(local_path), exist_ok=True)
        resp = mc.source_session().get(source_url, stream=True, timeout=300, verify=True)
        resp.raise_for_status()
        with open(local_path, "wb") as f:
            for chunk in resp.iter_content(chunk_size=8192):
                f.write(chunk)
        print(f"  OK: {local_path}")
        return True
    except requests.exceptions.RequestException as e:
        print(f"  ERROR: {e}", file=sys.stderr)
        return False


def download_helm_chart_local(dep: dict, local_dir: str) -> bool:
    name = dep["name"]
    version = dep["version"]
    source_repo = dep.get("source_repo", "")
    source_type = dep.get("source_type", "repo")
    repo_alias = f"tmp-{name}-{os.getpid()}"
    chart_dir = os.path.join(local_dir, "helm_charts")
    os.makedirs(chart_dir, exist_ok=True)

    try:
        if source_type == "oci":
            oci_ref = f"{source_repo}/{name}"
            print(f"  Pull OCI: {oci_ref} --version {version}")
            subprocess.run(
                ["helm", "pull", oci_ref, "--version", version, "-d", chart_dir],
                check=True, capture_output=True, text=True,
            )
        else:
            subprocess.run(["helm", "repo", "add", repo_alias, source_repo, "--force-update"],
                           check=True, capture_output=True)
            subprocess.run(["helm", "repo", "update", repo_alias], check=True, capture_output=True)
            subprocess.run(
                ["helm", "pull", f"{repo_alias}/{name}", "--version", version, "-d", chart_dir],
                check=True, capture_output=True, text=True,
            )

        expected = os.path.join(chart_dir, f"{name}-{version}.tgz")
        if os.path.exists(expected):
            print(f"  OK: {name}-{version}.tgz")
            return True

        tgz_files = glob.glob(os.path.join(chart_dir, f"{name}-*.tgz"))
        if tgz_files:
            print(f"  OK: {os.path.basename(tgz_files[0])}")
            return True

        print(f"  ERROR: chart {name}-{version}.tgz not found", file=sys.stderr)
        return False
    except subprocess.CalledProcessError as e:
        stderr = e.stderr if e.stderr else e.stdout if e.stdout else str(e)
        print(f"  ERROR helm: {stderr}", file=sys.stderr)
        return False
    finally:
        if source_type != "oci":
            subprocess.run(["helm", "repo", "remove", repo_alias], capture_output=True)


def chart_in_registry(charts: str, name: str, version: str) -> bool:
    ref = f"oci://{charts}/{name}"
    result = subprocess.run(["helm", "show", "chart", ref, "--version", version,
                             *mc.helm_tls_args()], capture_output=True, text=True)
    return result.returncode == 0


def upload_helm_chart(dep: dict, local_dir: str, charts: str, force: bool) -> str:
    """'ok', 'skip' or 'error'."""
    name, version = dep["name"], dep["version"]
    chart_dir = os.path.join(local_dir, "helm_charts")
    chart_file = os.path.join(chart_dir, f"{name}-{version}.tgz")

    if not os.path.exists(chart_file):
        tgz_files = glob.glob(os.path.join(chart_dir, f"{name}-*.tgz"))
        if not tgz_files:
            print(f"  ERROR: {name}-{version}.tgz not found in {chart_dir}", file=sys.stderr)
            return "error"
        chart_file = tgz_files[0]

    if not force and chart_in_registry(charts, name, version):
        print(f"  SKIP: {name} {version} already in the registry")
        return "skip"

    oci_target = f"oci://{charts}"
    try:
        print(f"  Push OCI: {os.path.basename(chart_file)} -> {oci_target}")
        subprocess.run(["helm", "push", chart_file, oci_target, *mc.helm_tls_args()],
                       check=True, capture_output=True, text=True)
        print(f"  OK: {os.path.basename(chart_file)}")
        return "ok"
    except subprocess.CalledProcessError as e:
        stderr = e.stderr if e.stderr else e.stdout if e.stdout else str(e)
        print(f"  ERROR helm push: {stderr}", file=sys.stderr)
        return "error"


def upload_one(store: mc.Store, rel: str, local_path: str, force: bool) -> str:
    """'ok', 'skip' or 'error'."""
    print(f"  Upload: {store.describe(rel)}")
    try:
        if force:
            store.put(rel, local_path)
            return "ok"
        return "ok" if store.put_checked(rel, local_path) else "skip"
    except (requests.exceptions.RequestException, OSError) as e:
        print(f"  ERROR: {e}", file=sys.stderr)
        return "error"


def provider_tree(entries: list[tuple[dict, str]], out_dir: str) -> list[str]:
    """Write a static OpenTofu network_mirror tree and return its relative file paths.

    Layout, as `tofu providers mirror` produces it:
      <hostname>/<namespace>/<type>/index.json              {"versions": {"<v>": {}}}
      <hostname>/<namespace>/<type>/<version>.json          {"archives": {"<os_arch>": {...}}}
      <hostname>/<namespace>/<type>/terraform-provider-*.zip
    `entries` pairs each tofu_providers entry with its local archive.
    """
    out = Path(out_dir)
    versions: dict[tuple[str, str, str], dict[str, dict]] = {}
    files: list[str] = []
    for dep, archive in entries:
        host = dep.get("registry", DEFAULT_PROVIDER_HOST)
        namespace, ptype = dep["path"].split("/")[:2]
        name = os.path.basename(dep["path"])
        match = PLATFORM.search(name)
        if not match:
            raise ValueError(f"provider archive {name!r} does not end in _<os>_<arch>.zip")
        directory = out / host / namespace / ptype
        directory.mkdir(parents=True, exist_ok=True)
        dest = directory / name
        dest.write_bytes(Path(archive).read_bytes())
        files.append(str(dest.relative_to(out)))
        platforms = versions.setdefault((host, namespace, ptype), {}).setdefault(dep["version"], {})
        platforms[match.group(1)] = {"url": name, "hashes": [f"zh:{mc.sha256_of(archive)}"]}
    for (host, namespace, ptype), by_version in versions.items():
        directory = out / host / namespace / ptype
        for version, platforms in by_version.items():
            path = directory / f"{version}.json"
            path.write_text(json.dumps({"archives": platforms}, indent=2) + "\n")
            files.append(str(path.relative_to(out)))
        index = directory / "index.json"
        index.write_text(
            json.dumps({"versions": {v: {} for v in sorted(by_version)}}, indent=2) + "\n")
        files.append(str(index.relative_to(out)))
    return files


def upload_providers(dep_list: list[dict], local_dir: str, store: mc.Store,
                     force: bool) -> tuple[int, int, int]:
    entries, failed = [], 0
    for dep in dep_list:
        archive = get_local_path(local_dir, "tofu_providers", dep)
        if os.path.exists(archive):
            entries.append((dep, archive))
        else:
            print(f"  ERROR: {archive} not found", file=sys.stderr)
            failed += 1
    ok = skipped = 0
    with tempfile.TemporaryDirectory() as tmp:
        files = provider_tree(entries, tmp)
        # Metadata last: a client never reads the description of a missing archive.
        files.sort(key=lambda f: f.endswith(".json"))
        for rel in files:
            state = upload_one(store, rel, os.path.join(tmp, rel), force)
            if state == "ok":
                ok += 1
            elif state == "skip":
                skipped += 1
            else:
                failed += 1
    return ok, skipped, failed


def main():
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--deps", required=True, help="Resolved deps JSON file")
    parser.add_argument("--type", required=True,
                        choices=["binaries", "tofu_providers", "helm_charts", "grafana_plugins"],
                        help="Type of dependencies to process")
    mc.add_target_args(parser, oci=True, files=True, providers=True)
    parser.add_argument("--force", action="store_true",
                        help="Upload even when the .sha256 sidecar matches")
    parser.add_argument("--local-dir", default="",
                        help="Local directory for the artifacts (a temporary one by default)")
    parser.add_argument("--download-only", action="store_true",
                        help="Download only (no upload)")
    parser.add_argument("--upload-only", action="store_true",
                        help="Upload only, from --local-dir")
    args = parser.parse_args()

    if args.download_only and args.upload_only:
        mc.die("--download-only and --upload-only are mutually exclusive")
    if (args.download_only or args.upload_only) and not args.local_dir:
        mc.die("--local-dir is required with --download-only or --upload-only")

    uploading = not args.download_only
    if uploading:
        if args.type == "helm_charts" and not (args.oci_registry or args.charts_oci):
            mc.die("--oci-registry (MIRROR_REGISTRY) or --charts-oci (MIRROR_CHARTS_OCI) is "
                   "required to upload charts")
        if args.type == "tofu_providers" and not (args.provider_mirror or args.files_url):
            mc.die("--provider-mirror (MIRROR_PROVIDER_URL) or --files-url is required to "
                   "upload providers")
        if args.type not in ("helm_charts", "tofu_providers") and not args.files_url:
            mc.die("--files-url (MIRROR_FILES_URL) is required to upload")

    with open(args.deps) as f:
        dep_list = json.load(f).get(args.type, [])

    tmp_dir = None
    local_dir = args.local_dir
    if not local_dir:
        tmp_dir = tempfile.TemporaryDirectory()
        local_dir = tmp_dir.name

    files_store = provider_store = None
    if uploading and args.type == "helm_charts":
        mc.helm_login(mc.charts_ref(args.oci_registry, args.charts_oci))
    elif uploading and args.type == "tofu_providers":
        provider_store = (mc.store_for(args.provider_mirror) if args.provider_mirror
                          else mc.sub_store(args.files_url, "providers"))
    elif uploading:
        files_store = mc.store_for(args.files_url)

    success = skipped = failed = 0

    for dep in dep_list:
        print(f"\n{'='*60}\n  {dep['name']} {dep['version']}\n{'='*60}")

        if not args.upload_only:
            if args.type == "helm_charts":
                ok = download_helm_chart_local(dep, local_dir)
            elif not dep.get("source_url"):
                print(f"  ERROR: no source_url for {dep['name']}", file=sys.stderr)
                ok = False
            else:
                ok = download_to_local(dep["source_url"], get_local_path(local_dir, args.type, dep))
            if not ok:
                failed += 1
                continue

        if not uploading:
            success += 1
            continue
        if args.type == "tofu_providers":
            continue  # uploaded as one tree below
        if args.type == "helm_charts":
            state = upload_helm_chart(dep, local_dir, mc.charts_ref(args.oci_registry, args.charts_oci),
                                      args.force)
        else:
            state = upload_one(files_store, dep["path"],
                               get_local_path(local_dir, args.type, dep), args.force)
        if state == "ok":
            success += 1
        elif state == "skip":
            skipped += 1
        else:
            failed += 1

    if uploading and args.type == "tofu_providers":
        print(f"\n{'='*60}\n  OpenTofu network mirror\n{'='*60}")
        ok, skip, bad = upload_providers(dep_list, local_dir, provider_store, args.force)
        success += ok
        skipped += skip
        failed += bad

    report = {"type": args.type, "total": len(dep_list), "success": success,
              "skipped": skipped, "failed": failed}
    with open(f"download-report-{args.type}.json", "w") as f:
        json.dump(report, f, indent=2)

    print(f"\n{'='*60}")
    print(f"Result: {success} OK, {skipped} skipped, {failed} failed out of {len(dep_list)}")
    print(f"{'='*60}")

    if tmp_dir:
        tmp_dir.cleanup()
    if failed > 0:
        sys.exit(1)


if __name__ == "__main__":
    main()
