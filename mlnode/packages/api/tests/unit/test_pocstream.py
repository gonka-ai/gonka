import pytest
from unittest.mock import patch, MagicMock, AsyncMock
from fastapi.testclient import TestClient

from api.app import app
from api.inference.pow_v2_routes import resolve_callback_url, LOCAL_SINK_PORT
from api.pocstream import callback_buffer
from api.pocstream.buffer import CallbackBuffer


@pytest.fixture
def client():
    return TestClient(app)


def make_mock_response(status_code: int, json_data: dict):
    mock = MagicMock()
    mock.status_code = status_code
    mock.json.return_value = json_data
    mock.text = str(json_data)
    return mock


class TestCallbackBuffer:
    @pytest.mark.asyncio
    async def test_add_stream_ack(self):
        buffer = CallbackBuffer()
        id1 = await buffer.add("/v2/poc-batches/m/generated", b"{}")
        id2 = await buffer.add("/v2/poc-batches/m/validated", b"[]")

        stream = buffer.stream()
        first = await anext(stream)
        second = await anext(stream)
        assert (first.id, first.path, first.body) == (id1, "/v2/poc-batches/m/generated", b"{}")
        assert (second.id, second.path, second.body) == (id2, "/v2/poc-batches/m/validated", b"[]")

        await buffer.ack([id1, id2])
        assert buffer.pending_count() == 0

    @pytest.mark.asyncio
    async def test_unacked_callbacks_redelivered_on_new_stream(self):
        buffer = CallbackBuffer()
        id1 = await buffer.add("/p", b"1")

        stream1 = buffer.stream()
        assert (await anext(stream1)).id == id1
        stream2 = buffer.stream()
        assert (await anext(stream2)).id == id1

    @pytest.mark.asyncio
    async def test_resume_evicts_acked_prefix(self):
        buffer = CallbackBuffer()
        id1 = await buffer.add("/p", b"1")
        id2 = await buffer.add("/p", b"2")

        stream = buffer.stream(resume_after_id=id1)
        assert (await anext(stream)).id == id2
        assert buffer.pending_count() == 1

    @pytest.mark.asyncio
    async def test_resume_from_other_boot_replays_everything(self):
        buffer = CallbackBuffer()
        id1 = await buffer.add("/p", b"1")

        stream = buffer.stream(resume_after_id="other-boot-99")
        assert (await anext(stream)).id == id1

    @pytest.mark.asyncio
    async def test_new_stream_terminates_previous_consumer(self):
        buffer = CallbackBuffer()
        await buffer.add("/p", b"1")

        stream1 = buffer.stream()
        await anext(stream1)
        stream2 = buffer.stream()
        await anext(stream2)
        with pytest.raises(StopAsyncIteration):
            await anext(stream1)

    @pytest.mark.asyncio
    async def test_overflow_drops_oldest(self):
        buffer = CallbackBuffer(max_callbacks=2)
        await buffer.add("/p", b"1")
        id2 = await buffer.add("/p", b"2")
        id3 = await buffer.add("/p", b"3")

        assert buffer.pending_count() == 2
        stream = buffer.stream()
        assert (await anext(stream)).id == id2
        assert (await anext(stream)).id == id3


class TestResolveCallbackUrl:
    def test_legacy_url_passthrough(self):
        url = "http://api:9100/v2/poc-batches/some%2Fmodel"
        assert resolve_callback_url(url, "some/model") == url

    def test_missing_url_routes_to_local_sink(self):
        url = resolve_callback_url(None, "org/model-x")
        assert url == (
            f"http://127.0.0.1:{LOCAL_SINK_PORT}"
            "/api/v1/inference/pow/local-sink/v2/poc-batches/org%252Fmodel-x"
        )

    def test_empty_url_routes_to_local_sink(self):
        assert "local-sink" in resolve_callback_url("", "m")


