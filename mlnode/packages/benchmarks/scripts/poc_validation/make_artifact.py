#!/usr/bin/env python3
"""
Bake a PoC validation artifact from a trusted, locally deployed MLNode.

Run this once against a known-honest MLNode + vLLM PoC v2 deployment to
collect a fixed set of nonces and persist them as an artifact JSON
consumable by `validate.py`. The artifact then becomes the golden
reference that `validate.py` checks other deployments against.

The script does not deploy a model. The MLNode must already serve
`--model` (caller's responsibility).

It pulls vectors synchronously via
    POST /api/v1/inference/pow/generate  (wait=true, no validation block)
which returns the computed `artifacts` list inline. No callback HTTP
server, no SSH tunnel, no inbound port required.

Usage:
  python3 make_artifact.py \\
      --mlnode-url http://127.0.0.1:8080 \\
      --model Qwen/Qwen3-0.6B \\
      --seq-len 1024 --k-dim 12 \\
      --num-nonces 32 --batch-size 32 \\
      --out artifacts/qwen-qwen3-0.6b.json

With --scheme decode it records each nonce's point trajectory instead of a
vector (seq_len 256 and max_tokens 256 unless given). MLNode waits 60 s for
its vLLM backend per request, so decode nonces are requested in pieces of
--request-nonces (default 200, the sample the chain validates) and the
server picks the decode batch itself.
"""

from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime
from pathlib import Path

import requests

