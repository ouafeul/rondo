import textwrap

import pytest
import yaml

_HELM_STUB = """
sub="$1"; shift
if [ "$sub" = pull ]; then
  name="$1"; dest=.
  while [ $# -gt 0 ]; do
    if [ "$1" = --destination ]; then dest="$2"; fi
    shift
  done
  mkdir -p "$dest/$name"
  printf 'name: %s\\nversion: 0.0.1\\n' "$name" > "$dest/$name/Chart.yaml"
  printf 'image:\\n  repository: probe/app\\n  tag: v1\\n' > "$dest/$name/values.yaml"
  exit 0
fi
if [ "$sub" = template ]; then
  vals=""
  while [ $# -gt 0 ]; do
    if [ "$1" = -f ]; then vals="$vals $2"; fi
    shift
  done
  printf 'apiVersion: apps/v1\\nkind: Deployment\\nspec:\\n  template:\\n    spec:\\n'
  printf '      containers:\\n        - name: app\\n          image: docker.io/probe/app:v1\\n'
  if [ -n "$vals" ] && grep -q 'enabled: true' $vals 2>/dev/null; then
    printf '      initContainers:\\n        - name: init-chown-data\\n'
    printf '          image: docker.io/library/busybox:1.37.0\\n'
  fi
  exit 0
fi
exit 0
"""

_MANIFEST = """\
helm_charts:
  - name: probe-chart
    version: "1.0.0"
    source_repo: "https://charts.example/probe"
    target_repo: rondo-helm-local
"""

_COMPONENT = """\
apiVersion: rondo.ouafeul.github.io/v1alpha1
kind: Component
metadata:
  name: probe
spec:
  chart:
    oci: oci://${REGISTRY_HOST}/${ARTIFACTORY_HELM_REPO}/probe-chart
    version: 1.0.0
  namespace: probe
  values:
    persistence:
      enabled: true
      storageClassName: ${target.values.storageClass}
    image:
      repository: ${REGISTRY_HOST}/${ARTIFACTORY_DOCKER_REPO}/probe/app
"""


def _tree(tmp_path, component=_COMPONENT, manifest=_MANIFEST, enrollment=None):
    (tmp_path / "manifests").mkdir()
    (tmp_path / "manifests" / "dependencies.yaml").write_text(manifest)
    comps = tmp_path / "platform" / "components"
    comps.mkdir(parents=True)
    if component:
        (comps / "probe.yaml").write_text(component)
    enrolls = tmp_path / "platform" / "enrollments"
    enrolls.mkdir(parents=True)
    if enrollment:
        (enrolls / "probe.yaml").write_text(enrollment)
    return tmp_path


def _run(module, monkeypatch, argv):
    monkeypatch.setattr("sys.argv", ["extract-helm-images.py", *argv])
    module.main()


def test_component_values_are_read_and_keyed_by_chart(extract_helm_images, tmp_path):
    root = _tree(tmp_path)
    by_chart = extract_helm_images.parse_platform_values(str(root / "platform"))

    assert list(by_chart) == ["probe-chart"], "chart name taken from spec.chart.oci"
    (job,) = by_chart["probe-chart"]
    assert job["origin"] == "Component/probe"
    assert job["values"]["persistence"]["enabled"] is True


def test_platform_placeholders_are_dropped_so_upstream_default_wins(
    extract_helm_images, tmp_path
):
    root = _tree(tmp_path)
    (job,) = extract_helm_images.parse_platform_values(str(root / "platform"))["probe-chart"]

    assert "image" not in job["values"], "the registry override must be dropped"
    assert ("probe/app", "Component/probe.image.repository") in job["pins"], (
        "the pin must stay TRACKED: it is what the Component will ask the mirror for"
    )


def test_target_placeholders_are_substituted_not_dropped(extract_helm_images, tmp_path):
    root = _tree(tmp_path)
    (job,) = extract_helm_images.parse_platform_values(str(root / "platform"))["probe-chart"]
    assert job["values"]["persistence"]["storageClassName"] == "rondo-probe"


def test_ingress_base_domain_is_substituted_not_dropped(extract_helm_images):
    got = extract_helm_images._resolve_placeholders(
        {"hosts": [{"name": "vlogs.monitoring.${INGRESS_BASE_DOMAIN}", "path": "/"}]}, "C", set()
    )
    assert got == {"hosts": [{"name": "vlogs.monitoring.rondo-probe", "path": "/"}]}, (
        "a dropped ingress host breaks the render of charts that read host.name"
    )


