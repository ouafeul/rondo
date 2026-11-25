#!/usr/bin/env python3

import argparse
import json
import os
import re
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed

import requests

_REGISTRY_PATTERN = re.compile(r"^[a-zA-Z0-9][-a-zA-Z0-9]*\.[a-zA-Z0-9][-a-zA-Z0-9.]*(/|$)")

INSPECT_TIMEOUT = 30
COPY_TIMEOUT = 600


def normalize_source_ref(image: str) -> str:
    if _REGISTRY_PATTERN.match(image):
        return image
    if "/" not in image:
        return f"docker.io/library/{image}"
    return f"docker.io/{image}"


def strip_source_registry(image: str) -> str:
    if _REGISTRY_PATTERN.match(image):
        return image.split("/", 1)[1]
    return image


def split_ref(ref: str) -> tuple[str, str]:
    if ":" in ref.rsplit("/", 1)[-1]:
        return tuple(ref.rsplit(":", 1))
    return ref, "latest"


def get_local_path(local_dir: str, name: str, tag: str) -> str:
    safe = normalize_source_ref(name).replace("/", "_").replace(":", "_")
    return os.path.join(local_dir, "docker_images", f"{safe}_{tag}.tar")


def _src_creds_args(source_normalized: str) -> list[str]:
    src_creds = os.environ.get("DOCKERHUB_CREDS", "")
    if src_creds and source_normalized.startswith("docker.io/"):
        return ["--src-creds", src_creds]
    return []


def image_exists_in_target(name: str, tag: str, target_registry: str,
                           dest_creds: str) -> bool:
    if not target_registry:
        return False
    target = f"docker://{target_registry}/{strip_source_registry(name)}:{tag}"
    cmd = ["skopeo", "inspect", "--raw", target]
    if dest_creds:
        cmd.extend(["--creds", dest_creds])
    try:
        result = subprocess.run(cmd, capture_output=True, text=True,
                                timeout=INSPECT_TIMEOUT)
        return result.returncode == 0
    except subprocess.TimeoutExpired:
        return False


def _run_skopeo(cmd: list[str]) -> tuple[bool, str]:
    try:
        result = subprocess.run(cmd, capture_output=True, text=True,
                                timeout=COPY_TIMEOUT)
    except subprocess.TimeoutExpired:
        return False, f"timeout (> {COPY_TIMEOUT}s)"
    if result.returncode != 0:
        return False, result.stderr.strip()[:300]
    return True, "OK"


def skopeo_download(name: str, tag: str, local_path: str) -> tuple[bool, str]:
    source_normalized = normalize_source_ref(name)
    os.makedirs(os.path.dirname(local_path), exist_ok=True)
    cmd = [
        "skopeo", "copy",
        "--override-arch", "amd64",
        "--override-os", "linux",
        f"docker://{source_normalized}:{tag}",
        f"docker-archive:{local_path}",
    ] + _src_creds_args(source_normalized)
    return _run_skopeo(cmd)


def skopeo_upload(local_path: str, name: str, tag: str,
                  target_registry: str, dest_creds: str) -> tuple[bool, str]:
    target = f"docker://{target_registry}/{strip_source_registry(name)}:{tag}"
    cmd = ["skopeo", "copy", f"docker-archive:{local_path}", target]
    if dest_creds:
        cmd.extend(["--dest-creds", dest_creds])
    return _run_skopeo(cmd)


def skopeo_copy(name: str, tag: str, target_registry: str,
                dest_creds: str) -> tuple[bool, str]:
    source_normalized = normalize_source_ref(name)
    target = f"docker://{target_registry}/{strip_source_registry(name)}:{tag}"
    cmd = [
        "skopeo", "copy",
        "--override-arch", "amd64",
        "--override-os", "linux",
        f"docker://{source_normalized}:{tag}",
        target,
    ]
    if dest_creds:
        cmd.extend(["--dest-creds", dest_creds])
    cmd.extend(_src_creds_args(source_normalized))
    return _run_skopeo(cmd)


