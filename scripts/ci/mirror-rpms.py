#!/usr/bin/env python3
"""Mirror RPM packages to an Artifactory YUM repository (rondo-rpms-local).

Resolves dependencies with `dnf download --resolve --alldeps`, then pushes each .rpm
to Artifactory, which indexes the repository itself (repodata): no `createrepo` to run.

The wanted packages are declared and CI resolves them, in a UBI9 image: the full tree
changes with every upstream release, and pinning it by hand would be unmanageable.

  scripts/ci/mirror-rpms.py --deps deps-resolved.json \
      --artifactory-url https://arti/artifactory --repo rondo-rpms-local \
      --creds user:password

Usage: CI job `deps-push-rpms` (online zone). Idempotent: a package already present
with the same checksum is skipped.
"""

import argparse
import hashlib
import json
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

import requests

REPOS_ENTRY = "_repos"


def load_packages(deps_file: str) -> tuple[list[str], list[dict], bool]:
    with open(deps_file) as f:
        deps = json.load(f)
    present = "rpm_packages" in deps
    entries = deps.get("rpm_packages") or []
    packages, repos = [], []
    for e in entries:
        if e.get("name") == REPOS_ENTRY:
            repos = e.get("upstream_repos", [])
            continue
        packages.append(e["name"])
    return packages, repos, present


def write_repo_files(repos: list[dict], repo_dir: str) -> list[str]:
    Path(repo_dir).mkdir(parents=True, exist_ok=True)
    names = []
    for r in repos:
        names.append(r["name"])
        if not r.get("baseurl"):
            continue
        path = Path(repo_dir) / f"{r['name']}.repo"
        gpgkey = r.get("gpgkey", "")
        path.write_text(
            f"[{r['name']}]\n"
            f"name={r.get('description', r['name'])}\n"
            f"baseurl={r['baseurl']}\n"
            f"enabled=1\n"
            f"gpgcheck={1 if gpgkey else 0}\n"
            + (f"gpgkey={gpgkey}\n" if gpgkey else "")
            + "module_hotfixes=1\n"
        )
        print(f"  upstream repository configured: {r['name']}")
    return names


def resolve_and_download(packages: list[str], repo_names: list[str],
                         dest: str, fail_on_missing: bool) -> list[Path]:
    Path(dest).mkdir(parents=True, exist_ok=True)

    available, missing = [], []
    for pkg in packages:
        rc = subprocess.run(
            ["dnf", "repoquery", "--quiet", "--queryformat", "%{name}", pkg],
            capture_output=True, text=True, check=False,
        )
        if rc.returncode == 0 and rc.stdout.strip():
            available.append(pkg)
        else:
            missing.append(pkg)

    if missing:
        print("\nPackages NOT FOUND upstream:", file=sys.stderr)
        for pkg in missing:
            print(f"  - {pkg}", file=sys.stderr)
        print("  Usual cause: an upstream rename (clamav-update -> clamav-freshclam,\n"
              "  php-pecl-redis5 -> php-pecl-redis6 for PHP 8.4). Fix\n"
              "  manifests/dependencies.yaml.", file=sys.stderr)
        if fail_on_missing:
            sys.exit(1)

    if not available:
        print("No package to download.", file=sys.stderr)
        return []

    enable = [f"--enablerepo={n}" for n in repo_names] if repo_names else []
    cmd = ["dnf", "download", "--resolve", "--alldeps",
           f"--destdir={dest}"] + enable + available
    print(f"\n==> dnf download --resolve ({len(available)} wanted packages)")
    rc = subprocess.run(cmd, check=False)
    if rc.returncode != 0:
        print("dnf download failed", file=sys.stderr)
        sys.exit(1)

    rpms = sorted(Path(dest).glob("*.rpm"))
    print(f"==> {len(rpms)} RPMs obtained (wanted + resolved dependencies)")
    return rpms


def rpm_target_url(base_url: str, repo: str, path_prefix: str, name: str) -> str:
    segments = [base_url.rstrip("/"), repo.strip("/")]
    if path_prefix.strip("/"):
        segments.append(path_prefix.strip("/"))
    segments.append(name)
    return "/".join(segments)


