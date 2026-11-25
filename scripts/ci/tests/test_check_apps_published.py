import json
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest
import yaml

ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / "scripts" / "ci" / "check-apps-published.py"
REPO = "rondo-helm-local"
CREDS = {"ARTIFACTORY_DOCKER_USER": "ci", "ARTIFACTORY_DOCKER_PASSWORD": "secret"}


@pytest.fixture()
def registry():
    state = {"published": set(), "status": None, "hits": []}

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):  # noqa: N802 (name imposed by BaseHTTPRequestHandler)
            state["hits"].append(self.path)
            if not self.headers.get("Authorization"):
                self.send_response(401)
                self.end_headers()
                return
            if state["status"] is not None:
                self.send_response(state["status"])
                self.end_headers()
                return
            parts = self.path.strip("/").split("/")
            found = (len(parts) == 5 and parts[0] == "v2" and parts[3] == "manifests"
                     and (parts[2], parts[4]) in state["published"])
            body = json.dumps({"schemaVersion": 2}).encode()
            self.send_response(200 if found else 404)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    state["url"] = "http://127.0.0.1:%d" % server.server_address[1]
    yield state
    server.shutdown()
    server.server_close()


def make_tree(root: Path, charts: dict, pins: dict):
    for name, version in charts.items():
        chart_dir = root / "apps" / name
        chart_dir.mkdir(parents=True)
        (chart_dir / "Chart.yaml").write_text(
            yaml.dump({"apiVersion": "v2", "name": name, "version": version}))
    components = root / "platform" / "components"
    components.mkdir(parents=True)
    for name, (chart, version) in pins.items():
        (components / f"{name}.yaml").write_text(yaml.dump({
            "apiVersion": "rondo.example.com/v1alpha1",
            "kind": "Component",
            "metadata": {"name": name},
            "spec": {"chart": {"oci": f"oci://host/{REPO}/{chart}", "version": version}},
        }))
    return root


def run(cwd: Path, registry_url: str, *args, env_extra=None):
    env = dict(CREDS, PATH="/usr/bin:/bin", PYTHONIOENCODING="utf-8")
    env.update(env_extra or {})
    return subprocess.run(
        [sys.executable, str(SCRIPT), "platform", "--registry", registry_url,
         "--repo", REPO, *args],
        cwd=cwd, env=env, capture_output=True, text=True, check=False)


def test_a_published_pin_passes(tmp_path, registry):
    make_tree(tmp_path, {"hello-world": "0.1.2"}, {"hello-world": ("hello-world", "0.1.2")})
    registry["published"].add(("hello-world", "0.1.2"))
    result = run(tmp_path, registry["url"])
    assert result.returncode == 0, result.stderr
    assert "0.1.2, published" in result.stdout


def test_a_pin_the_registry_lacks_turns_red(tmp_path, registry):
    make_tree(tmp_path, {"hello-world": "0.1.2"}, {"hello-world": ("hello-world", "0.9.9")})
    registry["published"].add(("hello-world", "0.1.2"))
    result = run(tmp_path, registry["url"])
    assert result.returncode == 1
    assert "pins hello-world 0.9.9" in result.stderr
    assert "apps/ has 0.1.2" in result.stderr


def test_the_version_this_commit_bumps_is_tolerated(tmp_path, registry):
    make_tree(tmp_path, {"hello-world": "0.1.3"}, {"hello-world": ("hello-world", "0.1.3")})
    result = run(tmp_path, registry["url"])
    assert result.returncode == 0, result.stderr
    assert "PENDING hello-world 0.1.3" in result.stdout


def test_the_impossibility_is_stated_when_a_version_is_pending(tmp_path, registry):
    make_tree(tmp_path, {"hello-world": "0.1.3"}, {"hello-world": ("hello-world", "0.1.3")})
    result = run(tmp_path, registry["url"])
    assert "CANNOT prove" in result.stdout
    assert "No branch publishes an apps/ chart" in result.stdout
    assert "helm template apps/<chart>" in result.stdout


