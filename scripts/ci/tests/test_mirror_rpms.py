import importlib.util
import json
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[3]


@pytest.fixture(scope="module")
def mirror_rpms():
    spec = importlib.util.spec_from_file_location(
        "mirror_rpms", ROOT / "scripts" / "ci" / "mirror-rpms.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def make_repo(tmp_path):
    work = tmp_path / "work"
    (work / "repodata").mkdir(parents=True)
    (work / "httpd-2.4.rpm").write_bytes(b"rpm")
    (work / "repodata" / "primary.xml.gz").write_bytes(b"primary")
    (work / "repodata" / "repomd.xml").write_text("<repomd/>")
    return work


def test_upload_tree_puts_packages_then_metadata_with_sidecars(mirror_rpms, mirror_common, tmp_path):
    work = make_repo(tmp_path)
    dest = tmp_path / "site"
    order = []

    class Spy(mirror_common.DirStore):
        def put(self, rel, local):
            order.append(rel)
            super().put(rel, local)

    results = mirror_rpms.upload_tree(str(work), Spy(str(dest)), "el9", True, 1)
    assert set(results.values()) == {"ok"}
    assert (dest / "el9" / "httpd-2.4.rpm").read_bytes() == b"rpm"
    assert (dest / "el9" / "repodata" / "repomd.xml").exists()
    assert (dest / "el9" / "httpd-2.4.rpm.sha256").read_text().split()[0] == \
        mirror_common.sha256_of(work / "httpd-2.4.rpm")
    assert order.index("el9/repodata/repomd.xml") > order.index("el9/repodata/primary.xml.gz")
    assert order.index("el9/repodata/primary.xml.gz") > order.index("el9/httpd-2.4.rpm")


def test_upload_tree_skips_what_the_sidecar_covers(mirror_rpms, mirror_common, tmp_path):
    work = make_repo(tmp_path)
    store = mirror_common.DirStore(str(tmp_path / "site"))
    mirror_rpms.upload_tree(str(work), store, "el9", True, 1)
    (work / "repodata" / "repomd.xml").write_text("<repomd rev='2'/>")
    again = mirror_rpms.upload_tree(str(work), store, "el9", True, 1)
    assert again["el9/httpd-2.4.rpm"] == "skip"
    assert again["el9/repodata/repomd.xml"] == "ok"


def test_build_repodata_runs_createrepo_c(mirror_rpms, stub_bin, tmp_path):
    marker = tmp_path / "called"
    stub_bin("createrepo_c", f'echo "$@" > {marker}')
    assert mirror_rpms.build_repodata(str(tmp_path)) is True
    assert str(tmp_path) in marker.read_text()


def test_build_repodata_without_createrepo_c_says_so(mirror_rpms, monkeypatch, capsys):
    monkeypatch.setenv("PATH", "/nonexistent")
    assert mirror_rpms.build_repodata("/tmp") is False
    assert "createrepo_c is not installed" in capsys.readouterr().err


def test_load_packages_separates_upstream_repos(mirror_rpms, tmp_path):
    deps = tmp_path / "deps.json"
    deps.write_text(json.dumps({
        "rpm_packages": [
            {"name": "_repos", "version": "el9",
             "upstream_repos": [{"name": "epel", "baseurl": "https://e/"},
                                {"name": "ubi-9-baseos-rpms"}]},
            {"name": "httpd", "version": "el9"},
            {"name": "redis", "version": "el9"},
        ]
    }))
    packages, repos, present = mirror_rpms.load_packages(str(deps))
    assert packages == ["httpd", "redis"]
    assert [r["name"] for r in repos] == ["epel", "ubi-9-baseos-rpms"]
    assert present is True


def test_load_packages_distinguishes_absent_from_empty(mirror_rpms, tmp_path):
    absent = tmp_path / "absent.json"
    absent.write_text(json.dumps({"binaries": []}))
    packages, repos, present = mirror_rpms.load_packages(str(absent))
    assert (packages, repos) == ([], [])
    assert present is False, "missing section reported as present"

    empty = tmp_path / "empty.json"
    empty.write_text(json.dumps({"rpm_packages": []}))
    packages, repos, present = mirror_rpms.load_packages(str(empty))
    assert (packages, repos) == ([], [])
    assert present is True, "empty section reported as missing, the script would fail"

    nul = tmp_path / "nul.json"
    nul.write_text(json.dumps({"rpm_packages": None}))
    assert mirror_rpms.load_packages(str(nul)) == ([], [], True)


def test_write_repo_files_only_for_declared_baseurls(mirror_rpms, tmp_path):
    repos = [
        {"name": "epel", "baseurl": "https://dl.fedoraproject.org/pub/epel/9/",
         "gpgkey": "https://dl.fedoraproject.org/pub/epel/KEY"},
        {"name": "ubi-9-baseos-rpms"},
    ]
    names = mirror_rpms.write_repo_files(repos, str(tmp_path))
    assert names == ["epel", "ubi-9-baseos-rpms"]
    assert (tmp_path / "epel.repo").exists()
    assert not (tmp_path / "ubi-9-baseos-rpms.repo").exists()

    body = (tmp_path / "epel.repo").read_text()
    assert "gpgcheck=1" in body
    assert "gpgkey=https://" in body
    assert "module_hotfixes=1" in body


def test_write_repo_file_without_key_disables_gpgcheck(mirror_rpms, tmp_path):
    mirror_rpms.write_repo_files(
        [{"name": "local", "baseurl": "https://x/"}], str(tmp_path))
    assert "gpgcheck=0" in (tmp_path / "local.repo").read_text()


def test_manifest_rpm_packages_stays_readable_by_the_script():
    import yaml
    data = yaml.safe_load((ROOT / "manifests" / "dependencies.yaml").read_text())
    assert "rpm_packages" in data, (
        "the rpm_packages section is missing from the manifest: emptying it is fine, "
        "removing it breaks mirror-rpms.py, which tells the two apart")
    entries = data["rpm_packages"] or []

    packages = [e for e in entries if e.get("name") != "_repos"]
    repos = [e for e in entries if e.get("name") == "_repos"]

    if not packages:
        assert not repos, (
            "`_repos` entry without any package: upstream repositories only serve to "
            "resolve packages, so this is a half-done removal")
        return

    assert len(repos) == 1, "exactly one _repos entry expected as soon as there are packages"
    assert repos[0]["upstream_repos"], "no upstream repository declared"
    assert {e["kind"] for e in packages} == {"rpm"}, "rpm_packages entries are kind: rpm"
    assert len({e["path"] for e in packages}) == 1, "the entries feed several repository paths"
