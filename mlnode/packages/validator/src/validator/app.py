import os
from contextlib import asynccontextmanager

from fastapi import FastAPI
import uvicorn

from common.logger import create_logger
from api.models.manager import ModelManager
from api.proxy import start_vllm_proxy, stop_vllm_proxy, ProxyMiddleware
from validator.vllm_manager import VLLMManager
from validator.routes import router, init_managers

logger = create_logger(__name__)


@asynccontextmanager
async def lifespan(app: FastAPI):
    logger.info("Starting Validator App")

    vllm_manager = VLLMManager()
    model_manager = ModelManager()

    init_managers(vllm_manager, model_manager)

    await start_vllm_proxy()
    logger.info("vLLM proxy started")

    logger.info("Registered routes:")
    for route in app.routes:
        methods = getattr(route, "methods", None)
        path = getattr(route, "path", None)
        if methods and path:
            logger.info(f"  {methods} {path}")

    yield

    logger.info("Shutting down Validator App")

    if vllm_manager.is_running():
        await vllm_manager.stop()

    await stop_vllm_proxy()
    logger.info("vLLM proxy stopped")

    logger.info("Validator App shutdown complete")


app = FastAPI(
    title="MLNode Validator API",
    description="Validator service with vLLM integration and request queuing",
    version="0.1.0"
)

app.add_middleware(ProxyMiddleware)

app.include_router(router, prefix="/api/v1")
app.router.lifespan_context = lifespan


@app.get("/")
async def root():
    return {
        "service": "mlnode-validator",
        "version": "0.1.0",
        "status": "running"
    }


def main():
    port = int(os.getenv("VALIDATOR_PORT", 8080))
    host = os.getenv("VALIDATOR_HOST", "0.0.0.0")

    logger.info(f"Starting validator service on {host}:{port}")

    uvicorn.run(
        "validator.app:app",
        host=host,
        port=port,
        log_level="info"
    )


if __name__ == "__main__":
    main()