API = "/api/v1"


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--mlnode-url", required=True, help="MLNode base URL (required)")
    p.add_argument("--model", required=True, help="Model id the MLNode is currently serving (required)")
    p.add_argument("--scheme", choices=("prefill", "decode"), default="prefill")
    p.add_argument("--seq-len", type=int, default=None, help="Default: 1024 for prefill, 256 for decode")
    p.add_argument("--max-tokens", type=int, default=256, help="Decode steps per nonce (decode only)")
    p.add_argument("--k-dim", type=int, default=12)
    p.add_argument("--block-hash", default="TEST_BLOCK")
    p.add_argument("--block-height", type=int, default=100)
    p.add_argument("--public-key", default="test_pub_keys")
    p.add_argument("--node-id", type=int, default=0)
    p.add_argument("--node-count", type=int, default=1)
    p.add_argument("--num-nonces", type=int, default=32, help="How many leading nonces (0..N-1) to bake")
    p.add_argument("--batch-size", type=int, default=32,
                   help="batch_size sent to /pow/generate (default 32; prefill only)")
    p.add_argument("--request-nonces", type=int, default=None,
                   help="Nonces per /pow/generate request (default: all for prefill, 200 for decode)")
    p.add_argument("--timeout", type=float, default=900.0,
                   help="HTTP timeout for /pow/generate in seconds")
    p.add_argument("--dist-threshold", type=float, default=None,
                   help="Threshold to record in the artifact: the L2 dist threshold for prefill (default 0.2), "
                        "the claimed-margin tau for decode (default 0.05)")
    p.add_argument("--source", default="locally generated via make_artifact.py",
                   help="Provenance string written to the artifact")
    p.add_argument("--additional-args", default=None,
                   help="JSON list of vLLM additional_args to record in the artifact "
                        "(does not affect this run; the model must already be deployed)")
    p.add_argument("--out", required=True, help="Output artifact path")
    args = p.parse_args()

    decode = args.scheme == "decode"
    seq_len = args.seq_len if args.seq_len is not None else (256 if decode else 1024)
    dist_threshold = args.dist_threshold if args.dist_threshold is not None else (0.05 if decode else 0.2)
    base = args.mlnode_url.rstrip("/")
    out = Path(args.out).resolve()
    out.parent.mkdir(parents=True, exist_ok=True)

    # 1. Sanity check: vLLM must already be serving the requested model.
    r = requests.get(f"{base}{API}/inference/up/status", timeout=10)
    r.raise_for_status()
    if not r.json().get("is_running"):
        sys.stderr.write(f"ERROR: vLLM is not running at {base}; deploy the model first\n")
        return 1

    nonces = list(range(args.num_nonces))
    params = {"model": args.model, "seq_len": seq_len, "k_dim": args.k_dim}
    if decode:
        params.update({"decode": True, "max_tokens": args.max_tokens})
    step = args.request_nonces or (200 if decode else len(nonces))

    print(f"MLNode:      {base}")
    print(f"Model:       {args.model}    scheme: {args.scheme}")
    print(f"Nonces:      {len(nonces)} (0..{len(nonces) - 1}), {step} per request")
    print(f"seq_len:     {seq_len}    k_dim: {args.k_dim}    "
          + (f"max_tokens: {args.max_tokens}" if decode else f"batch_size: {args.batch_size}"))
    print("Sending POST /inference/pow/generate (wait=true, no validation)...")

    by_nonce = {}
    body = {}
    for i in range(0, len(nonces), step):
        payload = {
            "block_hash": args.block_hash,
            "block_height": args.block_height,
            "public_key": args.public_key,
            "node_id": args.node_id,
            "node_count": args.node_count,
            "nonces": nonces[i:i + step],
            "params": params,
            "wait": True,
        }
        if not decode:
            payload["batch_size"] = args.batch_size
        r = requests.post(f"{base}{API}/inference/pow/generate", json=payload, timeout=args.timeout)
        if r.status_code != 200:
            sys.stderr.write(f"ERROR: /inference/pow/generate failed: {r.status_code} {r.text}\n")
            return 1
        body = r.json()
        for a in body.get("artifacts") or []:
            if decode and a.get("k_points_steps"):
                points = a["k_points_steps"]
                if len(points) != args.max_tokens + 1 or not all(0 <= k < 16 for k in points):
                    sys.stderr.write(f"ERROR: nonce {a.get('nonce')}: trajectory not {args.max_tokens + 1} "
                                     f"points in 0..15 (a non-finite step?)\n")
                    return 1
                by_nonce[int(a["nonce"])] = "".join("%x" % k for k in points)
            elif not decode and "vector_b64" in a:
                by_nonce[int(a["nonce"])] = a["vector_b64"]
        print(f"  {min(i + step, len(nonces))}/{len(nonces)}")
    missing = [n for n in nonces if n not in by_nonce]
    if missing:
        sys.stderr.write(
            f"ERROR: server returned {len(by_nonce)}/{len(nonces)} artifacts; "
            f"missing nonces: {missing[:20]}{' ...' if len(missing) > 20 else ''}\n"
        )
        return 1

    key = "k_points" if decode else "vector_b64"
    collected = [{"nonce": n, key: by_nonce[n]} for n in nonces]

    additional_args = []
    if args.additional_args:
        try:
            additional_args = json.loads(args.additional_args)
            if not isinstance(additional_args, list) or not all(isinstance(x, str) for x in additional_args):
                raise ValueError("must be a JSON list of strings")
        except Exception as e:
            sys.stderr.write(f"ERROR: --additional-args is not a valid JSON list of strings: {e}\n")
            return 1

    artifact = {
        "model": args.model,
        **({"scheme": "decode", "max_tokens": args.max_tokens} if decode else {}),
        "seq_len": seq_len,
        "k_dim": args.k_dim,
        "block_hash": args.block_hash,
        "block_height": args.block_height,
        "public_key": args.public_key,
        "node_id": args.node_id,
        "node_count": args.node_count,
        "dist_threshold": dist_threshold,
        "additional_args": additional_args,
        "source": args.source,
        "generated_at": datetime.now().isoformat(),
        "encoding": ({"k_points": "one hex digit per step: the codebook index 0-15 of the point chosen at the "
                                  "prefill step, then at each decode step"} if decode else
                     body.get("encoding") or {"dtype": "f16", "k_dim": args.k_dim, "endian": "le"}),
        "artifacts": collected,
    }
    out.write_text(json.dumps(artifact, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"wrote {out} ({len(collected)} nonces)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