def test_ip_valued_placeholder_gets_an_ip_probe(extract_helm_images):
    assert extract_helm_images._probe_literal("ingressIP") == "192.0.2.1"
    assert extract_helm_images._probe_literal("grafanaHostname") == "rondo-probe"


def test_unknown_placeholder_is_fatal(extract_helm_images, tmp_path):
    root = _tree(tmp_path, component=_COMPONENT.replace(
        "${target.values.storageClass}", "${ARTIFACTORY_URLL}/typo"))
    with pytest.raises(extract_helm_images.PlatformError, match="unknown placeholder"):
        extract_helm_images.parse_platform_values(str(root / "platform"))


def test_enrollment_overlay_yields_an_extra_value_set(extract_helm_images, tmp_path):
    enrollment = textwrap.dedent("""\
        apiVersion: rondo.ouafeul.github.io/v1alpha1
        kind: Enrollment
        metadata:
          name: probe
        spec:
          componentRef:
            name: probe
          targetSelector:
            matchLabels:
              rondo.ouafeul.github.io/role-probe: "true"
          values:
            extra:
              enabled: true
        """)
    root = _tree(tmp_path, enrollment=enrollment)
    jobs = extract_helm_images.parse_platform_values(str(root / "platform"))["probe-chart"]

    origins = [j["origin"] for j in jobs]
    assert origins == ["Component/probe", "Component/probe+Enrollment/probe"]
    assert jobs[1]["values"]["extra"]["enabled"] is True
    assert jobs[1]["values"]["persistence"]["enabled"] is True, "overlay ON TOP"


def test_two_components_on_one_chart_yield_two_value_sets(extract_helm_images, tmp_path):
    root = _tree(tmp_path)
    second = _COMPONENT.replace("name: probe\n", "name: probe-forward\n", 1)
    (root / "platform" / "components" / "probe-forward.yaml").write_text(second)

    jobs = extract_helm_images.parse_platform_values(str(root / "platform"))["probe-chart"]
    assert [j["origin"] for j in jobs] == ["Component/probe", "Component/probe-forward"]


def test_vm_component_is_ignored(extract_helm_images, tmp_path):
    vm_component = textwrap.dedent("""\
        apiVersion: rondo.ouafeul.github.io/v1alpha1
        kind: Component
        metadata:
          name: appliance
        spec:
          vm:
            groups:
              - name: node
                count: 1
        """)
    root = _tree(tmp_path, component=vm_component)
    assert extract_helm_images.parse_platform_values(str(root / "platform")) == {}


def test_pin_absent_from_the_render_is_reported(extract_helm_images):
    job = {"origin": "Component/x", "values": {},
           "pins": {("fluent/fluentbit", "Component/x.image.repository")}}
    errors = extract_helm_images._check_pins(
        job, ["cr.fluentbit.io/fluent/fluent-bit:4.2.0"], "fluent-bit")
    assert len(errors) == 1
    assert "fluent/fluentbit" in errors[0]
    assert "fluent/fluent-bit" in errors[0], "the rendered paths are listed"


def test_pin_matching_the_render_passes(extract_helm_images):
    job = {"origin": "Component/x", "values": {},
           "pins": {("fluent/fluent-bit", "Component/x.image.repository")}}
    assert extract_helm_images._check_pins(
        job, ["cr.fluentbit.io/fluent/fluent-bit:4.2.0"], "fluent-bit") == []


def test_rondo_own_images_stay_out_of_the_inventory(extract_helm_images, tmp_path):
    chart_dir = tmp_path / "chart"
    chart_dir.mkdir()
    (chart_dir / "Chart.yaml").write_text("name: rondo-dashboard\nversion: 0.1.0\n")
    (chart_dir / "values.yaml").write_text(
        "image:\n  repository: rondo-toolbox\n  tag: latest\n")

    got = extract_helm_images._describe(
        str(chart_dir), ["rondo-toolbox:latest", "docker.io/probe/app:v1"],
        "rondo-dashboard", "rondo-docker-local")
    assert [r["image_path"] for r in got] == ["probe/app"]


def test_rondo_own_pin_is_not_reported_as_missing(extract_helm_images):
    job = {"origin": "Component/rondo-dashboard", "values": {},
           "pins": {("rondo-toolbox", "Component/rondo-dashboard.image.repository")}}
    assert extract_helm_images._check_pins(job, ["rondo-toolbox:latest"], "rondo-dashboard") == []


