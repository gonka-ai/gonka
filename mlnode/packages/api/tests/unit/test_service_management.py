import pytest
from unittest.mock import MagicMock, AsyncMock
from fastapi import HTTPException

from api.service_management import (
    ServiceState,
    check_service_conflicts,
)


class MockState:
    def __init__(self):
        self.service_state = ServiceState.STOPPED

        self.inference_manager = MagicMock()
        self.inference_manager._async_stop = AsyncMock()
        self.train_manager = MagicMock()

        self.inference_manager.is_running.return_value = False
        self.train_manager.is_running.return_value = False


class MockApp:
    def __init__(self):
        self.state = MockState()


class MockURL:
    def __init__(self, path: str):
        self.path = path


class MockRequest:
    def __init__(self, path: str):
        self.app = MockApp()
        self.url = MockURL(path)


@pytest.mark.parametrize(
    "path,inf_running,train_running,expected_state",
    [
        ("/api/v1/inference", True, False, ServiceState.INFERENCE),
        ("/api/v1/train", False, True, ServiceState.TRAIN),
        ("/api/v1/inference", False, False, ServiceState.STOPPED),
    ],
)
@pytest.mark.asyncio
async def test_single_service_runs_without_conflict(
    path,
    inf_running,
    train_running,
    expected_state,
):
    request = MockRequest(path)
    request.app.state.inference_manager.is_running.return_value = inf_running
    request.app.state.train_manager.is_running.return_value = train_running

    await check_service_conflicts(request)
    assert request.app.state.service_state == expected_state


@pytest.mark.parametrize(
    "path,inf_running,train_running",
    [
        ("/api/v1/inference", True, True),
    ],
)
@pytest.mark.asyncio
async def test_multiple_services_raise_conflict_and_stop_all(path, inf_running, train_running):
    request = MockRequest(path)
    request.app.state.inference_manager.is_running.return_value = inf_running
    request.app.state.train_manager.is_running.return_value = train_running

    with pytest.raises(HTTPException) as excinfo:
        await check_service_conflicts(request)

    assert excinfo.value.status_code == 409
    request.app.state.inference_manager._async_stop.assert_called_once()
    request.app.state.train_manager.stop.assert_called_once()


@pytest.mark.parametrize(
    "current_service_path,new_request_path",
    [
        ("/api/v1/inference", "/api/v1/train"),
        ("/api/v1/train", "/api/v1/inference"),
    ],
)
@pytest.mark.asyncio
async def test_conflict_if_different_service_is_already_running(
    current_service_path, new_request_path
):
    current_req = MockRequest(current_service_path)

    if "inference" in current_service_path:
        current_req.app.state.inference_manager.is_running.return_value = True
        current_req.app.state.service_state = ServiceState.INFERENCE
    else:
        current_req.app.state.train_manager.is_running.return_value = True
        current_req.app.state.service_state = ServiceState.TRAIN

    new_req = MockRequest(new_request_path)
    new_req.app = current_req.app

    with pytest.raises(HTTPException) as excinfo:
        await check_service_conflicts(new_req)

    assert excinfo.value.status_code == 409
    new_req.app.state.inference_manager._async_stop.assert_not_called()
    new_req.app.state.train_manager.stop.assert_not_called()
