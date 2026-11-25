"""Shape and safety rules of the GitHub Actions workflows."""

import re
import subprocess
from pathlib import Path

import pytest
import yaml

ROOT = Path(__file__).resolve().parents[3]
WORKFLOWS = ROOT / ".github" / "workflows"
FIRST_PARTY = ("actions/", "github/")
SHA = re.compile(r"^[0-9a-f]{40}$")


def load(path):
    # PyYAML reads the bare `on:` key as the boolean True.
    data = yaml.safe_load(path.read_text())
    if True in data:
        data["on"] = data.pop(True)
    return data


@pytest.fixture(scope="module")
def workflows():
    found = {p.name: load(p) for p in sorted(WORKFLOWS.glob("*.yml"))}
    assert found, "no workflow under .github/workflows"
    return found


def steps(workflow):
    for job_name, job in workflow["jobs"].items():
        for step in job.get("steps") or []:
            yield job_name, step


def step_named(workflow, job, name):
    for step in workflow["jobs"][job]["steps"]:
        if step.get("name") == name:
            return step
    raise AssertionError(f"no step {name!r} in job {job!r}")


def test_every_step_runs_a_string_or_uses_an_action(workflows):
    bad = []
    for wf_name, wf in workflows.items():
        for job, step in steps(wf):
            kinds = [k for k in ("run", "uses") if k in step]
            if len(kinds) != 1:
                bad.append(f"{wf_name}:{job}: step has {kinds or 'neither run nor uses'}")
            elif "run" in step and not isinstance(step["run"], str):
                bad.append(f"{wf_name}:{job}: `run` is a {type(step['run']).__name__}, quote it")
    assert not bad, "\n".join(bad)


def test_every_job_has_a_runner(workflows):
    bad = [f"{n}:{j}" for n, wf in workflows.items() for j, job in wf["jobs"].items()
           if "runs-on" not in job and "uses" not in job]
    assert not bad, f"jobs without runs-on: {bad}"


def test_third_party_actions_are_pinned_to_a_commit(workflows):
    bad = []
    for wf_name, wf in workflows.items():
        for job, step in steps(wf):
            uses = step.get("uses", "")
            if not uses or uses.startswith(FIRST_PARTY) or uses.startswith("./"):
                continue
            ref = uses.rpartition("@")[2]
            if not SHA.match(ref):
                bad.append(f"{wf_name}:{job}: {uses}")
    assert not bad, "third-party actions must be pinned to a full commit SHA:\n" + "\n".join(bad)


def test_workflows_default_to_read_only_token(workflows):
    bad = [n for n, wf in workflows.items() if wf.get("permissions") != {"contents": "read"}]
    assert not bad, f"top-level permissions must be contents: read in {bad}"


def test_ci_ok_gates_every_other_job(workflows):
    jobs = workflows["ci.yml"]["jobs"]
    gate = jobs["ci-ok"]
    assert gate.get("if") == "always()"
    assert set(gate["needs"]) == set(jobs) - {"ci-ok"}


def test_release_runs_only_for_version_tags(workflows):
    on = workflows["release.yml"]["on"]
    assert set(on) == {"push", "workflow_dispatch"}
    assert on["push"] == {"tags": ["v*"]}


def test_stable_channel_only_moves_on_final_releases(workflows):
    script = step_named(workflows["release.yml"], "artifacts", "Bundles")["run"]
    lines = script.splitlines()
    guard = next(i for i, line in enumerate(lines)
                 if "grep -Eq '^v[0-9]+\\.[0-9]+\\.[0-9]+$'" in line)
    writes = [i for i, line in enumerate(lines) if "stable.json" in line]
    assert writes, "the release no longer writes stable.json"
    indent = len(lines[guard]) - len(lines[guard].lstrip())
    for i in writes:
        assert i > guard, "stable.json written before the final-version guard"
        assert len(lines[i]) - len(lines[i].lstrip()) > indent, "stable.json written outside the guard"


def test_edge_tags_only_from_main_and_pull_requests_never_push(workflows):
    wf = workflows["images.yml"]
    job = wf["jobs"]["image"]
    assert job["env"]["PUSH"] == "${{ github.event_name != 'pull_request' }}"
    meta = next(s for _, s in steps(wf) if s.get("id") == "meta")
    for line in meta["with"]["tags"].splitlines():
        if "edge-main" in line or "steps.v.outputs.version" in line:
            assert "enable=${{ github.ref == 'refs/heads/main' }}" in line, line
    for _, step in steps(wf):
        if step.get("with", {}).get("push") is True:
            assert step.get("if") == "env.PUSH == 'true'"