def sha256_of(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def upload(rpm: Path, base_url: str, repo: str, path_prefix: str,
           auth: tuple[str, str], skip_existing: bool) -> tuple[str, str]:
    target = rpm_target_url(base_url, repo, path_prefix, rpm.name)
    digest = sha256_of(rpm)

    if skip_existing:
        head = requests.head(target, auth=auth, timeout=30)
        if head.status_code == 200:
            remote = head.headers.get("X-Checksum-Sha256", "")
            if remote == digest:
                return rpm.name, "skip"

    with open(rpm, "rb") as f:
        resp = requests.put(
            target, data=f, auth=auth, timeout=600,
            headers={"X-Checksum-Sha256": digest},
        )
    if resp.status_code not in (200, 201):
        return rpm.name, f"error: HTTP {resp.status_code} {resp.text[:200]}"
    return rpm.name, "ok"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--deps", required=True, help="resolved manifest (JSON)")
    parser.add_argument("--artifactory-url", required=True)
    parser.add_argument("--repo", default="rondo-rpms-local")
    parser.add_argument("--path-prefix", default="el9",
                        help="sub-path in the repository (must match the "
                             "component's spec.vm.rpmRepos[].path)")
    parser.add_argument("--creds", default="", help="user:password")
    parser.add_argument("--dest", default="/tmp/rpms", help="working directory")
    parser.add_argument("--repo-dir", default="/etc/yum.repos.d")
    parser.add_argument("--workers", type=int, default=8)
    parser.add_argument("--skip-existing", action=argparse.BooleanOptionalAction,
                        default=True)
    parser.add_argument("--fail-on-missing", action="store_true",
                        help="fail if a wanted package is not found upstream "
                             "(enable once the list is stable)")
    parser.add_argument("--download-only", action="store_true",
                        help="resolve and download without pushing (debugging)")
    args = parser.parse_args()

    packages, repos, present = load_packages(args.deps)
    if not present:
        print("The rpm_packages section is MISSING from the resolved manifest: broken "
              "manifest, or --deps points to the wrong place. An empty section is a "
              "legitimate state, a missing one is not.", file=sys.stderr)
        sys.exit(1)
    if not packages:
        print("==> No RPM package to mirror: the rpm_packages section is empty.\n"
              "    This is the expected state while each component publishes its own "
              "packages through ITS pipeline (the rondo mirror cannot verify an "
              "upstream signature). Nothing to do.")
        return
    print(f"==> {len(packages)} wanted packages, {len(repos)} upstream repository(ies)")

    repo_names = write_repo_files(repos, args.repo_dir)
    rpms = resolve_and_download(packages, repo_names, args.dest, args.fail_on_missing)
    if not rpms:
        sys.exit(1)
    if args.download_only:
        print("--download-only: nothing is pushed.")
        return

    user, _, password = args.creds.partition(":")
    if not user or not password:
        print("--creds user:password is required to push.", file=sys.stderr)
        sys.exit(1)
    auth = (user, password)

    results: dict[str, str] = {}
    with ThreadPoolExecutor(max_workers=args.workers) as pool:
        futures = {
            pool.submit(upload, r, args.artifactory_url, args.repo,
                        args.path_prefix, auth, args.skip_existing): r
            for r in rpms
        }
        for fut in as_completed(futures):
            name, state = fut.result()
            results[name] = state
            if state.startswith("error"):
                print(f"  KO   {name} : {state}", file=sys.stderr)

    ok = sum(1 for s in results.values() if s == "ok")
    skipped = sum(1 for s in results.values() if s == "skip")
    errors = [n for n, s in results.items() if s.startswith("error")]
    print(f"\n==> {ok} pushed, {skipped} already up to date, {len(errors)} failed")
    if errors:
        sys.exit(1)
    print(f"==> Artifactory is indexing repository {args.repo} (asynchronous "
          "repodata); wait before running `dnf makecache` on the machine.")


if __name__ == "__main__":
    main()
