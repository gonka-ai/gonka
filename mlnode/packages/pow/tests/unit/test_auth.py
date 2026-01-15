import asyncio
import base64
import hashlib
from unittest.mock import AsyncMock, MagicMock

import pytest
from ecdsa import SECP256k1, SigningKey
from fastapi import HTTPException


@pytest.fixture
def test_keypair():
    sk = SigningKey.generate(curve=SECP256k1)
    vk = sk.get_verifying_key()
    return sk, vk


@pytest.fixture
def sign_body(test_keypair):
    sk, _ = test_keypair
    def _sign(body: bytes) -> str:
        sig = sk.sign(body, hashfunc=hashlib.sha256)
        return base64.b64encode(sig).decode()
    return _sign


@pytest.fixture
def pubkey_b64(test_keypair):
    _, vk = test_keypair
    return base64.b64encode(vk.to_string()).decode()


def make_mock_request(body: bytes):
    mock_request = AsyncMock()
    mock_request.body = AsyncMock(return_value=body)
    return mock_request


def test_verify_signature_valid(sign_body, pubkey_b64):
    body = b'{"test": "data"}'
    signature = sign_body(body)

    import pow.service.auth as auth_module
    auth_module.SIGNER_PUBKEY = pubkey_b64
    asyncio.run(auth_module.verify_signature(make_mock_request(body), signature))


def test_verify_signature_invalid(pubkey_b64):
    import pow.service.auth as auth_module
    auth_module.SIGNER_PUBKEY = pubkey_b64

    with pytest.raises(HTTPException) as exc_info:
        asyncio.run(auth_module.verify_signature(
            make_mock_request(b'{"test": "data"}'),
            base64.b64encode(b"invalid" * 8).decode()
        ))
    assert exc_info.value.status_code == 403


def test_verify_signature_missing(pubkey_b64):
    import pow.service.auth as auth_module
    auth_module.SIGNER_PUBKEY = pubkey_b64

    with pytest.raises(HTTPException) as exc_info:
        asyncio.run(auth_module.verify_signature(make_mock_request(b'{"test": "data"}'), None))
    assert exc_info.value.status_code == 403


def test_verify_signature_disabled():
    import pow.service.auth as auth_module
    auth_module.SIGNER_PUBKEY = ""
    asyncio.run(auth_module.verify_signature(make_mock_request(b'{"test": "data"}'), None))


def test_verify_signature_wrong_body(sign_body, pubkey_b64):
    signature = sign_body(b'{"test": "original"}')

    import pow.service.auth as auth_module
    auth_module.SIGNER_PUBKEY = pubkey_b64

    with pytest.raises(HTTPException):
        asyncio.run(auth_module.verify_signature(make_mock_request(b'{"test": "tampered"}'), signature))


def test_verify_signature_empty_body(sign_body, pubkey_b64):
    body = b''
    signature = sign_body(body)

    import pow.service.auth as auth_module
    auth_module.SIGNER_PUBKEY = pubkey_b64
    asyncio.run(auth_module.verify_signature(make_mock_request(body), signature))


def test_verify_signature_compressed_pubkey(test_keypair):
    sk, vk = test_keypair
    compressed_pubkey = base64.b64encode(vk.to_string("compressed")).decode()
    
    body = b'{"test": "data"}'
    signature = base64.b64encode(sk.sign(body, hashfunc=hashlib.sha256)).decode()

    import pow.service.auth as auth_module
    auth_module.SIGNER_PUBKEY = compressed_pubkey
    asyncio.run(auth_module.verify_signature(make_mock_request(body), signature))
