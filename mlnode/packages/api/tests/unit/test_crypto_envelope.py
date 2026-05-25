                                               

from __future__ import annotations

import base64
import json

import pytest

from _fixtures.ephemeral_key_provider import EphemeralKeyProvider as LocalKeyProvider

from api.crypto import (
    EncryptedRequest,
    EncryptedResponse,
    open_request,
    open_response,
    recipient_key_id,
    seal_request,
    seal_response,
)
from api.crypto.envelope import (
    AEAD_ID,
    ENVELOPE_VERSION,
    KDF_ID,
    KEM_ID,
    build_request_aad,
    build_response_aad,
    private_key_from_raw,
    prompt_hash,
    public_key_from_raw,
)
from pyhpke.exceptions import OpenError


def _hint_for(provider):
    return recipient_key_id(provider.identity().public_key)


def _seal(provider, session, nonce, body):
    pk = public_key_from_raw(provider.identity().public_key)
    return seal_request(pk, session, nonce, body, recipient_key_id_hex=_hint_for(provider))


def test_round_trip_request_and_response():
    provider = LocalKeyProvider()

    plaintext = json.dumps({"model": "llama", "messages": [{"role": "user", "content": "hi"}]}).encode()

    env, sender_response_key = _seal(provider, "escrow-1", 42, plaintext)

    assert env.version == ENVELOPE_VERSION
    assert env.kem_id == int(KEM_ID.value)
    assert env.kdf_id == int(KDF_ID.value)
    assert env.aead_id == int(AEAD_ID.value)
    assert base64.b64decode(env.prompt_hash) == prompt_hash(plaintext)

    opened = open_request(provider.private_key(), env)
    assert opened.plaintext == plaintext
    assert opened.session_id == "escrow-1"
    assert opened.nonce == 42
    assert opened.response_key == sender_response_key

    response_body = json.dumps({"choices": [{"message": {"content": "hello"}}]}).encode()
    sealed_response = seal_response(opened.response_key, opened.session_id, opened.nonce, response_body)
    assert sealed_response.session_id == "escrow-1"
    assert sealed_response.nonce == 42

    recovered = open_response(sender_response_key, sealed_response)
    assert recovered == response_body


def test_tampered_ciphertext_fails_to_open():
    provider = LocalKeyProvider()

    env, _ = _seal(provider, "escrow-1", 1, b"prompt")

    ct = bytearray(base64.b64decode(env.ciphertext))
    ct[0] ^= 0xFF
    env.ciphertext = base64.b64encode(bytes(ct)).decode("ascii")

    with pytest.raises(OpenError):
        open_request(provider.private_key(), env)


def test_tampered_prompt_hash_breaks_aad_binding():
    provider = LocalKeyProvider()

    env, _ = _seal(provider, "escrow-1", 1, b"prompt")

    wrong = prompt_hash(b"different prompt")
    env.prompt_hash = base64.b64encode(wrong).decode("ascii")

                                                             
    with pytest.raises(OpenError):
        open_request(provider.private_key(), env)


def test_tampered_session_id_breaks_aad_binding():
    provider = LocalKeyProvider()
    env, _ = _seal(provider, "escrow-1", 1, b"prompt")

    env.session_id = "escrow-2"
    with pytest.raises(OpenError):
        open_request(provider.private_key(), env)


def test_wrong_recipient_key_fails_to_open():
    sender_provider = LocalKeyProvider()
    env, _ = _seal(sender_provider, "escrow-1", 1, b"prompt")

    other_provider = LocalKeyProvider()
    with pytest.raises(OpenError):
        open_request(other_provider.private_key(), env)