def make_loopback_request(body: bytes) -> MagicMock:
    request = MagicMock()
    request.body = AsyncMock(return_value=body)
    request.client.host = "127.0.0.1"
    return request


class TestLocalSink:
    @pytest.mark.asyncio
    async def test_sink_buffers_path_and_body(self):
        from api.inference.pow_v2_routes import local_sink

        body = b'{"public_key": "pk", "nonces": [1], "dist": [0.5]}'
        before = callback_buffer.pending_count()
        response = await local_sink(
            "v2/poc-batches/test-model/generated", make_loopback_request(body)
        )
        assert response["status"] == "OK"
        assert callback_buffer.pending_count() == before + 1
        stored = callback_buffer._callbacks[-1]
        assert stored.path == "/v2/poc-batches/test-model/generated"
        assert stored.body == body

    @pytest.mark.asyncio
    async def test_sink_preserves_escaped_model_id(self):
        from api.inference.pow_v2_routes import local_sink

        body = b"{}"
        before = callback_buffer.pending_count()
        await local_sink(
            "v2/poc-batches/org%2Fmodel/validated", make_loopback_request(body)
        )
        assert callback_buffer.pending_count() == before + 1
        stored = callback_buffer._callbacks[-1]
        assert stored.path == "/v2/poc-batches/org%2Fmodel/validated"
        assert stored.body == body

    def test_sink_rejects_non_loopback(self, client):
        before = callback_buffer.pending_count()
        response = client.post(
            "/api/v1/inference/pow/local-sink/v2/poc-batches/test-model/generated",
            content=b"{}",
        )
        assert response.status_code == 403
        assert callback_buffer.pending_count() == before


class TestUrlSubstitution:
    @patch('api.proxy.vllm_backend_ports', [5001])
    @patch('api.proxy.vllm_healthy', {5001: True})
    @patch('api.proxy.vllm_counts', {5001: 0})
    @patch('api.proxy.poc_status_by_port', {5001: "IDLE"})
    def test_init_generate_without_url_uses_local_sink(self, client):
        captured = []

        async def mock_post(url, json=None, timeout=None):
            captured.append(json)
            return make_mock_response(200, {"status": "OK", "pow_status": {"status": "GENERATING"}})

        with patch('api.proxy.vllm_client') as mock_client:
            mock_client.post = AsyncMock(side_effect=mock_post)
            response = client.post("/api/v1/inference/pow/init/generate", json={
                "block_hash": "0xabc",
                "block_height": 1,
                "public_key": "pk",
                "node_id": 0,
                "node_count": 1,
                "params": {"model": "test-model", "seq_len": 256},
            })
            assert response.status_code == 200
            assert len(captured) == 1
            assert captured[0]["url"] == (
                f"http://127.0.0.1:{LOCAL_SINK_PORT}"
                "/api/v1/inference/pow/local-sink/v2/poc-batches/test-model"
            )

    @patch('api.proxy.vllm_backend_ports', [5001])
    @patch('api.proxy.vllm_healthy', {5001: True})
    @patch('api.proxy.vllm_counts', {5001: 0})
    @patch('api.proxy.poc_status_by_port', {5001: "IDLE"})
    def test_init_generate_with_url_keeps_legacy_callback(self, client):
        captured = []

        async def mock_post(url, json=None, timeout=None):
            captured.append(json)
            return make_mock_response(200, {"status": "OK", "pow_status": {"status": "GENERATING"}})

        with patch('api.proxy.vllm_client') as mock_client:
            mock_client.post = AsyncMock(side_effect=mock_post)
            response = client.post("/api/v1/inference/pow/init/generate", json={
                "block_hash": "0xabc",
                "block_height": 1,
                "public_key": "pk",
                "node_id": 0,
                "node_count": 1,
                "url": "http://api:9100/v2/poc-batches/test-model",
                "params": {"model": "test-model", "seq_len": 256},
            })
            assert response.status_code == 200
            assert captured[0]["url"] == "http://api:9100/v2/poc-batches/test-model"
