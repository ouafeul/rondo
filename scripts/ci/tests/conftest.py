import importlib.util
import sys
from pathlib import Path

import pytest

_SCRIPTS_DIR = Path(__file__).resolve().parents[1]
# The scripts import their helper module (mirror_common) from their own directory.
sys.path.insert(0, str(_SCRIPTS_DIR))


def _load(name: str):
    path = _SCRIPTS_DIR / f"{name}.py"
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


@pytest.fixture(scope="session")
def mirror_images():
    return _load("mirror-images")


@pytest.fixture(scope="session")
def clamav_scan():
    return _load("clamav-scan")


@pytest.fixture(scope="session")
def mirror_common():
    return _load("mirror_common")


@pytest.fixture(scope="session")
def download_deps():
    return _load("download-deps")


@pytest.fixture(scope="session")
def resolve_deps():
    return _load("resolve-deps")


@pytest.fixture(scope="session")
def extract_helm_images():
    return _load("extract-helm-images")


@pytest.fixture(scope="session")
def fetch_mirror_tool():
    return _load("fetch-mirror-tool")


@pytest.fixture(scope="session")
def check_mirror_tools():
    return _load("check-mirror-tools")


@pytest.fixture()
def load():
    return _load


@pytest.fixture()
def stub_bin(tmp_path, monkeypatch):
    bin_dir = tmp_path / "stubbin"
    bin_dir.mkdir()
    monkeypatch.setenv("PATH", str(bin_dir), prepend=":")

    def make(name: str, body: str):
        script = bin_dir / name
        script.write_text(f"#!/bin/sh\n{body}\n")
        script.chmod(0o755)
        return script

    return make
