"""REST API routes for model management."""

from fastapi import APIRouter, HTTPException, Request, status
from typing import Dict
import os

from api.models.types import (
    Model,
    ModelStatusResponse,
    DownloadStartResponse,
    DeleteResponse,
    ModelListResponse,
    DiskSpaceInfo,
    ModelStatus,
)
from api.models.manager import ModelManager
from common.logger import create_logger

logger = create_logger(__name__)

router = APIRouter()


def get_model_manager(request: Request) -> ModelManager:
    """Get the ModelManager from app state."""
    return request.app.state.model_manager


def _is_offline_mode_enabled() -> bool:
    offline_value = os.getenv("HF_HUB_OFFLINE", "").strip().lower()
    return offline_value in ("true", "1", "yes")


@router.post(
    "/status",
    response_model=ModelStatusResponse,
    summary="Check model status",
    description="""Check if a model exists in cache with verification.
    
    Returns the current status of the model:
    - DOWNLOADED: Model is fully downloaded and verified
    - DOWNLOADING: Download is in progress (includes progress info)
    - NOT_FOUND: No trace of model in cache
    - PARTIAL: Some files exist but model is incomplete (e.g., failed or cancelled download)
    
    Example request:
    ```json
    {
        "hf_repo": "meta-llama/Llama-2-7b-hf",
        "hf_commit": "abc123def456"
    }
    ```
    
    Example response (downloaded):
    ```json
    {
        "model": {
            "hf_repo": "meta-llama/Llama-2-7b-hf",
            "hf_commit": "abc123def456"
        },
        "status": "DOWNLOADED",
        "progress": null,
        "error_message": null
    }
    ```
    
    Example response (downloading):
    ```json
    {
        "model": {
            "hf_repo": "meta-llama/Llama-2-7b-hf",
            "hf_commit": null
        },
        "status": "DOWNLOADING",
        "progress": {
            "start_time": 1728565234.123,
            "elapsed_seconds": 125.5
        },
        "error_message": null
    }
    ```
    """,
)
async def check_model_status(
    model: Model,
    request: Request
) -> ModelStatusResponse:
    """Check the status of a model in cache."""
    manager = get_model_manager(request)
    
    try:
        status = await manager.get_model_status_async(model)
        logger.info(f"Status check for {model.hf_repo}: {status.status}")
        return status
    except Exception as e:
        logger.error(f"Error checking model status: {e}")
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=f"Error checking model status: {str(e)}"
        )


@router.post(
    "/download",
    response_model=DownloadStartResponse,
    status_code=status.HTTP_202_ACCEPTED,
    responses={
        202: {"description": "Download started successfully"},
        409: {"description": "Model is already downloading"},
        429: {"description": "Too many concurrent downloads"},
    },
    summary="Start model download",
    description="""Start downloading a model asynchronously.
    
    The download runs in the background and can be tracked using the status endpoint.
    
    Constraints:
    - Maximum 3 concurrent downloads
    - Cannot start duplicate downloads for the same model
    - If model already exists, returns immediately with DOWNLOADED status
    - If HF_HUB_OFFLINE environment variable is set to true, returns IGNORED status instead of downloading
    
    Example request:
    ```json
    {
        "hf_repo": "meta-llama/Llama-2-7b-hf",
        "hf_commit": null
    }
    ```
    
    Example response:
    ```json
    {
        "task_id": "meta-llama/Llama-2-7b-hf:latest",
        "status": "DOWNLOADING",
        "model": {
            "hf_repo": "meta-llama/Llama-2-7b-hf",
            "hf_commit": null
        }
    }
    ```
    
    Example response (offline mode):
    ```json
    {
        "task_id": "meta-llama/Llama-2-7b-hf:latest:offline",
        "status": "IGNORED",
        "model": {
            "hf_repo": "meta-llama/Llama-2-7b-hf",
            "hf_commit": null
        }
    }
    ```
    """,
)
async def download_model(
    model: Model,
    request: Request
) -> DownloadStartResponse:
    """Start downloading a model."""
    manager = get_model_manager(request)
    
    # Check if offline mode is enabled - if so, acknowledge the request but don't download
    if _is_offline_mode_enabled():
        logger.info(f"HF_HUB_OFFLINE is enabled, skipping download for {model.hf_repo}")
        return DownloadStartResponse(
            task_id=f"{model.hf_repo}:{model.hf_commit or 'latest'}:offline",
            status="IGNORED",  # This indicates the request was acknowledged but not processed
            model=model
        )
    
    try:
        task_id = await manager.add_model(model)
        
        # Get current status to determine if already downloaded
        model_status = manager.get_model_status(model)
        
        logger.info(f"Download started for {model.hf_repo}, task_id: {task_id}")
        
        return DownloadStartResponse(
            task_id=task_id,
            status=model_status.status,
            model=model
        )
    except ValueError as e:
        error_msg = str(e)
        if "already downloading" in error_msg:
            raise HTTPException(
                status_code=status.HTTP_409_CONFLICT,
                detail=error_msg
            )
        elif "Maximum concurrent downloads" in error_msg:
            raise HTTPException(
                status_code=status.HTTP_429_TOO_MANY_REQUESTS,
                detail=error_msg
            )
        else:
            raise HTTPException(
                status_code=status.HTTP_400_BAD_REQUEST,
                detail=error_msg
            )
    except Exception as e:
        logger.error(f"Error starting download: {e}")
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=f"Error starting download: {str(e)}"
        )


