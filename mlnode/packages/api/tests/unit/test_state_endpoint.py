"""Tests for GET /api/v1/state — state endpoint."""

from unittest.mock import Mock, AsyncMock, patch

import pytest
from fastapi.testclient import TestClient

from api.app import app
from api.service_management import ServiceState


@pytest.fixture(autouse=True)
def reset_app_state():
    """Each test starts with a clean slate: no services running, no vLLM backends."""
    pow_manager = Mock()
    pow_manager.is_running.return_value = False

    inference_manager = Mock()
    inference_manager.is_running.return_value = False
    inference_manager.vllm_runner = None
    inference_manager._async_stop = AsyncMock()

    train_manager = Mock()
    train_manager.is_running.return_value = False

    app.state.pow_manager = pow_manager
    app.state.inference_manager = inference_manager
    app.state.train_manager = train_manager
    app.state.service_state = ServiceState.STOPPED

    yield


@pytest.fixture
def client():
    return TestClient(app)


def test_stopped_node_returns_only_state_field(client):
    """Node is idle after clean shutdown. No vLLM, no PoC — minimal response."""
    response = client.get("/api/v1/state")

    assert response.status_code == 200
    data = response.json()
    assert data["state"] == "STOPPED"
    assert data["poc_status"] is None
    assert data["inference_healthy"] is None
    assert data["loaded_model"] is None


def test_pow_mode_returns_only_state_field(client):
    """Node is running old chain-level PoW (not vLLM-based PoC) — no enrichment."""
    app.state.pow_manager.is_running.return_value = True

    response = client.get("/api/v1/state")

    assert response.status_code == 200
    data = response.json()
    assert data["state"] == "POW"
    assert data["poc_status"] is None
    assert data["inference_healthy"] is None
    assert data["loaded_model"] is None


def test_train_mode_returns_only_state_field(client):
    """Node is training — no inference running, no enrichment."""
    app.state.train_manager.is_running.return_value = True

    response = client.get("/api/v1/state")

    assert response.status_code == 200
    data = response.json()
    assert data["state"] == "TRAIN"
    assert data["poc_status"] is None
    assert data["inference_healthy"] is None
    assert data["loaded_model"] is None


# ---------------------------------------------------------------------------
# INFERENCE state — enriched fields reflect live vLLM backend status.
# ---------------------------------------------------------------------------

def test_inference_started_but_vllm_not_healthy_yet(client):
    """InferenceManager launched vLLM but /health hasn't passed yet.

    The broker learns in one call that it should not dispatch PoC work yet:
    inference_healthy=False, poc_status=NO_BACKENDS.
    """
    app.state.inference_manager.is_running.return_value = True

    with patch.dict("api.proxy.vllm_healthy", {8000: False}, clear=True), \
         patch.dict("api.proxy.poc_status_by_port", {8000: ""}, clear=True):
        response = client.get("/api/v1/state")

    assert response.status_code == 200
    data = response.json()
    assert data["state"] == "INFERENCE"
    assert data["inference_healthy"] is False
    assert data["poc_status"] == "NO_BACKENDS"
    assert data["loaded_model"] is None


def test_inference_healthy_poc_idle(client):
    """vLLM is up, PoC generation has not been requested yet.

    Happy path right after inference boot. The broker now knows:
    - vLLM is healthy (skip /health call)
    - PoC is not running (skip /poc/status call)
    - which model is loaded (skip /v1/models call)
    All from a single /state response.
    """
    runner = Mock()
    runner.model = "Qwen/Qwen3-0.6B"
    app.state.inference_manager.is_running.return_value = True
    app.state.inference_manager.vllm_runner = runner

    with patch.dict("api.proxy.vllm_healthy", {8000: True}, clear=True), \
         patch.dict("api.proxy.poc_status_by_port", {8000: "IDLE"}, clear=True):
        response = client.get("/api/v1/state")

    assert response.status_code == 200
    data = response.json()
    assert data["state"] == "INFERENCE"
    assert data["inference_healthy"] is True
    assert data["poc_status"] == "IDLE"
    assert data["loaded_model"] == "Qwen/Qwen3-0.6B"


