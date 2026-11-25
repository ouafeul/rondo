import json
from pathlib import Path

import pytest
import yaml
from jinja2 import Environment, FileSystemLoader, StrictUndefined

ROOT = Path(__file__).resolve().parents[3]
TEMPLATES = ROOT / "ansible" / "roles" / "bundle_install" / "templates"

CONFIG = {
    "NC_ADMIN_USER": {"kind": "string", "value": 'a "quote" and $dollar'},
    "NC_DEBUG": {"kind": "bool", "value": False},
    "NC_MAX_UPLOAD_GB": {"kind": "number", "value": 12},
    "NC_TRUSTED_DOMAINS": {"kind": "list", "value": ["nc.example.local", "10.0.0.10"]},
}


def _env() -> Environment:
    env = Environment(
        loader=FileSystemLoader(str(TEMPLATES)),
        undefined=StrictUndefined,
        keep_trailing_newline=True,
    )
    env.filters["to_json"] = json.dumps
    env.filters["to_nice_json"] = lambda v: json.dumps(v, indent=4, sort_keys=True)
    env.filters["to_nice_yaml"] = lambda v: yaml.dump(v, default_flow_style=False)
    env.filters["items2dict"] = lambda v: {i["key"]: i["value"] for i in v}
    return env


def render(fmt: str, config=None) -> str:
    return _env().get_template(f"{fmt}-config.j2").render(
        rondo_config=config if config is not None else CONFIG,
        rondo_component="demo-mft",
        inventory_hostname="mft-front-1",
    )


@pytest.mark.parametrize("fmt", ["shell", "env", "yaml", "json"])
def test_each_format_has_its_template(fmt):
    assert (TEMPLATES / f"{fmt}-config.j2").is_file()


def test_shell_distinguishes_the_four_kinds():
    out = render("shell")
    assert 'NC_ADMIN_USER="a \\"quote\\" and $dollar"' in out
    assert "NC_DEBUG=false" in out
    assert "NC_MAX_UPLOAD_GB=12" in out
    assert "NC_TRUSTED_DOMAINS=(" in out
    assert out.count('"nc.example.local"') == 1 and out.count('"10.0.0.10"') == 1


def test_env_quotes_everything_and_joins_the_list():
    out = render("env")
    assert 'NC_ADMIN_USER="a \\"quote\\" and $dollar"' in out
    assert "NC_DEBUG=false" in out
    assert "NC_MAX_UPLOAD_GB=12" in out
    assert 'NC_TRUSTED_DOMAINS="nc.example.local 10.0.0.10"' in out


def test_json_preserves_types():
    got = json.loads(render("json"))
    assert got == {
        "NC_ADMIN_USER": 'a "quote" and $dollar',
        "NC_DEBUG": False,
        "NC_MAX_UPLOAD_GB": 12,
        "NC_TRUSTED_DOMAINS": ["nc.example.local", "10.0.0.10"],
    }
    assert isinstance(got["NC_DEBUG"], bool)
    assert isinstance(got["NC_MAX_UPLOAD_GB"], int)


def test_yaml_preserves_types():
    got = yaml.safe_load(render("yaml"))
    assert got["NC_ADMIN_USER"] == 'a "quote" and $dollar'
    assert got["NC_TRUSTED_DOMAINS"] == ["nc.example.local", "10.0.0.10"]
    assert isinstance(got["NC_DEBUG"], bool)
    assert isinstance(got["NC_MAX_UPLOAD_GB"], int)


@pytest.mark.parametrize("fmt", ["json", "yaml"])
def test_ambiguous_string_does_not_become_a_number(fmt):
    config = {
        "A": {"kind": "string", "value": "33.0"},
        "B": {"kind": "string", "value": "true"},
        "C": {"kind": "string", "value": "12"},
    }
    load = json.loads if fmt == "json" else yaml.safe_load
    got = load(render(fmt, config))
    assert got == {"A": "33.0", "B": "true", "C": "12"}


@pytest.mark.parametrize("fmt", ["shell", "env", "yaml", "json"])
def test_input_order_does_not_change_the_render(fmt):
    reverse = dict(reversed(list(CONFIG.items())))
    assert render(fmt, reverse) == render(fmt)


@pytest.mark.parametrize("fmt", ["shell", "env", "yaml"])
def test_file_says_it_is_generated(fmt):
    assert "RONDO" in render(fmt)