def process_ref(ref: str, args) -> tuple[str, str]:
    name, tag = split_ref(ref)

    if args.skip_existing and image_exists_in_target(
            name, tag, args.target_registry, args.dest_creds):
        return ref, "skip"

    if args.download_only:
        ok, msg = skopeo_download(name, tag, get_local_path(args.local_dir, name, tag))
    elif args.upload_only:
        local_path = get_local_path(args.local_dir, name, tag)
        if not os.path.exists(local_path):
            return ref, "skip"
        ok, msg = skopeo_upload(local_path, name, tag,
                                args.target_registry, args.dest_creds)
    else:
        ok, msg = skopeo_copy(name, tag, args.target_registry, args.dest_creds)

    return ref, "ok" if ok else f"error: {msg}"


def load_refs(args) -> list[str]:
    if args.deps:
        with open(args.deps) as f:
            deps = json.load(f)
        entries = deps.get(args.section, [])
        return [f"{e['name']}:{e['version']}" for e in entries]

    if args.list_url:
        print(f"Downloading the list: {args.list_url}")
        resp = requests.get(args.list_url, timeout=60)
        resp.raise_for_status()
        text = resp.text
    else:
        with open(args.list_file) as f:
            text = f.read()

    return [line.strip() for line in text.splitlines()
            if line.strip() and not line.strip().startswith("#")]


def main():
    parser = argparse.ArgumentParser(
        description="Mirror OCI images to a private registry via skopeo")
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--deps", help="Resolved deps JSON file")
    source.add_argument("--list-url", help="URL of a plain-text image list")
    source.add_argument("--list-file", help="Local image list file")
    parser.add_argument("--section", default="docker_images",
                        choices=["docker_images"],
                        help="Manifest section to mirror (with --deps)")
    parser.add_argument("--target-registry", default="",
                        help="Target Docker registry (Artifactory)")
    parser.add_argument("--dest-creds", default="",
                        help="Target registry credentials (user:password)")
    parser.add_argument("--local-dir", default="",
                        help="Local directory for the tars (staged modes)")
    parser.add_argument("--download-only", action="store_true",
                        help="Download only, to --local-dir")
    parser.add_argument("--upload-only", action="store_true",
                        help="Push only, from --local-dir")
    parser.add_argument("--skip-existing", action=argparse.BooleanOptionalAction,
                        default=True,
                        help="Skip images already present in the target (delta)")
    parser.add_argument("--workers", type=int, default=8,
                        help="Parallel workers (default: 8)")
    args = parser.parse_args()

    if args.download_only and args.upload_only:
        print("ERROR: --download-only and --upload-only are mutually exclusive",
              file=sys.stderr)
        sys.exit(1)
    if (args.download_only or args.upload_only) and not args.local_dir:
        print("ERROR: --local-dir is required with --download-only or --upload-only",
              file=sys.stderr)
        sys.exit(1)
    if not args.download_only and not args.target_registry:
        print("ERROR: --target-registry is required for upload or direct copy",
              file=sys.stderr)
        sys.exit(1)

    refs = load_refs(args)
    if not refs:
        print("No image to process")
        return

    mode = ("download" if args.download_only
            else "upload" if args.upload_only else "copie directe")
    print(f"{len(refs)} image(s) to process, mode {mode}, {args.workers} workers, "
          f"skip-existing={args.skip_existing}\n")

    ok_count = skip_count = fail_count = 0
    failures = []

    with ThreadPoolExecutor(max_workers=args.workers) as executor:
        futures = {executor.submit(process_ref, ref, args): ref for ref in refs}
        for future in as_completed(futures):
            ref, status = future.result()
            if status == "skip":
                skip_count += 1
                print(f"  SKIP  {ref}")
            elif status == "ok":
                ok_count += 1
                print(f"  OK    {ref}")
            else:
                fail_count += 1
                failures.append((ref, status))
                print(f"  FAIL  {ref}: {status}", file=sys.stderr)

    print(f"\n{'='*60}")
    print(f"Result: {ok_count} OK, {skip_count} skipped, {fail_count} failed "
          f"out of {len(refs)}")
    print(f"{'='*60}")

    if failures:
        print("\nFailed images:", file=sys.stderr)
        for ref, status in failures:
            print(f"  {ref}: {status}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
