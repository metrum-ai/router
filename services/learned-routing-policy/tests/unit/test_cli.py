# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import pytest
from lrp.cli import parser


@pytest.mark.parametrize("args", [["serve", "--secret", "PRIVATE-SENTINEL"],
                                  ["featurize", "--seed", "PRIVATE-SENTINEL"]])
def test_parser_never_echoes_invalid_values(args, capsys):
    with pytest.raises(SystemExit):
        parser().parse_args(args)
    assert "PRIVATE-SENTINEL" not in capsys.readouterr().err


def test_cli_seed_and_serial_uncapped_options():
    args = parser().parse_args(["featurize", "--requests", "/data/in", "--out", "/data/out",
                                "--synthetic", "--seed", "7"])
    assert args.seed == 7
    assert args.near_dup_cosine is None
    args = parser().parse_args(["featurize", "--requests", "/data/in", "--out", "/data/out",
                                "--synthetic", "--near-dup-cosine", "0.98"])
    assert args.near_dup_cosine == 0.98
    args = parser().parse_args(["fanout", "--requests", "/data/in", "--out", "/data/out",
                                "--targets", "/data/targets", "--allow-uncapped",
                                "--concurrency", "1", "--max-total-cost-usd", "1"])
    assert args.concurrency == 1


def test_cli_deadline_override_is_forwarded(monkeypatch):
    from lrp.cli import execute

    seen = {}
    monkeypatch.setattr("lrp.serve.serve", lambda **kwargs: seen.update(kwargs))
    args = parser().parse_args(["serve", "--bundle", "/data/bundle", "--config", "/data/config",
                                "--deadline-ms", "500", "--trust", "/data/trust.json",
                                "--require-signed"])
    assert execute(args) == 0
    assert seen["deadline_ms"] == 500
    assert seen["require_signed"] is True
    assert str(seen["trusted_keys"]).endswith("trust.json")


def test_deadline_yaml_override_and_safe_config_input(tmp_path):
    import os

    from lrp.serve import load_settings

    path = tmp_path / "config.yaml"
    path.write_text("groups: {}\ndeadline_ms: 500\n")
    assert load_settings(path).deadline_ms == 500
    assert load_settings(path, 4500).deadline_ms == 4500
    with pytest.raises(ValueError):
        load_settings(path, 4501)
    path.unlink()
    os.mkfifo(path)
    with pytest.raises(ValueError):
        load_settings(path)
    path.unlink()
    path.write_bytes(b"x" * 1_048_577)
    with pytest.raises(ValueError):
        load_settings(path)