def test_production_package_has_no_suffix():
    nfpm = yaml.safe_load((ROOT / "packaging" / "nfpm.yaml").read_text())
    assert nfpm["name"].endswith("${PKG_SUFFIX}")
    assert "replaces" not in nfpm and "provides" not in nfpm


# Package versions: the derivation in release.yml, run as-is under sh.

def derivation_script(workflows):
    script = step_named(workflows["release.yml"], "artifacts", "Packages (rpm, deb)")["run"]
    start = script.index('tag="${TAG#v}"')
    end = script.index("export VERSION")
    return script[start:end] + 'printf "%s" "$VERSION"\n'


@pytest.mark.parametrize("tag,want", [
    ("v1.0.0", "1.0.0"),
    ("v1.0.0-rc.1", "1.0.0~rc.1"),
    ("v1.2.0-alpha.1", "1.2.0~alpha.1"),
    ("v1.0.0-alpha-5", "1.0.0~alpha.5"),
    ("v10.20.30-rc1", "10.20.30~rc1"),
])
def test_package_version(workflows, tag, want):
    out = subprocess.run(["sh", "-c", derivation_script(workflows)], env={"TAG": tag, "PATH": "/usr/bin:/bin"},
                         capture_output=True, text=True, check=True)
    assert out.stdout == want
    assert "-" not in out.stdout


def test_prerelease_sorts_below_its_final_release(workflows):
    def version(tag):
        return subprocess.run(["sh", "-c", derivation_script(workflows)], env={"TAG": tag, "PATH": "/usr/bin:/bin"},
                              capture_output=True, text=True, check=True).stdout
    assert version("v1.0.0-rc.1") == version("v1.0.0") + "~rc.1"


def test_release_ships_the_mirror_script(workflows):
    script = step_named(workflows["release.yml"], "artifacts", "Bundles")["run"]
    assert "scripts/mirror-release.sh" in script
    assert (ROOT / "scripts" / "mirror-release.sh").exists()


def test_mirror_reads_the_vendor_neutral_secrets(workflows):
    env = workflows["mirror.yml"]["jobs"]["mirror"]["env"]
    assert {k: v for k, v in env.items() if k.startswith("MIRROR_")} == {
        "MIRROR_REGISTRY": "${{ secrets.MIRROR_REGISTRY }}",
        "MIRROR_FILES_URL": "${{ secrets.MIRROR_FILES_URL }}",
        "MIRROR_RPM_URL": "${{ secrets.MIRROR_RPM_URL }}",
        "MIRROR_USER": "${{ secrets.MIRROR_USER }}",
        "MIRROR_PASSWORD": "${{ secrets.MIRROR_PASSWORD }}",
        "MIRROR_CA_CERT_PEM": "${{ secrets.MIRROR_CA_CERT }}",
    }
    text = (WORKFLOWS / "mirror.yml").read_text()
    for gone in ("--artifactory-url", "--target-registry", "--dest-creds", "xray"):
        assert gone not in text.lower(), gone


def test_mirror_scans_with_trivy_and_does_not_fail_on_findings_by_default(workflows):
    wf = workflows["mirror.yml"]
    assert wf["on"]["workflow_dispatch"]["inputs"]["fail_on_vulns"]["default"] is False
    trivy = [s for _, s in steps(wf) if s.get("uses", "").startswith("aquasecurity/trivy-action@")]
    assert trivy
    pinned = "aquasecurity/trivy-action@ed142fd0673e97e23eac54620cfb913e5ce36c25"
    assert all(s["uses"] == pinned for s in trivy)
    assert not (ROOT / "scripts" / "ci" / "xray-scan.py").exists()


def test_mirror_copies_the_rancher_and_rke2_image_lists(workflows):
    step = step_named(workflows["mirror.yml"], "mirror", "Upstream image lists (Rancher, rke2)")["run"]
    assert "rancher/rancher/releases/download/v${rancher_version#v}/rancher-images.txt" in step
    assert "rancher/rke2/releases/download/${rke2_version}/rke2-images.linux-amd64.txt" in step
