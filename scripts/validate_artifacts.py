#!/usr/bin/env python3
"""Validate the release inventory against AmongClusters build sources."""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
CHARTS = (
    "among-clusters",
    "among-clusters-hub",
    "among-clusters-agent",
    "among-clusters-headlamp",
)


def chart_version(name: str) -> tuple[str, str]:
    text = (ROOT / "charts" / name / "Chart.yaml").read_text()
    version = re.search(r"^version:\s*([^\s]+)\s*$", text, re.MULTILINE)
    app_version = re.search(r'^appVersion:\s*["\']?([^\s"\']+)', text, re.MULTILINE)
    if not version or not app_version:
        raise ValueError(f"{name}: Chart.yaml must declare version and appVersion")
    return version.group(1), app_version.group(1)


def validate(requested_version: str | None = None) -> dict[str, object]:
    inventory = json.loads((ROOT / "artifacts.json").read_text())
    if inventory.get("schemaVersion") != "among-clusters.dev/artifacts/v1":
        raise ValueError("unsupported artifacts.json schemaVersion")

    versions = {value for chart in CHARTS for value in chart_version(chart)}
    if len(versions) != 1:
        raise ValueError(f"release versions disagree: {sorted(versions)}")
    version = versions.pop()
    if requested_version and requested_version.removeprefix("v") != version:
        raise ValueError(
            f"release version {requested_version!r} does not match {version!r}"
        )

    artifacts = inventory.get("artifacts")
    if not isinstance(artifacts, list) or not artifacts:
        raise ValueError("artifacts.json must register at least one artifact")
    ids: set[str] = set()
    chart_sources: set[str] = set()
    for artifact in artifacts:
        artifact_id = artifact.get("id")
        if not artifact_id or artifact_id in ids:
            raise ValueError(f"artifact id is missing or duplicated: {artifact_id!r}")
        ids.add(artifact_id)
        source = artifact.get("source")
        if not source or not (ROOT / source).is_file():
            raise ValueError(f"{artifact_id}: source does not exist: {source!r}")
        if artifact.get("kind") == "helm-oci-chart":
            chart_sources.add(source)
        if artifact.get("kind") == "github-release-assets":
            files = set(artifact.get("files", []))
            payloads = {
                name for name in files
                if not name.endswith((".sha256", ".bundle.json"))
            }
            for payload in payloads:
                for suffix in (".sha256", ".bundle.json"):
                    if payload + suffix not in files:
                        raise ValueError(
                            f"{artifact_id}: {payload} is missing {suffix} evidence"
                        )
        if "{version}" not in artifact.get("reference", ""):
            raise ValueError(f"{artifact_id}: reference must contain {{version}}")
        if not artifact.get("signed"):
            raise ValueError(f"{artifact_id}: signing method is not declared")

    expected_chart_sources = {f"charts/{name}/Chart.yaml" for name in CHARTS}
    if chart_sources != expected_chart_sources:
        raise ValueError(
            f"registered chart sources differ: {sorted(chart_sources)} != "
            f"{sorted(expected_chart_sources)}"
        )
    required = {"runtime-image", "github-release-assets"}
    if not required.issubset(ids):
        raise ValueError(f"missing required artifacts: {sorted(required - ids)}")

    for path in inventory.get("security", {}).get("automation", []):
        if not (ROOT / path).is_file():
            raise ValueError(f"security automation does not exist: {path}")

    workflow = (ROOT / inventory["release"]["workflow"]).read_text()
    for marker in ("ghcr.io/re8ch/among-clusters", "Package and publish four OCI charts", "cosign sign"):
        if marker not in workflow:
            raise ValueError(f"release workflow does not publish registered artifact: {marker}")

    return {"version": version, "artifacts": sorted(ids), "status": "valid"}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--release-version")
    args = parser.parse_args()
    try:
        print(json.dumps(validate(args.release_version), indent=2))
    except (KeyError, TypeError, ValueError, json.JSONDecodeError) as exc:
        print(f"artifact inventory validation failed: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
