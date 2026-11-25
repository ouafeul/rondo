#!/usr/bin/env python3

import argparse
import os
import re
import subprocess
import sys
import tempfile

import yaml


_REGISTRY_PATTERN = re.compile(
    r"^[a-zA-Z0-9][-a-zA-Z0-9]*\.[a-zA-Z0-9][-a-zA-Z0-9.]*(:[0-9]+)?/"
)

_GOTPL_SENTINEL = "__GOTPL__"

_ARG_IMAGE_PATTERN = re.compile(
    r"[a-zA-Z0-9][-a-zA-Z0-9]*\.[a-zA-Z0-9][-a-zA-Z0-9.]*/[a-zA-Z0-9][-a-zA-Z0-9_./]*:[a-zA-Z0-9][-a-zA-Z0-9_.]*"
)


def parse_helmfile_values(helmfile_path: str) -> dict[str, dict]:
    with open(helmfile_path) as f:
        raw = f.read()

    cleaned = re.sub(r"^\s*\{\{.*\}\}\s*$", "", raw, flags=re.MULTILINE)
    cleaned = re.sub(r"\{\{[^}]*\}\}", _GOTPL_SENTINEL, cleaned)

    try:
        helmfile_data = yaml.safe_load(cleaned)
    except yaml.YAMLError as e:
        print(f"WARN: cannot parse helmfile.yaml: {e}", file=sys.stderr)
        return {}

    if not helmfile_data or "releases" not in helmfile_data:
        return {}

    result = {}
    for release in helmfile_data["releases"]:
        name = release.get("name", "")
        values_list = release.get("values", [])

        merged = {}
        for entry in values_list:
            if isinstance(entry, dict):
                _deep_merge(merged, entry)

        filtered = _strip_sentinel_values(merged)
        if filtered:
            result[name] = filtered

    return result


def find_local_chart_releases(helmfile_path: str) -> list[dict]:
    with open(helmfile_path) as f:
        raw = f.read()
    cleaned = re.sub(r"^\s*\{\{.*\}\}\s*$", "", raw, flags=re.MULTILINE)
    cleaned = re.sub(r"\{\{[^}]*\}\}", _GOTPL_SENTINEL, cleaned)
    try:
        data = yaml.safe_load(cleaned) or {}
    except yaml.YAMLError:
        return []
    out = []
    for release in data.get("releases", []):
        chart = release.get("chart", "")
        if isinstance(chart, str) and chart.startswith((".", "/")):
            out.append({"name": release.get("name", ""), "chart": chart})
    return out


def _deep_merge(base: dict, override: dict) -> None:
    for key, value in override.items():
        if key in base and isinstance(base[key], dict) and isinstance(value, dict):
            _deep_merge(base[key], value)
        else:
            base[key] = value


def _strip_sentinel_values(obj):
    if isinstance(obj, dict):
        cleaned = {}
        for key, value in obj.items():
            result = _strip_sentinel_values(value)
            if result is not None:
                cleaned[key] = result
        return cleaned if cleaned else None
    elif isinstance(obj, list):
        cleaned = []
        for item in obj:
            result = _strip_sentinel_values(item)
            if result is not None:
                cleaned.append(result)
        return cleaned if cleaned else None
    elif isinstance(obj, str) and _GOTPL_SENTINEL in obj:
        return None
    else:
        return obj


class PlatformError(RuntimeError):
    """Explicit refusal to guess."""


_PLATFORM_PLACEHOLDERS = (
    "${REGISTRY_HOST}",
    "${ARTIFACTORY_URL}",
    "${ARTIFACTORY_HELM_REPO}",
    "${ARTIFACTORY_DOCKER_REPO}",
    "${ARTIFACTORY_GENERIC_REPO}",
    "${ARTIFACTORY_RPM_REPO}",
)

_RUNTIME_PLACEHOLDER = re.compile(r"\$\{(?:target\.values|secrets)\.([A-Za-z0-9_.-]+)\}")
_ANY_PLACEHOLDER = re.compile(r"\$\{[^}]*\}")

