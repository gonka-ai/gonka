import base64
import hashlib

import pytest
from ecdsa import VerifyingKey, SECP256k1, BadSignatureError


GO_TEST_VECTOR = {
    "body": b'{"test": "data"}',
    "pubkey_b64": "RkauUEcxa0Iw0Ahsis7Gh/ALHNnR3GNPbLNYrAqaj//+d7TdCkv7lYUfO3NVx4HdYPhBj8imXRSQev9HyQOlWQ==",
    "signature_b64": "mMhJz0He2kXjTXKmGO+QQ3SBd2lW9dpgdWj1VF+8yVtAkcGODcEFQOkaRPzCmCPnFnaSxV1w0UrxUBbNQEy5Kg==",
}


def test_go_signature_verifiable_in_python():
    v = GO_TEST_VECTOR
    vk = VerifyingKey.from_string(base64.b64decode(v["pubkey_b64"]), curve=SECP256k1)
    vk.verify(base64.b64decode(v["signature_b64"]), v["body"], hashfunc=hashlib.sha256)


def test_go_signature_rejects_tampered_body():
    v = GO_TEST_VECTOR
    vk = VerifyingKey.from_string(base64.b64decode(v["pubkey_b64"]), curve=SECP256k1)
    with pytest.raises(BadSignatureError):
        vk.verify(base64.b64decode(v["signature_b64"]), b'{"test": "tampered"}', hashfunc=hashlib.sha256)
