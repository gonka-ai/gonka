import asyncio

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
from api.watcher import watch_managers
from api.proxy import ProxyMiddleware, start_vllm_proxy, stop_vllm_proxy, setup_vllm_proxy, start_backward_compatibility, stop_backward_compatibility


WATCH_INTERVAL = 2


@asynccontextmanager
async def lifespan(app: FastAPI):
    app.state.service_state = ServiceState.STOPPED
    app.state.pow_manager = PowManager()
    app.state.inference_manager = InferenceManager()
    app.state.train_manager = TrainManager()

    await start_vllm_proxy()

    monitor_task = asyncio.create_task(
        watch_managers(
            app,
            [
                app.state.pow_manager,
                app.state.inference_manager,
                app.state.train_manager,
            ],
            interval=WATCH_INTERVAL
        )
    )

    yield
    
    if app.state.pow_manager.is_running():
        app.state.pow_manager.stop()
    if app.state.inference_manager.is_running():
        app.state.inference_manager.stop()
    if app.state.train_manager.is_running():
        app.state.train_manager.stop()

    await stop_vllm_proxy()
    await stop_backward_compatibility()

    monitor_task.cancel()
    try:
        await monitor_task
    except asyncio.CancelledError:
        pass


app = FastAPI(lifespan=lifespan)

app.add_middleware(ProxyMiddleware)

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
