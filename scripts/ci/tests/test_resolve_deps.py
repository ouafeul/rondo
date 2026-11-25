
def test_resolve_version_placeholders(resolve_deps):
    out = resolve_deps.resolve_version_placeholders({
        "binaries": [{
            "name": "k3s", "version": "v1.31.4+k3s1",
            "source_url": "https://github.com/k3s-io/k3s/releases/download/{version}/k3s",
            "target_path": "k3s/{version}/k3s",
        }],
    })
    entry = out["binaries"][0]
    assert entry["source_url"].endswith("/v1.31.4+k3s1/k3s")
    assert entry["target_path"] == "k3s/v1.31.4+k3s1/k3s"


def test_validate_manifest_ok(resolve_deps):
    errors = resolve_deps.validate_manifest({
        "binaries": [{"name": "k3s", "version": "v1", "target_repo": "r",
                      "source_url": "https://x"}],
        "helm_charts": [{"name": "c", "version": "1", "target_repo": "r",
                         "source_repo": "https://charts.example"}],
    })
    assert errors == []


def test_validate_manifest_reports_missing_fields(resolve_deps):
    errors = resolve_deps.validate_manifest({
        "binaries": [{"name": "k3s"}],
        "helm_charts": [{"name": "c", "version": "1", "target_repo": "r"}],
        "typo_section": [],
    })
    text = "\n".join(errors)
    assert "version" in text and "target_repo" in text and "source_url" in text
    assert "source_repo" in text
    assert "typo_section" in text
