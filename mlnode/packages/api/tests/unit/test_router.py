import pytest
from unittest.mock import MagicMock
from fastapi import FastAPI
from fastapi.testclient import TestClient

from api.inference.manager import InferenceManager
from api.inference.routes import router

@pytest.fixture
def mock_manager():
    manager = MagicMock(spec=InferenceManager)
    manager.is_running.return_value = False
    return manager

@pytest.fixture
def client(mock_manager):
    app = FastAPI()
    app.state.inference_manager = mock_manager
    app.include_router(router)
    return TestClient(app)

def test_inference_up_already_running(client, mock_manager):
    # Mock the behavior: initially running, then stopped after stop() call
    mock_manager.is_running.side_effect = [True, False]  # First call returns True, second call returns False

    response = client.post("/inference/up", json={"model": "test-model", "dtype": "auto"})
    assert response.status_code == 200
    assert response.json()["status"] == "OK"

    mock_manager.stop.assert_called_once()
    mock_manager.init_vllm.assert_called_once()
    mock_manager.start.assert_called_once()

def test_inference_up_not_running(client, mock_manager):
    mock_manager.is_running.return_value = False

    response = client.post("/inference/up", json={"model": "test-model", "dtype": "auto"})
    assert response.status_code == 200
    assert response.json()["status"] == "OK"

    mock_manager.stop.assert_not_called()

    mock_manager.init_vllm.assert_called_once()
    mock_manager.start.assert_called_once()

def test_inference_down(client, mock_manager):
    response = client.post("/inference/down")
    assert response.status_code == 200
    assert response.json()["status"] == "OK"

    mock_manager.stop.assert_called_once()