_PIN_TAIL = re.compile(r"^/([A-Za-z0-9][-A-Za-z0-9_./]*?)(?::[^\s/]+)?$")

_IMAGE_PREFIX_MARKERS = (
    "${target.values.imageRegistry}",
    "${ARTIFACTORY_DOCKER_REPO}",
)

# Built and published by RONDO itself, never mirrored.
_RONDO_OWN_IMAGES = frozenset({"rondo-toolbox", "rondo-controller"})

# Published by the component's own CI (--own-image), never mirrored either.
OWN_IMAGE_PATHS: frozenset[str] = frozenset()


def is_own_image(image_path):
    return image_path in OWN_IMAGE_PATHS or image_path.rsplit("/", 1)[-1] in _RONDO_OWN_IMAGES


RENDERED_GVKS: set[str] = set()


def extract_gvks_from_yaml(yaml_content):
    out = set()
    for doc in yaml.safe_load_all(yaml_content):
        if not isinstance(doc, dict):
            continue
        kind = doc.get("kind")
        api = doc.get("apiVersion")
        if not kind or not api:
            continue
        if kind == "List":
            for item in doc.get("items") or []:
                if isinstance(item, dict) and item.get("kind") and item.get("apiVersion"):
                    group = item["apiVersion"].rsplit("/", 1)[0] if "/" in item["apiVersion"] else ""
                    out.add(f"{group}/{item['kind']}")
            continue
        group = api.rsplit("/", 1)[0] if "/" in api else ""
        out.add(f"{group}/{kind}")
    return out


def _probe_literal(key: str) -> str:
    if key.lower().endswith("ip"):
        return "192.0.2.1"
    return "rondo-probe"


def _resolve_placeholders(obj, where, pins):
    if isinstance(obj, dict):
        cleaned = {}
        for key, value in obj.items():
            result = _resolve_placeholders(value, f"{where}.{key}", pins)
            if result is not None:
                cleaned[key] = result
        return cleaned if cleaned else None
    if isinstance(obj, list):
        cleaned = []
        for idx, item in enumerate(obj):
            result = _resolve_placeholders(item, f"{where}[{idx}]", pins)
            if result is not None:
                cleaned.append(result)
        return cleaned if cleaned else None
    if not isinstance(obj, str):
        return obj

    for marker in _IMAGE_PREFIX_MARKERS:
        if marker in obj:
            tail = obj.split(marker, 1)[1]
            match = _PIN_TAIL.match(tail)
            if match:
                pins.add((match.group(1), where))
            break

    if any(p in obj for p in _IMAGE_PREFIX_MARKERS) or any(
        p in obj for p in _PLATFORM_PLACEHOLDERS
    ):
        return None

    resolved = _RUNTIME_PLACEHOLDER.sub(lambda m: _probe_literal(m.group(1)), obj)
    # A domain never decides an image: render it, as the target values.
    resolved = resolved.replace("${INGRESS_BASE_DOMAIN}", "rondo-probe")

    leftover = _ANY_PLACEHOLDER.search(resolved)
    if leftover:
        raise PlatformError(
            f"{where}: unknown placeholder {leftover.group(0)}, neither a platform "
            f"placeholder (internal/subst whitelist) nor a ${{target.values.x}} / "
            f"${{secrets.y}}. Refusing to render the chart: this script cannot guess "
            f"whether this value decides an image."
        )
    return resolved


def chart_name_from_oci(oci: str) -> str:
    if not oci:
        return ""
    return oci.rstrip("/").rsplit("/", 1)[-1]


def _load_yaml_docs(path):
    with open(path) as f:
        try:
            return [d for d in yaml.safe_load_all(f) if isinstance(d, dict)]
        except yaml.YAMLError as e:
            raise PlatformError(f"{path}: unreadable YAML: {e}") from e


def _yaml_files(directory):
    if not os.path.isdir(directory):
        return []
    return [
        os.path.join(directory, n)
        for n in sorted(os.listdir(directory))
        if n.endswith((".yaml", ".yml"))
    ]


