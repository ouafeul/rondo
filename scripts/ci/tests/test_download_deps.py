import json
import subprocess
import sys
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "download-deps.py"

PROVIDERS = [
    {"name": "hashicorp/vsphere", "version": "2.9.3", "kind": "provider",
     "path": "hashicorp/vsphere/terraform-provider-vsphere_2.9.3_linux_amd64.zip"},
    {"name": "hashicorp/vsphere", "version": "2.9.2", "kind": "provider",
     "path": "hashicorp/vsphere/terraform-provider-vsphere_2.9.2_linux_amd64.zip"},
    {"name": "acme/thing", "version": "1.0.0", "kind": "provider", "registry": "tofu.acme.test",
     "path": "acme/thing/terraform-provider-thing_1.0.0_linux_arm64.zip"},
]


def stage(download_deps, tmp_path, entries):
    pairs = []
    for i, dep in enumerate(entries):
        archive = Path(download_deps.get_local_path(str(tmp_path / "local"), "tofu_providers", dep))
        archive.parent.mkdir(parents=True, exist_ok=True)
        archive.write_bytes(b"zip-%d" % i)
        pairs.append((dep, str(archive)))
    return pairs


def test_provider_tree_is_a_network_mirror(download_deps, mirror_common, tmp_path):
    pairs = stage(download_deps, tmp_path, PROVIDERS)
    out = tmp_path / "tree"
    files = download_deps.provider_tree(pairs, str(out))

    base = out / "registry.opentofu.org" / "hashicorp" / "vsphere"
    assert json.loads((base / "index.json").read_text()) == {
        "versions": {"2.9.2": {}, "2.9.3": {}}}
    version = json.loads((base / "2.9.3.json").read_text())
    archive = "terraform-provider-vsphere_2.9.3_linux_amd64.zip"
    assert version == {"archives": {"linux_amd64": {
        "url": archive, "hashes": ["zh:" + mirror_common.sha256_of(pairs[0][1])]}}}
    assert (base / archive).read_bytes() == b"zip-0"

    other = out / "tofu.acme.test" / "acme" / "thing"
    assert "linux_arm64" in json.loads((other / "1.0.0.json").read_text())["archives"]
    assert sorted(files) == sorted(
        str(p.relative_to(out)) for p in out.rglob("*") if p.is_file())


def test_providers_upload_archives_before_metadata(download_deps, mirror_common, tmp_path):
    stage(download_deps, tmp_path, PROVIDERS)
    order = []

    class Spy(mirror_common.DirStore):
        def put(self, rel, local):
            order.append(rel)
            super().put(rel, local)

    store = Spy(str(tmp_path / "site"))
    ok, skipped, failed = download_deps.upload_providers(
        PROVIDERS, str(tmp_path / "local"), store, False)
    assert (ok, skipped, failed) == (8, 0, 0)  # 3 archives, 3 version files, 2 indexes
    firsts = [r for r in order if not r.endswith(".sha256")]
    assert firsts.index("registry.opentofu.org/hashicorp/vsphere/index.json") > \
        firsts.index("registry.opentofu.org/hashicorp/vsphere/"
                     "terraform-provider-vsphere_2.9.3_linux_amd64.zip")
    again = download_deps.upload_providers(PROVIDERS, str(tmp_path / "local"), store, False)
    assert again == (0, 8, 0)


def test_cli_uploads_files_with_sidecars_and_skips_a_rerun(tmp_path):
    local = tmp_path / "local"
    (local / "binaries" / "k3s").mkdir(parents=True)
    (local / "binaries" / "k3s" / "k3s").write_bytes(b"k3s-binary")
    deps = tmp_path / "deps.json"
    deps.write_text(json.dumps({"binaries": [
        {"name": "k3s", "version": "v1", "kind": "file", "path": "k3s/v1/k3s"}]}))
    site = tmp_path / "site"
    cmd = [sys.executable, str(SCRIPT), "--deps", str(deps), "--type", "binaries",
           "--upload-only", "--local-dir", str(local), "--files-url", str(site)]

    first = subprocess.run(cmd, cwd=tmp_path, capture_output=True, text=True)
    assert first.returncode == 0, first.stderr
    assert (site / "k3s" / "v1" / "k3s").read_bytes() == b"k3s-binary"
    assert (site / "k3s" / "v1" / "k3s.sha256").exists()
    assert "1 OK, 0 skipped" in first.stdout

    second = subprocess.run(cmd, cwd=tmp_path, capture_output=True, text=True)
    assert "0 OK, 1 skipped" in second.stdout

    forced = subprocess.run(cmd + ["--force"], cwd=tmp_path, capture_output=True, text=True)
    assert "1 OK, 0 skipped" in forced.stdout


def test_cli_needs_a_target_to_upload(tmp_path):
    deps = tmp_path / "deps.json"
    deps.write_text(json.dumps({"binaries": []}))
    result = subprocess.run(
        [sys.executable, str(SCRIPT), "--deps", str(deps), "--type", "binaries"],
        cwd=tmp_path, capture_output=True, text=True, env={"PATH": "/usr/bin:/bin"})
    assert result.returncode == 1
    assert "--files-url" in result.stderr


def test_chart_upload_pushes_under_the_registry_prefix(download_deps, stub_bin, tmp_path):
    log = tmp_path / "helm.log"
    stub_bin("helm", f'echo "$@" >> {log}\n[ "$1" = "show" ] && exit 1\nexit 0')
    chart_dir = tmp_path / "local" / "helm_charts"
    chart_dir.mkdir(parents=True)
    (chart_dir / "metallb-0.14.9.tgz").write_bytes(b"chart")
    dep = {"name": "metallb", "version": "0.14.9", "kind": "chart", "path": "metallb"}
    state = download_deps.upload_helm_chart(dep, str(tmp_path / "local"), "reg.site/mirror", False)
    assert state == "ok"
    lines = log.read_text().splitlines()
    assert lines[0].startswith("show chart oci://reg.site/mirror/charts/metallb --version 0.14.9")
    assert lines[1].endswith("metallb-0.14.9.tgz oci://reg.site/mirror/charts")


def test_chart_already_in_the_registry_is_skipped(download_deps, stub_bin, tmp_path):
    stub_bin("helm", "exit 0")
    chart_dir = tmp_path / "local" / "helm_charts"
    chart_dir.mkdir(parents=True)
    (chart_dir / "metallb-0.14.9.tgz").write_bytes(b"chart")
    dep = {"name": "metallb", "version": "0.14.9", "kind": "chart", "path": "metallb"}
    assert download_deps.upload_helm_chart(dep, str(tmp_path / "local"), "reg.site", False) == "skip"
