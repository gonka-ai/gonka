import pytest
from fastapi.testclient import TestClient
from unittest.mock import Mock, AsyncMock

from api.app import app
from api.service_management import ServiceState


@pytest.fixture
def client():
    return TestClient(app)


@pytest.fixture
def setup_app_state():
    inference_manager_mock = Mock()
    inference_manager_mock._async_stop = AsyncMock()
    train_manager_mock = Mock()

    app.state.inference_manager = inference_manager_mock
    app.state.train_manager = train_manager_mock
    app.state.service_state = ServiceState.STOPPED

    return {
        "inference_manager": inference_manager_mock,
        "train_manager": train_manager_mock,
    }


def test_stop_endpoint_with_no_services_running(client, setup_app_state):
    setup_app_state["inference_manager"].is_running.return_value = False
    setup_app_state["train_manager"].is_running.return_value = False

    response = client.post("/api/v1/stop")

    assert response.status_code == 200
    assert response.json() == {"status": "OK"}

    setup_app_state["inference_manager"]._async_stop.assert_not_called()
    setup_app_state["train_manager"].stop.assert_not_called()


def test_stop_endpoint_with_inference_running(client, setup_app_state):
    setup_app_state["inference_manager"].is_running.return_value = True
    setup_app_state["train_manager"].is_running.return_value = False
    app.state.service_state = ServiceState.INFERENCE

    response = client.post("/api/v1/stop")

    assert response.status_code == 200
    assert response.json() == {"status": "OK"}

    setup_app_state["inference_manager"]._async_stop.assert_called_once()
    setup_app_state["train_manager"].stop.assert_not_called()


def test_stop_endpoint_with_multiple_services_running(client, setup_app_state):
    setup_app_state["inference_manager"].is_running.return_value = True
    setup_app_state["train_manager"].is_running.return_value = True
    app.state.service_state = ServiceState.INFERENCE

    response = client.post("/api/v1/stop")

    assert response.status_code == 200
    assert response.json() == {"status": "OK"}

    setup_app_state["inference_manager"]._async_stop.assert_called_once()
    setup_app_state["train_manager"].stop.assert_called_once()