def parse_platform_values(platform_dir: str) -> dict[str, list[dict]]:
    components = {}
    for path in _yaml_files(os.path.join(platform_dir, "components")):
        for doc in _load_yaml_docs(path):
            if doc.get("kind") != "Component":
                continue
            name = (doc.get("metadata") or {}).get("name", "")
            spec = doc.get("spec") or {}
            chart = spec.get("chart")
            if not isinstance(chart, dict):
                continue
            chart_name = chart_name_from_oci(chart.get("oci", ""))
            if not chart_name:
                raise PlatformError(
                    f"{path}: Component/{name} exposes no usable chart name "
                    f"in spec.chart.oci ({chart.get('oci')!r})"
                )
            components[name] = {
                "chart": chart_name,
                "values": spec.get("values") or {},
                "path": path,
            }

    overlays = {}
    for path in _yaml_files(os.path.join(platform_dir, "enrollments")):
        for doc in _load_yaml_docs(path):
            if doc.get("kind") != "Enrollment":
                continue
            spec = doc.get("spec") or {}
            values = spec.get("values")
            if not values:
                continue
            ref = ((spec.get("componentRef") or {}).get("name")) or ""
            if ref not in components:
                raise PlatformError(
                    f"{path}: Enrollment carries a spec.values overlay for Component "
                    f"{ref!r}, which is not in {platform_dir}/components"
                )
            overlays.setdefault(ref, []).append(
                {"name": (doc.get("metadata") or {}).get("name", ""), "values": values}
            )

    by_chart: dict[str, list[dict]] = {}
    for name in sorted(components):
        comp = components[name]
        variants = [(f"Component/{name}", comp["values"])]
        for overlay in overlays.get(name, []):
            merged = {}
            _deep_merge(merged, comp["values"])
            _deep_merge(merged, overlay["values"])
            variants.append(
                (f"Component/{name}+Enrollment/{overlay['name']}", merged)
            )
        for origin, raw in variants:
            pins = set()
            resolved = _resolve_placeholders(raw, origin, pins) or {}
            by_chart.setdefault(comp["chart"], []).append(
                {"origin": origin, "values": resolved, "pins": pins}
            )
    return by_chart


def helm_pull(chart_name, version, repo, dest_dir):
    cmd = [
        "helm", "pull", chart_name,
        "--version", version,
        "--repo", repo,
        "--destination", dest_dir,
        "--untar",
    ]
    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        raise RuntimeError(f"helm pull {chart_name}:{version} failed: {result.stderr}")
    return os.path.join(dest_dir, chart_name)


# Kubernetes version charts are rendered for: the newest cluster RONDO deploys (set by main()),
# not Helm's built-in default, which follows Helm releases and trips charts' kubeVersion bounds.
KUBE_VERSION = None


def cluster_kube_version(manifest):
    """Highest k3s/rke2 Kubernetes version pinned in the manifest, e.g. 'v1.34.5'."""
    found = []
    for entry in (manifest or {}).get("binaries") or []:
        if isinstance(entry, dict) and entry.get("name") in ("k3s", "rke2"):
            m = re.match(r"v?(\d+)\.(\d+)\.(\d+)", str(entry.get("version", "")))
            if m:
                found.append(tuple(int(x) for x in m.groups()))
    if not found:
        return None
    return "v%d.%d.%d" % max(found)


def helm_template(chart_dir, values_files=None):
    cmd = ["helm", "template", "extract", chart_dir]
    if KUBE_VERSION:
        cmd.extend(["--kube-version", KUBE_VERSION])
    for vf in (values_files or []):
        cmd.extend(["-f", vf])
    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        raise RuntimeError(f"helm template failed: {result.stderr}")
    return result.stdout


def extract_images_from_yaml(yaml_content):
    images = set()
    for doc in yaml.safe_load_all(yaml_content):
        _find_images(doc, images)
    return sorted(images)


