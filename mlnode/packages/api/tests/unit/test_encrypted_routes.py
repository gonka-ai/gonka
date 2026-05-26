                                                             

from __future__ import annotations

import base64
import json
from unittest.mock import AsyncMock, MagicMock

import httpx
import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

import api.proxy as proxy_module
from _fixtures.ephemeral_key_provider import EphemeralKeyProvider as LocalKeyProvider

from api.crypto import (
    EncryptedResponse,
    open_response,
    recipient_key_id,
    seal_request,
)
from api.crypto.envelope import public_key_from_raw
from api.crypto.session_store import SessionNonceStore
from api.encrypted.routes import router as encrypted_router


@pytest.fixture
def provider():
    return LocalKeyProvider()


@pytest.fixture
def app(provider):
    app = FastAPI()
    app.state.key_provider = provider
    app.state.session_nonce_store = SessionNonceStore()
    app.include_router(encrypted_router, prefix="/api/v1/encrypted")
    return app


@pytest.fixture
def client(app):
    return TestClient(app)


def _seal_chat_request(provider, session_id, nonce, body):
    pk = public_key_from_raw(provider.identity().public_key)
    hint = recipient_key_id(provider.identity().public_key)
    env, response_key = seal_request(
        pk, session_id, nonce, json.dumps(body).encode(), recipient_key_id_hex=hint
    )
    return env, response_key


def test_identity_endpoint_returns_provider_pubkey(client, provider):
    from api.crypto import recipient_key_id

    resp = client.get("/api/v1/encrypted/identity")
    assert resp.status_code == 200
    body = resp.json()
    assert body["provider"] == "test-ephemeral"
    assert body["envelope_version"] == "v1"
    assert body["quote"] is None
    assert body["measurement"] == ""
    assert base64.b64decode(body["public_key"]) == provider.identity().public_key
                                                                                       
    assert body["key_id"] == recipient_key_id(provider.identity().public_key)


def test_identity_503_when_provider_missing():
    app = FastAPI()
    app.state.key_provider = None
    app.include_router(encrypted_router, prefix="/api/v1/encrypted")
    client = TestClient(app)
    resp = client.get("/api/v1/encrypted/identity")
    assert resp.status_code == 503


def test_chat_completions_round_trip(client, provider, monkeypatch):
    response_body = {"id": "cmpl-1", "choices": [{"message": {"content": "ok"}}]}

    fake_response = MagicMock(spec=httpx.Response)
    fake_response.status_code = 200
    fake_response.content = json.dumps(response_body).encode()
    fake_response.text = json.dumps(response_body)

    fake_client = MagicMock()
    fake_client.post = AsyncMock(return_value=fake_response)
    monkeypatch.setattr(proxy_module, "vllm_client", fake_client)
    monkeypatch.setattr(proxy_module, "_pick_vllm_backend", AsyncMock(return_value=8000))
    monkeypatch.setattr(proxy_module, "_release_vllm_backend", AsyncMock(return_value=None))

    env, response_key = _seal_chat_request(
        provider,
        "escrow-1",
        1,
        {"model": "llama", "messages": [{"role": "user", "content": "hi"}]},
    )

    resp = client.post("/api/v1/encrypted/chat/completions", json=env.model_dump())
    assert resp.status_code == 200, resp.text

    sealed = EncryptedResponse.model_validate(resp.json())
    assert sealed.session_id == "escrow-1"
    assert sealed.nonce == 1

    plaintext = open_response(response_key, sealed)
    assert json.loads(plaintext) == response_body

    fake_client.post.assert_awaited_once()
    args, kwargs = fake_client.post.call_args
    assert args[0].endswith("/v1/chat/completions")
    assert kwargs["json"]["model"] == "llama"


def test_chat_completions_rejects_stream(client, provider, monkeypatch):
    monkeypatch.setattr(proxy_module, "vllm_client", MagicMock())

    env, _ = _seal_chat_request(provider, "escrow-1", 1, {"model": "m", "stream": True})
    resp = client.post("/api/v1/encrypted/chat/completions", json=env.model_dump())
    assert resp.status_code == 400
    assert "stream" in resp.json()["detail"]


def test_chat_completions_replays_blocked(client, provider, monkeypatch):
    fake_response = MagicMock(spec=httpx.Response)
    fake_response.status_code = 200
    fake_response.content = b"{}"
    fake_response.text = "{}"

    fake_client = MagicMock()
    fake_client.post = AsyncMock(return_value=fake_response)
    monkeypatch.setattr(proxy_module, "vllm_client", fake_client)
    monkeypatch.setattr(proxy_module, "_pick_vllm_backend", AsyncMock(return_value=8000))
    monkeypatch.setattr(proxy_module, "_release_vllm_backend", AsyncMock(return_value=None))

    env1, _ = _seal_chat_request(provider, "escrow-1", 5, {"model": "m"})
    resp1 = client.post("/api/v1/encrypted/chat/completions", json=env1.model_dump())
    assert resp1.status_code == 200

                                                                     
    env2, _ = _seal_chat_request(provider, "escrow-1", 5, {"model": "m"})
    resp2 = client.post("/api/v1/encrypted/chat/completions", json=env2.model_dump())
    assert resp2.status_code == 409


