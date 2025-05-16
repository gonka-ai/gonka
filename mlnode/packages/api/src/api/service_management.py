from enum import Enum
from fastapi import HTTPException, Request

API_PREFIX = "/api/v1"

class ServiceState(str, Enum):
    POW = "POW"
    INFERENCE = "INFERENCE"
    TRAIN = "TRAIN"
    STOPPED = "STOPPED"

def get_service_name(request: Request):
    path = request.url.path
    return path.removeprefix(API_PREFIX).lstrip("/").split("/")[0].upper()

def update_service_state(request: Request):
    pow_running = request.app.state.pow_manager.is_running()
    inference_running = request.app.state.inference_manager.is_running()
    train_running = request.app.state.train_manager.is_running()

    running_services = sum([pow_running, inference_running, train_running])
    if running_services > 1:
        request.app.state.pow_manager.stop()
        request.app.state.inference_manager.stop()
        request.app.state.train_manager.stop()
        raise HTTPException(
            status_code=409,
            detail="Multiple services detected. MLNode allows only one service to run at a time. All running services have been stopped."
        )

    if pow_running:
        request.app.state.service_state = ServiceState.POW
    elif inference_running:
        request.app.state.service_state = ServiceState.INFERENCE
    elif train_running:
        request.app.state.service_state = ServiceState.TRAIN
    else:
        request.app.state.service_state = ServiceState.STOPPED

def handle_conflicts(request: Request):
    requested_service = get_service_name(request)
    current_service = request.app.state.service_state

    if current_service == ServiceState.STOPPED or requested_service == "MLNODE":
        return

    if current_service != requested_service:
        raise HTTPException(
            status_code=409,
            detail=(
                f"Cannot run {requested_service} because MLNode is currently "
                f"in {current_service} mode. Please stop {current_service} first."
            )
        )

def check_service_conflicts(request: Request):
    update_service_state(request)
    handle_conflicts(request)