def test_response_tamper_fails_to_open():
    provider = LocalKeyProvider()
    env, sender_response_key = _seal(provider, "escrow-1", 7, b"prompt")
    opened = open_request(provider.private_key(), env)

    sealed = seal_response(opened.response_key, "escrow-1", 7, b"answer")
    ct = bytearray(base64.b64decode(sealed.ciphertext))
    ct[-1] ^= 0xFF
    sealed.ciphertext = base64.b64encode(bytes(ct)).decode("ascii")

    with pytest.raises(Exception):                                
        open_response(sender_response_key, sealed)


def test_response_session_or_nonce_mismatch_fails():
    provider = LocalKeyProvider()
    env, sender_response_key = _seal(provider, "escrow-1", 7, b"prompt")
    opened = open_request(provider.private_key(), env)

    sealed = seal_response(opened.response_key, "escrow-1", 7, b"answer")
    sealed.nonce = 99                               

    with pytest.raises(Exception):
        open_response(sender_response_key, sealed)


def test_unsupported_envelope_version_rejected():
    provider = LocalKeyProvider()
    env, _ = _seal(provider, "escrow-1", 1, b"prompt")
    env.version = ENVELOPE_VERSION + 1
    with pytest.raises(ValueError):
        open_request(provider.private_key(), env)


def test_unsupported_suite_rejected():
    provider = LocalKeyProvider()
    env, _ = _seal(provider, "escrow-1", 1, b"prompt")
    env.aead_id = 1                               
    with pytest.raises(ValueError):
        open_request(provider.private_key(), env)


def test_envelope_is_json_serialisable():
    provider = LocalKeyProvider()
    env, _ = _seal(provider, "escrow-1", 5, b"prompt")

    raw = env.model_dump_json()
    restored = EncryptedRequest.model_validate_json(raw)

    opened = open_request(provider.private_key(), restored)
    assert opened.plaintext == b"prompt"


def test_aad_builders_are_stable():
    aad1 = build_request_aad("s", 1, b"\x00" * 32)
    aad2 = build_request_aad("s", 1, b"\x00" * 32)
    assert aad1 == aad2
    assert b"gonka/devshard/req/v1" in aad1
    assert b"gonka/devshard/resp/v1" in build_response_aad("s", 1)


def test_raw_public_key_round_trip():
    provider = LocalKeyProvider()
    env, sender_response_key = _seal(provider, "escrow-1", 1, b"hi")

    opened = open_request(provider.private_key(), env)
    assert opened.plaintext == b"hi"

                                                    
    sk_handle = provider.private_key()
    assert sk_handle.to_private_bytes() is not None
    rebuilt_sk = private_key_from_raw(sk_handle.to_private_bytes())
    opened2 = open_request(rebuilt_sk, env)
    assert opened2.plaintext == b"hi"


def test_seal_request_requires_recipient_key_id():
    provider = LocalKeyProvider()
    pk = public_key_from_raw(provider.identity().public_key)
    with pytest.raises(ValueError):
        seal_request(pk, "escrow-1", 1, b"hi", recipient_key_id_hex="short")


def test_envelope_rejects_missing_recipient_key_id():
    provider = LocalKeyProvider()
    env, _ = _seal(provider, "escrow-1", 1, b"hi")
    raw = env.model_dump()
    raw.pop("recipient_key_id")
    with pytest.raises(Exception):
        EncryptedRequest.model_validate(raw)


def test_envelope_rejects_invalid_recipient_key_id_format():
    provider = LocalKeyProvider()
    env, _ = _seal(provider, "escrow-1", 1, b"hi")
    raw = env.model_dump()
    raw["recipient_key_id"] = "ZZZZZZZZZZZZZZZZ"                     
    with pytest.raises(Exception):
        EncryptedRequest.model_validate(raw)


def test_response_short_key_rejected():
    with pytest.raises(ValueError):
        seal_response(b"\x00" * 16, "s", 1, b"x")
    with pytest.raises(ValueError):
        open_response(
            b"\x00" * 16,
            EncryptedResponse(
                session_id="s", nonce=1, response_nonce="AAAAAAAAAAAAAAAA", ciphertext="AAAA"
            ),
        )
