from fastapi import FastAPI, Request, HTTPException
from contextlib import asynccontextmanager
from fastapi.responses import JSONResponse
import logging

from inference.manager import InferenceManager
from inference.routes import router as inference_router

from zeroband.service.manager import TrainManager
from zeroband.service.routes import router as train_router

from pow.service.manager import PowManager
from pow.service.routes import router as pow_router

from api.service_management import ServiceState, update_service_state, handle_conflicts, API_PREFIX
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


@app.middleware("http")
async def service_conflict_middleware(request: Request, call_next):
    update_service_state(request)
    handle_conflicts(request)
    response = await call_next(request)
    return response

@app.exception_handler(HTTPException)
async def http_exception_handler(request: Request, exc: HTTPException) -> JSONResponse:
    logging.error(exc.detail)
    return JSONResponse({"detail": exc.detail}, status_code=exc.status_code)


app.include_router(pow_router, prefix=API_PREFIX)
app.include_router(train_router, prefix=API_PREFIX)
app.include_router(inference_router, prefix=API_PREFIX)
app.include_router(api_router, prefix=API_PREFIX)