def _find_images(obj, images):
    if obj is None:
        return
    if isinstance(obj, dict):
        if "image" in obj and isinstance(obj["image"], str):
            images.add(obj["image"])
        for key in ("args", "command"):
            if key in obj and isinstance(obj[key], list):
                for arg in obj[key]:
                    if isinstance(arg, str):
                        for match in _ARG_IMAGE_PATTERN.findall(arg):
                            images.add(match)
        for v in obj.values():
            _find_images(v, images)
    elif isinstance(obj, list):
        for item in obj:
            _find_images(item, images)


def parse_image_ref(image_ref):
    if "@" in image_ref:
        name, tag = image_ref.rsplit("@", 1)
    elif ":" in image_ref:
        name, candidate_tag = image_ref.rsplit(":", 1)
        if "/" not in candidate_tag:
            tag = candidate_tag
        else:
            name = image_ref
            tag = "latest"
    else:
        name = image_ref
        tag = "latest"

    first_seg = name.split("/")[0]
    if "." not in first_seg and ":" not in first_seg:
        if "/" not in name:
            name = f"docker.io/library/{name}"
        else:
            name = f"docker.io/{name}"

    return name, tag


def strip_registry(image_name):
    if _REGISTRY_PATTERN.match(image_name):
        return image_name.split("/", 1)[1]
    return image_name


def find_image_value_paths(chart_dir):
    result = {}

    values_file = os.path.join(chart_dir, "values.yaml")
    if os.path.exists(values_file):
        with open(values_file) as f:
            values = yaml.safe_load(f) or {}
        _search_image_repos(values, [], result)

    charts_dir = os.path.join(chart_dir, "charts")
    if os.path.isdir(charts_dir):
        for entry in sorted(os.listdir(charts_dir)):
            subchart_dir = os.path.join(charts_dir, entry)
            if not os.path.isdir(subchart_dir):
                continue
            sub_values = os.path.join(subchart_dir, "values.yaml")
            if os.path.exists(sub_values):
                with open(sub_values) as f:
                    sv = yaml.safe_load(f) or {}
                _search_image_repos(sv, [entry], result)

    return result


def _search_image_repos(obj, path, result):
    if not isinstance(obj, dict):
        return
    if "repository" in obj and isinstance(obj["repository"], str):
        if "tag" in obj or "pullPolicy" in obj or (path and path[-1] == "image"):
            repo_value = obj["repository"]
            normalized = _normalize_image_name(repo_value)
            result[normalized] = ".".join(path + ["repository"])
    for k, v in obj.items():
        if isinstance(v, dict):
            _search_image_repos(v, path + [k], result)


def _normalize_image_name(name):
    first_seg = name.split("/")[0]
    if "." not in first_seg and ":" not in first_seg:
        if "/" not in name:
            return f"docker.io/library/{name}"
        return f"docker.io/{name}"
    return name


def render_jobs_for(chart_name, helmfile_values, platform_values):
    jobs = []
    if chart_name in helmfile_values:
        jobs.append({
            "origin": f"helmfile.yaml/{chart_name}",
            "values": helmfile_values[chart_name],
            "pins": set(),
        })
    jobs.extend(platform_values.get(chart_name, []))
    return jobs


def _check_pins(job, rendered_refs, label):
    paths = {strip_registry(parse_image_ref(ref)[0]) for ref in rendered_refs}
    errors = []
    for pin, where in sorted(job["pins"]):
        if pin not in paths and not is_own_image(pin):
            errors.append(
                f"{label}: {where} pins image {pin!r} under "
                f"${{ARTIFACTORY_DOCKER_REPO}}, but rendering this chart produces "
                f"no image at that path. Rendered paths: "
                f"{', '.join(sorted(paths)) or '(none)'}. "
                f"The mirror will not hold this path, and the pod will pull a 404."
            )
    return errors


