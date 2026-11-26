"""install.sh against a local mirror served over HTTP."""

import functools
import hashlib
import http.server
import shutil
import subprocess
import threading
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[3]
VERSION = "v9.9.9"
FAKE_RONDO = b'#!/bin/sh\n[ "$1" = init ] && : > "$RONDO_HOME/rondo.yaml"\necho "rondo FAKE-MARKER $*"\n'


@pytest.fixture()
def mirror(tmp_path):
    site = tmp_path / "site"
    release = site / "rondo" / VERSION
    release.mkdir(parents=True)
    (release / "rondo-linux-amd64").write_bytes(FAKE_RONDO)
    digest = hashlib.sha256(FAKE_RONDO).hexdigest()
    (release / "SHA256SUMS").write_text(f"{digest}  rondo-linux-amd64\n")
    (site / "rondo" / "stable.json").write_text(f'{{"version":"{VERSION}"}}\n')
    handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=str(site))
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    yield {"url": f"http://127.0.0.1:{server.server_address[1]}", "release": release}
    server.shutdown()


def run_install(tmp_path, mirror_url, **env):
    home = tmp_path / "home"
    home.mkdir(exist_ok=True)
    environ = {"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "HOME": str(home),
               "RONDO_MIRROR_URL": mirror_url, **env}
    return subprocess.run(["sh", str(ROOT / "install.sh")], env=environ, capture_output=True,
                          text=True, errors="replace", stdin=subprocess.DEVNULL, timeout=120)


def test_install_from_the_stable_channel_of_a_mirror(tmp_path, mirror):
    result = run_install(tmp_path, mirror["url"])
    assert result.returncode == 0, result.stderr + result.stdout
    binary = tmp_path / "home" / ".rondo" / "bin" / "rondo"
    assert binary.read_bytes() == FAKE_RONDO
    assert binary.stat().st_mode & 0o111
    assert f"rondo version: {VERSION}" in result.stdout
    # The binary is written to its file, not to the terminal.
    assert result.stdout.count("FAKE-MARKER") == 1  # only `rondo init` ran it
    assert (tmp_path / "home" / ".local" / "bin" / "rondo").is_symlink()


@pytest.mark.skipif(shutil.which("sha256sum") is None, reason="needs sha256sum")
def test_a_binary_that_does_not_match_sha256sums_is_refused(tmp_path, mirror):
    (mirror["release"] / "rondo-linux-amd64").write_bytes(FAKE_RONDO + b"# tampered\n")
    result = run_install(tmp_path, mirror["url"], RONDO_VERSION=VERSION)
    assert result.returncode != 0
    assert "checksum mismatch" in result.stderr
    assert not (tmp_path / "home" / ".rondo" / "bin" / "rondo").exists()
