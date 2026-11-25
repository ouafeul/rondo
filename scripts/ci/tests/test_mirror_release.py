"""scripts/mirror-release.sh with skopeo, helm, git, curl and createrepo_c stubbed on PATH."""

import hashlib
import subprocess
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / "scripts" / "mirror-release.sh"

ASSETS = {
    "rondo-linux-amd64": b"cli",
    "install.sh": b"#!/bin/sh\n",
    "iac.tar.gz": b"iac",
    "iac.tar.gz.sha256": b"sidecar",
    "vm.tar.gz": b"vm",
    "rondo-ansible.tar.gz": b"ansible",
    "stable.json": b'{"version":"v1.2.3"}',
    "rondo-1.2.3.x86_64.rpm": b"rpm",
    "rondo-sandbox_1.2.3_amd64.deb": b"deb",
}


@pytest.fixture()
def release(tmp_path, stub_bin, monkeypatch):
    fixtures = tmp_path / "fixtures"
    fixtures.mkdir()
    for name, body in ASSETS.items():
        (fixtures / name).write_bytes(body)
    sums = "".join(f"{hashlib.sha256(b).hexdigest()}  {n}\n" for n, b in ASSETS.items())
    (fixtures / "SHA256SUMS").write_text(sums)
    log = tmp_path / "calls.log"
    monkeypatch.setenv("FIXTURES", str(fixtures))
    monkeypatch.setenv("CALLS", str(log))
    stub_bin("curl", '''out=""; url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -K|-T|--retry) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
echo "curl $url" >> "$CALLS"
[ -f "$FIXTURES/${url##*/}" ] || exit 22
cp "$FIXTURES/${url##*/}" "$out"''')
    stub_bin("git", '''for a; do dir="$a"; done
echo "git $*" >> "$CALLS"
mkdir -p "$dir/apps/controller-chart" "$dir/apps/dashboard"
printf 'apiVersion: v2\\nname: controller-chart\\nversion: 0.1.2\\n' > "$dir/apps/controller-chart/Chart.yaml"
printf 'name: "dashboard"\\nversion: "0.2.0"\\n' > "$dir/apps/dashboard/Chart.yaml"''')
    stub_bin("skopeo", 'echo "skopeo $*" >> "$CALLS"')
    stub_bin("helm", 'echo "helm $*" >> "$CALLS"\n[ "$1" = "show" ] && exit 1\nexit 0')
    # POSIX sh (dash on Linux): the last argument via a loop, not ${@: -1}.
    createrepo = stub_bin("createrepo_c", 'echo "createrepo_c $*" >> "$CALLS"\nfor last; do :; done\n'
                             'mkdir -p "$last/repodata"\n'
                             'echo md > "$last/repodata/repomd.xml"; echo p > "$last/repodata/primary.xml.gz"')
    site = tmp_path / "site"

    def run(*args, env=None, check=True):
        result = subprocess.run(
            ["bash", str(SCRIPT), "--version", "v1.2.3", "--registry", "reg.site/rondo",
             "--files-url", str(site), *args],
            capture_output=True, text=True, env=env)
        if check:
            assert result.returncode == 0, result.stderr + result.stdout
        return result

    return type("Release", (), {"run": staticmethod(run), "site": site, "log": log,
                                "fixtures": fixtures, "createrepo": createrepo})


def plan(result):
    return [line[len("PLAN "):] for line in result.stdout.splitlines() if line.startswith("PLAN ")]


def calls(release):
    return release.log.read_text().splitlines() if release.log.exists() else []


def test_dry_run_plans_the_copy_list_and_changes_nothing(release):
    result = release.run("--dry-run", "--channel", "stable")
    lines = plan(result)

    for image in ("rondo-controller", "rondo-toolbox", "rondo-component-toolbox", "rondo-sandbox-machine"):
        assert f"image: ghcr.io/ouafeul/{image}:v1.2.3 -> reg.site/rondo/{image}:v1.2.3" in lines
    assert ("chart: oci://ghcr.io/ouafeul/charts/controller-chart 0.1.2 -> "
            "oci://reg.site/rondo/charts/controller-chart") in lines
    assert ("chart: oci://ghcr.io/ouafeul/charts/dashboard 0.2.0 -> "
            "oci://reg.site/rondo/charts/dashboard") in lines

    base = f"{release.site}/rondo/v1.2.3"
    assert f"file: rondo-linux-amd64 -> {base}/rondo-linux-amd64" in lines
    assert f"file: vm.tar.gz -> {base}/vm.tar.gz" in lines
    assert f"file: vm.tar.gz -> {release.site}/tofu-modules/vm/v1.2.3/vm.tar.gz" in lines
    assert (f"file: rondo-ansible.tar.gz -> "
            f"{release.site}/ansible/rondo/v1.2.3/rondo-ansible.tar.gz") in lines
    assert "skip: stable.json (channels are set with --channel)" in lines
    assert "skip: iac.tar.gz.sha256 (replaced by the sidecars)" in lines
    assert "skip: rondo-1.2.3.x86_64.rpm (no --rpm-url)" in lines
    assert not any(line.startswith("rpm:") for line in lines)
    assert lines[-1] == f'channel: {release.site}/rondo/stable.json -> {{"version":"v1.2.3"}} (last)'

    assert not release.site.exists()
    assert not any(c.startswith(("skopeo", "helm")) for c in calls(release))
    assert "git clone --quiet --depth 1 --branch v1.2.3 https://github.com/ouafeul/rondo.git" in calls(release)[1]


