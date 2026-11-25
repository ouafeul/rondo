from pathlib import Path


def test_resolve_version_placeholders(resolve_deps):
    out = resolve_deps.resolve_version_placeholders({
        "binaries": [{
            "name": "k3s", "version": "v1.31.4+k3s1",
            "source_url": "https://github.com/k3s-io/k3s/releases/download/{version}/k3s",
            "path": "k3s/{version}/k3s",
        }],
    })
    entry = out["binaries"][0]
    assert entry["source_url"].endswith("/v1.31.4+k3s1/k3s")
    assert entry["path"] == "k3s/v1.31.4+k3s1/k3s"


def test_validate_manifest_ok(resolve_deps):
    errors = resolve_deps.validate_manifest({
        "binaries": [{"name": "k3s", "version": "v1", "kind": "file", "path": "k3s/v1/k3s",
                      "source_url": "https://x"}],
        "helm_charts": [{"name": "c", "version": "1", "kind": "chart", "path": "c",
                         "source_repo": "https://charts.example"}],
        "tofu_providers": [{"name": "hashicorp/x", "version": "1", "kind": "provider",
                            "path": "hashicorp/x/terraform-provider-x_1_linux_amd64.zip",
                            "source_url": "https://x"}],
        "rpm_packages": [{"name": "_repos", "upstream_repos": []},
                         {"name": "httpd", "version": "el9", "kind": "rpm", "path": "el9"}],
    })
    assert errors == []


def test_validate_manifest_reports_missing_fields(resolve_deps):
    errors = resolve_deps.validate_manifest({
        "binaries": [{"name": "k3s"}],
        "helm_charts": [{"name": "c", "version": "1", "kind": "file"}],
        "typo_section": [],
    })
    text = "\n".join(errors)
    assert "version" in text and "'kind'" in text and "'path'" in text and "source_url" in text
    assert "source_repo" in text
    assert "typo_section" in text


def test_validate_manifest_rejects_a_bad_kind_or_path(resolve_deps):
    errors = resolve_deps.validate_manifest({
        "binaries": [{"name": "a", "version": "1", "kind": "chart", "path": "a",
                      "source_url": "https://x"},
                     {"name": "b", "version": "1", "kind": "file", "path": "/abs/b",
                      "source_url": "https://x"},
                     {"name": "c", "version": "1", "kind": "file", "path": "../c",
                      "source_url": "https://x"}],
        "tofu_providers": [{"name": "d/e", "version": "1", "kind": "provider",
                            "path": "d/e/archive.zip", "source_url": "https://x"}],
    })
    text = "\n".join(errors)
    assert "binaries[0]: 'kind' must be 'file'" in text
    assert "binaries[1]: 'path' must be relative" in text
    assert "binaries[2]: 'path' must be relative" in text
    assert "tofu_providers[0]: 'path' must end in _<os>_<arch>.zip" in text


def test_the_repo_manifest_validates(resolve_deps):
    import yaml
    root = Path(__file__).resolve().parents[3]
    data = yaml.safe_load((root / "manifests" / "dependencies.yaml").read_text())
    assert resolve_deps.validate_manifest(data) == []
