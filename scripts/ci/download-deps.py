#!/usr/bin/env python3

import argparse
import glob
import json
import os
import re
import subprocess
import sys
import tempfile

import requests
from requests.adapters import HTTPAdapter
from urllib3.util.retry import Retry


def get_session() -> requests.Session:
    session = requests.Session()
    user = os.environ.get("ARTIFACTORY_USER", "")
    password = os.environ.get("ARTIFACTORY_PASSWORD", "")
    token = os.environ.get("ARTIFACTORY_TOKEN", "")

    if token:
        session.headers["Authorization"] = f"Bearer {token}"
    elif user and password:
        session.auth = (user, password)

    return session


def source_session() -> requests.Session:
    session = requests.Session()
    retry = Retry(
        total=3,
        connect=3,
        read=3,
        backoff_factor=2,
        status_forcelist=(429, 500, 502, 503, 504),
        allowed_methods=frozenset(["GET", "HEAD"]),
        raise_on_status=False,
    )
    session.mount("https://", HTTPAdapter(max_retries=retry))
    session.mount("http://", HTTPAdapter(max_retries=retry))
    return session


def get_local_path(local_dir: str, dep_type: str, dep: dict) -> str:
    if dep_type == "helm_charts":
        return os.path.join(local_dir, "helm_charts", f"{dep['name']}-{dep['version']}.tgz")
    safe_name = dep["name"].replace("/", "_")
    basename = os.path.basename(dep["target_path"])
    return os.path.join(local_dir, dep_type, safe_name, basename)


def check_exists_in_artifactory(session: requests.Session, artifactory_url: str,
                                 repo: str, path: str) -> bool:
    url = f"{artifactory_url}/{repo}/{path}"
    resp = session.head(url, timeout=30)
    return resp.status_code == 200


def download_to_local(source_url: str, local_path: str) -> bool:
    print(f"  Downloading: {source_url}")
    try:
        os.makedirs(os.path.dirname(local_path), exist_ok=True)
        resp = source_session().get(source_url, stream=True, timeout=300, verify=True)
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
            subprocess.run(
                ["helm", "repo", "add", repo_alias, source_repo, "--force-update"],
                check=True, capture_output=True,
            )
            subprocess.run(
                ["helm", "repo", "update", repo_alias],
                check=True, capture_output=True,
            )
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


def upload_to_artifactory(session: requests.Session, local_path: str,
                          artifactory_url: str, target_repo: str,
                          target_path: str) -> bool:
    upload_url = f"{artifactory_url}/{target_repo}/{target_path}"
    print(f"  Upload: {upload_url}")
    try:
        with open(local_path, "rb") as f:
            resp = session.put(upload_url, data=f, timeout=600)
            resp.raise_for_status()
        print(f"  OK: {target_path}")
        return True
    except (requests.exceptions.RequestException, FileNotFoundError) as e:
        print(f"  ERROR: {e}", file=sys.stderr)
        return False


def upload_helm_chart(dep: dict, local_dir: str, artifactory_url: str) -> bool:
    name = dep["name"]
    version = dep["version"]
    target_repo = dep["target_repo"]
    chart_dir = os.path.join(local_dir, "helm_charts")
    chart_file = os.path.join(chart_dir, f"{name}-{version}.tgz")

    if not os.path.exists(chart_file):
        tgz_files = glob.glob(os.path.join(chart_dir, f"{name}-*.tgz"))
        if not tgz_files:
            print(f"  ERROR: {name}-{version}.tgz not found in {chart_dir}", file=sys.stderr)
            return False
        chart_file = tgz_files[0]

    try:
        helm_host = re.sub(r'^https?://', '', artifactory_url).split('/')[0]
        oci_target = f"oci://{helm_host}/{target_repo}"
        print(f"  Push OCI: {os.path.basename(chart_file)} -> {oci_target}")
        subprocess.run(
            ["helm", "push", chart_file, oci_target],
            check=True, capture_output=True, text=True,
        )
        print(f"  OK: {os.path.basename(chart_file)}")
        return True
    except subprocess.CalledProcessError as e:
        stderr = e.stderr if e.stderr else e.stdout if e.stdout else str(e)
        print(f"  ERROR helm push: {stderr}", file=sys.stderr)
        return False