def test_dry_run_with_an_rpm_url_plans_packages(release):
    lines = plan(release.run("--dry-run", "--rpm-url", "https://rpm.site/m"))
    assert "rpm: rondo-1.2.3.x86_64.rpm -> https://rpm.site/m/rondo/v1.2.3/rondo-1.2.3.x86_64.rpm" in lines
    assert ("rpm: rondo-sandbox_1.2.3_amd64.deb -> "
            "https://rpm.site/m/rondo/v1.2.3/rondo-sandbox_1.2.3_amd64.deb") in lines
    assert not any(line.startswith("channel:") for line in lines)


def test_a_version_without_v_is_accepted(release):
    result = subprocess.run(
        ["bash", str(SCRIPT), "--version", "1.2.3", "--registry", "r", "--files-url", str(release.site),
         "--dry-run"], capture_output=True, text=True)
    assert result.returncode == 0, result.stderr
    assert "ghcr.io/ouafeul/rondo-controller:v1.2.3" in result.stdout


@pytest.mark.parametrize("args,message", [
    (["--version", "main", "--registry", "r", "--files-url", "x"], "is not a vMAJOR"),
    (["--registry", "r", "--files-url", "x"], "--version is required"),
    (["--version", "v1.0.0", "--files-url", "x"], "--registry"),
    (["--version", "v1.0.0", "--registry", "r"], "--files-url"),
    (["--version", "v1.0.0", "--registry", "r", "--files-url", "x", "--channel", "Bad Name"], "invalid channel"),
    (["--version", "v1.0.0", "--bogus"], "unknown argument"),
])
def test_bad_arguments_are_refused(args, message, monkeypatch):
    for var in ("MIRROR_REGISTRY", "MIRROR_FILES_URL"):
        monkeypatch.delenv(var, raising=False)
    result = subprocess.run(["bash", str(SCRIPT), *args], capture_output=True, text=True)
    assert result.returncode != 0
    assert message in result.stderr


def test_a_real_run_copies_everything_and_writes_the_channel_last(release):
    result = release.run("--channel", "stable")
    site = release.site
    assert (site / "rondo" / "v1.2.3" / "rondo-linux-amd64").read_bytes() == b"cli"
    assert (site / "rondo" / "v1.2.3" / "rondo-linux-amd64.sha256").read_text().split()[0] == \
        hashlib.sha256(b"cli").hexdigest()
    assert (site / "tofu-modules" / "vm" / "v1.2.3" / "vm.tar.gz").read_bytes() == b"vm"
    assert (site / "ansible" / "rondo" / "v1.2.3" / "rondo-ansible.tar.gz").read_bytes() == b"ansible"
    assert not (site / "rondo" / "v1.2.3" / "stable.json").exists()
    assert (site / "rondo" / "stable.json").read_text() == '{"version":"v1.2.3"}\n'
    uploads = [line for line in result.stdout.splitlines() if line.startswith("==> upload ")]
    assert uploads[-1].endswith("/rondo/stable.json")

    done = calls(release)
    assert sum(1 for c in done if c.startswith("skopeo copy --all")) == 4
    assert any(c.startswith("skopeo copy --all") and c.endswith(
        "docker://ghcr.io/ouafeul/rondo-toolbox:v1.2.3 docker://reg.site/rondo/rondo-toolbox:v1.2.3")
        for c in done)
    assert "helm pull oci://ghcr.io/ouafeul/charts/dashboard --version 0.2.0 -d" in " ".join(
        c for c in done if c.startswith("helm pull"))
    assert any(c.startswith("helm push") and "dashboard-0.2.0.tgz oci://reg.site/rondo/charts" in c
               for c in done)

    again = release.run("--channel", "stable")
    assert "skip " in again.stdout
    assert not [line for line in again.stdout.splitlines() if line.startswith("==> upload ")]


def test_a_checksum_mismatch_stops_before_the_channel(release):
    (release.fixtures / "iac.tar.gz").write_bytes(b"tampered")
    result = release.run("--channel", "stable", check=False)
    assert result.returncode != 0
    assert "iac.tar.gz: checksum differs from SHA256SUMS" in result.stderr
    assert not (release.site / "rondo" / "stable.json").exists()


def test_rpms_go_to_the_rpm_url_with_repodata(release, tmp_path):
    rpm_site = tmp_path / "rpm-site"
    release.run("--rpm-url", str(rpm_site))
    base = rpm_site / "rondo" / "v1.2.3"
    assert (base / "rondo-1.2.3.x86_64.rpm").read_bytes() == b"rpm"
    assert (base / "rondo-sandbox_1.2.3_amd64.deb").read_bytes() == b"deb"
    assert (base / "repodata" / "repomd.xml").exists()
    assert any(c.startswith("createrepo_c") for c in calls(release))


def test_without_createrepo_c_the_packages_still_go_and_the_script_warns(release, tmp_path):
    release.createrepo.unlink()
    rpm_site = tmp_path / "rpm-site"
    result = release.run("--rpm-url", str(rpm_site))
    assert "createrepo_c is not installed" in result.stderr
    assert (rpm_site / "rondo" / "v1.2.3" / "rondo-1.2.3.x86_64.rpm").exists()
    assert not (rpm_site / "rondo" / "v1.2.3" / "repodata").exists()