@router.delete(
    "",
    response_model=DeleteResponse,
    summary="Delete model or cancel download",
    description="""Delete a model from cache or cancel an ongoing download.
    
    Behavior:
    - If a `hf_commit` is provided, only that specific revision is deleted.
    - If `hf_commit` is null, **all versions** of the model are deleted.
    - If model is currently downloading: cancels the download and cleans up partial files.
    - If model exists in cache: deletes it from cache.
    - If model not found: returns 404 error.
    
    Example request:
    ```json
    {
        "hf_repo": "meta-llama/Llama-2-7b-hf",
        "hf_commit": "abc123def456"
    }
    ```
    
    Example response (cancelled):
    ```json
    {
        "status": "cancelled",
        "model": {
            "hf_repo": "meta-llama/Llama-2-7b-hf",
            "hf_commit": "abc123def456"
        }
    }
    ```
    
    Example response (deleted):
    ```json
    {
        "status": "deleted",
        "model": {
            "hf_repo": "meta-llama/Llama-2-7b-hf",
            "hf_commit": "abc123def456"
        }
    }
    ```
    """,
)
async def delete_model(
    model: Model,
    request: Request
) -> DeleteResponse:
    """Delete a model from cache or cancel download."""
    manager = get_model_manager(request)
    
    try:
        result = await manager.delete_model(model)
        logger.info(f"Model {model.hf_repo} {result}")
        
        return DeleteResponse(
            status=result,
            model=model
        )
    except ValueError as e:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=str(e)
        )
    except Exception as e:
        logger.error(f"Error deleting model: {e}")
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=f"Error deleting model: {str(e)}"
        )


@router.get(
    "/list",
    response_model=ModelListResponse,
    summary="List cached models",
    description="""List all models currently in the HuggingFace cache.
    
    Returns all model revisions found in the cache directory with their status:
    - DOWNLOADED: Fully downloaded and verified
    - PARTIAL: Some files exist but model is incomplete
    
    Example response:
    ```json
    {
        "models": [
            {
                "model": {
                    "hf_repo": "meta-llama/Llama-2-7b-hf",
                    "hf_commit": "abc123def456"
                },
                "status": "DOWNLOADED"
            },
            {
                "model": {
                    "hf_repo": "microsoft/phi-2",
                    "hf_commit": "def789ghi012"
                },
                "status": "PARTIAL"
            }
        ]
    }
    ```
    """,
)
async def list_models(request: Request) -> ModelListResponse:
    """List all cached models."""
    manager = get_model_manager(request)
    
    try:
        models = await manager.list_models_async()
        logger.info(f"Listed {len(models)} models")
        
        return ModelListResponse(models=models)
    except Exception as e:
        logger.error(f"Error listing models: {e}")
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=f"Error listing models: {str(e)}"
        )


@router.get(
    "/space",
    response_model=DiskSpaceInfo,
    summary="Get disk space information",
    description="""Get information about disk space usage and availability.
    
    Returns:
    - Total size of HuggingFace cache in GB
    - Available disk space in GB
    - Cache directory path
    
    Example response:
    ```json
    {
        "cache_size_gb": 13.0,
        "available_gb": 465.66,
        "cache_path": "/root/.cache/hub"
    }
    ```
    """,
)
async def get_disk_space(request: Request) -> DiskSpaceInfo:
    """Get disk space information."""
    manager = get_model_manager(request)
    
    try:
        space_info = manager.get_disk_space()
        logger.info(
            f"Disk space: cache={space_info.cache_size_gb} GB, "
            f"available={space_info.available_gb} GB"
        )
        
        return space_info
    except Exception as e:
        logger.error(f"Error getting disk space: {e}")
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=f"Error getting disk space: {str(e)}"
        )

