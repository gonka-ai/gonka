from fastapi import FastAPI, Depends
from contextlib import asynccontextmanager

from api.inference.manager import InferenceManager
from api.inference.routes import router as inference_router

from zeroband.service.manager import TrainManager
from zeroband.service.routes import router as train_router

from pow.service.manager import PowManager
from pow.service.routes import router as pow_router

from api.service_management import (
    ServiceState,
    check_service_conflicts,
    API_PREFIX
)
from api.routes import router as api_router

@asynccontextmanager
async def lifespan(app: FastAPI):
    app.state.service_state = ServiceState.STOPPED
    app.state.pow_manager = PowManager()
    app.state.inference_manager = InferenceManager()
    app.state.train_manager = TrainManager()

    yield
    
    if app.state.pow_manager.is_running():
        app.state.pow_manager.stop()
    if app.state.inference_manager.is_running():
        app.state.inference_manager.stop()
    if app.state.train_manager.is_running():
        app.state.train_manager.stop()

app = FastAPI(lifespan=lifespan)

app.include_router(
    pow_router,
    prefix=API_PREFIX,
    tags=["PoW"],
    dependencies=[Depends(check_service_conflicts)]
)

app.include_router(
    train_router,
    prefix=API_PREFIX,
    tags=["Train"],
    dependencies=[Depends(check_service_conflicts)]
)

app.include_router(
    inference_router,
    prefix=API_PREFIX,
    tags=["Inference"],
    dependencies=[Depends(check_service_conflicts)]
)

app.include_router(
    api_router,
    prefix=API_PREFIX,
    tags=["API"],
)
