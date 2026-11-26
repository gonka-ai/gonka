#!/usr/bin/env python3
"""Read production image references from a previous release deploy compose file."""

from __future__ import annotations

import argparse
import os
import re
from pathlib import Path
from typing import Mapping


SERVICE_RE = re.compile(r"^  ([A-Za-z0-9_-]+):\s*(?:#.*)?$")
# A top-level extension block that defines an anchor, e.g. `x-proxy-policy: &proxy-policy`.
ANCHOR_RE = re.compile(r"^[A-Za-z0-9_.-]+:\s*&([A-Za-z0-9_-]+)\s*(?:#.*)?$")
MERGE_RE = re.compile(r"^\s+<<:\s*\*([A-Za-z0-9_-]+)\s*(?:#.*)?$")
IMAGE_RE = re.compile(r"^(\s+)image:\s*([^#\s]+)")
# `image: >-` and the other YAML block scalar headers put the value on the next lines.
BLOCK_SCALAR_RE = re.compile(r"^[>|][+-]?$")
# Compose interpolation: ${VAR}, ${VAR:-default} and ${VAR-default}.
VARIABLE_RE = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?-)([^}]*))?\}")
REFERENCE_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/:@-]*$")

LOCAL_TARGETS = {
    "node": "ghcr.io/product-science/inferenced",
    "api": "ghcr.io/product-science/api",
    "proxy": "ghcr.io/product-science/proxy:latest",
    "versiond": "versiond:latest",
}

# The services each local image is read from, first match wins. Since v0.2.16 the `proxy`
# service runs proxy-router (HAProxy); the nginx image that local Testermint runs as
# ghcr.io/product-science/proxy:latest is the one the proxy-policy service uses.
SOURCE_SERVICES = {
    "node": ("node",),
    "api": ("api",),
    "proxy": ("proxy-policy", "proxy"),
    "versiond": ("versiond",),
}


def block_scalar_value(lines: list[str], start: int, key_indent: int) -> str:
    parts: list[str] = []
    for line in lines[start:]:
        if not line.strip():
            if parts:
                break
            continue
        if len(line) - len(line.lstrip()) <= key_indent:
            break
        parts.append(line.strip())
    return " ".join(parts)


def parse_service_images(compose_file: Path) -> dict[str, str]:
    images: dict[str, str] = {}
    anchors: dict[str, str] = {}
    merges: dict[str, str] = {}
    current_service: str | None = None
    current_anchor: str | None = None
    lines = compose_file.read_text().splitlines()
    for index, raw_line in enumerate(lines):
        if raw_line[:1] not in ("", " ", "#"):
            anchor_match = ANCHOR_RE.match(raw_line)
            current_anchor = anchor_match.group(1) if anchor_match else None
            current_service = None
            continue

        service_match = SERVICE_RE.match(raw_line)
        if service_match and current_anchor is None:
            current_service = service_match.group(1)
            continue

        merge_match = MERGE_RE.match(raw_line)
        if merge_match and current_service:
            merges[current_service] = merge_match.group(1)
            continue

        image_match = IMAGE_RE.match(raw_line)
        if image_match and (current_service or current_anchor):
            value = image_match.group(2)
            if BLOCK_SCALAR_RE.match(value):
                value = block_scalar_value(lines, index + 1, len(image_match.group(1)))
            if current_service:
                images[current_service] = value.strip()
            else:
                anchors[current_anchor] = value.strip()

    # `<<: *anchor` takes the image from the anchor unless the service sets its own.
    for service, anchor in merges.items():
        if service not in images and anchor in anchors:
            images[service] = anchors[anchor]
    return images


def resolve_image(service: str, value: str, environ: Mapping[str, str] = os.environ) -> str:
    """Apply Compose quoting and interpolation to one image value."""
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "'\"":
        value = value[1:-1]

    def substitute(match: re.Match[str]) -> str:
        name, operator, default = match.group(1), match.group(2), match.group(3)
        current = environ.get(name)
        if operator == ":-" and not current:
            return default
        if operator == "-" and current is None:
            return default
        if current is None:
            raise RuntimeError(f"image of service {service!r} uses ${{{name}}}, which is not set: {value!r}")
        return current

    image = VARIABLE_RE.sub(substitute, value)
    if not REFERENCE_RE.match(image):
        raise RuntimeError(f"cannot read the image of service {service!r}: {value!r} resolves to {image!r}")
    return image


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
    for name, target in LOCAL_TARGETS.items():
        source = next((service for service in SOURCE_SERVICES[name] if service in images), None)
        outputs[f"{name}_image"] = resolve_image(source, images[source]) if source else ""
        outputs[f"{name}_target_image"] = target

    write_outputs(outputs)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
