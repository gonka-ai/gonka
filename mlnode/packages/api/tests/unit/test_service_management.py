import pytest
from unittest.mock import MagicMock
from fastapi import HTTPException

from api.service_management import (
    ServiceState,
    check_service_conflicts,
)

class MockState:
    def __init__(self):
        self.service_state = ServiceState.STOPPED
        
        self.pow_manager = MagicMock()
        self.inference_manager = MagicMock()
        self.train_manager = MagicMock()
        
        self.pow_manager.is_running.return_value = False
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
    "path,pow_running,inf_running,train_running,expected_state",
    [
        ("/api/v1/pow",       True,  False, False, ServiceState.POW),
        ("/api/v1/inference", False, True,  False, ServiceState.INFERENCE),
        ("/api/v1/train",     False, False, True,  ServiceState.TRAIN),
        ("/api/v1/pow",       False, False, False, ServiceState.STOPPED),
    ]
)
def test_single_service_runs_without_conflict(
    path,
    pow_running,
    inf_running,
    train_running,
    expected_state
):
    request = MockRequest(path)
    request.app.state.pow_manager.is_running.return_value = pow_running
    request.app.state.inference_manager.is_running.return_value = inf_running
    request.app.state.train_manager.is_running.return_value = train_running

    check_service_conflicts(request)
    assert request.app.state.service_state == expected_state

@pytest.mark.parametrize(
    "path,pow_running,inf_running,train_running",
    [
        ("/api/v1/pow",       True, True, False),
        ("/api/v1/pow",       True, True, True),
        ("/api/v1/train",     False, True, True),
        # etc...
    ]
)
def test_multiple_services_raise_conflict_and_stop_all(path, pow_running, inf_running, train_running):
    request = MockRequest(path)
    request.app.state.pow_manager.is_running.return_value = pow_running
    request.app.state.inference_manager.is_running.return_value = inf_running
    request.app.state.train_manager.is_running.return_value = train_running

    with pytest.raises(HTTPException) as excinfo:
        check_service_conflicts(request)

    assert excinfo.value.status_code == 409
    # The code should stop all managers
    request.app.state.pow_manager.stop.assert_called_once()
    request.app.state.inference_manager.stop.assert_called_once()
    request.app.state.train_manager.stop.assert_called_once()

@pytest.mark.parametrize(
    "current_service_path,new_request_path",
    [
        ("/api/v1/pow",       "/api/v1/inference"),
        ("/api/v1/inference", "/api/v1/train"),
        ("/api/v1/train",     "/api/v1/pow"),
    ]
)
def test_conflict_if_different_service_is_already_running(current_service_path, new_request_path):
    current_req = MockRequest(current_service_path)

    if "pow" in current_service_path:
        current_req.app.state.pow_manager.is_running.return_value = True
        current_req.app.state.service_state = ServiceState.POW
    elif "inference" in current_service_path:
        current_req.app.state.inference_manager.is_running.return_value = True
        current_req.app.state.service_state = ServiceState.INFERENCE
    else:
        current_req.app.state.train_manager.is_running.return_value = True
        current_req.app.state.service_state = ServiceState.TRAIN

    new_req = MockRequest(new_request_path)
    new_req.app = current_req.app

    with pytest.raises(HTTPException) as excinfo:
        check_service_conflicts(new_req)

    assert excinfo.value.status_code == 409
    new_req.app.state.pow_manager.stop.assert_not_called()
    new_req.app.state.inference_manager.stop.assert_not_called()
    new_req.app.state.train_manager.stop.assert_not_called()