def test_inference_healthy_poc_generating(client):
    """vLLM is healthy and PoC generation is active.

    The broker confirms the node is doing work without a separate /poc/status call.
    """
    runner = Mock()
    runner.model = "meta-llama/Llama-3.1-8B"
    app.state.inference_manager.is_running.return_value = True
    app.state.inference_manager.vllm_runner = runner

    with patch.dict("api.proxy.vllm_healthy", {8000: True}, clear=True), \
         patch.dict("api.proxy.poc_status_by_port", {8000: "GENERATING"}, clear=True):
        response = client.get("/api/v1/state")

    assert response.status_code == 200
    data = response.json()
    assert data["state"] == "INFERENCE"
    assert data["inference_healthy"] is True
    assert data["poc_status"] == "GENERATING"
    assert data["loaded_model"] == "meta-llama/Llama-3.1-8B"


def test_inference_healthy_no_runner_yet(client):
    """vLLM process is healthy but runner object not yet attached (edge case).

    loaded_model is None — broker should verify model via /v1/models as fallback.
    """
    app.state.inference_manager.is_running.return_value = True
    app.state.inference_manager.vllm_runner = None

    with patch.dict("api.proxy.vllm_healthy", {8000: True}, clear=True), \
         patch.dict("api.proxy.poc_status_by_port", {8000: "IDLE"}, clear=True):
        response = client.get("/api/v1/state")

    assert response.status_code == 200
    data = response.json()
    assert data["inference_healthy"] is True
    assert data["poc_status"] == "IDLE"
    assert data["loaded_model"] is None


def test_inference_healthy_poc_validating(client):
    """vLLM is healthy and PoC validation is active."""
    app.state.inference_manager.is_running.return_value = True

    with patch.dict("api.proxy.vllm_healthy", {8000: True}, clear=True), \
         patch.dict("api.proxy.poc_status_by_port", {8000: "VALIDATING"}, clear=True):
        response = client.get("/api/v1/state")

    assert response.status_code == 200
    data = response.json()
    assert data["state"] == "INFERENCE"
    assert data["inference_healthy"] is True
    assert data["poc_status"] == "VALIDATING"


# ---------------------------------------------------------------------------
# Multi-backend (multi-GPU) scenarios
# ---------------------------------------------------------------------------

def test_multi_gpu_all_backends_generating(client):
    """Two vLLM backends, both generating — aggregate is GENERATING."""
    app.state.inference_manager.is_running.return_value = True

    with patch.dict("api.proxy.vllm_healthy", {8000: True, 8001: True}, clear=True), \
         patch.dict("api.proxy.poc_status_by_port", {8000: "GENERATING", 8001: "GENERATING"}, clear=True):
        response = client.get("/api/v1/state")

    assert response.status_code == 200
    data = response.json()
    assert data["poc_status"] == "GENERATING"
    assert data["inference_healthy"] is True


def test_multi_gpu_mixed_poc_state(client):
    """Fan-out started but one backend lags behind — broker gets MIXED.

    MIXED tells the broker it cannot rely on the aggregate alone; it should
    fall back to the full /poc/status breakdown if exact per-backend state matters.
    """
    app.state.inference_manager.is_running.return_value = True

    with patch.dict("api.proxy.vllm_healthy", {8000: True, 8001: True}, clear=True), \
         patch.dict("api.proxy.poc_status_by_port", {8000: "GENERATING", 8001: "IDLE"}, clear=True):
        response = client.get("/api/v1/state")

    assert response.status_code == 200
    data = response.json()
    assert data["poc_status"] == "MIXED"
    assert data["inference_healthy"] is True


def test_multi_gpu_one_backend_down(client):
    """One of two backends went unhealthy. Enrichment is based on healthy ones only."""
    runner = Mock()
    runner.model = "Qwen/Qwen3-0.6B"
    app.state.inference_manager.is_running.return_value = True
    app.state.inference_manager.vllm_runner = runner

    with patch.dict("api.proxy.vllm_healthy", {8000: True, 8001: False}, clear=True), \
         patch.dict("api.proxy.poc_status_by_port", {8000: "IDLE", 8001: "IDLE"}, clear=True):
        response = client.get("/api/v1/state")

    assert response.status_code == 200
    data = response.json()
    assert data["state"] == "INFERENCE"
    assert data["inference_healthy"] is True   # ≥1 healthy backend
    assert data["poc_status"] == "IDLE"
    assert data["loaded_model"] == "Qwen/Qwen3-0.6B"
