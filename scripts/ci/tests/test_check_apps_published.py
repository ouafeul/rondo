import base64
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
CREDS = {"MIRROR_USER": "ci", "MIRROR_PASSWORD": "secret"}
BASIC = "Basic " + base64.b64encode(b"ci:secret").decode()


@pytest.fixture()
def registry():
    """An OCI registry stub. mode "basic": Basic challenge; "bearer": token endpoint at /token;
    "open": anonymous."""
    state = {"published": set(), "status": None, "hits": [], "paths": [], "mode": "basic",
             "tokens": []}

    class Handler(BaseHTTPRequestHandler):
        def reply(self, status, body=b"", headers=None):
            self.send_response(status)
            for k, v in (headers or {}).items():
                self.send_header(k, v)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):  # noqa: N802 (name imposed by BaseHTTPRequestHandler)
            auth = self.headers.get("Authorization", "")
            if self.path.startswith("/token"):
                state["tokens"].append((self.path, auth))
                if auth != BASIC:
                    return self.reply(401)
                return self.reply(200, json.dumps({"token": "tok"}).encode())
            if state["mode"] == "basic" and auth != BASIC:
                return self.reply(401, headers={"WWW-Authenticate": 'Basic realm="registry"'})
            if state["mode"] == "bearer" and auth != "Bearer tok":
                scope = "repository:charts/x:pull"
                return self.reply(401, headers={"WWW-Authenticate": (
                    f'Bearer realm="{state["url"]}/token",service="reg",scope="{scope}"')})
            state["hits"].append(self.path)
            if state["status"] is not None:
                return self.reply(state["status"])
            parts = self.path.strip("/").split("/")
            state["paths"].append("/".join(parts[1:-2]))
            found = (parts[0] == "v2" and parts[-2] == "manifests"
                     and (parts[-3], parts[-1]) in state["published"])
            self.reply(200 if found else 404, json.dumps({"schemaVersion": 2}).encode())

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
        [sys.executable, str(SCRIPT), "platform", "--oci-registry", registry_url, *args],
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
                 env_extra={"MIRROR_USER": "", "MIRROR_PASSWORD": ""})
    assert result.returncode == 1
    assert "not anonymous" in result.stderr
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
        [sys.executable, str(SCRIPT), "platform", "examples", "--oci-registry", registry["url"]],
        cwd=ROOT, env=dict(CREDS, PATH="/usr/bin:/bin", PYTHONIOENCODING="utf-8"),
        capture_output=True, text=True, check=False)
    assert result.returncode == 0, result.stderr
    assert "%d pins examined" % len(pins) in result.stdout


def test_a_bearer_registry_gets_a_token_from_its_realm(tmp_path, registry):
    registry["mode"] = "bearer"
    make_tree(tmp_path, {"hello-world": "0.1.2"}, {"hello-world": ("hello-world", "0.1.2")})
    registry["published"].add(("hello-world", "0.1.2"))
    result = run(tmp_path, registry["url"])
    assert result.returncode == 0, result.stderr
    assert "0.1.2, published" in result.stdout
    (path, auth), = registry["tokens"]
    assert auth == BASIC, "the credentials go to the token endpoint, not to the registry"
    assert "service=reg" in path and "scope=repository%3Acharts%2Fx%3Apull" in path


def test_a_bearer_registry_with_bad_credentials_is_red(tmp_path, registry):
    registry["mode"] = "bearer"
    make_tree(tmp_path, {"hello-world": "0.1.2"}, {"hello-world": ("hello-world", "0.1.2")})
    result = run(tmp_path, registry["url"], env_extra={"MIRROR_PASSWORD": "wrong"})
    assert result.returncode == 1
    assert "token endpoint" in result.stderr and "HTTP 401" in result.stderr


def test_an_anonymous_registry_needs_no_credentials(tmp_path, registry):
    registry["mode"] = "open"
    make_tree(tmp_path, {"hello-world": "0.1.2"}, {"hello-world": ("hello-world", "0.1.2")})
    registry["published"].add(("hello-world", "0.1.2"))
    result = run(tmp_path, registry["url"], env_extra={"MIRROR_USER": "", "MIRROR_PASSWORD": ""})
    assert result.returncode == 0, result.stderr


def test_charts_live_under_the_registry_prefix(tmp_path, registry):
    make_tree(tmp_path, {"hello-world": "0.1.2"}, {"hello-world": ("hello-world", "0.1.2")})
    registry["published"].add(("hello-world", "0.1.2"))
    result = run(tmp_path, registry["url"] + "/site/mirror")
    assert result.returncode == 0, result.stderr
    assert registry["paths"] == ["site/mirror/charts/hello-world"]
    result = run(tmp_path, registry["url"], "--charts-path", "helm")
    assert registry["paths"][-1] == "helm/hello-world"
