#!/usr/bin/env python3
"""Read production image references from a previous release deploy compose file."""

from __future__ import annotations

import argparse
import os
import re
from pathlib import Path


SERVICE_RE = re.compile(r"^  ([A-Za-z0-9_-]+):\s*(?:#.*)?$")
IMAGE_RE = re.compile(r"^\s+image:\s*([^#\s]+)")

LOCAL_TARGETS = {
    "node": "ghcr.io/product-science/inferenced",
    "api": "ghcr.io/product-science/api",
    "proxy": "ghcr.io/product-science/proxy:latest",
    "versiond": "versiond:latest",
}


def parse_service_images(compose_file: Path) -> dict[str, str]:
    images: dict[str, str] = {}
    current_service: str | None = None
    for raw_line in compose_file.read_text().splitlines():
        service_match = SERVICE_RE.match(raw_line)
        if service_match:
            current_service = service_match.group(1)
            continue

        image_match = IMAGE_RE.match(raw_line)
        if image_match and current_service:
            images[current_service] = image_match.group(1).strip()

    return images


def write_outputs(values: dict[str, str]) -> None:
    output_file = os.environ.get("GITHUB_OUTPUT")
    if output_file:
        with open(output_file, "a", encoding="utf-8") as handle:
            for key, value in values.items():
                handle.write(f"{key}={value}\n")

    for key, value in values.items():
        print(f"{key}={value}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--compose", required=True, help="path to previous release deploy/join/docker-compose.yml")
    args = parser.parse_args()

    compose_file = Path(args.compose).resolve()
    images = parse_service_images(compose_file)

    missing = [service for service in ("node", "api") if service not in images]
    if missing:
        raise RuntimeError(f"missing required service image(s) in {compose_file}: {', '.join(missing)}")

    outputs: dict[str, str] = {}
    for service, target in LOCAL_TARGETS.items():
        image = images.get(service, "")
        outputs[f"{service}_image"] = image
        outputs[f"{service}_target_image"] = target

    write_outputs(outputs)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
