#!/usr/bin/env python3

import argparse
import json
import sys
from pathlib import Path

import yaml


def resolve_version_placeholders(data: dict) -> dict:
    resolved = {}
    for dep_type, deps in data.items():
        resolved[dep_type] = []
        for dep in deps:
            entry = dict(dep)
            version = entry.get("version", "")
            for key in ("source_url", "target_path"):
                if key in entry and "{version}" in entry[key]:
                    entry[key] = entry[key].replace("{version}", version)
            resolved[dep_type].append(entry)
    return resolved


def validate_manifest(data: dict) -> list[str]:
    errors = []
    valid_types = {"binaries", "tofu_providers", "docker_images", "helm_charts", "grafana_plugins",
                   "base_images", "rpm_packages"}
    types_requiring_source = {"binaries", "tofu_providers", "grafana_plugins"}
    types_without_target = {"base_images"}

    for dep_type in data:
        if dep_type not in valid_types:
            errors.append(f"Unknown type: {dep_type}")
            continue

        for i, dep in enumerate(data[dep_type]):
            if "name" not in dep:
                errors.append(f"{dep_type}[{i}]: missing 'name' field")
            if "version" not in dep:
                errors.append(f"{dep_type}[{i}]: missing 'version' field")
            if "target_repo" not in dep and dep_type not in types_without_target:
                errors.append(f"{dep_type}[{i}]: missing 'target_repo' field")
            if dep_type in types_requiring_source and "source_url" not in dep:
                errors.append(f"{dep_type}[{i}]: missing 'source_url' field")
            if dep_type == "helm_charts" and "source_repo" not in dep:
                errors.append(f"{dep_type}[{i}]: missing 'source_repo' field")

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
