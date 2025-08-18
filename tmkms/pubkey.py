#!/usr/bin/env python3
import argparse
import base64
import os
import sys
import json
from typing import Optional

from nacl.signing import SigningKey

from cryptography.hazmat.primitives.asymmetric import ed25519
from cryptography.hazmat.primitives import serialization


def decode_base64_key(file_path: str) -> bytes:
    with open(file_path, "rb") as f:
        raw = f.read()
    # Allow whitespace/newlines; base64 decoder will ignore newlines
    key_bytes = base64.b64decode(raw, validate=False)
    if len(key_bytes) not in (32, 64):
        raise ValueError(
            f"Unexpected key length {len(key_bytes)} bytes in {file_path}; expected 32 or 64"
        )
    # If 64, many tools store seed||pub; take first 32 as seed
    if len(key_bytes) == 64:
        key_bytes = key_bytes[:32]
    return key_bytes


def derive_pubkey_with_cryptography(seed: bytes) -> Optional[bytes]:
    private_key = ed25519.Ed25519PrivateKey.from_private_bytes(seed)
    public_key = private_key.public_key()
    pub_raw = public_key.public_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PublicFormat.Raw,
    )
    return pub_raw


def derive_pubkey_with_pynacl(seed: bytes) -> Optional[bytes]:
    sk = SigningKey(seed)
    vk = sk.verify_key
    return bytes(vk)


def derive_ed25519_pubkey(seed: bytes) -> bytes:
    pub = derive_pubkey_with_cryptography(seed)
    if pub is not None:
        return pub

    pub = derive_pubkey_with_pynacl(seed)
    if pub is not None:
        return pub

    raise RuntimeError(
        "No Ed25519 backend available. Install 'cryptography' or 'pynacl'."
    )


def main() -> None:
    default_key_path = "/root/.tmkms/secrets/priv_validator_key.softsign"

    parser = argparse.ArgumentParser(
        description="Extract Ed25519 public key (base64) from TMKMS softsign key file",
    )
    parser.add_argument(
        "--key",
        dest="key_path",
        default=default_key_path,
        help=f"Path to TMKMS softsign key file (default: {default_key_path})",
    )
    parser.add_argument(
        "--json",
        action="store_true",
        help="Output Tendermint/CometBFT-style JSON object",
    )

    args = parser.parse_args()

    try:
        seed = decode_base64_key(args.key_path)
        pubkey_raw = derive_ed25519_pubkey(seed)
        pubkey_b64 = base64.b64encode(pubkey_raw).decode("ascii")
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)

    if args.json:
        obj = {"@type": "/cosmos.crypto.ed25519.PubKey", "key": pubkey_b64}
        print(json.dumps(obj, indent=2))
    else:
        print(pubkey_b64)


if __name__ == "__main__":
    main()
