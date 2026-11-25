DEPS = {
    "binaries": [
        {"name": "rke2", "version": "v1.34.7+rke2r1", "target_repo": "rondo-generic-local",
         "target_path": "rke2/v1.34.7+rke2r1/rke2.linux-amd64.tar.gz"},
    ],
    "grafana_plugins": [
        {"name": "victoriametrics-logs-datasource", "version": "1.0.0",
         "target_repo": "rondo-generic-local",
         "target_path": "grafana-plugins/victoriametrics-logs-datasource-1.0.0.zip"},
    ],
    "docker_images": [
        {"name": "docker.io/grafana/grafana", "version": "11.0.0", "target_repo": "rondo-docker-local"},
    ],
    "helm_charts": [
        {"name": "rancher", "version": "2.11.1", "target_repo": "rondo-helm-local"},
    ],
}


def test_build_checks_paths(xray_scan):
    checks = {label: (repo, path) for label, repo, path in xray_scan.build_checks(DEPS)}

    assert checks["rke2 v1.34.7+rke2r1"] == (
        "rondo-generic-local", "rke2/v1.34.7+rke2r1/rke2.linux-amd64.tar.gz")
    assert "victoriametrics-logs-datasource 1.0.0" in checks

    assert checks["docker.io/grafana/grafana:11.0.0"] == (
        "rondo-docker-local", "grafana/grafana/11.0.0/manifest.json")

    assert not any(label.startswith("xpkg ") for label in checks)

    assert not any(label.startswith("chart ") for label in checks)


def test_extract_violations_counts(xray_scan):
    v = xray_scan.extract_violations({"artifacts": [{"issues": [
        {"severity": "Critical", "summary": "RCE", "cve": "CVE-1"},
        {"severity": "High", "summary": "DoS", "cve": "CVE-2"},
        {"severity": "Low", "summary": "meh", "cve": "CVE-3"},
    ]}]})
    assert (v["critical"], v["high"], v["total"]) == (1, 1, 3)
    assert v["error"] is None
    assert len(v["details"]) == 2


def test_extract_violations_empty_is_error_not_clean(xray_scan):
    v = xray_scan.extract_violations({"artifacts": []})
    assert v["error"] is not None
    assert v["total"] == 0


def test_extract_violations_api_errors_array(xray_scan):
    v = xray_scan.extract_violations({
        "artifacts": [],
        "errors": [{"identifier": "repo/x/y", "error": "artifact doesn't exist"}],
    })
    assert "artifact doesn't exist" in v["error"]


def test_extract_violations_transport_error(xray_scan):
    v = xray_scan.extract_violations({"error": "connection refused"})
    assert "connection refused" in v["error"]
