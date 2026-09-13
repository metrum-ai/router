# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline training commands. Secrets are environment-only; errors omit input data."""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import sys
from pathlib import Path
from typing import Any, NoReturn

import yaml


class SafeParser(argparse.ArgumentParser):
    def error(self, message: str) -> NoReturn:
        self.exit(2, "lrp: invalid_arguments; use --help for supported options\n")


def parser() -> argparse.ArgumentParser:
    root = SafeParser(prog="lrp", description="Metrum learned routing policy")
    sub = root.add_subparsers(dest="command", required=True)
    collect = sub.add_parser("collect", help="Normalize approved content; logs alone are metadata")
    for name in ("router-log", "content-capture", "dataset"):
        collect.add_argument(f"--{name}", type=Path)
    collect.add_argument(
        "--usage-db",
        help="Read-only SQLite path or PostgreSQL DSN for explore metadata joins",
    )
    collect.add_argument("--out", type=Path, required=True)
    collect.add_argument("--approved-content", action="store_true")

    usage = sub.add_parser(
        "import-usage",
        help="Export read-only explore metadata from usage DB (no prompts)",
    )
    usage.add_argument(
        "--db",
        required=True,
        help="SQLite path/URI or PostgreSQL DSN (opened read-only)",
    )
    usage.add_argument("--out", type=Path, required=True)
    usage.add_argument("--class-label", default="lrp:explore")
    usage.add_argument(
        "--content-ids",
        type=Path,
        help="Governed content JSONL; only matching explore request IDs are written",
    )

    fanout = sub.add_parser("fanout", help="Collect candidate responses in protected storage")
    fanout.add_argument("--requests", type=Path, required=True)
    fanout.add_argument("--targets", type=Path, required=True)
    fanout.add_argument("--out", type=Path, required=True)
    fanout.add_argument("--via", choices=("router", "openrouter"), default="router")
    fanout.add_argument("--base-url")
    fanout.add_argument("--refresh-pricing", action="store_true", default=True)
    fanout.add_argument("--approved-content", action="store_true")
    fanout.add_argument("--allow-uncapped", action="store_true")
    fanout.add_argument("--max-total-cost-usd", type=float)
    fanout.add_argument("--concurrency", type=int, choices=range(1, 5), default=4)

    judge = sub.add_parser("judge", help="Verify or send approved content to a third-party judge")
    for name in ("requests", "responses", "out"):
        judge.add_argument(f"--{name}", type=Path, required=True)
    judge.add_argument("--judge", default="anthropic/claude-sonnet-4.6")
    judge.add_argument("--anchor", required=True, help="Actual upstream model ID")
    judge.add_argument("--anchor-provider", default="openrouter")
    judge.add_argument("--approved-content", action="store_true")
    judge.add_argument("--sandbox-rootfs", type=Path)
    judge.add_argument(
        "--audit-queue",
        type=Path,
        help="Protected scalar human-audit queue (no content)",
    )
    judge.add_argument(
        "--audit-sample-rate",
        type=float,
        help="Deterministic sample rate for pairwise/absolute judgments (default 0.02)",
    )
    judge.add_argument("--audit-seed", default="lrp-human-audit-v1")

    audit = sub.add_parser(
        "judge-audit",
        help="Sample or gate human judge audit without exporting protected content",
    )
    audit_sub = audit.add_subparsers(dest="audit_command", required=True)
    audit_sample = audit_sub.add_parser("sample", help="Build a scalar review queue")
    audit_sample.add_argument("--judgments", type=Path, required=True)
    audit_sample.add_argument("--out", type=Path, required=True)
    audit_sample.add_argument("--rate", type=float, default=0.02)
    audit_sample.add_argument("--seed", default="lrp-human-audit-v1")
    audit_report = audit_sub.add_parser("report", help="Agreement gate from human reviews")
    audit_report.add_argument("--queue", type=Path, required=True)
    audit_report.add_argument("--reviews", type=Path, required=True)
    audit_report.add_argument("--out", type=Path, required=True)
    audit_report.add_argument("--min-agreement", type=float, default=0.8)
    audit_report.add_argument("--min-coverage", type=float, default=0.5)
    audit_report.add_argument("--min-reviews", type=int, default=5)

    features = sub.add_parser("featurize")
    features.add_argument("--requests", type=Path, required=True)
    features.add_argument("--out", type=Path, required=True)
    features.add_argument("--embedding-model", type=Path)
    features.add_argument("--tokenizer", type=Path)
    features.add_argument("--synthetic", action="store_true", help="Wiring only; never promotable")
    features.add_argument("--seed", type=int, default=42)
    features.add_argument(
        "--near-dup-cosine",
        type=float,
        default=None,
        metavar="THRESHOLD",
        help=(
            "Before session splits, drop later rows with embedding cosine above "
            "THRESHOLD (reviewed value 0.98); keep earliest. "
            "Filter judgments/responses to kept request_ids before train/eval."
        ),
    )

    train = sub.add_parser("train")
    for name in ("features", "judgments", "responses", "out"):
        train.add_argument(f"--{name}", type=Path, required=True)
    train.add_argument("--embedding-model", type=Path)
    train.add_argument("--tokenizer", type=Path)
    train.add_argument("--synthetic", action="store_true")
    train.add_argument("--seed", type=int, default=42)
    train.add_argument("--anchor", required=True, help="Actual upstream model ID")
    train.add_argument("--anchor-provider", default="openrouter")
    train.add_argument(
        "--ensemble-size",
        type=int,
        default=5,
        choices=[1, 2, 3, 4, 5],
        help="Bootstrap ensemble members for uncertainty (reviewed default 5)",
    )

    evaluate = sub.add_parser("eval")
    for name in ("bundle", "features", "judgments", "responses", "config", "out"):
        evaluate.add_argument(f"--{name}", type=Path, required=True)
    evaluate.add_argument("--split", choices=("test",), default="test")
    evaluate.add_argument(
        "--synthetic", action="store_true", help="Evaluate gates without promotion"
    )

    drift = sub.add_parser(
        "drift-check",
        help="Compute PSI + embedding-distance drift; optional auto-shadow recommendation",
    )
    drift.add_argument("--reference-features", type=Path, required=True)
    drift.add_argument("--observed-features", type=Path, required=True)
    drift.add_argument("--out", type=Path, required=True)
    drift.add_argument("--psi-threshold", type=float, default=0.25)
    drift.add_argument("--embedding-distance-threshold", type=float, default=0.15)
    drift.add_argument(
        "--auto-shadow",
        action="store_true",
        help="Recommend router shadow when thresholds are exceeded (does not mutate router)",
    )

    serving = sub.add_parser("serve")
    serving.add_argument("--bundle", type=Path, required=True)
    serving.add_argument("--config", type=Path, required=True)
    serving.add_argument("--port", type=int, default=18093)
    serving.add_argument("--admin-port", type=int, default=18094)
    serving.add_argument("--enable-admin", action="store_true")
    serving.add_argument("--deadline-ms", type=int, help="Override YAML deadline_ms (1..4500; default 200)")
    serving.add_argument("--trust", type=Path, help="Operator Ed25519 trust JSON for signed bundles")
    serving.add_argument(
        "--require-signed",
        action="store_true",
        help="Reject unsigned bundles on serve and reload",
    )
    validate = sub.add_parser("validate")
    validate.add_argument("--bundle", type=Path, required=True)
    validate.add_argument("--trust", type=Path, help="Operator Ed25519 trust JSON")
    validate.add_argument(
        "--require-signed",
        action="store_true",
        help="Reject unsigned bundles",
    )

    sign = sub.add_parser("sign-bundle", help="Detach-sign manifest.json with Ed25519")
    sign.add_argument("--bundle", type=Path, required=True)
    sign.add_argument("--key", type=Path, required=True, help="Base64 Ed25519 seed or private key")
    sign.add_argument("--key-id", required=True)

    verify = sub.add_parser("verify-bundle", help="Verify optional operator manifest signature")
    verify.add_argument("--bundle", type=Path, required=True)
    verify.add_argument("--trust", type=Path, required=True)
    verify.add_argument("--require-signed", action="store_true")
    return root