def download_and_upload(session: requests.Session, source_url: str,
                        artifactory_url: str, target_repo: str,
                        target_path: str) -> bool:
    print(f"  Downloading: {source_url}")

    tmp_path = None
    try:
        with tempfile.NamedTemporaryFile(delete=False) as tmp:
            tmp_path = tmp.name
            resp = source_session().get(source_url, stream=True, timeout=300, verify=True)
            resp.raise_for_status()
            for chunk in resp.iter_content(chunk_size=8192):
                tmp.write(chunk)
            tmp.flush()

        upload_url = f"{artifactory_url}/{target_repo}/{target_path}"
        print(f"  Upload: {upload_url}")

        with open(tmp_path, "rb") as f:
            resp = session.put(upload_url, data=f, timeout=600)
            resp.raise_for_status()

        print(f"  OK: {target_path}")
        return True
    except requests.exceptions.RequestException as e:
        print(f"  ERROR: {e}", file=sys.stderr)
        return False
    finally:
        if tmp_path and os.path.exists(tmp_path):
            os.unlink(tmp_path)


def download_helm_chart(session: requests.Session, dep: dict,
                        artifactory_url: str) -> bool:
    name = dep["name"]
    version = dep["version"]
    source_repo = dep.get("source_repo", "")
    source_type = dep.get("source_type", "repo")
    target_repo = dep["target_repo"]
    repo_alias = f"tmp-{name}-{os.getpid()}"

    try:
        with tempfile.TemporaryDirectory() as tmp_dir:
            if source_type == "oci":
                oci_ref = f"{source_repo}/{name}"
                print(f"  Pull OCI: {oci_ref} --version {version}")
                subprocess.run(
                    ["helm", "pull", oci_ref, "--version", version, "-d", tmp_dir],
                    check=True, capture_output=True, text=True
                )
            else:
                subprocess.run(
                    ["helm", "repo", "add", repo_alias, source_repo, "--force-update"],
                    check=True, capture_output=True
                )
                subprocess.run(["helm", "repo", "update", repo_alias], check=True, capture_output=True)
                subprocess.run(
                    ["helm", "pull", f"{repo_alias}/{name}", "--version", version, "-d", tmp_dir],
                    check=True, capture_output=True, text=True
                )

            tgz_files = glob.glob(os.path.join(tmp_dir, "*.tgz"))
            if not tgz_files:
                print(f"  ERROR: chart {name}-{version}.tgz not found", file=sys.stderr)
                return False

            chart_file = tgz_files[0]
            chart_filename = os.path.basename(chart_file)

            helm_host = os.environ.get("ARTIFACTORY_ONLINE_URL", "")
            if not helm_host:
                print("  ERROR: ARTIFACTORY_ONLINE_URL is required for helm push OCI", file=sys.stderr)
                return False

            helm_host = re.sub(r'^https?://', '', helm_host).split('/')[0]

            oci_target = f"oci://{helm_host}/{target_repo}"
            print(f"  Push OCI: {chart_filename} -> {oci_target}")
            subprocess.run(
                ["helm", "push", chart_file, oci_target],
                check=True, capture_output=True, text=True
            )

            print(f"  OK: {chart_filename}")
            return True
    except subprocess.CalledProcessError as e:
        stderr = e.stderr if e.stderr else e.stdout if e.stdout else str(e)
        print(f"  ERROR helm: {stderr}", file=sys.stderr)
        return False
    except requests.exceptions.RequestException as e:
        print(f"  ERROR upload: {e}", file=sys.stderr)
        return False
    finally:
        if source_type != "oci":
            subprocess.run(["helm", "repo", "remove", repo_alias], capture_output=True)


