# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Fail closed when deployed vLLM or SGLang image pins match advisories."""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import yaml


ROOT = Path(__file__).resolve().parents[1]
DEPLOY = ROOT / "deploy"
GITHUB_ADVISORIES = "https://api.github.com/advisories"
OSV_QUERY = "https://api.osv.dev/v1/query"
TIMEOUT_SECONDS = 15
MAX_RESPONSE_BYTES = 5 * 1024 * 1024
MAX_ENGINE_IMAGES = 200
MAX_DISTINCT_ENGINE_VERSIONS = 5
MAX_GITHUB_ADVISORY_PAGES = 3
MAX_FINDINGS = 10_000
MAX_RETRY_DELAY_SECONDS = 2.0
VERSION_RE = re.compile(r"^\d+\.\d+\.\d+(?:\.(?:post|dev)\d+)?(?:\+[A-Za-z0-9.]+)?$")
VERSION_IN_TAG_RE = re.compile(r"(?:^|[_-])v?(\d+\.\d+\.\d+(?:\.(?:post|dev)\d+)?)(?:$|[+_-])", re.IGNORECASE)
VLLM_TAG_RE = re.compile(r"(?:^|[_-])vllm[_-]v?(\d+\.\d+\.\d+(?:\.(?:post|dev)\d+)?)(?:$|[+_-])", re.IGNORECASE)
EXCEPTION_RE = re.compile(r"<!--\s*engine-advisory-exception\s+(\{.*?\})\s*-->")


class GateError(RuntimeError):
    """A pin or advisory source could not be checked safely."""


def read_json_response(response: Any) -> Any:
    body = response.read(MAX_RESPONSE_BYTES + 1)
    if len(body) > MAX_RESPONSE_BYTES:
        raise GateError("advisory API response exceeded the size limit")
    return json.loads(body)


@dataclass(frozen=True)
class EngineImage:
    source: Path
    reference: str
    engine: str
    package: str
    version: str


def engine_for(reference: str) -> str | None:
    image_name = reference.split("@", 1)[0].split(":", 1)[0].lower()
    if "vllm" in image_name:
        return "vllm"
    if "sglang" in image_name:
        return "sglang"
    return None


def resolve_variable_default(reference: str) -> str:
    """Resolve a Compose `${NAME:-image}` default without reading the environment."""
    return re.sub(r"\$\{[^}:]+:-([^}]+)\}", lambda match: match.group(1), reference)


def version_from_reference(reference: str) -> str:
    base = reference.split("@", 1)[0]
    if ":" not in base:
        raise GateError(f"engine image has no version tag: {reference}")
    tag = base.rsplit(":", 1)[1]
    match = VLLM_TAG_RE.search(tag)
    if not match:
        match = VERSION_IN_TAG_RE.search(tag)
    if not match:
        raise GateError(f"could not resolve engine version from image: {reference}")
    version = match.group(1)
    if not VERSION_RE.fullmatch(version):
        raise GateError(f"unsupported engine version in image: {reference}")
    return version


def image_values(value: Any) -> list[str]:
    found: list[str] = []
    if isinstance(value, dict):
        for key, child in value.items():
            if key == "image" and isinstance(child, str):
                found.append(child)
            else:
                found.extend(image_values(child))
    elif isinstance(value, list):
        for child in value:
            found.extend(image_values(child))
    return found


def display_path(path: Path) -> str:
    """Render repository paths relatively while supporting isolated test trees."""
    try:
        return path.resolve().relative_to(ROOT.resolve()).as_posix()
    except ValueError:
        return path.as_posix()


def image_identity(reference: str) -> tuple[str, str]:
    """Identify an image by repository and parsed version, ignoring digest suffixes."""
    tagged = reference.split("@", 1)[0]
    if ":" not in tagged:
        raise GateError(f"engine image has no version tag: {reference}")
    repository = tagged.rsplit(":", 1)[0].lower()
    return repository, version_from_reference(reference)