def test_a_green_run_states_nothing_it_did_not_prove(tmp_path, registry):
    make_tree(tmp_path, {"hello-world": "0.1.2"}, {"hello-world": ("hello-world", "0.1.2")})
    registry["published"].add(("hello-world", "0.1.2"))
    result = run(tmp_path, registry["url"])
    assert "CANNOT prove" not in result.stdout


def test_an_inconclusive_answer_is_red(tmp_path, registry):
    make_tree(tmp_path, {"hello-world": "0.1.2"}, {"hello-world": ("hello-world", "0.1.2")})
    registry["status"] = 502
    result = run(tmp_path, registry["url"])
    assert result.returncode == 1
    assert "HTTP 502" in result.stderr
    assert "cannot conclude" in result.stderr


def test_an_unreachable_registry_is_red(tmp_path):
    make_tree(tmp_path, {"hello-world": "0.1.2"}, {"hello-world": ("hello-world", "0.1.2")})
    result = run(tmp_path, "http://127.0.0.1:1")
    assert result.returncode == 1
    assert "unreachable" in result.stderr


def test_missing_credentials_is_red(tmp_path, registry):
    make_tree(tmp_path, {"hello-world": "0.1.2"}, {"hello-world": ("hello-world", "0.1.2")})
    result = run(tmp_path, registry["url"],
                 env_extra={"ARTIFACTORY_DOCKER_USER": "", "ARTIFACTORY_DOCKER_PASSWORD": ""})
    assert result.returncode == 1
    assert "no registry credentials" in result.stderr
    assert registry["hits"] == []


def test_no_chart_under_apps_is_red(tmp_path, registry):
    make_tree(tmp_path, {}, {})
    (tmp_path / "apps").mkdir(exist_ok=True)
    result = run(tmp_path, registry["url"])
    assert result.returncode == 1
    assert "nothing to examine" in result.stderr


def test_no_pin_in_the_tree_is_red(tmp_path, registry):
    make_tree(tmp_path, {"hello-world": "0.1.2"}, {})
    result = run(tmp_path, registry["url"])
    assert result.returncode == 1
    assert "no Component pins an apps/ chart" in result.stderr


def test_an_upstream_chart_is_not_probed(tmp_path, registry):
    make_tree(tmp_path, {"hello-world": "0.1.2"},
              {"hello-world": ("hello-world", "0.1.2"), "cert-manager": ("cert-manager", "v1.16.2")})
    registry["published"].add(("hello-world", "0.1.2"))
    result = run(tmp_path, registry["url"])
    assert result.returncode == 0, result.stderr
    assert all("cert-manager" not in hit for hit in registry["hits"])


def test_a_version_pinned_twice_is_probed_once(tmp_path, registry):
    make_tree(tmp_path, {"hello-world": "0.1.2"},
              {"a": ("hello-world", "0.1.2"), "b": ("hello-world", "0.1.2")})
    registry["published"].add(("hello-world", "0.1.2"))
    result = run(tmp_path, registry["url"])
    assert result.returncode == 0, result.stderr
    assert len(registry["hits"]) == 1


def test_the_repo_tree_is_what_the_guard_examines(load):
    module = load("check-apps-published")
    charts = module.chart_sources(ROOT / "apps")
    assert set(charts) == {"downstream-bootstrap", "local-path-provisioner"}
    pins = module.first_party_pins([str(ROOT / "platform")], charts)
    assert {pin.chart for pin in pins} == set(charts)


def test_the_repo_tree_passes_against_a_registry_that_has_its_pins(load, registry):
    module = load("check-apps-published")
    charts = module.chart_sources(ROOT / "apps")
    pins = module.first_party_pins([str(ROOT / "platform"), str(ROOT / "examples")], charts)
    assert pins, "no pin in the real tree: the test would prove nothing"
    for pin in pins:
        registry["published"].add((pin.chart, pin.version))
    result = subprocess.run(
        [sys.executable, str(SCRIPT), "platform", "examples", "--registry", registry["url"],
         "--repo", REPO],
        cwd=ROOT, env=dict(CREDS, PATH="/usr/bin:/bin", PYTHONIOENCODING="utf-8"),
        capture_output=True, text=True, check=False)
    assert result.returncode == 0, result.stderr
    assert "%d pins examined" % len(pins) in result.stdout
