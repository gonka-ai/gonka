#!/usr/bin/env python3
"""Resolve the target upgrade and previous canonical release tag."""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path


SEMVER_RE = re.compile(r"^v?(\d+)\.(\d+)\.(\d+)$")
CANONICAL_RELEASE_RE = re.compile(r"^release/v(\d+)\.(\d+)\.(\d+)$")
UPGRADE_NAME_RE = re.compile(r'UpgradeName\s*=\s*"([^"]+)"')


@dataclass(frozen=True, order=True)
class SemVer:
    major: int
    minor: int
    patch: int

    @classmethod
    def parse(cls, value: str) -> "SemVer":
        match = SEMVER_RE.match(value)
        if not match:
            raise ValueError(f"not a semantic version: {value}")
        return cls(*(int(part) for part in match.groups()))

    def with_v(self) -> str:
        return f"v{self.major}.{self.minor}.{self.patch}"

    def without_v(self) -> str:
        return f"{self.major}.{self.minor}.{self.patch}"


def run_git(repo: Path, *args: str) -> str:
    return subprocess.check_output(["git", "-C", str(repo), *args], text=True).strip()


def normalize_upgrade(value: str) -> tuple[str, SemVer]:
    cleaned = value.strip()
    if cleaned.startswith("release/"):
        cleaned = cleaned[len("release/") :]
    version = SemVer.parse(cleaned)
    return version.with_v(), version


def normalize_release(value: str) -> tuple[str, SemVer]:
    cleaned = value.strip()
    if cleaned.startswith("release/"):
        cleaned = cleaned[len("release/") :]
    upgrade, version = normalize_upgrade(cleaned)
    return f"release/{upgrade}", version


def discover_target_upgrade(repo: Path) -> tuple[str, SemVer]:
    candidates: list[tuple[SemVer, str, Path]] = []
    upgrades_dir = repo / "inference-chain" / "app" / "upgrades"
    for constants_file in upgrades_dir.glob("*/constants.go"):
        text = constants_file.read_text()
        match = UPGRADE_NAME_RE.search(text)
        if not match:
            continue
        name = match.group(1)
        try:
            upgrade, version = normalize_upgrade(name)
        except ValueError:
            continue
        candidates.append((version, upgrade, constants_file))

    if not candidates:
        raise RuntimeError(f"no semantic UpgradeName constants found under {upgrades_dir}")

    _, upgrade, source = max(candidates, key=lambda item: item[0])
    print(f"Discovered target upgrade {upgrade} from {source}", file=sys.stderr)
    return normalize_upgrade(upgrade)


def canonical_release_tags(repo: Path) -> list[tuple[SemVer, str]]:
    tags = run_git(repo, "tag", "--list", "release/v*").splitlines()
    releases: list[tuple[SemVer, str]] = []
    for tag in tags:
        match = CANONICAL_RELEASE_RE.match(tag)
        if not match:
            continue
        version = SemVer(*(int(part) for part in match.groups()))
        releases.append((version, tag))
    return sorted(releases)


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
    parser.add_argument("--repo", default=".", help="candidate repository checkout")
    parser.add_argument("--target-upgrade", default="", help="override target upgrade, e.g. v0.2.14")
    parser.add_argument("--previous-release", default="", help="override previous release, e.g. release/v0.2.13")
    args = parser.parse_args()

    repo = Path(args.repo).resolve()
    if args.target_upgrade.strip():
        target_upgrade, target_version = normalize_upgrade(args.target_upgrade)
    else:
        target_upgrade, target_version = discover_target_upgrade(repo)

    if args.previous_release.strip():
        previous_release, previous_version = normalize_release(args.previous_release)
    else:
        candidates = [
            (version, tag)
            for version, tag in canonical_release_tags(repo)
            if version < target_version
        ]
        if not candidates:
            raise RuntimeError(f"no canonical release tag found before {target_upgrade}")
        previous_version, previous_release = candidates[-1]

    if previous_version >= target_version:
        raise RuntimeError(
            f"previous release {previous_release} must be lower than target upgrade {target_upgrade}"
        )

    write_outputs(
        {
            "target_upgrade": target_upgrade,
            "target_version": target_version.without_v(),
            "target_release": f"release/{target_upgrade}",
            "previous_upgrade": previous_version.with_v(),
            "previous_version": previous_version.without_v(),
            "previous_release": previous_release,
        }
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
