import json


def test_split_ref(mirror_images):
    assert mirror_images.split_ref("grafana/grafana:11.0.0") == ("grafana/grafana", "11.0.0")
    assert mirror_images.split_ref("registry") == ("registry", "latest")
    assert mirror_images.split_ref("reg.local:5000/ns/img") == ("reg.local:5000/ns/img", "latest")
    assert mirror_images.split_ref("reg.local:5000/ns/img:v1") == ("reg.local:5000/ns/img", "v1")


def test_normalize_source_ref(mirror_images):
    assert mirror_images.normalize_source_ref("registry") == "docker.io/library/registry"
    assert mirror_images.normalize_source_ref("rancher/rancher") == "docker.io/rancher/rancher"
    assert mirror_images.normalize_source_ref("quay.io/argoproj/argocd") == "quay.io/argoproj/argocd"


def test_strip_source_registry_keeps_namespace(mirror_images):
    assert mirror_images.strip_source_registry("docker.io/grafana/grafana") == "grafana/grafana"
    assert mirror_images.strip_source_registry("xpkg.upbound.io/upbound/provider-opentofu") == "upbound/provider-opentofu"
    assert mirror_images.strip_source_registry("rancher/rancher") == "rancher/rancher"


def test_get_local_path_stable_naming(mirror_images, tmp_path):
    p = mirror_images.get_local_path(str(tmp_path), "grafana/grafana", "11.0.0")
    assert p.endswith("docker_images/docker.io_grafana_grafana_11.0.0.tar")


def test_load_refs_deps_sections(mirror_images, tmp_path):
    deps_file = tmp_path / "deps.json"
    deps_file.write_text(json.dumps({
        "docker_images": [{"name": "grafana/grafana", "version": "11.0.0"}],
    }))

    args = _args(deps=str(deps_file), section="docker_images")
    assert mirror_images.load_refs(args) == ["grafana/grafana:11.0.0"]


def test_load_refs_list_file_ignores_comments(mirror_images, tmp_path):
    lst = tmp_path / "images.txt"
    lst.write_text("# commentaire\nrancher/rancher:v2.14.0\n\n  registry:2  \n")

    args = _args(list_file=str(lst))
    assert mirror_images.load_refs(args) == ["rancher/rancher:v2.14.0", "registry:2"]


def _args(**kw):
    class Args:
        deps = None
        list_url = None
        list_file = None
        section = "docker_images"
        oci_registry = "reg.local/rondo-docker-local"
        local_dir = ""
        download_only = False
        upload_only = False
        skip_existing = True
        workers = 1
    for k, v in kw.items():
        setattr(Args, k, v)
    return Args


def test_process_ref_skips_existing(mirror_images, stub_bin):
    stub_bin("skopeo", 'if [ "$1" = "inspect" ]; then exit 0; fi\nexit 1')
    ref, st = mirror_images.process_ref("grafana/grafana:11.0.0", _args())
    assert st == "skip"


def test_process_ref_direct_copy(mirror_images, stub_bin):
    stub_bin("skopeo", 'if [ "$1" = "inspect" ]; then exit 1; fi\nexit 0')
    ref, st = mirror_images.process_ref("grafana/grafana:11.0.0", _args())
    assert st == "ok"


def test_process_ref_copy_failure_reported(mirror_images, stub_bin):
    stub_bin("skopeo", 'if [ "$1" = "inspect" ]; then exit 1; fi\necho "boom" >&2\nexit 2')
    ref, st = mirror_images.process_ref("grafana/grafana:11.0.0", _args())
    assert st.startswith("error:")
    assert "boom" in st


def test_process_ref_upload_missing_tar_is_skip(mirror_images, stub_bin, tmp_path):
    stub_bin("skopeo", 'if [ "$1" = "inspect" ]; then exit 1; fi\nexit 0')
    args = _args(upload_only=True, local_dir=str(tmp_path))
    ref, st = mirror_images.process_ref("grafana/grafana:11.0.0", args)
    assert st == "skip"