def test_component_own_image_stays_out_of_the_inventory(
    extract_helm_images, tmp_path, monkeypatch, stub_bin
):
    stub_bin("helm", _HELM_STUB)
    monkeypatch.setattr(extract_helm_images, "OWN_IMAGE_PATHS", frozenset())
    root = _tree(tmp_path, component=_COMPONENT.replace(
        "${REGISTRY_HOST}/${ARTIFACTORY_DOCKER_REPO}/probe/app",
        "${target.values.imageRegistry}/probe/app",
    ))
    monkeypatch.chdir(root)

    _run(extract_helm_images, monkeypatch, [
        "--manifest", "manifests/dependencies.yaml",
        "--platform", "platform", "--update-manifest", "--own-image", "probe/app",
    ])

    images = yaml.safe_load(
        (root / "manifests" / "dependencies.yaml").read_text())["docker_images"]
    refs = {f"{i['name']}:{i['version']}" for i in images}
    assert "docker.io/probe/app:v1" not in refs, (
        "an image published by the component's CI has no upstream: the mirror would fail"
    )
    assert "docker.io/library/busybox:1.37.0" in refs, "the other images remain"


def test_component_values_bring_the_conditional_image_into_the_inventory(
    extract_helm_images, tmp_path, monkeypatch, stub_bin
):
    stub_bin("helm", _HELM_STUB)
    root = _tree(tmp_path)
    monkeypatch.chdir(root)

    _run(extract_helm_images, monkeypatch, [
        "--manifest", "manifests/dependencies.yaml",
        "--platform", "platform", "--update-manifest",
    ])

    images = yaml.safe_load(
        (root / "manifests" / "dependencies.yaml").read_text())["docker_images"]
    refs = {f"{i['name']}:{i['version']}" for i in images}
    assert "docker.io/library/busybox:1.37.0" in refs, (
        "the image enabled by the Component values must enter the inventory"
    )


def test_without_platform_values_the_conditional_image_is_missing(
    extract_helm_images, tmp_path, monkeypatch, stub_bin
):
    stub_bin("helm", _HELM_STUB)
    root = _tree(tmp_path)
    monkeypatch.chdir(root)

    _run(extract_helm_images, monkeypatch, [
        "--manifest", "manifests/dependencies.yaml", "--update-manifest",
    ])

    images = yaml.safe_load(
        (root / "manifests" / "dependencies.yaml").read_text())["docker_images"]
    refs = {f"{i['name']}:{i['version']}" for i in images}
    assert "docker.io/library/busybox:1.37.0" not in refs
    assert "docker.io/probe/app:v1" in refs


def test_helmfile_values_are_still_extracted(
    extract_helm_images, tmp_path, monkeypatch, stub_bin
):
    stub_bin("helm", _HELM_STUB)
    root = _tree(tmp_path, component=None)
    (root / "helmfile.yaml").write_text(textwrap.dedent("""\
        releases:
          - name: probe-chart
            chart: oci://{{ .Values.repo }}/probe-chart
            values:
              - persistence:
                  enabled: true
        """))
    monkeypatch.chdir(root)

    _run(extract_helm_images, monkeypatch, [
        "--manifest", "manifests/dependencies.yaml",
        "--helmfile", "helmfile.yaml", "--update-manifest",
    ])

    images = yaml.safe_load(
        (root / "manifests" / "dependencies.yaml").read_text())["docker_images"]
    refs = {f"{i['name']}:{i['version']}" for i in images}
    assert "docker.io/library/busybox:1.37.0" in refs


def test_component_chart_nobody_renders_is_fatal(
    extract_helm_images, tmp_path, monkeypatch, stub_bin
):
    stub_bin("helm", _HELM_STUB)
    root = _tree(tmp_path)
    orphan = _COMPONENT.replace("name: probe\n", "name: orphan\n", 1).replace(
        "probe-chart", "chart-nobody-mirrors")
    (root / "platform" / "components" / "orphan.yaml").write_text(orphan)
    monkeypatch.chdir(root)

    with pytest.raises(SystemExit) as exc:
        _run(extract_helm_images, monkeypatch, [
            "--manifest", "manifests/dependencies.yaml",
            "--platform", "platform", "--update-manifest",
        ])
    assert exc.value.code == 1


