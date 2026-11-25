#!/usr/bin/env python3

import argparse
import json
import re
import sys
from pathlib import Path

import yaml

PROVIDER_ARCHIVE = re.compile(r"_[a-z0-9]+_[a-z0-9]+\.zip$")


def resolve_version_placeholders(data: dict) -> dict:
    resolved = {}
    for dep_type, deps in data.items():
        resolved[dep_type] = []
        for dep in deps or []:
            entry = dict(dep)
            version = entry.get("version", "")
            for key in ("source_url", "path"):
                if key in entry and "{version}" in entry[key]:
                    entry[key] = entry[key].replace("{version}", version)
            resolved[dep_type].append(entry)
    return resolved


# Section -> the `kind` its entries must declare (see the header of manifests/dependencies.yaml).
SECTION_KINDS = {
    "binaries": "file",
    "grafana_plugins": "grafana_plugin",
    "tofu_providers": "provider",
    "helm_charts": "chart",
    "docker_images": "image",
    "rpm_packages": "rpm",
}
# rpm_packages carries one `_repos` pseudo-entry (upstream repositories), which is not mirrored.
PSEUDO_ENTRIES = {"rpm_packages": "_repos"}


def validate_manifest(data: dict) -> list[str]:
    errors = []
    valid_types = set(SECTION_KINDS) | {"base_images"}
    types_requiring_source = {"binaries", "tofu_providers", "grafana_plugins"}

    for dep_type in data:
        if dep_type not in valid_types:
            errors.append(f"Unknown type: {dep_type}")
            continue

        for i, dep in enumerate(data[dep_type] or []):
            if "name" not in dep:
                errors.append(f"{dep_type}[{i}]: missing 'name' field")
            if "version" not in dep and dep.get("name") != PSEUDO_ENTRIES.get(dep_type):
                errors.append(f"{dep_type}[{i}]: missing 'version' field")
            if dep_type in types_requiring_source and "source_url" not in dep:
                errors.append(f"{dep_type}[{i}]: missing 'source_url' field")
            if dep_type == "helm_charts" and "source_repo" not in dep:
                errors.append(f"{dep_type}[{i}]: missing 'source_repo' field")
            if dep_type not in SECTION_KINDS or dep.get("name") == PSEUDO_ENTRIES.get(dep_type):
                continue
            want = SECTION_KINDS[dep_type]
            if dep.get("kind") != want:
                errors.append(f"{dep_type}[{i}]: 'kind' must be {want!r}, got {dep.get('kind')!r}")
            path = dep.get("path")
            if not path or not isinstance(path, str):
                errors.append(f"{dep_type}[{i}]: missing 'path' field")
            elif path.startswith("/") or ".." in path.split("/"):
                errors.append(f"{dep_type}[{i}]: 'path' must be relative, without '..': {path}")
            elif dep_type == "tofu_providers" and not PROVIDER_ARCHIVE.search(path):
                errors.append(f"{dep_type}[{i}]: 'path' must end in _<os>_<arch>.zip: {path}")

    return errors


def main():
    parser = argparse.ArgumentParser(description="Resolve dependencies manifest")
    parser.add_argument("--manifest", required=True, help="Path to dependencies.yaml")
    parser.add_argument("--output", required=True, help="Output JSON file")
    args = parser.parse_args()

    manifest_path = Path(args.manifest)
    if not manifest_path.exists():
        print(f"ERROR: manifest not found: {manifest_path}", file=sys.stderr)
        sys.exit(1)

    with open(manifest_path) as f:
        data = yaml.safe_load(f)

    if data is None:
        print("ERROR: empty manifest", file=sys.stderr)
        sys.exit(1)

    errors = validate_manifest(data)
    if errors:
        print("ERROR: invalid manifest:", file=sys.stderr)
        for e in errors:
            print(f"  - {e}", file=sys.stderr)
        sys.exit(1)

    resolved = resolve_version_placeholders(data)

    total = sum(len(deps) for deps in resolved.values())
    print(f"Manifest resolved: {total} dependencies")
    for dep_type, deps in resolved.items():
        print(f"  {dep_type}: {len(deps)}")

    with open(args.output, "w") as f:
        json.dump(resolved, f, indent=2)

    print(f"Output written to {args.output}")


if __name__ == "__main__":
    main()