def collect_chart_images(chart_dir, label, jobs, tmpdir):
    if not jobs:
        jobs = [{"origin": "chart defaults", "values": {}, "pins": set()}]

    images = set()
    pin_errors = []
    slug = re.sub(r"[^A-Za-z0-9]+", "-", label).strip("-")
    for idx, job in enumerate(jobs):
        values_files = []
        if job["values"]:
            vpath = os.path.join(tmpdir, f"{slug}-{idx}-values.yaml")
            with open(vpath, "w") as f:
                yaml.dump(job["values"], f, default_flow_style=False)
            values_files.append(vpath)
        print(f"  values <- {job['origin']}", file=sys.stderr)
        rendered = helm_template(chart_dir, values_files)
        RENDERED_GVKS.update(extract_gvks_from_yaml(rendered))
        found = set(extract_images_from_yaml(rendered))
        images |= found
        pin_errors.extend(_check_pins(job, found, label))
    return sorted(images), pin_errors


def _describe(chart_dir, images, source_chart, docker_target_repo):
    value_paths = find_image_value_paths(chart_dir)
    results = []
    for img_ref in images:
        name, tag = parse_image_ref(img_ref)
        image_path = strip_registry(name)
        if is_own_image(image_path):
            continue

        helm_path = value_paths.get(name)
        if not helm_path:
            for val_name, val_path in value_paths.items():
                if strip_registry(val_name) == image_path:
                    helm_path = val_path
                    break

        results.append({
            "name": name,
            "version": tag,
            "target_repo": docker_target_repo,
            "image_path": image_path,
            "helm_value_path": helm_path,
            "source_chart": source_chart,
        })
        print(f"  {name}:{tag}", file=sys.stderr)
        if helm_path:
            print(f"    -> {helm_path}", file=sys.stderr)
    return results


def process_chart(chart_info, helmfile_values, platform_values, tmpdir,
                  docker_target_repo):
    chart_name = chart_info["name"]
    chart_version = chart_info["version"]
    source_repo = chart_info.get("source_repo", "")

    print(f"\n{'=' * 60}", file=sys.stderr)
    print(f"  {chart_name}:{chart_version}", file=sys.stderr)
    print(f"{'=' * 60}", file=sys.stderr)

    chart_dir = helm_pull(chart_name, chart_version, source_repo, tmpdir)
    jobs = render_jobs_for(chart_name, helmfile_values, platform_values)
    images, pin_errors = collect_chart_images(chart_dir, chart_name, jobs, tmpdir)
    results = _describe(chart_dir, images, f"{chart_name}:{chart_version}",
                        docker_target_repo)
    return results, pin_errors


def process_local_chart(release, helmfile_values, platform_values, base_dir, tmpdir,
                        docker_target_repo):
    name = release["name"]
    chart_dir = release["chart"]
    if not os.path.isabs(chart_dir):
        chart_dir = os.path.normpath(os.path.join(base_dir, chart_dir))

    print(f"\n{'=' * 60}", file=sys.stderr)
    print(f"  {name} (local chart {release['chart']})", file=sys.stderr)
    print(f"{'=' * 60}", file=sys.stderr)

    jobs = render_jobs_for(name, helmfile_values, platform_values)
    images, pin_errors = collect_chart_images(chart_dir, name, jobs, tmpdir)
    results = _describe(chart_dir, images, f"{name} (local)", docker_target_repo)
    return results, pin_errors


def process_first_party_chart(chart_name, chart_dir, platform_values, tmpdir,
                              docker_target_repo):
    print(f"\n{'=' * 60}", file=sys.stderr)
    print(f"  {chart_name} (first-party chart {chart_dir})", file=sys.stderr)
    print(f"{'=' * 60}", file=sys.stderr)

    jobs = render_jobs_for(chart_name, {}, platform_values)
    images, pin_errors = collect_chart_images(chart_dir, chart_name, jobs, tmpdir)
    results = _describe(chart_dir, images, f"{chart_name} (first-party)",
                        docker_target_repo)
    return results, pin_errors


