from fastapi import APIRouter, HTTPException, Request, Response
from pydantic import BaseModel, Field
from typing import Optional, List
from datetime import datetime

from validator.vllm_manager import VLLMManager, VLLMStatus
from api.models.manager import ModelManager
from api.models.types import Model
from common.logger import create_logger


logger = create_logger(__name__)
router = APIRouter()


class DownloadModelRequest(BaseModel):
    repo_id: str = Field(..., description="HuggingFace model repository ID")
    revision: Optional[str] = Field(None, description="Model revision/commit hash")


class DownloadModelResponse(BaseModel):
    status: str
    repo_id: str
    path: str


class SetModelRequest(BaseModel):
    model: str = Field(..., description="Model path or HuggingFace repo ID")
    dtype: str = Field(default="auto", description="Data type for model weights")
    additional_args: List[str] = Field(default_factory=list, description="Additional vLLM arguments")


class SetModelResponse(BaseModel):
    status: str
    model: str


class StartVLLMResponse(BaseModel):
    status: str
    message: str


class StopVLLMResponse(BaseModel):
    status: str
    message: str


class HealthResponse(BaseModel):
    status: str
    vllm_status: str
    vllm_available: bool


class CacheInfoResponse(BaseModel):
    cache_dir: str
    repos: List[dict]
    total_size: int


vllm_manager: Optional[VLLMManager] = None
model_manager: Optional[ModelManager] = None


def init_managers(vm: VLLMManager, mm: ModelManager):
    global vllm_manager, model_manager
    vllm_manager = vm
    model_manager = mm


@router.post("/models/download", response_model=DownloadModelResponse)
async def download_model(request: DownloadModelRequest):
    if model_manager is None:
        raise HTTPException(status_code=500, detail="Model manager not initialized")
    
    try:
        model = Model(hf_repo=request.repo_id, hf_commit=request.revision)
        task_id = await model_manager.add_model(model)
        
        return DownloadModelResponse(
            status="success",
            repo_id=request.repo_id,
            path=task_id
        )
    except Exception as e:
        logger.error(f"Failed to download model: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@router.post("/models/set", response_model=SetModelResponse)
async def set_model(request: SetModelRequest):
    if vllm_manager is None:
        raise HTTPException(status_code=500, detail="vLLM manager not initialized")
    
    try:
        vllm_manager.set_model(
            model=request.model,
            dtype=request.dtype,
            additional_args=request.additional_args
        )
        return SetModelResponse(
            status="success",
            model=request.model
        )
    except Exception as e:
        logger.error(f"Failed to set model: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@router.post("/vllm/start", response_model=StartVLLMResponse)
async def start_vllm():
    if vllm_manager is None:
        raise HTTPException(status_code=500, detail="vLLM manager not initialized")
    
    try:
        await vllm_manager.start()
        return StartVLLMResponse(
            status="success",
            message="vLLM started successfully"
        )
    except Exception as e:
        logger.error(f"Failed to start vLLM: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@router.post("/vllm/stop", response_model=StopVLLMResponse)
async def stop_vllm():
    if vllm_manager is None:
        raise HTTPException(status_code=500, detail="vLLM manager not initialized")
    
    try:
        await vllm_manager.stop()
        return StopVLLMResponse(
            status="success",
            message="vLLM stopped successfully"
        )
    except Exception as e:
        logger.error(f"Failed to stop vLLM: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@router.get("/health", response_model=HealthResponse)
async def health_check():
    if vllm_manager is None:
        raise HTTPException(status_code=500, detail="Managers not initialized")
    
    vllm_available = await vllm_manager.is_available()
    
    return HealthResponse(
        status="healthy",
        vllm_status=vllm_manager.get_status().value,
        vllm_available=vllm_available
    )


@router.get("/models/cache", response_model=CacheInfoResponse)
async def get_cache_info():
    if model_manager is None:
        raise HTTPException(status_code=500, detail="Model manager not initialized")
    
    try:
        from huggingface_hub import scan_cache_dir
        cache_info = scan_cache_dir(model_manager.cache_dir)
        repos = [
            {
                "repo_id": repo.repo_id,
                "size_on_disk": repo.size_on_disk,
                "nb_files": repo.nb_files,
            }
            for repo in cache_info.repos
        ]
        return CacheInfoResponse(
            cache_dir=model_manager.cache_dir,
            repos=repos,
            total_size=sum(repo.size_on_disk for repo in cache_info.repos)
        )
    except Exception as e:
        logger.error(f"Error getting cache info: {e}")
        raise HTTPException(status_code=500, detail=str(e))