def enumerate_images(deploy_root: Path = DEPLOY) -> list[EngineImage]:
    images: dict[tuple[Path, str], EngineImage] = {}
    for path in sorted(deploy_root.rglob("*")):
        is_yaml = path.suffix.lower() in {".yaml", ".yml"}
        if not path.is_file() or (not is_yaml and path.name != "images.lock"):
            continue
        try:
            content = path.read_text(encoding="utf-8")
            # Parse the deploy bundle's template YAML without expanding its placeholders.
            content = re.sub(r"@@[A-Z0-9_]+@@", "template-value", content)
            documents = list(yaml.safe_load_all(content))
        except (OSError, yaml.YAMLError) as exc:
            raise GateError(f"cannot parse deployment YAML: {display_path(path)}") from exc
        for document in documents:
            for raw_reference in image_values(document):
                reference = resolve_variable_default(raw_reference)
                engine = engine_for(reference)
                if engine is None:
                    if engine_for(raw_reference):
                        raise GateError(f"cannot resolve engine image variable in {display_path(path)}")
                    continue
                image = EngineImage(path, reference, engine, engine, version_from_reference(reference))
                images[(path, reference)] = image
                if len(images) > MAX_ENGINE_IMAGES:
                    raise GateError("deployment contains too many engine image references")
    return sorted(images.values(), key=lambda item: (item.source.as_posix(), item.reference))


def request_json(url: str, payload: dict[str, Any] | None = None) -> Any:
    data = None if payload is None else json.dumps(payload).encode("utf-8")
    headers = {"Accept": "application/vnd.github+json"} if payload is None else {"Content-Type": "application/json"}
    headers["User-Agent"] = "router-engine-advisory-gate"
    request = urllib.request.Request(url, data=data, headers=headers, method="GET" if data is None else "POST")
    try:
        with urllib.request.urlopen(request, timeout=TIMEOUT_SECONDS) as response:
            if response.status != 200:
                raise GateError("advisory API returned a non-success response")
            return read_json_response(response)
    except urllib.error.HTTPError as exc:
        raise GateError(f"advisory API returned HTTP {exc.code}") from exc
    except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
        raise GateError("advisory API request failed or timed out") from exc


def github_advisories(package: str, version: str) -> list[dict[str, Any]]:
    query = urllib.parse.urlencode({"ecosystem": "pip", "affects": f"{package}@{version}", "per_page": 100})
    url = f"{GITHUB_ADVISORIES}?{query}"
    records: list[dict[str, Any]] = []
    page_count = 0
    while url:
        if page_count >= MAX_GITHUB_ADVISORY_PAGES:
            raise GateError("GitHub advisory pagination exceeded the page limit")
        page_count += 1
        parsed_url = urllib.parse.urlparse(url)
        if parsed_url.scheme != "https" or parsed_url.hostname != "api.github.com":
            raise GateError("GitHub advisory pagination returned an untrusted host")
        headers = {
            "Accept": "application/vnd.github+json",
            "User-Agent": "router-engine-advisory-gate",
        }
        token = os.environ.get("GITHUB_TOKEN", "")
        if token:
            headers["Authorization"] = f"Bearer {token}"
        request = urllib.request.Request(url, headers=headers)
        for attempt in range(2):
            try:
                with urllib.request.urlopen(request, timeout=TIMEOUT_SECONDS) as response:
                    page = read_json_response(response)
                    if response.status != 200 or not isinstance(page, list):
                        raise GateError("GitHub advisory API returned an invalid response")
                    records.extend(page)
                    link = response.headers.get("Link", "")
                break
            except urllib.error.HTTPError as exc:
                rate_limited = exc.code == 403 and exc.headers.get("X-RateLimit-Remaining") == "0"
                retryable = exc.code in {429, 500, 502, 503, 504} or rate_limited
                if attempt == 0 and retryable:
                    try:
                        delay = min(float(exc.headers.get("Retry-After", "0.25")), MAX_RETRY_DELAY_SECONDS)
                    except ValueError:
                        delay = 0.25
                    time.sleep(max(0.0, delay))
                    continue
                raise GateError(f"GitHub advisory API returned HTTP {exc.code}") from exc
            except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
                if attempt == 0:
                    time.sleep(0.25)
                    continue
                raise GateError("GitHub advisory API request failed or timed out") from exc
        next_url = re.search(r'<([^>]+)>;\s*rel="next"', link)
        url = next_url.group(1) if next_url else ""
    return [item for item in records if not item.get("withdrawn_at")]


