# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Device resolver unit tests with controlled discovery (#156 C)."""

from __future__ import annotations

import logging

import pytest
from lrp.device import (
    DeviceDiscovery,
    check_device_class_compatibility,
    ort_provider_device_class,
    parse_device_request,
    resolve_compute_device,
)


def test_parse_device_request_values():
    assert parse_device_request("CPU") == ("cpu", None)
    assert parse_device_request("cuda:1") == ("cuda", 1)
    assert parse_device_request("rocm") == ("rocm", None)
    assert parse_device_request("auto") == ("auto", None)
    with pytest.raises(ValueError, match="invalid compute device"):
        parse_device_request("gpu")


def test_explicit_cpu_onnx():
    discovery = DeviceDiscovery(ort_providers=("CPUExecutionProvider", "CUDAExecutionProvider"))
    resolved = resolve_compute_device(
        "cpu", backend="onnxruntime", discovery=discovery
    )
    assert resolved.device_class == "cpu"
    assert resolved.ort_providers == ("CPUExecutionProvider",)
    assert resolved.torch_device == "cpu"


def test_cuda_index_and_invalid_index():
    discovery = DeviceDiscovery(
        ort_providers=("CUDAExecutionProvider", "CPUExecutionProvider"),
        cuda_available=True,
        cuda_device_count=2,
    )
    resolved = resolve_compute_device(
        "cuda:1", backend="onnxruntime", discovery=discovery
    )
    assert resolved.device_class == "cuda"
    assert resolved.device_index == 1
    assert resolved.ort_providers[0][0] == "CUDAExecutionProvider"
    assert resolved.ort_providers[0][1]["device_id"] == 1
    with pytest.raises(ValueError, match="invalid cuda device index"):
        resolve_compute_device("cuda:9", backend="onnxruntime", discovery=discovery)


def test_auto_order_cuda_then_rocm_then_cpu():
    cuda = DeviceDiscovery(
        ort_providers=("CUDAExecutionProvider", "CPUExecutionProvider", "ROCMExecutionProvider"),
        cuda_available=True,
        cuda_device_count=1,
    )
    assert resolve_compute_device("auto", backend="onnxruntime", discovery=cuda).device_class == "cuda"
    rocm = DeviceDiscovery(ort_providers=("ROCMExecutionProvider", "CPUExecutionProvider"))
    assert resolve_compute_device("auto", backend="onnxruntime", discovery=rocm).device_class == "rocm"
    cpu = DeviceDiscovery(ort_providers=("CPUExecutionProvider",))
    assert resolve_compute_device("auto", backend="onnxruntime", discovery=cpu).device_class == "cpu"


def test_absent_cuda_provider_refuses():
    discovery = DeviceDiscovery(ort_providers=("CPUExecutionProvider",))
    with pytest.raises(ValueError, match="no CUDA provider"):
        resolve_compute_device("cuda:0", backend="onnxruntime", discovery=discovery)


def test_absent_rocm_provider_refuses():
    discovery = DeviceDiscovery(ort_providers=("CPUExecutionProvider",))
    with pytest.raises(ValueError, match="ROCm provider"):
        resolve_compute_device("rocm", backend="onnxruntime", discovery=discovery)


def test_sentence_transformers_rocm_maps_to_torch_cuda():
    discovery = DeviceDiscovery(
        cuda_available=True,
        cuda_device_count=1,
        hip_is_available=True,
    )
    resolved = resolve_compute_device(
        "rocm", backend="sentence-transformers", discovery=discovery
    )
    assert resolved.device_class == "rocm"
    assert resolved.torch_device == "cuda:0"
    assert "rocm" not in resolved.torch_device


def test_unsupported_backend_string():
    with pytest.raises(ValueError, match="unsupported embedding backend"):
        resolve_compute_device("cpu", backend="remote")  # type: ignore[arg-type]


def test_unintended_fallback_warn_vs_strict(caplog):
    discovery = DeviceDiscovery(
        ort_providers=("CUDAExecutionProvider", "CPUExecutionProvider"),
        cuda_available=True,
        cuda_device_count=1,
    )

    def assert_cpu(_resolved):
        return "cpu"

    with caplog.at_level(logging.WARNING, logger="lrp.device"):
        resolved = resolve_compute_device(
            "cuda:0",
            backend="onnxruntime",
            discovery=discovery,
            strict_device=False,
            assert_execution=assert_cpu,
        )
    assert resolved.fallback_to_cpu is True
    assert "fell back to cpu" in caplog.text
    with pytest.raises(ValueError, match="fell back to cpu"):
        resolve_compute_device(
            "cuda:0",
            backend="onnxruntime",
            discovery=discovery,
            strict_device=True,
            assert_execution=assert_cpu,
        )


def test_device_class_mismatch_warn_vs_strict(caplog):
    with caplog.at_level(logging.WARNING, logger="lrp.device"):
        check_device_class_compatibility(
            train_device_class="cuda",
            serve_device_class="cpu",
            strict_device=False,
        )
    assert "train_device_class=cuda" in caplog.text
    with pytest.raises(ValueError, match="strict_device refuses"):
        check_device_class_compatibility(
            train_device_class="cuda",
            serve_device_class="cpu",
            strict_device=True,
        )
    check_device_class_compatibility(
        train_device_class="cpu",
        serve_device_class="cpu",
        strict_device=True,
    )


def test_ort_provider_device_class_helper():
    assert ort_provider_device_class(["CPUExecutionProvider"]) == "cpu"
    assert (
        ort_provider_device_class([("CUDAExecutionProvider", {"device_id": 0})]) == "cuda"
    )
    assert ort_provider_device_class(["ROCMExecutionProvider"]) == "rocm"
