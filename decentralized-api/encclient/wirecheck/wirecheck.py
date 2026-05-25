                      
\
\
\
\
\
\
\
\
\
\
   

from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
MLNODE_SRC = REPO_ROOT / "mlnode" / "packages" / "api" / "src"
MLNODE_COMMON = REPO_ROOT / "mlnode" / "packages" / "common" / "src"

sys.path.insert(0, str(MLNODE_SRC))
sys.path.insert(0, str(MLNODE_COMMON))

from api.crypto.envelope import (              
    EncryptedRequest,
    open_request,
    open_response,
    private_key_from_raw,
    recipient_key_id,
    seal_response,
)
from cryptography.hazmat.primitives import serialization              
from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey              


def main() -> int:
    sk = X25519PrivateKey.generate()
    pub_raw = sk.public_key().public_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PublicFormat.Raw,
    )
    priv_raw = sk.private_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PrivateFormat.Raw,
        encryption_algorithm=serialization.NoEncryption(),
    )

    go_dir = REPO_ROOT / "decentralized-api"
    proc = subprocess.run(
        [
            "go", "run", "./encclient/wirecheck",
            f"-recipient-pub-hex={pub_raw.hex()}",
            "-session-id=wirecheck-session",
            "-nonce=7",
            "-plaintext=hello-from-go",
        ],
        cwd=go_dir,
        capture_output=True,
        text=True,
        env={**os.environ},
        check=True,
    )
    payload = json.loads(proc.stdout)
    env = EncryptedRequest(**payload["envelope"])

                                                                   
    expected_key_id = recipient_key_id(pub_raw)
    assert env.recipient_key_id == expected_key_id, (
        f"recipient_key_id mismatch: go={env.recipient_key_id} py={expected_key_id}"
    )

    opened = open_request(private_key_from_raw(priv_raw), env)
    assert opened.plaintext == b"hello-from-go", opened.plaintext
    assert opened.session_id == "wirecheck-session"
    assert opened.nonce == 7

                                                                     
                                     
    resp = seal_response(
        opened.response_key,
        opened.session_id,
        opened.nonce,
        b"hello-from-py",
    )
    plaintext = open_response(opened.response_key, resp)
    assert plaintext == b"hello-from-py"

    print("wirecheck OK: Go-sealed envelope opened by Python, response round-trip clean.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