def osv_advisories(package: str, version: str) -> list[dict[str, Any]]:
    result = request_json(OSV_QUERY, {"package": {"name": package, "ecosystem": "PyPI"}, "version": version})
    if not isinstance(result, dict) or not isinstance(result.get("vulns", []), list):
        raise GateError("OSV returned an invalid response")
    return [item for item in result.get("vulns", []) if not item.get("withdrawn")]


def nearest_readme(path: Path, deploy_root: Path = DEPLOY) -> Path | None:
    current = path.parent
    while current == deploy_root or deploy_root in current.parents:
        candidate = current / "README.md"
        if candidate.is_file():
            return candidate
        if current == deploy_root:
            break
        current = current.parent
    return None


def exceptions_in(readme: Path | None) -> dict[tuple[str, str, str], str]:
    if readme is None:
        return {}
    exceptions: dict[tuple[str, str, str], str] = {}
    readme_path = display_path(readme)
    for match in EXCEPTION_RE.finditer(readme.read_text(encoding="utf-8")):
        try:
            item = json.loads(match.group(1))
        except json.JSONDecodeError as exc:
            raise GateError(f"malformed advisory exception in {readme_path}") from exc
        if not isinstance(item, dict):
            raise GateError(f"malformed advisory exception in {readme_path}")
        advisory = item.get("advisory", "")
        image = item.get("image", "")
        reason = item.get("reason", "")
        if not all(isinstance(value, str) for value in (advisory, image, reason)):
            raise GateError(f"incomplete advisory exception in {readme_path}")
        reason = reason.strip()
        if not advisory or not image or len(reason) < 20:
            raise GateError(f"incomplete advisory exception in {readme_path}")
        exceptions[(advisory, *image_identity(image))] = reason
    return exceptions


def advisory_id(item: dict[str, Any]) -> str:
    aliases = item.get("aliases", [])
    if not isinstance(aliases, list):
        raise GateError("advisory source returned invalid aliases")
    for alias in aliases:
        if isinstance(alias, str) and alias.startswith("GHSA-"):
            return alias
    return str(item.get("ghsa_id") or item.get("id") or "")


def run_gate(images: list[EngineImage], deploy_root: Path = DEPLOY) -> dict[str, Any]:
    if len(images) > MAX_ENGINE_IMAGES:
        raise GateError("deployment contains too many engine image references")
    distinct_versions = {(image.package, image.version) for image in images}
    if len(distinct_versions) > MAX_DISTINCT_ENGINE_VERSIONS:
        raise GateError("deployment contains too many distinct engine versions")
    findings: list[dict[str, Any]] = []
    cache: dict[tuple[str, str], tuple[list[dict[str, Any]], list[dict[str, Any]]]] = {}
    for image in images:
        key = (image.package, image.version)
        if key not in cache:
            cache[key] = (github_advisories(*key), osv_advisories(*key))
        github, osv = cache[key]
        exceptions = exceptions_in(nearest_readme(image.source, deploy_root=deploy_root))
        records: dict[str, set[str]] = {}
        for source, advisories in (("GitHub", github), ("OSV", osv)):
            for advisory in advisories:
                identifier = advisory_id(advisory)
                if not identifier:
                    raise GateError("advisory source returned a record without an identifier")
                records.setdefault(identifier, set()).add(source)
        for identifier, sources in sorted(records.items()):
            findings.append({
                "source": display_path(image.source),
                "image": image.reference,
                "engine": image.engine,
                "version": image.version,
                "advisory": identifier,
                "sources": sorted(sources),
                "exception": exceptions.get((identifier, *image_identity(image.reference))),
            })
            if len(findings) > MAX_FINDINGS:
                raise GateError("advisory results exceeded the finding limit")
    failures = [finding for finding in findings if finding["exception"] is None]
    return {
        "schema": "metrum.ai/engine-advisory-gate/v1",
        "checked_images": len(images),
        "advisories": len(findings),
        "unexcepted": len(failures),
        "findings": findings,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="check deploy images and return nonzero on unexcepted advisories")
    args = parser.parse_args()
    try:
        result = run_gate(enumerate_images(DEPLOY), deploy_root=DEPLOY)
    except GateError as exc:
        print(f"engine advisory gate failed closed: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(result, indent=2, sort_keys=True))
    return 1 if args.check and result["unexcepted"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
