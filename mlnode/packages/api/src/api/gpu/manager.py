import asyncio
import logging
from typing import List

import pynvml

from api.gpu.types import GPUDevice, DriverInfo

logger = logging.getLogger(__name__)


class GPUManager:
    """Minimalistic GPU manager for monitoring CUDA devices using pynvml."""

    def __init__(self):
        """Initialize the GPU manager and pynvml library."""
        self._nvml_initialized = False
        self._init_nvml()

    def _init_nvml(self):
        """Initialize pynvml library for GPU monitoring."""
        try:
            pynvml.nvmlInit()
            self._nvml_initialized = True
            device_count = pynvml.nvmlDeviceGetCount()
            logger.info(f"NVML initialized successfully. Found {device_count} GPU(s)")
        except Exception as e:
            logger.warning(f"NVML initialization failed: {e}. GPU features disabled.")

    def _shutdown_nvml(self):
        """Cleanup pynvml library on shutdown."""
        if self._nvml_initialized:
            try:
                pynvml.nvmlShutdown()
                logger.info("NVML shutdown successfully")
            except Exception as e:
                logger.error(f"Error during NVML shutdown: {e}")

    def is_cuda_available(self) -> bool:
        """Check if CUDA is available."""
        return self._nvml_initialized
    
    async def is_cuda_available_async(self) -> bool:
        return await asyncio.to_thread(self.is_cuda_available)

    def get_devices(self) -> List[GPUDevice]:
        """
        Query all GPU devices with current metrics.
        
        Returns:
            List of GPUDevice objects with current metrics.
            Returns empty list if NVML not initialized or no GPUs detected.
        """
        if not self._nvml_initialized:
            logger.debug("NVML not initialized, returning empty device list")
            return []

        try:
            device_count = pynvml.nvmlDeviceGetCount()
            devices = []

            for i in range(device_count):
                try:
                    handle = pynvml.nvmlDeviceGetHandleByIndex(i)
                    name = pynvml.nvmlDeviceGetName(handle)
                    
                    # Try to get memory info
                    try:
                        mem_info = pynvml.nvmlDeviceGetMemoryInfo(handle)
                        total_memory_mb = mem_info.total // (1024 * 1024)
                        free_memory_mb = mem_info.free // (1024 * 1024)
                        used_memory_mb = mem_info.used // (1024 * 1024)
                    except Exception as e:
                        logger.error(f"Error querying memory for GPU device {i}: {e}")
                        total_memory_mb = None
                        free_memory_mb = None
                        used_memory_mb = None

                    # Try to get utilization
                    try:
                        utilization = pynvml.nvmlDeviceGetUtilizationRates(handle)
                        utilization_percent = utilization.gpu
                    except Exception as e:
                        logger.error(f"Error querying utilization for GPU device {i}: {e}")
                        utilization_percent = None

                    # Try to get temperature
                    try:
                        temperature_c = pynvml.nvmlDeviceGetTemperature(
                            handle, pynvml.NVML_TEMPERATURE_GPU
                        )
                    except Exception as e:
                        logger.error(f"Error querying temperature for GPU device {i}: {e}")
                        temperature_c = None

                    device = GPUDevice(
                        index=i,
                        name=name,
                        total_memory_mb=total_memory_mb,
                        free_memory_mb=free_memory_mb,
                        used_memory_mb=used_memory_mb,
                        utilization_percent=utilization_percent,
                        temperature_c=temperature_c,
                        is_available=True,
                        error_message=None
                    )
                    devices.append(device)

                except Exception as e:
                    logger.error(f"Error querying GPU device {i}: {e}")
                    # Create a device entry with error information
                    device = GPUDevice(
                        index=i,
                        name="Unknown",
                        is_available=False,
                        error_message=str(e)
                    )
                    devices.append(device)

            return devices

        except Exception as e:
            logger.error(f"Error enumerating GPU devices: {e}")
            return []
    
    async def get_devices_async(self) -> List[GPUDevice]:
        return await asyncio.to_thread(self.get_devices)

    def get_driver_info(self) -> DriverInfo:
        """
        Get CUDA driver version information from NVML.
        
        Returns:
            DriverInfo object with driver and CUDA version information.
        
        Raises:
            RuntimeError: If NVML is not initialized or driver info cannot be retrieved.
        """
        if not self._nvml_initialized:
            raise RuntimeError("NVML not initialized. GPU features are not available.")

        try:
            # Get driver version
            driver_version = pynvml.nvmlSystemGetDriverVersion()
            
            # Get CUDA driver version (max CUDA supported by driver)
            cuda_version = pynvml.nvmlSystemGetCudaDriverVersion()
            # Convert from integer (e.g., 12020) to string (e.g., "12.2")
            cuda_major = cuda_version // 1000
            cuda_minor = (cuda_version % 1000) // 10
            cuda_driver_version = f"{cuda_major}.{cuda_minor}"
            
            # Get NVML version
            nvml_version = pynvml.nvmlSystemGetNVMLVersion()
            
            return DriverInfo(
                driver_version=driver_version,
                cuda_driver_version=cuda_driver_version,
                nvml_version=nvml_version
            )

        except Exception as e:
            logger.error(f"Error querying driver info: {e}")
            raise RuntimeError(f"Failed to query driver information: {e}")
    
    async def get_driver_info_async(self) -> DriverInfo:
        return await asyncio.to_thread(self.get_driver_info)

