# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Shared compute-device resolution for LRP train and serve."""

from __future__ import annotations

import logging
import re
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from typing import Any, Literal

LOG = logging.getLogger("lrp.device")

BackendName = Literal["onnxruntime", "sentence-transformers"]
DeviceClass = Literal["cpu", "cuda", "rocm"]

_CUDA_RE = re.compile(r"^cuda:(\d+)$")


@dataclass(frozen=True)
class DeviceDiscovery:
    """Injectable accelerator discovery for unit tests."""

    ort_providers: tuple[str, ...] = ()
    cuda_available: bool = False
    cuda_device_count: int = 0
    hip_is_available: bool = False


@dataclass(frozen=True)
class ResolvedDevice:
    request: str
    device_class: DeviceClass
    device_index: int | None
    backend: BackendName
    ort_providers: tuple[Any, ...]
    torch_device: str
    fallback_to_cpu: bool = False


def discover_devices() -> DeviceDiscovery:
    ort_providers: tuple[str, ...] = ()
    try:
        import onnxruntime as ort

        ort_providers = tuple(ort.get_available_providers())
    except Exception:  # noqa: BLE001 - discovery must not crash train/serve startup
        ort_providers = ()
    cuda_available = False
    cuda_device_count = 0
    hip_is_available = False
    try:
        import torch

        cuda_available = bool(torch.cuda.is_available())
        cuda_device_count = int(torch.cuda.device_count()) if cuda_available else 0
        hip_is_available = bool(getattr(torch.version, "hip", None))
    except Exception:  # noqa: BLE001,S110 - torch is optional; discovery must not crash
        pass
    return DeviceDiscovery(
        ort_providers=ort_providers,
        cuda_available=cuda_available,
        cuda_device_count=cuda_device_count,
        hip_is_available=hip_is_available,
    )


def parse_device_request(device: str) -> tuple[DeviceClass | Literal["auto"], int | None]:
    value = str(device).strip().lower()
    if value == "auto":
        return "auto", None
    if value == "cpu":
        return "cpu", None
    if value == "rocm":
        return "rocm", None
    matched = _CUDA_RE.fullmatch(value)
    if matched:
        return "cuda", int(matched.group(1))
    raise ValueError("invalid compute device; expected auto|cpu|cuda:N|rocm")


def _onnx_cpu_providers() -> tuple[str, ...]:
    return ("CPUExecutionProvider",)


def _onnx_cuda_providers(index: int) -> tuple[Any, ...]:
    return (("CUDAExecutionProvider", {"device_id": index}), "CPUExecutionProvider")


def _onnx_rocm_providers() -> tuple[str, ...]:
    return ("ROCMExecutionProvider", "CPUExecutionProvider")


def _has_ort(discovery: DeviceDiscovery, name: str) -> bool:
    return name in discovery.ort_providers


def _cuda_count(discovery: DeviceDiscovery, backend: BackendName) -> int:
    if backend == "onnxruntime":
        if not _has_ort(discovery, "CUDAExecutionProvider"):
            return 0
        # ORT does not always expose a device count. Prefer torch when present.
        if discovery.cuda_device_count > 0:
            return discovery.cuda_device_count
        return 1 if discovery.cuda_available or _has_ort(discovery, "CUDAExecutionProvider") else 0
    if discovery.cuda_available:
        return max(discovery.cuda_device_count, 0)
    return 0


def _rocm_available(discovery: DeviceDiscovery, backend: BackendName) -> bool:
    if backend == "onnxruntime":
        return _has_ort(discovery, "ROCMExecutionProvider")
    # PyTorch HIP reuses cuda interfaces; never pass literal "rocm" to torch.
    return bool(discovery.hip_is_available and discovery.cuda_available)


def _select_auto(
    discovery: DeviceDiscovery, backend: BackendName
) -> tuple[DeviceClass, int | None]:
    cuda_count = _cuda_count(discovery, backend)
    if cuda_count > 0:
        return "cuda", 0
    if _rocm_available(discovery, backend):
        return "rocm", None
    return "cpu", None


