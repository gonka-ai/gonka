#!/usr/bin/env python3
"""
Simple Python version of test-utils-gen-authz.sh

Builds app_state.authz.authorization grants JSON from:
- Address pair files in a directory:
  - address-0.txt       (line1 = granter / cold)
  - address-0-warm.txt  (line1 = grantee / warm)
  - address-1.txt, address-1-warm.txt, ...
- Message types file (one per line): full type URLs like "/pkg.path.v1.MsgFoo"
  or bare names like "MsgFoo" when --namespace is provided.

Outputs JSON to authz_grants.json by default.
"""

from __future__ import annotations

import argparse
import json
import re
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Iterable, List, Optional, Tuple


DEFAULT_OUT = "authz_grants.json"
DEFAULT_DAYS = 365

# Naming convention (kept same as the bash script)
COLD_PREFIX = "address-"
COLD_SUFFIX = ".txt"
WARM_SUFFIX = "-warm.txt"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Build authz grants JSON from address pairs and message types. "
            "Pairs are read from files 'address-N.txt' (granter) and "
            "'address-N-warm.txt' (grantee)."
        )
    )
    parser.add_argument("--dir", "-d", dest="pairs_dir", required=True, help="Directory with address-N.txt and address-N-warm.txt files")
    parser.add_argument("--msgs", "-m", dest="msgs_file", required=True, help="File with message types (one per line)")
    parser.add_argument("--out", "-o", dest="out_file", default=DEFAULT_OUT, help=f"Output JSON (default: {DEFAULT_OUT})")
    parser.add_argument("--namespace", dest="namespace", default="", help="Protobuf package for bare names (e.g., inferenced.inference.v1)")
    parser.add_argument("--days", dest="days", type=int, default=DEFAULT_DAYS, help=f"Expiration offset in days (default: {DEFAULT_DAYS})")
    parser.add_argument("--expiration", "-e", dest="explicit_exp", default="", help="RFC3339 timestamp (e.g., 2026-08-08T00:00:00Z). Overrides --days.")
    return parser.parse_args()


def normalize_expiration(explicit_exp: str, days: int) -> str:
    """Return an RFC3339 UTC timestamp with 'Z'. Fallback mirrors bash behavior."""
    if explicit_exp:
        # If only YYYY-MM-DD, append T00:00:00Z
        if re.fullmatch(r"\d{4}-\d{2}-\d{2}", explicit_exp):
            return f"{explicit_exp}T00:00:00Z"

        # Try to parse common RFC3339/ISO-8601 variants and convert to Z
        iso_candidate = explicit_exp.replace(" ", "T")
        try:
            # Handle cases with or without timezone; default to UTC
            dt = None
            # Try with 'Z'
            try:
                if iso_candidate.endswith("Z"):
                    dt = datetime.fromisoformat(iso_candidate.replace("Z", "+00:00"))
            except Exception:
                dt = None
            if dt is None:
                dt = datetime.fromisoformat(iso_candidate)
            if dt.tzinfo is None:
                dt = dt.replace(tzinfo=timezone.utc)
            return dt.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        except Exception:
            # Keep as-is if we cannot normalize; match bash script warning-less fallback
            return explicit_exp

    # Default: now + days in UTC
    try:
        exp_dt = datetime.now(tz=timezone.utc) + timedelta(days=days)
        return exp_dt.strftime("%Y-%m-%dT%H:%M:%SZ")
    except Exception:
        # Very unlikely in Python; provide a distant fallback
        return "2099-12-31T00:00:00Z"


def read_nonempty_noncomment_first_line(path: Path) -> str:
    with path.open("r", encoding="utf-8") as f:
        for raw in f:
            line = raw.strip()
            if not line or line.lstrip().startswith("#"):
                continue
            return line
    return ""


def load_messages(msgs_file: Path, namespace: str) -> List[str]:
    msgs: List[str] = []
    with msgs_file.open("r", encoding="utf-8") as f:
        for raw in f:
            s = raw.strip()
            if not s or s.startswith("#"):
                continue
            if s.startswith("/"):
                msgs.append(s)
            else:
                if not namespace:
                    raise SystemExit(f"ERROR: '{s}' is not a type URL and --namespace not provided")
                msgs.append(f"/{namespace}.{s}")
    if not msgs:
        raise SystemExit("ERROR: no messages found")
    return msgs


def discover_cold_files(pairs_dir: Path) -> List[Path]:
    files: List[Path] = []
    for p in sorted(pairs_dir.glob(f"{COLD_PREFIX}*{COLD_SUFFIX}")):
        # Skip warm files which also match *.txt
        if p.name.endswith(WARM_SUFFIX):
            continue
        files.append(p)
    if not files:
        raise SystemExit(f"ERROR: no '{COLD_PREFIX}N{COLD_SUFFIX}' files in '{pairs_dir}'")
    return files


def extract_index_from_cold(cold_file: Path) -> str:
    # basename: address-12.txt -> index: 12
    name = cold_file.name
    if not (name.startswith(COLD_PREFIX) and name.endswith(COLD_SUFFIX)):
        return ""
    core = name[len(COLD_PREFIX) : -len(COLD_SUFFIX)]
    return core


def build_grants(
    pairs_dir: Path,
    messages: List[str],
    expiration: str,
) -> List[dict]:
    grants: List[dict] = []
    for cold in discover_cold_files(pairs_dir):
        idx = extract_index_from_cold(cold)
        warm = pairs_dir / f"{COLD_PREFIX}{idx}{WARM_SUFFIX}"
        if not warm.exists():
            print(f"WARN: missing warm file for index '{idx}' (expected '{warm}') — skipping")
            continue

        granter = read_nonempty_noncomment_first_line(cold).strip()
        grantee = read_nonempty_noncomment_first_line(warm).strip()
        if not granter or not grantee:
            print(f"WARN: empty granter/grantee in index '{idx}' — skipping")
            continue

        for msg in messages:
            grants.append(
                {
                    "granter": granter,
                    "grantee": grantee,
                    "authorization": {
                        "@type": "/cosmos.authz.v1beta1.GenericAuthorization",
                        "msg": msg,
                    },
                    "expiration": expiration,
                }
            )
    return dedupe_grants(grants)


def dedupe_grants(grants: List[dict]) -> List[dict]:
    seen: set[Tuple[str, str, str]] = set()
    unique: List[dict] = []
    for g in grants:
        key = (g.get("granter", ""), g.get("grantee", ""), g.get("authorization", {}).get("msg", ""))
        if key in seen:
            continue
        seen.add(key)
        unique.append(g)
    return unique


def write_output(grants: List[dict], out_file: Path) -> None:
    payload = {"app_state": {"authz": {"authorization": grants}}}
    with out_file.open("w", encoding="utf-8") as f:
        json.dump(payload, f, indent=2, ensure_ascii=False)
        f.write("\n")
    print(f"Wrote {out_file}")


def main() -> None:
    args = parse_args()
    pairs_dir = Path(args.pairs_dir)
    msgs_file = Path(args.msgs_file)
    out_file = Path(args.out_file)

    if not pairs_dir.is_dir():
        raise SystemExit(f"Error: --dir '{pairs_dir}' is not a directory")
    if not msgs_file.is_file():
        raise SystemExit(f"Error: --msgs '{msgs_file}' not found")

    expiration = normalize_expiration(args.explicit_exp, args.days)
    messages = load_messages(msgs_file, args.namespace)
    grants = build_grants(pairs_dir, messages, expiration)
    write_output(grants, out_file)


if __name__ == "__main__":
    main()