def main():
    parser = argparse.ArgumentParser(
        description="Extract the Docker images of Helm charts"
    )
    parser.add_argument(
        "--manifest", required=True,
        help="dependencies.yaml file"
    )
    parser.add_argument(
        "--helmfile", default="",
        help="helmfile.yaml file (functional values are extracted automatically)"
    )
    parser.add_argument(
        "--platform", default="",
        help="platform/ tree (Component values + Enrollment overlays). "
             "WITHOUT it, every workload chart is rendered with its upstream defaults."
    )
    parser.add_argument(
        "--apps", default="",
        help="Directory of first-party charts (apps/), rendered from disk"
    )
    parser.add_argument(
        "--target-registry",
        help="Target Docker registry (e.g. host/rondo-docker-local)"
    )
    parser.add_argument(
        "--update-manifest", action="store_true",
        help="Update the docker_images section of dependencies.yaml"
    )
    parser.add_argument(
        "--docker-target-repo", default="rondo-docker-local",
        help="Target Artifactory repo for the extracted Docker images (default: rondo-docker-local)"
    )
    parser.add_argument(
        "--helm-sets", action="store_true",
        help="Print the Helm --set flags that override the registries"
    )
    parser.add_argument(
        "--own-image", action="append", default=[], metavar="PATH",
        help="First-party image of the component, path without registry (e.g. "
             "acme/log-summarizer), published by its own CI: never mirrored. Repeatable."
    )
    parser.add_argument(
        "--emit-gvks", default="",
        help="Write the GVKs rendered by the charts to this file, one per line. "
             "scripts/ci/check-health-model.sh compares them with the health model of "
             "internal/probe: a shipped but unmodelled shape blocks an Enrollment."
    )
    args = parser.parse_args()

    global OWN_IMAGE_PATHS
    OWN_IMAGE_PATHS = frozenset(args.own_image)

    with open(args.manifest) as f:
        manifest = yaml.safe_load(f)

    global KUBE_VERSION
    KUBE_VERSION = os.environ.get("RONDO_KUBE_VERSION") or cluster_kube_version(manifest)
    if KUBE_VERSION:
        print(f"==> rendering charts for Kubernetes {KUBE_VERSION}")

    charts = manifest.get("helm_charts", [])
    if not charts:
        print("No Helm chart in the manifest.", file=sys.stderr)
        sys.exit(0)

    helmfile_values = {}
    if args.helmfile and os.path.exists(args.helmfile):
        helmfile_values = parse_helmfile_values(args.helmfile)
        if helmfile_values:
            print(f"Helmfile values extracted for: {', '.join(helmfile_values.keys())}", file=sys.stderr)

    platform_values = {}
    if args.platform:
        try:
            platform_values = parse_platform_values(args.platform)
        except PlatformError as e:
            print(f"ERROR (platform/ tree): {e}", file=sys.stderr)
            sys.exit(1)
        if platform_values:
            print(
                "platform/ values extracted for: "
                + ", ".join(
                    f"{chart} ({len(jobs)} set{'s' if len(jobs) > 1 else ''})"
                    for chart, jobs in sorted(platform_values.items())
                ),
                file=sys.stderr,
            )
        else:
            print(
                f"ERROR: {args.platform} declares no Component with a chart; wrong "
                f"path? Without these values, workload charts would be rendered with "
                f"their upstream defaults and the mirror would be incomplete.",
                file=sys.stderr,
            )
            sys.exit(1)

    local_charts = []
    if args.helmfile and os.path.exists(args.helmfile):
        local_charts = find_local_chart_releases(args.helmfile)
        if local_charts:
            print(f"Local charts detected: {', '.join(c['name'] for c in local_charts)}", file=sys.stderr)

    base_dir = os.path.dirname(os.path.abspath(args.helmfile)) if args.helmfile else os.getcwd()

    first_party = {}
    if args.apps and os.path.isdir(args.apps):
        for entry in sorted(os.listdir(args.apps)):
            chart_dir = os.path.join(args.apps, entry)
            if os.path.exists(os.path.join(chart_dir, "Chart.yaml")):
                first_party[entry] = chart_dir

    all_images = []
    pin_errors = []
    rendered_charts = set()
    with tempfile.TemporaryDirectory() as tmpdir:
        for chart in charts:
            try:
                images, errs = process_chart(chart, helmfile_values, platform_values,
                                             tmpdir, args.docker_target_repo)
            except RuntimeError as e:
                print(f"  ERROR: {e}", file=sys.stderr)
                sys.exit(1)
            all_images.extend(images)
            pin_errors.extend(errs)
            rendered_charts.add(chart["name"])
        for release in local_charts:
            try:
                images, errs = process_local_chart(release, helmfile_values,
                                                   platform_values, base_dir, tmpdir,
                                                   args.docker_target_repo)
            except RuntimeError as e:
                print(f"  ERROR: {e}", file=sys.stderr)
                sys.exit(1)
            all_images.extend(images)
            pin_errors.extend(errs)
            rendered_charts.add(release["name"])
        for chart_name, chart_dir in first_party.items():
            try:
                images, errs = process_first_party_chart(
                    chart_name, chart_dir, platform_values, tmpdir,
                    args.docker_target_repo)
            except RuntimeError as e:
                print(f"  ERROR: {e}", file=sys.stderr)
                sys.exit(1)
            all_images.extend(images)
            pin_errors.extend(errs)
            rendered_charts.add(chart_name)

    unrendered = sorted(set(platform_values) - rendered_charts)
    if unrendered:
        print(
            "ERROR: charts referenced by platform/ but NEVER rendered, so their "
            "images are not in the inventory:", file=sys.stderr)
        for chart_name in unrendered:
            origins = ", ".join(j["origin"] for j in platform_values[chart_name])
            print(f"  - {chart_name} ({origins})", file=sys.stderr)
        print(
            "  A workload chart must be either declared in helm_charts of "
            "manifests/dependencies.yaml (upstream mirror), or present under apps/ "
            "(first-party). Otherwise nothing templates it and nothing mirrors its images.",
            file=sys.stderr)
        sys.exit(1)

    if args.emit_gvks:
        with open(args.emit_gvks, "w") as f:
            for gvk in sorted(RENDERED_GVKS):
                f.write(gvk + "\n")
        print(f"{len(RENDERED_GVKS)} rendered GVKs written to {args.emit_gvks}",
              file=sys.stderr)

    if pin_errors:
        print("ERROR: image pins inconsistent with the chart render:",
              file=sys.stderr)
        for err in pin_errors:
            print(f"  - {err}", file=sys.stderr)
        sys.exit(1)

    seen = set()
    unique = []
    for img in all_images:
        key = f"{img['name']}:{img['version']}"
        if key not in seen:
            seen.add(key)
            unique.append(img)

    print(f"\n{len(unique)} unique images extracted.", file=sys.stderr)

    if args.update_manifest:
        docker_images = []
        for img in unique:
            docker_images.append({
                "name": img["name"],
                "version": img["version"],
                "target_repo": img["target_repo"],
            })
        manifest["docker_images"] = docker_images
        with open(args.manifest, "w") as f:
            yaml.dump(manifest, f, default_flow_style=False, sort_keys=False,
                      allow_unicode=True)
        print(f"Manifest updated: {args.manifest}", file=sys.stderr)
    else:
        print("\ndocker_images:")
        for img in unique:
            print(f'  - name: "{img["name"]}"')
            print(f'    version: "{img["version"]}"')
            print(f'    target_repo: {img["target_repo"]}')

    if args.helm_sets and args.target_registry:
        by_chart = {}
        for img in unique:
            chart = img["source_chart"]
            by_chart.setdefault(chart, []).append(img)

        print("\n# Helm --set flags to override the registries:")
        for chart, images in by_chart.items():
            print(f"\n# {chart}")
            for img in images:
                if img["helm_value_path"]:
                    new_repo = f'{args.target_registry}/{img["image_path"]}'
                    print(f'  --set "{img["helm_value_path"]}={new_repo}" \\')
                else:
                    print(
                        f'  # WARNING: no value path for {img["name"]}:{img["version"]}'
                    )


if __name__ == "__main__":
    main()
