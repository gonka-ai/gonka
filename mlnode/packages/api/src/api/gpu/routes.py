from fastapi import APIRouter, Request, HTTPException

from api.gpu.types import GPUDevicesResponse, DriverInfo

router = APIRouter()


@router.get("/devices", response_model=GPUDevicesResponse)
async def get_gpu_devices(request: Request) -> GPUDevicesResponse:
    """
    List all CUDA devices with current metrics.
    
    Returns empty list if no GPUs are present or NVML is not initialized.
    
    Example response with GPU:
    ```json
    {
      "devices": [
        {
          "index": 0,
          "name": "NVIDIA A100-SXM4-40GB",
          "total_memory_mb": 40960,
          "free_memory_mb": 35000,
          "used_memory_mb": 5960,
          "utilization_percent": 45,
          "temperature_c": 52,
          "is_available": true,
          "error_message": null
        }
      ],
      "count": 1
    }
    ```
    
    Example response without GPU:
    ```json
    {
      "devices": [],
      "count": 0
    }
    ```
    """
    gpu_manager = request.app.state.gpu_manager
    devices = await gpu_manager.get_devices_async()
    return GPUDevicesResponse(devices=devices, count=len(devices))


@router.get("/driver", response_model=DriverInfo)
async def get_driver_info(request: Request) -> DriverInfo:
    """
    Get CUDA driver version information from NVML.
    
    Note: cuda_driver_version is the maximum CUDA version supported by the 
    installed NVIDIA driver, not the CUDA toolkit version.
    
    Example response:
    ```json
    {
      "driver_version": "535.104.05",
      "cuda_driver_version": "12.2",
      "nvml_version": "12.535.104"
    }
    ```
    
    Raises:
        HTTPException: 503 if NVML is not initialized or driver info cannot be retrieved.
    """
    gpu_manager = request.app.state.gpu_manager
    try:
        return await gpu_manager.get_driver_info_async()
    except RuntimeError as e:
        raise HTTPException(status_code=503, detail=str(e))