def test_chat_completions_503_when_no_vllm(client, provider, monkeypatch):
    monkeypatch.setattr(proxy_module, "vllm_client", None)

    env, _ = _seal_chat_request(provider, "escrow-1", 1, {"model": "m"})
    resp = client.post("/api/v1/encrypted/chat/completions", json=env.model_dump())
    assert resp.status_code == 503


def test_chat_completions_rejects_wrong_key_with_421(client, monkeypatch):
                                                                     
    other = LocalKeyProvider()
    pk = public_key_from_raw(other.identity().public_key)
    foreign_hint = recipient_key_id(other.identity().public_key)

    env, _ = seal_request(pk, "escrow-1", 1, b'{"model":"m"}', recipient_key_id_hex=foreign_hint)

    monkeypatch.setattr(proxy_module, "vllm_client", MagicMock())
    monkeypatch.setattr(proxy_module, "_pick_vllm_backend", AsyncMock(return_value=8000))
    monkeypatch.setattr(proxy_module, "_release_vllm_backend", AsyncMock(return_value=None))

    resp = client.post("/api/v1/encrypted/chat/completions", json=env.model_dump())
    assert resp.status_code == 421
    assert "recipient_key_id" in resp.json()["detail"]


def test_chat_completions_corrupt_with_matching_hint_returns_400(client, provider, monkeypatch):
                                                          
    monkeypatch.setattr(proxy_module, "vllm_client", MagicMock())
    monkeypatch.setattr(proxy_module, "_pick_vllm_backend", AsyncMock(return_value=8000))
    monkeypatch.setattr(proxy_module, "_release_vllm_backend", AsyncMock(return_value=None))

    pk = public_key_from_raw(provider.identity().public_key)
    own_hint = recipient_key_id(provider.identity().public_key)
    env, _ = seal_request(pk, "escrow-1", 1, b'{"model":"m"}', recipient_key_id_hex=own_hint)

    env_dict = env.model_dump()
    env_dict["ciphertext"] = base64.b64encode(b"not-a-real-ciphertext").decode("ascii")

    resp = client.post("/api/v1/encrypted/chat/completions", json=env_dict)
    assert resp.status_code == 400
    assert "corrupt" in resp.json()["detail"]


def test_chat_completions_rejects_recipient_key_id_mismatch_pre_decrypt(client, provider, monkeypatch):
                                                                        
                                                                    
    monkeypatch.setattr(proxy_module, "vllm_client", MagicMock())

    env, _ = _seal_chat_request(provider, "escrow-1", 1, {"model": "m"})
    env_dict = env.model_dump()
    env_dict["recipient_key_id"] = "deadbeefdeadbeef"

    resp = client.post("/api/v1/encrypted/chat/completions", json=env_dict)
    assert resp.status_code == 421
    assert "recipient_key_id" in resp.json()["detail"]


def test_chat_completions_rejects_missing_recipient_key_id(client, provider, monkeypatch):
    monkeypatch.setattr(proxy_module, "vllm_client", MagicMock())

    env, _ = _seal_chat_request(provider, "escrow-1", 1, {"model": "m"})
    env_dict = env.model_dump()
    env_dict.pop("recipient_key_id", None)

    resp = client.post("/api/v1/encrypted/chat/completions", json=env_dict)
    assert resp.status_code == 422                                                   


def test_chat_completions_failed_open_does_not_consume_nonce(client, provider, monkeypatch):
    fake_response = MagicMock(spec=httpx.Response)
    fake_response.status_code = 200
    fake_response.content = b"{}"
    fake_response.text = "{}"

    fake_client = MagicMock()
    fake_client.post = AsyncMock(return_value=fake_response)
    monkeypatch.setattr(proxy_module, "vllm_client", fake_client)
    monkeypatch.setattr(proxy_module, "_pick_vllm_backend", AsyncMock(return_value=8000))
    monkeypatch.setattr(proxy_module, "_release_vllm_backend", AsyncMock(return_value=None))

                                                                     
                                                                  
    foreign = LocalKeyProvider()
    foreign_pk = public_key_from_raw(foreign.identity().public_key)
    foreign_hint = recipient_key_id(foreign.identity().public_key)
    bad_env, _ = seal_request(
        foreign_pk, "escrow-1", 9, b'{"model":"m"}', recipient_key_id_hex=foreign_hint
    )
    bad = client.post("/api/v1/encrypted/chat/completions", json=bad_env.model_dump())
    assert bad.status_code == 421

                                                   
    good_env, _ = _seal_chat_request(provider, "escrow-1", 9, {"model": "m"})
    good = client.post("/api/v1/encrypted/chat/completions", json=good_env.model_dump())
    assert good.status_code == 200


def test_chat_completions_propagates_vllm_4xx(client, provider, monkeypatch):
    fake_response = MagicMock(spec=httpx.Response)
    fake_response.status_code = 422
    fake_response.content = b'{"error":"bad model"}'
    fake_response.text = '{"error":"bad model"}'

    fake_client = MagicMock()
    fake_client.post = AsyncMock(return_value=fake_response)
    monkeypatch.setattr(proxy_module, "vllm_client", fake_client)
    monkeypatch.setattr(proxy_module, "_pick_vllm_backend", AsyncMock(return_value=8000))
    monkeypatch.setattr(proxy_module, "_release_vllm_backend", AsyncMock(return_value=None))

    env, _ = _seal_chat_request(provider, "escrow-1", 1, {"model": "bad"})
    resp = client.post("/api/v1/encrypted/chat/completions", json=env.model_dump())
    assert resp.status_code == 422
