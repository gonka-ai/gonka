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

    @staticmethod
    def _try_sensor(fn, *args):
        """Missing sensor => None => the metric series is simply absent."""
        try:
            return fn(*args)
        except Exception:
            return None

    @staticmethod
    def _read_hbm_temp(handle):
        value = pynvml.nvmlDeviceGetFieldValues(
            handle, [pynvml.NVML_FI_DEV_MEMORY_TEMP])[0]
        temp = value.value.uiVal
        if value.nvmlReturn != pynvml.NVML_SUCCESS or not 0 < temp < 200:
            return None
        return temp

    # Schema v1 GPU fields: metric name -> reader(handle). Missing sensor or
    # unsupported call => None => no series (e.g. HBM temp / ECC on GDDR cards).
    # Throttle bitmask was renamed ThrottleReasons -> ClocksEventReasons in
    # newer NVML; HBM temp only exists via field values (no MEMORY member in
    # the temperature enum of nvidia-ml-py).
    _METRIC_READERS = {
        "mlnode_gpu_temp_core_celsius":
            lambda h: pynvml.nvmlDeviceGetTemperature(h, pynvml.NVML_TEMPERATURE_GPU),
        "mlnode_gpu_temp_hbm_celsius":
            lambda h: GPUManager._read_hbm_temp(h),
        "mlnode_gpu_power_draw_watts":
            lambda h: pynvml.nvmlDeviceGetPowerUsage(h) / 1000.0,
        "mlnode_gpu_power_limit_watts":
            lambda h: pynvml.nvmlDeviceGetEnforcedPowerLimit(h) / 1000.0,
        "mlnode_gpu_sm_clock_hertz":
            lambda h: pynvml.nvmlDeviceGetClockInfo(h, pynvml.NVML_CLOCK_SM) * 1e6,
        "mlnode_gpu_sm_clock_max_hertz":
            lambda h: pynvml.nvmlDeviceGetMaxClockInfo(h, pynvml.NVML_CLOCK_SM) * 1e6,
        "mlnode_gpu_clocks_event_reasons": lambda h: (
            getattr(pynvml, "nvmlDeviceGetCurrentClocksEventReasons", None)
            or pynvml.nvmlDeviceGetCurrentClocksThrottleReasons
        )(h),
        "mlnode_gpu_ecc_dbe_total":
            lambda h: pynvml.nvmlDeviceGetTotalEccErrors(
                h, pynvml.NVML_MEMORY_ERROR_TYPE_UNCORRECTED, pynvml.NVML_AGGREGATE_ECC),
        "mlnode_gpu_pcie_replay_total":
            lambda h: pynvml.nvmlDeviceGetPcieReplayCounter(h),
    }

    def collect_metrics(self) -> List[dict]:
        """Per-GPU raw fields for the metrics exporter (schema v1).

        Returns one dict per GPU: {"gpu_index", "gpu_model", "fields"} where
        fields holds only the sensors this environment actually exposes.
        """
        if not self._nvml_initialized:
            return []

        try:
            device_count = pynvml.nvmlDeviceGetCount()
        except Exception as e:
            logger.error(f"Error enumerating GPU devices for metrics: {e}")
            return []

        samples = []
        for i in range(device_count):
            handle = self._try_sensor(pynvml.nvmlDeviceGetHandleByIndex, i)
            if handle is None:
                continue
            name = self._try_sensor(pynvml.nvmlDeviceGetName, handle) or "Unknown"
            fields = {
                metric: float(value)
                for metric, reader in self._METRIC_READERS.items()
                if (value := self._try_sensor(reader, handle)) is not None
            }
            # One atomic memory snapshot feeding both fields (both-or-neither)
            mem_info = self._try_sensor(pynvml.nvmlDeviceGetMemoryInfo, handle)
            if mem_info is not None:
                fields["mlnode_gpu_memory_used_bytes"] = float(mem_info.used)
                fields["mlnode_gpu_memory_free_bytes"] = float(mem_info.free)
            samples.append({"gpu_index": str(i), "gpu_model": name, "fields": fields})

        return samples

    async def collect_metrics_async(self) -> List[dict]:
        return await asyncio.to_thread(self.collect_metrics)

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

