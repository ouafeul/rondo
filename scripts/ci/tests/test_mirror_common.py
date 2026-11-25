import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest


@pytest.fixture()
def file_server():
    """A dumb HTTP file server: PUT stores, GET reads, Basic auth checked when `auth` is set."""
    state = {"files": {}, "puts": [], "auth": None}

    class Handler(BaseHTTPRequestHandler):
        def authorized(self):
            if state["auth"] and self.headers.get("Authorization") != state["auth"]:
                self.send_response(401)
                self.end_headers()
                return False
            return True

        def do_PUT(self):  # noqa: N802
            if not self.authorized():
                return
            body = self.rfile.read(int(self.headers["Content-Length"]))
            state["files"][self.path] = body
            state["puts"].append(self.path)
            self.send_response(201)
            self.send_header("Content-Length", "0")
            self.end_headers()

        def do_GET(self):  # noqa: N802
            if not self.authorized():
                return
            body = state["files"].get(self.path)
            self.send_response(200 if body is not None else 404)
            self.send_header("Content-Length", str(len(body or b"")))
            self.end_headers()
            self.wfile.write(body or b"")

        def log_message(self, *args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    state["url"] = "http://127.0.0.1:%d" % server.server_address[1]
    yield state
    server.shutdown()
    server.server_close()


def test_http_store_uploads_with_a_sidecar_and_skips_the_second_time(mirror_common, file_server, tmp_path):
    f = tmp_path / "k3s"
    f.write_bytes(b"binary")
    store = mirror_common.store_for(file_server["url"] + "/mirror")
    assert store.put_checked("k3s/v1/k3s", f) is True
    assert file_server["files"]["/mirror/k3s/v1/k3s"] == b"binary"
    sidecar = file_server["files"]["/mirror/k3s/v1/k3s.sha256"].decode()
    assert sidecar.split()[0] == mirror_common.sha256_of(f)
    assert file_server["puts"][-1].endswith(".sha256"), "the sidecar is written after the file"

    file_server["puts"].clear()
    assert store.put_checked("k3s/v1/k3s", f) is False
    assert file_server["puts"] == []

    f.write_bytes(b"changed")
    assert store.put_checked("k3s/v1/k3s", f) is True


def test_http_store_sends_the_mirror_credentials(mirror_common, file_server, tmp_path, monkeypatch):
    import base64
    file_server["auth"] = "Basic " + base64.b64encode(b"u:p").decode()
    monkeypatch.setenv("MIRROR_USER", "u")
    monkeypatch.setenv("MIRROR_PASSWORD", "p")
    f = tmp_path / "x"
    f.write_text("x")
    mirror_common.store_for(file_server["url"]).put("x", f)
    assert file_server["files"]["/x"] == b"x"


def test_dir_store_is_a_store(mirror_common, tmp_path):
    f = tmp_path / "src"
    f.write_text("data")
    store = mirror_common.store_for(str(tmp_path / "site"))
    assert store.put_checked("a/b/file", f) is True
    assert (tmp_path / "site" / "a" / "b" / "file").read_text() == "data"
    assert store.put_checked("a/b/file", f) is False


def test_s3_store_without_boto3_explains_itself(mirror_common, monkeypatch):
    import builtins
    real_import = builtins.__import__

    def no_boto3(name, *args, **kwargs):
        if name == "boto3":
            raise ImportError(name)
        return real_import(name, *args, **kwargs)

    monkeypatch.setattr(builtins, "__import__", no_boto3)
    with pytest.raises(SystemExit) as exc:
        mirror_common.store_for("s3://bucket/prefix")
    assert "boto3" in str(exc.value)


def test_registry_helpers(mirror_common):
    assert mirror_common.registry_host("https://reg.site:5000/mirror") == "reg.site:5000"
    assert mirror_common.registry_ref("reg.site/mirror/", "charts") == "reg.site/mirror/charts"
    assert mirror_common.join_url("https://x/", "/a/", "b") == "https://x/a/b"


def test_target_flags_default_to_the_environment(mirror_common, monkeypatch):
    import argparse
    monkeypatch.setenv("MIRROR_REGISTRY", "reg.site/m")
    monkeypatch.setenv("MIRROR_FILES_URL", "https://files.site/m")
    monkeypatch.setenv("MIRROR_RPM_URL", "https://rpm.site/m")
    parser = argparse.ArgumentParser()
    mirror_common.add_target_args(parser, oci=True, files=True, rpm=True, providers=True)
    args = parser.parse_args([])
    assert (args.oci_registry, args.files_url, args.rpm_url) == (
        "reg.site/m", "https://files.site/m", "https://rpm.site/m")
    assert args.provider_mirror == ""
    args = parser.parse_args(["--oci-registry", "other/x"])
    assert args.oci_registry == "other/x"


def test_charts_ref_defaults_under_the_registry_and_follows_charts_oci(mirror_common):
    assert mirror_common.charts_ref("reg.site/rondo") == "reg.site/rondo/charts"
    assert mirror_common.charts_ref("https://reg.site", "") == "reg.site/charts"
    assert mirror_common.charts_ref("reg.site/rondo", "oci://reg.site/helm-local/") == "reg.site/helm-local"
