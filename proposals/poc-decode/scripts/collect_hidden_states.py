"""
Usage examples
--------------
# Auto-generate 4 random block hashes, nonces 0-255, batches of 8
python collect_hidden_states.py \\
    --url http://localhost:8000 \\
    --num-hashes 4 \\
    --nonces 0:256 \\
    --output hidden_states.npz

# With decode steps
python collect_hidden_states.py \\
    --url http://localhost:8000 \\
    --num-hashes 2 \\
    --nonces 0:128 \\
    --max-tokens 8 \\
    --output hidden_states.npz

# Explicit hashes, nonces 0-99
python collect_hidden_states.py \\
    --url http://localhost:8000 \\
    --block-hashes abc123 def456 \\
    --nonces 0:100 \\
    --output hidden_states.npz

# Explicit nonce list
python collect_hidden_states.py \\
    --url http://localhost:8000 \\
    --num-hashes 2 \\
    --nonces 0 5 10 42 99 \\
    --output hidden_states.npz
"""

import argparse
import base64
import hashlib
import os
import sys
from typing import List, Optional

import numpy as np
import requests


def generate_block_hashes(n: int) -> List[str]:
    """Generate n random 32-byte hex block hashes (SHA-256 of random bytes)."""
    return [hashlib.sha256(os.urandom(32)).hexdigest() for _ in range(n)]


def decode_vector(b64: str) -> np.ndarray:
    """Decode base64 FP16 little-endian → float32 numpy array."""
    return np.frombuffer(base64.b64decode(b64), dtype="<f2").astype(np.float32)


def parse_nonces(nonce_args: List[str]) -> List[int]:
    """Parse nonce specs: plain ints, 'start:end', or 'start:end:step'."""
    nonces = []
    for token in nonce_args:
        if ":" in token:
            parts = [int(p) for p in token.split(":")]
            if len(parts) == 2:
                nonces.extend(range(parts[0], parts[1]))
            elif len(parts) == 3:
                nonces.extend(range(parts[0], parts[1], parts[2]))
            else:
                raise ValueError(f"Invalid nonce range token: {token!r}")
        else:
            nonces.append(int(token))
    return nonces


def send_batch(
    url: str,
    block_hash: str,
    public_key: str,
    nonces: List[int],
    model: str,
    seq_len: int,
    max_tokens: int,
    timeout: int,
) -> dict:
    endpoint = f"{url.rstrip('/')}/api/v1/pow/generate"
    payload = {
        "block_hash": block_hash,
        "block_height": 100,
        "public_key": public_key,
        "node_id": 0,
        "node_count": 1,
        "nonces": nonces,
        "params": {
            "model": model,
            "seq_len": seq_len,
            "k_dim": 12,
            "max_tokens": max_tokens
        },
        "batch_size":   len(nonces),
        "debug": True,
        "wait": True,
    }
    resp = requests.post(endpoint, json=payload, timeout=timeout)
    resp.raise_for_status()
    return resp.json()


def save_npz(
    path: str,
    block_hashes: List[str],
    nonces: List[int],
    hidden_states: List[np.ndarray],
    reduced_hidden_states: List[np.ndarray],
    sphere_k_steps: List[np.ndarray],
    reduced_hidden_states_decode: Optional[List[np.ndarray]],
) -> None:
    """Write accumulated data to a .npz file (overwrites if exists)."""
    arrays = dict(
        block_hashes=np.array(block_hashes, dtype=object),
        nonces=np.array(nonces, dtype=np.int64),
        hidden_states=np.stack(hidden_states).astype(np.float32),
        reduced_hidden_states=np.stack(reduced_hidden_states).astype(np.float32),
        sphere_k_steps=np.stack(sphere_k_steps).astype(np.int32),
    )
    if reduced_hidden_states_decode is not None:
        arrays["reduced_hidden_states_decode"] = (
            np.stack(reduced_hidden_states_decode).astype(np.float32)
        )
    np.savez(path, **arrays)


