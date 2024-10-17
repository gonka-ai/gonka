# src/pow/app/server.py

import logging
import os
from contextlib import asynccontextmanager

from fastapi import FastAPI

from pow.app.api.v0 import router as v0_router
from pow.app.api.v1 import router as v1_router
from pow.utils import setup_logger


logger = setup_logger(logging.getLogger("unicorn"))


@asynccontextmanager
async def lifespan(app: FastAPI):
    logger.info("App is starting...")
    yield
    logger.info("App is shutting down...")
    controller = app.state.controller
    if controller is not None:
        controller.stop()
        controller.terminate()


app = FastAPI(lifespan=lifespan)
app.state.controller = None
app.state.model_params_path = os.environ.get(
    "MODEL_PARAMS_PATH", "/app/resources/params.json"
)

app.include_router(
    v0_router,
    prefix="/api/v0"
)

app.include_router(
    v1_router,
    prefix="/api/v1"
)
