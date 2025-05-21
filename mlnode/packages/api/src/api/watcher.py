from fastapi import FastAPI
from typing import List

from common.manager import IManager

import asyncio
import sys
import os

from common.logger import create_logger

logger = create_logger(__name__)

MAX_UNHEALTHY_COUNT = 3


async def watch_managers(
    app: FastAPI,
    managers: List[IManager],
    interval: int = 2
):
    unhealthy_counts = {manager: 0 for manager in managers}
    
    while True:
        await asyncio.sleep(interval)
        for manager in managers:
            if not manager.is_healthy():
                unhealthy_counts[manager] += 1
                logger.error(f"Manager {manager.__class__.__name__} is unhealthy (count: {unhealthy_counts[manager]}/{MAX_UNHEALTHY_COUNT})")
                
                if unhealthy_counts[manager] >= MAX_UNHEALTHY_COUNT:
                    logger.critical(f"Manager {manager.__class__.__name__} has been unhealthy {MAX_UNHEALTHY_COUNT} times in a row. Shutting down the application.")
                    manager.stop()
                    os._exit(1)
            else:
                if unhealthy_counts[manager] > 0:
                    logger.info(f"Manager {manager.__class__.__name__} is healthy again, resetting unhealthy count")
                    unhealthy_counts[manager] = 0
