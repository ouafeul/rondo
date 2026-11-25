import re
import subprocess
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / "scripts" / "ci" / "envtest-version.sh"
DEPS = ROOT / "manifests" / "dependencies.yaml"


def _k3s_minor() -> str:
    deps = yaml.safe_load(DEPS.read_text())
    for entry in deps["binaries"]:
        if entry["name"] == "k3s":
            m = re.match(r"v?(\d+)\.(\d+)\.", str(entry["version"]))
            assert m, f"unreadable k3s version: {entry['version']!r}"
            return f"{m.group(1)}.{m.group(2)}"
    raise AssertionError("binaries.k3s entry missing from the manifest")


def test_version_is_derived_from_the_manifest():
    out = subprocess.run([str(SCRIPT)], capture_output=True, text=True, check=True)
    assert out.stdout.strip() == _k3s_minor()


def test_runs_without_a_python_interpreter(stub_bin):
    for name in ("python3", "python"):
        stub_bin(name, 'echo "ModuleNotFoundError: No module named yaml" >&2; exit 1')
    out = subprocess.run([str(SCRIPT)], capture_output=True, text=True)
    assert out.returncode == 0, f"stderr={out.stderr}"
    assert out.stdout.strip() == _k3s_minor()


def test_fails_when_the_k3s_entry_is_missing(tmp_path):
    fake = tmp_path / "scripts" / "ci"
    fake.mkdir(parents=True)
    (tmp_path / "manifests").mkdir()
    (tmp_path / "manifests" / "dependencies.yaml").write_text(
        'binaries:\n  - name: helm\n    version: "v3.17.3"\n'
    )
    copy = fake / SCRIPT.name
    copy.write_bytes(SCRIPT.read_bytes())
    copy.chmod(0o755)
    out = subprocess.run([str(copy)], capture_output=True, text=True)
    assert out.returncode != 0
    assert out.stdout.strip() == ""


def test_no_invocation_masks_the_failure():
    faulty = []
    for path in sorted((ROOT / ".github" / "workflows").glob("*.yml")):
        for num, line in enumerate(path.read_text().splitlines(), start=1):
            if "envtest-version.sh" not in line:
                continue
            stripped = line.lstrip(" -").lstrip()
            if stripped.startswith(("export ", "declare ", "readonly ", "local ")):
                faulty.append(f"{path.relative_to(ROOT)}:{num}: {stripped}")
    assert not faulty, (
        "invocation masks the exit code (use an assignment, then "
        "`export VAR`):\n" + "\n".join(faulty)
    )
