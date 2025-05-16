# src/pow/app/server.py

import logging
import os
from contextlib import asynccontextmanager

from fastapi import FastAPI

from pow.service.routes import router, API_PREFIX
from common.logger import setup_logger
from pow.service.manager import PowManager

logger = setup_logger(logging.getLogger("unicorn"))


@asynccontextmanager
async def lifespan(app: FastAPI):
    app.state.pow_manager: PowManager = PowManager()
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
    router,
    prefix=API_PREFIX
)
