import hashlib
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from api.inference import pow_v2_routes
from api.inference.pow_v2_routes import _rewrite_callback_url

API_PREFIX = "/api/v1"

MTLS_ENV = {
    "MLNODE_MTLS_CLIENT_CERT": "/root/mtls/mlnode.crt",
    "MLNODE_MTLS_CLIENT_KEY": "/root/mtls/mlnode.key",
    "MLNODE_MTLS_SERVER_CA": "/root/mtls/dapi.crt",
}

DAPI_URL = "https://api:9100/v2/poc-batches/test-model"


@pytest.fixture
def client():
    app = FastAPI()
    app.include_router(pow_v2_routes.router, prefix=API_PREFIX)
    return TestClient(app)


@pytest.fixture(autouse=True)
def clean_relay_state():
    pow_v2_routes._relay_targets.clear()
    yield
    pow_v2_routes._relay_targets.clear()


class TestRewriteCallbackUrl:
    def test_noop_when_mtls_disabled(self):
        with patch.dict("os.environ", {}, clear=True):
            assert _rewrite_callback_url(DAPI_URL) == DAPI_URL
            assert pow_v2_routes._relay_targets == {}

    def test_noop_for_empty_url(self):
        with patch.dict("os.environ", MTLS_ENV):
            assert _rewrite_callback_url(None) is None
            assert _rewrite_callback_url("") == ""

    def test_rewrites_to_local_relay_when_mtls_enabled(self):
        with patch.dict("os.environ", MTLS_ENV):
            rewritten = _rewrite_callback_url(DAPI_URL)

        token = hashlib.sha256(DAPI_URL.encode()).hexdigest()[:32]
        assert rewritten == (
            f"{pow_v2_routes.SELF_URL}{API_PREFIX}/inference/pow/callback-relay/{token}"
        )
        assert pow_v2_routes._relay_targets[token] == DAPI_URL

    def test_same_url_maps_to_same_token(self):
        with patch.dict("os.environ", MTLS_ENV):
            assert _rewrite_callback_url(DAPI_URL) == _rewrite_callback_url(DAPI_URL)
        assert len(pow_v2_routes._relay_targets) == 1


class TestCallbackRelayEndpoint:
    def test_unknown_token_is_rejected(self, client):
        resp = client.post(
            f"{API_PREFIX}/inference/pow/callback-relay/deadbeef/generated",
            json={"nonces": [1]},
        )
        assert resp.status_code == 404

    def test_forwards_to_registered_target_over_mtls_client(self, client):
        with patch.dict("os.environ", MTLS_ENV):
            relay_url = _rewrite_callback_url(DAPI_URL)
        token = relay_url.rsplit("/", 1)[-1]

        upstream_response = MagicMock()
        upstream_response.status_code = 200
        upstream_response.content = b'{"status":"OK"}'
        upstream_response.headers = {"Content-Type": "application/json"}

        mock_client = MagicMock()
        mock_client.post = AsyncMock(return_value=upstream_response)

        with patch.object(pow_v2_routes, "_get_relay_client", return_value=mock_client):
            resp = client.post(
                f"{API_PREFIX}/inference/pow/callback-relay/{token}/generated",
                json={"public_key": "pk", "nonces": [1, 2]},
            )

        assert resp.status_code == 200
        assert resp.json() == {"status": "OK"}

        mock_client.post.assert_awaited_once()
        args, kwargs = mock_client.post.call_args
        assert args[0] == f"{DAPI_URL}/generated"
        assert b'"nonces": [1, 2]' in kwargs["content"] or b'"nonces":[1,2]' in kwargs["content"]

    def test_upstream_error_status_is_propagated(self, client):
        with patch.dict("os.environ", MTLS_ENV):
            relay_url = _rewrite_callback_url(DAPI_URL)
        token = relay_url.rsplit("/", 1)[-1]

        upstream_response = MagicMock()
        upstream_response.status_code = 503
        upstream_response.content = b'{"detail":"busy"}'
        upstream_response.headers = {"Content-Type": "application/json"}

        mock_client = MagicMock()
        mock_client.post = AsyncMock(return_value=upstream_response)

        with patch.object(pow_v2_routes, "_get_relay_client", return_value=mock_client):
            resp = client.post(
                f"{API_PREFIX}/inference/pow/callback-relay/{token}/validated",
                json={},
            )

        assert resp.status_code == 503
