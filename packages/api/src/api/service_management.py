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
    return path.removeprefix(API_PREFIX).lstrip('/').split('/')[0].upper()

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
            status_code=400,
            detail="Something went wrong. MLNode can only run one service at a time. Stopping..."
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
    path = request.url.path
    method = request.method.upper()
    requested_service_state = get_service_name(request).upper()
    current_service_state = request.app.state.service_state
    
    if current_service_state == ServiceState.STOPPED:
        return
    
    if requested_service_state == 'MLNODE':
        return
    
    if current_service_state != requested_service_state:
        raise HTTPException(
            status_code=400,
            detail=f"Can't run {requested_service_state} because MLNode is in the {current_service_state} mode. "
                   f"Stop the {current_service_state} service first."
        )
