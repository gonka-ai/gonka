import asyncio
import shlex
from typing import Optional, List
from enum import Enum

from api.inference.vllm.runner import VLLMRunner
from common.logger import create_logger


logger = create_logger(__name__)


class VLLMStatus(str, Enum):
    STOPPED = "stopped"
    STARTING = "starting"
    RUNNING = "running"
    STOPPING = "stopping"
    ERROR = "error"


class VLLMManager:
    def __init__(
        self,
        model: Optional[str] = None,
        dtype: str = "auto",
        additional_args: Optional[List[str]] = None,
    ):
        self.model = model
        self.dtype = dtype
        self.additional_args = self._normalize_args(additional_args)
        self.runner: Optional[VLLMRunner] = None
        self.status = VLLMStatus.STOPPED
        self._lock = asyncio.Lock()

    @staticmethod
    def _normalize_args(args: Optional[List[str]]) -> List[str]:
        normalized: List[str] = []
        if not args:
            return normalized
        for arg in args:
            if not arg:
                continue
            if isinstance(arg, str):
                try:
                    parts = shlex.split(arg)
                except ValueError:
                    parts = [arg]
            else:
                parts = [str(arg)]
            for part in parts:
                if part:
                    normalized.append(part)
        return normalized

    def set_model(self, model: str, dtype: str = "auto", additional_args: Optional[List[str]] = None):
        if self.is_running():
            raise RuntimeError("Cannot change model while vLLM is running")
        self.model = model
        self.dtype = dtype
        self.additional_args = self._normalize_args(additional_args)

    async def start(self):
        async with self._lock:
            if self.runner is not None and self.is_running():
                logger.info("vLLM already running, verifying availability")
                await self._wait_until_available()
                self.status = VLLMStatus.RUNNING
                return

            if not self.model:
                raise RuntimeError("Model not set")

            self.status = VLLMStatus.STARTING
            logger.info(f"Starting vLLM with model: {self.model}")

            self.runner = VLLMRunner(
                model=self.model,
                dtype=self.dtype,
                additional_args=self.additional_args
            )

            try:
                loop = asyncio.get_event_loop()
                await loop.run_in_executor(None, self.runner.start)
                await self._wait_until_available()
                self.status = VLLMStatus.RUNNING
                logger.info("vLLM is up and running")
            except Exception as e:
                self.status = VLLMStatus.ERROR
                logger.error(f"Failed to start vLLM: {e}")
                self.runner = None
                raise

    async def stop(self):
        async with self._lock:
            if self.runner is None:
                logger.warning("vLLM stop called but no runner exists")
                self.status = VLLMStatus.STOPPED
                await self._cleanup_gpu_async()
                return

            logger.info("Stopping vLLM process...")
            self.status = VLLMStatus.STOPPING
            
            try:
                loop = asyncio.get_event_loop()
                await loop.run_in_executor(None, self.runner.stop)
            except Exception as e:
                logger.error(f"Error stopping vLLM: {e}")
            
            self.runner = None
            
            await self._cleanup_gpu_async()
            
            self.status = VLLMStatus.STOPPED
            logger.info("vLLM process stopped")

    def is_running(self) -> bool:
        return self.runner is not None and self.runner.is_running()

    async def _wait_until_available(self, timeout: float = 60.0, interval: float = 0.5):
        loop = asyncio.get_event_loop()
        deadline = loop.time() + timeout
        while loop.time() < deadline:
            if await self.is_available():
                return
            await asyncio.sleep(interval)
        raise RuntimeError("vLLM did not become available in time")

    async def is_available(self) -> bool:
        if not self.is_running():
            return False
        loop = asyncio.get_event_loop()
        return await loop.run_in_executor(None, self.runner.is_available)

    async def _cleanup_gpu_async(self):
        """Async GPU cleanup with delay to ensure proper memory release."""
        logger.info("Cleaning up GPU memory...")
        loop = asyncio.get_event_loop()
        
        def _cleanup():
            import torch
            import gc
            
            torch.cuda.synchronize()
            torch.cuda.empty_cache()
            gc.collect()
            
            torch.cuda.synchronize()
            torch.cuda.empty_cache()
        
        await loop.run_in_executor(None, _cleanup)
        
        await asyncio.sleep(2)
        logger.info("GPU cleanup complete")
    
    def get_status(self) -> VLLMStatus:
        return self.status
    
    @property
    def VLLM_HOST(self):
        return VLLMRunner.VLLM_HOST
    
    @property
    def VLLM_PORT(self):
        return VLLMRunner.VLLM_PORT
    
    @property
    def VLLM_API_PORT(self):
        return VLLMRunner.VLLM_PORT + 1
