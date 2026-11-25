import pytest

FRESH_VERSION = 'echo "ClamAV 1.4.3/27500/$(date \'+%a %b %d %H:%M:%S %Y\')"'
STALE_VERSION = 'echo "ClamAV 1.4.3/27500/Wed Jan 01 08:00:00 2025"'


def test_db_age_fresh_passes(clamav_scan, stub_bin):
    clamscan = stub_bin("clamscan", f'if [ "$1" = "--version" ]; then {FRESH_VERSION}; fi')
    clamav_scan.check_db_age(str(clamscan), 14)


def test_db_age_stale_exits(clamav_scan, stub_bin):
    clamscan = stub_bin("clamscan", f'if [ "$1" = "--version" ]; then {STALE_VERSION}; fi')
    with pytest.raises(SystemExit) as exc:
        clamav_scan.check_db_age(str(clamscan), 14)
    assert exc.value.code == 2


def test_db_age_unparseable_exits(clamav_scan, stub_bin):
    clamscan = stub_bin("clamscan", 'echo "ClamAV weird output"')
    with pytest.raises(SystemExit):
        clamav_scan.check_db_age(str(clamscan), 14)


def test_db_age_disabled_skips_check(clamav_scan, stub_bin):
    clamscan = stub_bin("clamscan", "exit 9")
    clamav_scan.check_db_age(str(clamscan), 0)


@pytest.fixture()
def scan_tree(tmp_path):
    d = tmp_path / "artifacts"
    (d / "binaries").mkdir(parents=True)
    (d / "binaries" / "k3s").write_text("a")
    (d / "binaries" / "rke2.tar.gz").write_text("b")
    (d / "helm_charts").mkdir()
    (d / "helm_charts" / "rancher.tgz").write_text("c")
    return d


def test_scan_local_dir_parses_verdicts(clamav_scan, stub_bin, scan_tree):
    stub_bin("clamscan", (
        'DIR="$4"\n'
        'echo "$DIR/binaries/k3s: OK"\n'
        'echo "$DIR/helm_charts/rancher.tgz: Eicar-Test-Signature FOUND"\n'
        "exit 1"
    ))
    results, infected = clamav_scan.scan_local_dir(str(scan_tree), "clamscan")

    by_name = {r["name"]: r["status"] for r in results}
    assert by_name["binaries/k3s"] == "clean"
    assert by_name["helm_charts/rancher.tgz"] == "infected"
    assert by_name["binaries/rke2.tar.gz"] == "error"
    assert infected == 1


def test_scan_local_dir_all_clean(clamav_scan, stub_bin, scan_tree):
    stub_bin("clamscan", (
        'DIR="$4"\n'
        'find "$DIR" -type f | while read -r f; do echo "$f: OK"; done\n'
        "exit 0"
    ))
    results, infected = clamav_scan.scan_local_dir(str(scan_tree), "clamscan")
    assert infected == 0
    assert {r["status"] for r in results} == {"clean"}
    assert len(results) == 3