def get_server_model(base_url: str, timeout: int = 10) -> str:
    """Fetch the first model name from /v1/models."""
    resp = requests.get(f"{base_url}/v1/models", timeout=timeout)
    resp.raise_for_status()
    data = resp.json()
    return data["data"][0]["id"]


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Collect hidden states from a vLLM PoC server into a .npz archive.",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument("--url",        required=True,
                        help="Server base URL, e.g. http://localhost:8001")
    parser.add_argument("--public-key", default="default_public_key",
                        help="Public key string (default: 'default_public_key')")

    hash_group = parser.add_mutually_exclusive_group(required=True)
    hash_group.add_argument("--num-hashes", type=int, metavar="N",
                            help="Generate N random SHA-256 block hashes automatically")
    hash_group.add_argument("--block-hashes", nargs="+", metavar="HASH",
                            help="Explicit block hash(es) to iterate over")
    parser.add_argument("--nonces",     nargs="+", required=True, metavar="SPEC",
                        help='Nonces: plain ints ("0 1 2"), range ("0:100"), step ("0:100:5")')
    parser.add_argument("--batch-size", type=int, default=8,
                        help="Nonces per HTTP request (default: 8)")
    parser.add_argument("--seq-len",    type=int, default=256,
                        help="Sequence length (default: 256)")
    parser.add_argument("--max-tokens", type=int, default=0,
                        help="Decode steps after prefill (default: 0 = prefill-only)")
    parser.add_argument("--output",     default="hidden_states.npz",
                        help="Output .npz file (default: hidden_states.npz)")
    args = parser.parse_args()

    if args.num_hashes is not None:
        block_hashes = generate_block_hashes(args.num_hashes)
        print(f"Generated {args.num_hashes} random block hashes:")
        for h in block_hashes:
            print(f"  {h}")
    else:
        block_hashes = args.block_hashes

    nonces = parse_nonces(args.nonces)
    if not nonces:
        print("No nonces specified – nothing to do.", file=sys.stderr)
        sys.exit(1)

    batches = [nonces[i:i + args.batch_size] for i in range(0, len(nonces), args.batch_size)]
    total_requests = len(block_hashes) * len(batches)
    save_decode = args.max_tokens > 0

    print(f"Block hashes      : {len(block_hashes)}")
    print(f"Nonces            : {len(nonces)}")
    print(f"Max decode tokens : {args.max_tokens}")
    print(f"Total requests    : {total_requests}")
    print(f"Output            : {args.output}")

    acc_block_hashes:                List[str]        = []
    acc_nonces:                      List[int]         = []
    acc_hidden_states:               List[np.ndarray]  = []
    acc_reduced_hidden_states:       List[np.ndarray]  = []
    acc_sphere_k_steps:              List[np.ndarray]  = []
    acc_reduced_hidden_states_decode: List[np.ndarray] = []

    n_written = 0
    n_errors  = 0

    model = get_server_model(args.url)
    
    for block_hash in block_hashes:
        print(f"\n── block_hash={block_hash} ──")
        for batch_idx, batch in enumerate(batches):
            tag = f"  batch {batch_idx+1}/{len(batches)} nonces={batch}"
            try:
                resp = send_batch(
                    url=args.url,
                    block_hash=block_hash,
                    public_key=args.public_key,
                    nonces=batch,
                    model=model,
                    seq_len=args.seq_len,
                    max_tokens=args.max_tokens,
                    timeout=300,
                )
            except Exception as e:
                n_errors += len(batch)
                print(f"{tag}  → FAILED: {e}", file=sys.stderr)
                continue

            artifacts = (
                resp if isinstance(resp, list)
                else resp.get("artifacts", resp.get("results", []))
            )

            batch_written = 0
            for artifact in artifacts:
                nonce   = artifact.get("nonce")
                hs_b64  = artifact.get("hidden_state_b64")
                rhs_b64 = artifact.get("reduced_hidden_state_b64")

                if hs_b64 is None or rhs_b64 is None:
                    print(f"  WARNING: nonce={nonce} missing hidden state fields"
                          " (server may need restart)", file=sys.stderr)
                    continue

                # Decode sphere projections per decode step.
                rhs_dec_b64: List[str] = artifact.get("sph_values_steps") or []
                rhs_dec_b64 = rhs_dec_b64[1:]
                if save_decode and len(rhs_dec_b64) != args.max_tokens:
                    print(f"  WARNING: nonce={nonce} expected {args.max_tokens} decode"
                          f" vectors, got {len(rhs_dec_b64)} — skipping", file=sys.stderr)
                    continue

                # sphere_k at each step: [prefill_k, decode_k_1, ..., decode_k_N]
                k_steps: List[int] = artifact.get("k_points_steps") or []
                expected_steps = args.max_tokens + 1
                if len(k_steps) != expected_steps:
                    print(f"  WARNING: nonce={nonce} expected {expected_steps} k-steps,"
                          f" got {len(k_steps)} — skipping", file=sys.stderr)
                    continue

                acc_block_hashes.append(block_hash)
                acc_nonces.append(nonce)
                acc_hidden_states.append(decode_vector(hs_b64))
                acc_reduced_hidden_states.append(decode_vector(rhs_b64))
                acc_sphere_k_steps.append(np.array(k_steps, dtype=np.int32))
                if save_decode:
                    dec_vecs = np.stack([decode_vector(b) for b in rhs_dec_b64])
                    acc_reduced_hidden_states_decode.append(dec_vecs)

                n_written += 1
                batch_written += 1

            print(f"{tag}  → collected {batch_written}/{len(artifacts)} records"
                  f"  (total: {n_written})")

    if n_written == 0:
        print(f"\nNo records collected. Output file not written.", file=sys.stderr)
    else:
        save_npz(
            args.output, acc_block_hashes, acc_nonces,
            acc_hidden_states, acc_reduced_hidden_states,
            acc_sphere_k_steps,
            acc_reduced_hidden_states_decode if save_decode else None,
        )
        print(f"\nDone.")


if __name__ == "__main__":
    main()