def main():
    parser = argparse.ArgumentParser(description="Download and push dependencies")
    parser.add_argument("--deps", required=True, help="Resolved deps JSON file")
    parser.add_argument("--type", required=True,
                        choices=["binaries", "tofu_providers", "helm_charts", "grafana_plugins"],
                        help="Type of dependencies to process")
    parser.add_argument("--artifactory-url", default="", help="Artifactory base URL")
    parser.add_argument("--skip-existing", action="store_true", default=True,
                        help="Skip deps already in Artifactory")
    parser.add_argument("--local-dir", default="",
                        help="Local directory for the artifacts")
    parser.add_argument("--download-only", action="store_true",
                        help="Download only (no upload to Artifactory)")
    parser.add_argument("--upload-only", action="store_true",
                        help="Upload only, from --local-dir to Artifactory")
    args = parser.parse_args()

    if args.download_only and args.upload_only:
        print("ERROR: --download-only and --upload-only are mutually exclusive", file=sys.stderr)
        sys.exit(1)

    if (args.download_only or args.upload_only) and not args.local_dir:
        print("ERROR: --local-dir is required with --download-only or --upload-only", file=sys.stderr)
        sys.exit(1)

    if not args.download_only and not args.artifactory_url:
        print("ERROR: --artifactory-url is required for upload", file=sys.stderr)
        sys.exit(1)

    with open(args.deps) as f:
        deps = json.load(f)

    session = get_session() if not args.download_only else None
    dep_list = deps.get(args.type, [])
    success = 0
    skipped = 0
    failed = 0

    for dep in dep_list:
        name = dep["name"]
        version = dep["version"]
        target_repo = dep["target_repo"]
        target_path = dep.get("target_path", "")

        print(f"\n{'='*60}")
        print(f"  {name} {version}")
        print(f"{'='*60}")

        if not args.download_only and args.skip_existing and target_path:
            if check_exists_in_artifactory(session, args.artifactory_url, target_repo, target_path):
                print(f"  SKIP: already present in {target_repo}/{target_path}")
                skipped += 1
                continue

        if args.download_only:
            if args.type == "helm_charts":
                ok = download_helm_chart_local(dep, args.local_dir)
            else:
                source_url = dep.get("source_url", "")
                if not source_url:
                    print(f"  ERROR: no source_url for {name}", file=sys.stderr)
                    failed += 1
                    continue
                local_path = get_local_path(args.local_dir, args.type, dep)
                ok = download_to_local(source_url, local_path)

        elif args.upload_only:
            if args.type == "helm_charts":
                ok = upload_helm_chart(dep, args.local_dir, args.artifactory_url)
            else:
                local_path = get_local_path(args.local_dir, args.type, dep)
                ok = upload_to_artifactory(session, local_path, args.artifactory_url,
                                           target_repo, target_path)

        else:
            if args.type == "helm_charts":
                ok = download_helm_chart(session, dep, args.artifactory_url)
            else:
                source_url = dep.get("source_url", "")
                if not source_url:
                    print(f"  ERROR: no source_url for {name}", file=sys.stderr)
                    failed += 1
                    continue
                ok = download_and_upload(session, source_url, args.artifactory_url,
                                         target_repo, target_path)

        if ok:
            success += 1
        else:
            failed += 1

    report = {
        "type": args.type,
        "total": len(dep_list),
        "success": success,
        "skipped": skipped,
        "failed": failed
    }

    report_file = f"download-report-{args.type}.json"
    with open(report_file, "w") as f:
        json.dump(report, f, indent=2)

    print(f"\n{'='*60}")
    print(f"Result: {success} OK, {skipped} skipped, {failed} failed out of {len(dep_list)}")
    print(f"{'='*60}")

    if failed > 0:
        sys.exit(1)


if __name__ == "__main__":
    main()
