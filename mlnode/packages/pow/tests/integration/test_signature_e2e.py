import base64
import hashlib
import os

import pytest
import requests
from ecdsa import SigningKey, SECP256k1


SERVER_URL = os.environ.get("SERVER_URL", "http://localhost:8080")


@pytest.fixture
def keypair():
    sk = SigningKey.generate(curve=SECP256k1)
    vk = sk.get_verifying_key()
    return sk, base64.b64encode(vk.to_string()).decode()


def sign_body(sk: SigningKey, body: bytes) -> str:
    return base64.b64encode(sk.sign(body, hashfunc=hashlib.sha256)).decode()


@pytest.mark.skipif(bool(os.environ.get("SIGNER_PUBKEY")), reason="SIGNER_PUBKEY is set")
def test_request_without_signature_when_disabled():
    try:
        resp = requests.get(f"{SERVER_URL}/api/v1/pow/status", timeout=10)
        assert resp.status_code != 403
    except requests.exceptions.ConnectionError:
        pytest.skip(f"Server not running at {SERVER_URL}")


def test_signature_verification_flow(keypair):
    sk, _ = keypair
    body = b'{"test": "data"}'
    signature = sign_body(sk, body)
    
    vk = sk.get_verifying_key()
    vk.verify(base64.b64decode(signature), body, hashfunc=hashlib.sha256)


def test_empty_body_signature(keypair):
    sk, _ = keypair
    body = b''
    signature = sign_body(sk, body)
    
    vk = sk.get_verifying_key()
    vk.verify(base64.b64decode(signature), body, hashfunc=hashlib.sha256)