def test_empty_platform_tree_is_fatal(
    extract_helm_images, tmp_path, monkeypatch, stub_bin
):
    stub_bin("helm", _HELM_STUB)
    root = _tree(tmp_path, component=None)
    monkeypatch.chdir(root)

    with pytest.raises(SystemExit) as exc:
        _run(extract_helm_images, monkeypatch, [
            "--manifest", "manifests/dependencies.yaml",
            "--platform", "platform", "--update-manifest",
        ])
    assert exc.value.code == 1


def test_first_party_chart_is_rendered_from_apps(
    extract_helm_images, tmp_path, monkeypatch, stub_bin
):
    stub_bin("helm", _HELM_STUB)
    root = _tree(tmp_path, component=_COMPONENT.replace(
        "probe-chart", "probe-first-party"))
    chart = root / "apps" / "probe-first-party"
    chart.mkdir(parents=True)
    (chart / "Chart.yaml").write_text("name: probe-first-party\nversion: 0.1.0\n")
    (chart / "values.yaml").write_text("{}\n")
    monkeypatch.chdir(root)

    _run(extract_helm_images, monkeypatch, [
        "--manifest", "manifests/dependencies.yaml",
        "--platform", "platform", "--apps", "apps", "--update-manifest",
    ])

    images = yaml.safe_load(
        (root / "manifests" / "dependencies.yaml").read_text())["docker_images"]
    refs = {f"{i['name']}:{i['version']}" for i in images}
    assert "docker.io/library/busybox:1.37.0" in refs, (
        "the Component points to an apps/ chart: its values must still be rendered"
    )


_COMPONENT_PER_TARGET_REGISTRY = _COMPONENT.replace(
    "${REGISTRY_HOST}/${ARTIFACTORY_DOCKER_REPO}/probe/app",
    "${target.values.imageRegistry}/probe/app",
)


def test_per_target_image_prefix_is_dropped_and_pinned(extract_helm_images, tmp_path):
    root = _tree(tmp_path, component=_COMPONENT_PER_TARGET_REGISTRY)
    (job,) = extract_helm_images.parse_platform_values(str(root / "platform"))["probe-chart"]

    assert "image" not in job["values"], (
        "the per-target registry override must be DROPPED, not probed: an "
        "`rondo-probe/probe/app` rendered here would become a mirror entry"
    )
    assert ("probe/app", "Component/probe.image.repository") in job["pins"], (
        "the pin must stay TRACKED: without it, deps-resolve would not detect an "
        "image path the chart does not render"
    )


def test_gvks_are_collected_with_the_probe_key_shape(extract_helm_images):
    got = extract_helm_images.extract_gvks_from_yaml("""
apiVersion: v1
kind: ConfigMap
metadata: {name: a}
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata: {name: b}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: c}
""")
    assert got == {"/ConfigMap", "storage.k8s.io/StorageClass", "apps/Deployment"}


def test_gvks_of_a_list_are_its_items_not_the_list(extract_helm_images):
    got = extract_helm_images.extract_gvks_from_yaml("""
apiVersion: v1
kind: List
items:
  - apiVersion: v1
    kind: Secret
    metadata: {name: a}
  - apiVersion: rbac.authorization.k8s.io/v1
    kind: Role
    metadata: {name: b}
""")
    assert got == {"/Secret", "rbac.authorization.k8s.io/Role"}
    assert "/List" not in got


def test_rendered_gvks_are_accumulated_across_charts(
    extract_helm_images, tmp_path, stub_bin
):
    stub_bin("helm", _HELM_STUB)
    extract_helm_images.RENDERED_GVKS.clear()
    root = _tree(tmp_path)
    jobs = extract_helm_images.parse_platform_values(str(root / "platform"))["probe-chart"]

    chart_dir = tmp_path / "chart"
    chart_dir.mkdir()
    (chart_dir / "Chart.yaml").write_text("name: probe-chart\nversion: 0.0.1\n")
    extract_helm_images.collect_chart_images(
        str(chart_dir), "probe-chart", jobs, str(tmp_path))

    assert "apps/Deployment" in extract_helm_images.RENDERED_GVKS, (
        "the helm stub renders a Deployment: it must appear in the accumulator"
    )


def test_cluster_kube_version_is_the_newest_pinned_cluster(extract_helm_images):
    manifest = {"binaries": [
        {"name": "k3s", "version": "v1.31.4+k3s1"},
        {"name": "rke2", "version": "v1.34.5+rke2r1"},
        {"name": "helm", "version": "v3.22.1"},
    ]}
    assert extract_helm_images.cluster_kube_version(manifest) == "v1.34.5"
    assert extract_helm_images.cluster_kube_version({"binaries": []}) is None
