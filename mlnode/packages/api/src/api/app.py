import asyncio

from fastapi import FastAPI, Depends
from contextlib import asynccontextmanager

from api.inference.manager import InferenceManager
from api.inference.routes import router as inference_router
from api.inference.pow_v2_routes import router as pow_v2_router

from api.models.manager import ModelManager
from api.models.routes import router as models_router

from api.gpu.manager import GPUManager
from api.gpu.routes import router as gpu_router

from zeroband.service.manager import TrainManager
from zeroband.service.routes import router as train_router

from pow.service.manager import PowManager
from pow.service.routes import router as pow_router

from api.health import router as health_router

from api.service_management import (
    ServiceState,
    check_service_conflicts,
    API_PREFIX
)
from api.routes import router as api_router
from api.metrics.routes import router as metrics_router, metrics_enabled
from api.metrics.allowlist import SCHEMA_VERSION, VLLM_ALLOWLIST
from api.metrics import xid_source
from api.watcher import watch_managers
from api.proxy import ProxyMiddleware, start_vllm_proxy, stop_vllm_proxy, setup_vllm_proxy, start_backward_compatibility, stop_backward_compatibility

from common.logger import create_logger

logger = create_logger(__name__)

WATCH_INTERVAL = 2


@asynccontextmanager
async def lifespan(app: FastAPI):
    # Honesty mechanics: operators must see the export mode in the startup log
    if metrics_enabled():
        logger.info(
            f"metrics export: GONKA_METRICS=full, schema v{SCHEMA_VERSION}, "
            f"{len(VLLM_ALLOWLIST)} vLLM families + GPU/host"
        )
    else:
        logger.info("metrics export: GONKA_METRICS=off, /api/v1/metrics disabled")

    app.state.service_state = ServiceState.STOPPED
    app.state.pow_manager = PowManager()
    app.state.inference_manager = InferenceManager()
    app.state.train_manager = TrainManager()
    app.state.model_manager = ModelManager()
    app.state.gpu_manager = GPUManager()

    if metrics_enabled() and app.state.gpu_manager.is_cuda_available():
        xid_source.start()

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
        # Use async stop in async context to avoid blocking event loop
        await app.state.inference_manager._async_stop()
    if app.state.train_manager.is_running():
        app.state.train_manager.stop()

    xid_source.stop()
    app.state.gpu_manager._shutdown_nvml()

    await stop_vllm_proxy()
    await stop_backward_compatibility()

    monitor_task.cancel()
    try:
        await monitor_task
    except asyncio.CancelledError:
        pass


app = FastAPI(lifespan=lifespan)

app.include_router(health_router)

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

# PoC v2 routes work when inference (vLLM) is running - no conflict check needed
app.include_router(
    pow_v2_router,
    prefix=API_PREFIX,
    tags=["PoC v2"],
)

app.include_router(
    api_router,
    prefix=API_PREFIX,
    tags=["API"],
)

# Always registered; GONKA_METRICS=off makes the handler answer 404 so a
# disabled node looks identical to a pre-metrics image (no conflict check:
# metrics must work in any service state).
app.include_router(
    metrics_router,
    prefix=API_PREFIX,
    tags=["Metrics"],
)

app.include_router(
    models_router,
    prefix=API_PREFIX + "/models",
    tags=["Models"],
)

app.include_router(
    gpu_router,
    prefix=API_PREFIX + "/gpu",
    tags=["GPU"],
)
