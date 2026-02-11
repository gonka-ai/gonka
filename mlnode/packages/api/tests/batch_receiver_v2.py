"""Batch receiver for PoC v2 artifact-based callbacks."""
from typing import List, Optional
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel


class ArtifactModel(BaseModel):
    nonce: int
    vector_b64: str


class EncodingModel(BaseModel):
    dtype: str = "f16"
    k_dim: int = 12
    endian: str = "le"


class GeneratedBatch(BaseModel):
    """Artifact batch from /init/generate callbacks."""
    block_hash: str
    block_height: int
    public_key: str
    node_id: int
    artifacts: List[ArtifactModel]
    encoding: EncodingModel
    request_id: Optional[str] = None  # Present for /generate callbacks


class ValidationResult(BaseModel):
    """Validation result from /generate with validation."""
    request_id: Optional[str] = None
    block_hash: Optional[str] = None
    block_height: Optional[int] = None
    public_key: Optional[str] = None
    node_id: Optional[int] = None
    n_total: int
    n_mismatch: int
    mismatch_nonces: List[int]
    p_value: float
    fraud_detected: bool


app = FastAPI(title="PoC v2 Batch Receiver")
app.state.generated_batches: List[GeneratedBatch] = []
app.state.validation_results: List[ValidationResult] = []


@app.post("/generated")
async def receive_generated(batch: GeneratedBatch):
    """Receive artifact batch from PoC generation."""
    try:
        app.state.generated_batches.append(batch)
        return {"status": "OK", "artifacts_count": len(batch.artifacts)}
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))


@app.post("/validated")
async def receive_validated(result: ValidationResult):
    """Receive validation result from PoC validation."""
    try:
        app.state.validation_results.append(result)
        return {"status": "OK"}
    except Exception as e:
        raise HTTPException(status_code=400, detail=str(e))


@app.get("/generated")
async def get_generated():
    """Get all received artifact batches."""
    return {
        "count": len(app.state.generated_batches),
        "batches": [b.model_dump() for b in app.state.generated_batches]
    }


@app.get("/validated")
async def get_validated():
    """Get all received validation results."""
    return {
        "count": len(app.state.validation_results),
        "results": [r.model_dump() for r in app.state.validation_results]
    }


@app.post("/clear")
async def clear_all():
    """Clear all received data."""
    app.state.generated_batches.clear()
    app.state.validation_results.clear()
    return {"status": "OK", "message": "All data cleared"}


@app.get("/health")
async def health():
    """Health check endpoint."""
    return {"status": "OK"}
