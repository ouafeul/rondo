from __future__ import annotations

import tempfile
from pathlib import Path

import pytest


def _fresh(load):
    return load("check-coverage")


def _run(module, text: str) -> int:
    with tempfile.TemporaryDirectory() as d:
        path = Path(d) / "cover.out"
        path.write_text(text, encoding="utf-8")
        return module.main(["check-coverage.py", str(path)])


PROFILE = """mode: set
rondo/internal/controllers/stack.go:10.2,12.3 4 1
rondo/internal/controllers/stack.go:20.2,21.3 1 0
rondo/internal/controllers/release.go:5.2,6.3 5 1
rondo/internal/values/values.go:1.2,2.3 10 1
"""


def test_percentage_is_weighted_by_statements(load):
    module = _fresh(load)
    per_package = module.parse_profile(PROFILE)
    covered, total = per_package["rondo/internal/controllers"]
    assert (covered, total) == (9, 10)
    assert module.percentage(covered, total) == pytest.approx(90.0)


def test_package_missing_from_profile_fails(load):
    module = _fresh(load)
    module.TOTAL_FLOOR = 0.0
    module.FLOORS = {"rondo/internal/absent": 50.0}
    assert _run(module, PROFILE) == 1


def test_missed_floor_fails_and_held_floor_passes(load):
    module = _fresh(load)
    module.TOTAL_FLOOR = 0.0
    module.FLOORS = {"rondo/internal/controllers": 95.0}
    assert _run(module, PROFILE) == 1

    module = _fresh(load)
    module.TOTAL_FLOOR = 0.0
    module.FLOORS = {"rondo/internal/controllers": 90.0}
    assert _run(module, PROFILE) == 0


def test_total_is_a_separate_floor(load):
    module = _fresh(load)
    module.FLOORS = {}
    module.TOTAL_FLOOR = 96.0
    assert _run(module, PROFILE) == 1
    module.TOTAL_FLOOR = 95.0
    assert _run(module, PROFILE) == 0


def test_empty_profile_is_refused(load):
    module = _fresh(load)
    with pytest.raises(SystemExit):
        _run(module, "mode: set\n")


def test_unreadable_line_is_refused(load):
    module = _fresh(load)
    with pytest.raises(SystemExit):
        module.parse_profile("mode: set\nnot a profile line\n")