def _torch_device(device_class: DeviceClass, index: int | None) -> str:
    if device_class == "cpu":
        return "cpu"
    if device_class == "cuda":
        return f"cuda:{0 if index is None else index}"
    # ROCm / HIP: torch uses cuda device strings, never "rocm".
    return "cuda:0" if index is None else f"cuda:{index}"


def _ort_providers(device_class: DeviceClass, index: int | None) -> tuple[Any, ...]:
    if device_class == "cpu":
        return _onnx_cpu_providers()
    if device_class == "cuda":
        return _onnx_cuda_providers(0 if index is None else index)
    return _onnx_rocm_providers()


def resolve_compute_device(
    device: str,
    *,
    strict_device: bool = False,
    backend: BackendName,
    discovery: DeviceDiscovery | None = None,
    assert_execution: Callable[[ResolvedDevice], DeviceClass] | None = None,
) -> ResolvedDevice:
    """Resolve operator device to backend-specific execution targets.

    ``assert_execution`` optionally reports the device class that actually ran.
    When an accelerator was requested and supported but execution is CPU, warn
    or refuse according to ``strict_device``.
    """
    if backend not in {"onnxruntime", "sentence-transformers"}:
        raise ValueError("unsupported embedding backend for device resolution")
    found = discovery if discovery is not None else discover_devices()
    requested, index = parse_device_request(device)
    if requested == "auto":
        device_class, index = _select_auto(found, backend)
    else:
        device_class = requested
        if device_class == "cuda":
            count = _cuda_count(found, backend)
            if count <= 0:
                raise ValueError("cuda device requested but no CUDA provider is available")
            assert index is not None
            if index < 0 or index >= count:
                raise ValueError("invalid cuda device index")
        elif device_class == "rocm":
            if not _rocm_available(found, backend):
                raise ValueError("rocm device requested but ROCm provider is unavailable")
        elif device_class != "cpu":
            raise ValueError("unsupported device class")

    resolved = ResolvedDevice(
        request=str(device).strip().lower(),
        device_class=device_class,
        device_index=index,
        backend=backend,
        ort_providers=_ort_providers(device_class, index),
        torch_device=_torch_device(device_class, index),
        fallback_to_cpu=False,
    )
    LOG.info(
        "resolved_compute_device class=%s index=%s backend=%s torch=%s",
        resolved.device_class,
        resolved.device_index,
        resolved.backend,
        resolved.torch_device,
    )
    if assert_execution is None:
        return resolved
    actual = assert_execution(resolved)
    if resolved.device_class != "cpu" and actual == "cpu":
        message = "accelerator execution fell back to cpu"
        if strict_device:
            raise ValueError(message)
        LOG.warning("%s (strict_device=false)", message)
        return ResolvedDevice(
            request=resolved.request,
            device_class=resolved.device_class,
            device_index=resolved.device_index,
            backend=resolved.backend,
            ort_providers=resolved.ort_providers,
            torch_device=resolved.torch_device,
            fallback_to_cpu=True,
        )
    if actual != resolved.device_class and actual != "cpu":
        raise ValueError("resolved execution device class mismatch")
    return resolved


def check_device_class_compatibility(
    *,
    train_device_class: str | None,
    serve_device_class: str,
    strict_device: bool,
) -> None:
    """Warn or refuse when serve device class differs from train."""
    if not train_device_class or train_device_class == serve_device_class:
        return
    message = (
        f"bundle train_device_class={train_device_class} "
        f"differs from serve_device_class={serve_device_class}"
    )
    if strict_device:
        raise ValueError("strict_device refuses train/serve device class mismatch")
    LOG.warning("%s", message)


def primary_ort_provider(providers: Sequence[Any]) -> str:
    if not providers:
        return "CPUExecutionProvider"
    first = providers[0]
    if isinstance(first, (tuple, list)) and first:
        return str(first[0])
    return str(first)


def ort_provider_device_class(providers: Sequence[Any]) -> DeviceClass:
    name = primary_ort_provider(providers)
    if name == "CUDAExecutionProvider":
        return "cuda"
    if name == "ROCMExecutionProvider":
        return "rocm"
    return "cpu"