def embedding_spec(args: argparse.Namespace) -> dict[str, Any]:
    if args.synthetic:
        return {"kind": "synthetic"}
    if args.embedding_model is None or args.tokenizer is None:
        raise ValueError("explicit embedding model and tokenizer required")
    return {
        "kind": "onnx",
        "model_path": str(args.embedding_model),
        "tokenizer_path": str(args.tokenizer),
        "pooling": "cls",
    }


def execute(args: argparse.Namespace) -> int:
    from lrp.collect import DataError, protected_path

    if hasattr(args, "out"):
        args.out = protected_path(args.out)
        if args.command == "eval":
            for suffix in (".md", ".svg"):
                protected_path(args.out.with_suffix(suffix))

    def read_yaml(path: Path) -> Any:
        if path.is_symlink() or path.stat().st_size > 1_048_576:
            raise DataError("invalid_config_file")
        return yaml.safe_load(path.read_text())

    if args.command == "collect":
        from lrp.collect import collect

        stats = collect(
            out=args.out,
            dataset=args.dataset,
            content_capture=args.content_capture,
            router_log=args.router_log,
            usage_db=args.usage_db,
            approved_content=args.approved_content,
        )
        print(json.dumps({"counts": stats}))
    elif args.command == "import-usage":
        from lrp.usage_import import import_usage

        stats = import_usage(
            dsn=args.db,
            out=args.out,
            class_label=args.class_label,
            content_ids=args.content_ids,
        )
        print(json.dumps({"counts": stats}))
    elif args.command == "fanout":
        from lrp.fanout import run_fanout

        configured = read_yaml(args.targets)
        targets = configured["targets"] if isinstance(configured, dict) else configured
        stats = asyncio.run(
            run_fanout(
                requests=args.requests,
                out=args.out,
                targets=targets,
                via=args.via,
                base_url=args.base_url,
                refresh_pricing=args.refresh_pricing,
                approved_content=args.approved_content,
                allow_uncapped=args.allow_uncapped,
                    max_total_cost_usd=args.max_total_cost_usd,
                    concurrency=args.concurrency,
            )
        )
        print(json.dumps({"counts": stats}))
    elif args.command == "judge":
        from lrp.judge import judge_requests
        from lrp.judge.sandbox import Sandbox

        stats = asyncio.run(
            judge_requests(
                requests=args.requests,
                responses=args.responses,
                out=args.out,
                anchor=(args.anchor_provider, args.anchor),
                judge_model=args.judge,
                approved_content=args.approved_content,
                sandbox=Sandbox(args.sandbox_rootfs) if args.sandbox_rootfs else None,
                audit_queue=args.audit_queue,
                audit_sample_rate=args.audit_sample_rate,
                audit_seed=args.audit_seed,
            )
        )
        print(json.dumps({"counts": stats}))
    elif args.command == "judge-audit":
        from lrp.judge.audit import (
            build_agreement_report,
            sample_judgments,
            write_agreement_report,
        )

        if args.audit_command == "sample":
            stats = sample_judgments(
                judgments=args.judgments,
                out=args.out,
                rate=args.rate,
                seed=args.seed,
            )
            print(json.dumps({"counts": stats}))
        else:
            report = build_agreement_report(
                queue=args.queue,
                reviews=args.reviews,
                min_agreement=args.min_agreement,
                min_coverage=args.min_coverage,
                min_reviews=args.min_reviews,
            )
            write_agreement_report(report, args.out)
            print(
                json.dumps(
                    {
                        "gate_passed": report["gate_passed"],
                        "agreement_rate": report["agreement_rate"],
                        "coverage": report["coverage"],
                        "reviewed_count": report["reviewed_count"],
                        "gate_reasons": report["gate_reasons"],
                    }
                )
            )
            if not report["gate_passed"]:
                return 1
    elif args.command == "featurize":
        from lrp.features import FeatureBuilder, ONNXEmbedder, SyntheticEmbedder, featurize

        spec = embedding_spec(args)
        embedder = (
            SyntheticEmbedder()
            if spec["kind"] == "synthetic"
            else ONNXEmbedder(Path(spec["model_path"]), Path(spec["tokenizer_path"]), threads=1)
        )
        near_dup = None if args.near_dup_cosine is None else float(args.near_dup_cosine)
        frame = featurize(
            args.requests,
            args.out,
            builder=FeatureBuilder(embedder),
            seed=args.seed,
            near_dup_cosine=near_dup,
        )
        print(json.dumps({"near_duplicate": dict(frame.attrs.get("near_duplicate", {}))}))
    elif args.command == "train":
        from lrp.train import train

        produced = train(
            args.features,
            args.judgments,
            args.responses,
            args.out,
            embedding=embedding_spec(args),
            seed=args.seed,
            threads=1,
            min_train_rows=200,
            anchor=(args.anchor_provider, args.anchor),
            ensemble_size=int(args.ensemble_size),
        )
        print(json.dumps({"bundle_version": produced.name}))
    elif args.command == "drift-check":
        import numpy as np
        import pyarrow.parquet as pq

        from lrp.drift import DriftThresholds, evaluate_drift, feature_matrix_split
        from lrp.features import FEATURE_NAMES, private_file

        def load_matrix(path: Path) -> np.ndarray:
            with private_file(path) as handle:
                frame = pq.ParquetFile(handle).read().to_pandas()
            matrix = frame.loc[:, list(FEATURE_NAMES)].to_numpy(dtype=np.float32)
            return np.asarray(matrix, dtype=np.float32)

        ref_emb, ref_scalar = feature_matrix_split(load_matrix(args.reference_features))
        obs_emb, obs_scalar = feature_matrix_split(load_matrix(args.observed_features))
        drift_report = evaluate_drift(
            reference_scalars=ref_scalar,
            observed_scalars=obs_scalar,
            reference_embeddings=ref_emb,
            observed_embeddings=obs_emb,
            thresholds=DriftThresholds(
                psi=float(args.psi_threshold),
                embedding_distance=float(args.embedding_distance_threshold),
            ),
            auto_shadow=bool(args.auto_shadow),
        )
        from lrp.bundle import canonical_json
        from lrp.features import write_private

        write_private(
            args.out,
            canonical_json(
                {
                    "schema_version": "lrp.drift.report.v1",
                    **drift_report.metrics(),
                    "reasons": list(drift_report.reasons),
                    "psi_by_feature": drift_report.psi_by_feature,
                }
            ),
        )
        print(
            json.dumps(
                {
                    "above_threshold": drift_report.above_threshold,
                    "recommend_shadow": drift_report.recommend_shadow,
                    "psi_max": drift_report.psi_max,
                    "embedding_centroid_distance": drift_report.embedding_centroid_distance,
                }
            )
        )
    elif args.command == "eval":
        from lrp.bundle import load_bundle
        from lrp.eval import evaluate

        config = read_yaml(args.config)
        report = evaluate(
            load_bundle(args.bundle),
            args.features,
            args.judgments,
            args.responses,
            config,
            out=args.out,
        )
        if not args.synthetic and not report.get("promotion_passed", False):
            return 1
    elif args.command == "validate":
        from lrp.bundle import load_bundle

        load_bundle(
            args.bundle,
            threads=1,
            require_signed=args.require_signed,
            trusted_keys=args.trust,
        )
    elif args.command == "sign-bundle":
        from lrp.bundle import sign_bundle

        envelope = sign_bundle(args.bundle, args.key, args.key_id)
        print(
            json.dumps(
                {
                    "key_id": envelope["key_id"],
                    "manifest_version": envelope["manifest_version"],
                    "manifest_sha256": envelope["manifest_sha256"],
                }
            )
        )
    elif args.command == "verify-bundle":
        from lrp.bundle import load_bundle, verify_bundle_signature

        if args.require_signed:
            loaded = load_bundle(
                args.bundle, threads=1, require_signed=True, trusted_keys=args.trust
            )
            print(json.dumps({"key_id": loaded.signature_key_id, "version": loaded.version}))
        else:
            key_id = verify_bundle_signature(args.bundle, args.trust)
            print(json.dumps({"key_id": key_id}))
    elif args.command == "serve":
        from lrp.serve import serve

        serve(
            bundle=args.bundle,
            config=args.config,
            port=args.port,
            admin_port=args.admin_port,
            enable_admin=args.enable_admin,
            deadline_ms=args.deadline_ms,
            require_signed=args.require_signed,
            trusted_keys=args.trust,
        )
    return 0


def main() -> int:
    from lrp.collect import DataError
    os.umask(0o077)
    args = parser().parse_args()
    try:
        result = execute(args)
    except KeyboardInterrupt:
        return 130
    except DataError as exc:
        print(f"lrp: {exc}", file=sys.stderr)
        return 1
    except Exception:  # noqa: BLE001 - content-safe boundary for third-party failures
        # Do not print exception messages: third-party validation and HTTP errors
        # can contain content, credentials, URLs or paths from protected inputs.
        print(
            "lrp: command_failed; check input schema, configuration and protected evidence",
            file=sys.stderr,
        )
        return 1
    if args.command != "serve":
        print(
            json.dumps({"command": args.command, "status": "ok" if result == 0 else "gates_failed"})
        )
    return result


if __name__ == "__main__":
    raise SystemExit(main())
