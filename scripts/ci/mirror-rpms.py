#!/usr/bin/env python3
"""Mirror RPM packages to an RPM repository served by any HTTP file server.

Resolves dependencies with `dnf download --resolve --alldeps`, builds the repository
metadata locally with `createrepo_c` (`repodata/`), then uploads packages and metadata to
`<rpm-url>/<path-prefix>/`. `--rpm-url` is an HTTP(S) base URL (PUT), s3://bucket/prefix
(boto3, optional) or a directory.

The wanted packages are declared and CI resolves them, in a UBI9 image: the full tree
changes with every upstream release, and pinning it by hand would be unmanageable.

  scripts/ci/mirror-rpms.py --deps deps-resolved.json --rpm-url https://files.site/rpms
  (MIRROR_USER / MIRROR_PASSWORD / MIRROR_CA_CERT in the environment)

Idempotent: a file whose `<file>.sha256` sidecar holds its digest is not uploaded again.
`repodata/` describes the packages resolved by this run, so the run must resolve the whole
set, which it always does.
"""

import argparse
import json
import shutil
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

import mirror_common as mc

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


def build_repodata(directory: str) -> bool:
    """createrepo_c over `directory`; False (with a message) when it is not installed."""
    if shutil.which("createrepo_c") is None:
        print("WARN: createrepo_c is not installed: repodata/ is NOT built, so the repository "
              "is not usable by dnf until it is (dnf install createrepo_c). "
              "Packages are still uploaded.", file=sys.stderr)
        return False
    subprocess.run(["createrepo_c", "--quiet", "--update", directory], check=True)
    return True


def upload_tree(directory: str, store: mc.Store, path_prefix: str,
                skip_existing: bool, workers: int) -> dict[str, str]:
    """Upload the packages, then repodata/ (repomd.xml last, it is what clients read first)."""
    root = Path(directory)
    files = [p for p in root.rglob("*") if p.is_file()]

    def rel(p: Path) -> str:
        return "/".join(x for x in (path_prefix.strip("/"), p.relative_to(root).as_posix()) if x)

    def send(p: Path) -> tuple[str, str]:
        try:
            if skip_existing:
                state = "ok" if store.put_checked(rel(p), p) else "skip"
            else:
                store.put(rel(p), p)
                state = "ok"
        except Exception as e:  # noqa: BLE001 (any transport error is a failed upload)
            state = f"error: {e}"
        return rel(p), state

    results: dict[str, str] = {}
    packages = [p for p in files if p.parent == root]
    metadata = [p for p in files if p.parent != root and p.name != "repomd.xml"]
    repomd = [p for p in files if p.name == "repomd.xml"]
    for batch in (packages, metadata):
        with ThreadPoolExecutor(max_workers=workers) as pool:
            for fut in as_completed([pool.submit(send, p) for p in batch]):
                name, state = fut.result()
                results[name] = state
                if state.startswith("error"):
                    print(f"  KO   {name} : {state}", file=sys.stderr)
    for p in repomd:
        name, state = send(p)
        results[name] = state
        if state.startswith("error"):
            print(f"  KO   {name} : {state}", file=sys.stderr)
    return results


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--deps", required=True, help="resolved manifest (JSON)")
    mc.add_target_args(parser, rpm=True)
    parser.add_argument("--path-prefix", default="el9",
                        help="sub-path under --rpm-url (must match the "
                             "component's spec.vm.rpmRepos[].path)")
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

    if not args.rpm_url:
        mc.die("--rpm-url (MIRROR_RPM_URL) is required to push.")
    store = mc.store_for(args.rpm_url)

    # createrepo_c writes repodata/ into --dest, next to the packages.
    build_repodata(args.dest)
    results = upload_tree(args.dest, store, args.path_prefix, args.skip_existing, args.workers)

    ok = sum(1 for s in results.values() if s == "ok")
    skipped = sum(1 for s in results.values() if s == "skip")
    errors = [n for n, s in results.items() if s.startswith("error")]
    print(f"\n==> {ok} pushed, {skipped} already up to date, {len(errors)} failed")
    if errors:
        sys.exit(1)


if __name__ == "__main__":
    main()
