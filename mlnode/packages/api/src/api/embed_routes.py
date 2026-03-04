from __future__ import annotations

import threading
from typing import List

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel

from common.logger import create_logger

logger = create_logger(__name__)

router = APIRouter()

# Thread-safe lazy singleton for the embedding model.
# all-MiniLM-L6-v2 is 22 MB, loads in ~1 s, inference ~2 ms/query on CPU.
# fastembed downloads the model on first use to ~/.cache/fastembed.
_model_lock = threading.Lock()
_model = None
_MODEL_NAME = "sentence-transformers/all-MiniLM-L6-v2"
_DIMENSIONS = 384


def _get_model():
    global _model
    if _model is not None:
        return _model
    with _model_lock:
        if _model is None:
            try:
                from fastembed import TextEmbedding  # type: ignore
                _model = TextEmbedding(model_name=_MODEL_NAME)
                logger.info(f"Loaded embedding model {_MODEL_NAME}")
            except Exception as e:
                logger.error(f"Failed to load embedding model: {e}")
                raise
    return _model


class EmbedRequest(BaseModel):
    # Canonical prompt payload bytes encoded as UTF-8 string.
    # Same bytes as used for ComputePromptHash on the chain side.
    text: str


class EmbedResponse(BaseModel):
    embedding: List[float]
    dimensions: int
    model: str


@router.post("/embed", response_model=EmbedResponse)
async def embed(request: EmbedRequest):
    """
    Compute a 384-dimensional all-MiniLM-L6-v2 embedding for the given text.

    Used by the decentralized-api SemanticCache for nearest-neighbour lookup
    in InMemoryCacheStore before dispatching inference to GPU nodes.  The endpoint
    is always available (independent of inference/PoC state) because it runs
    a CPU-only sentence-transformer model.
    """
    if not request.text:
        raise HTTPException(status_code=400, detail="text must not be empty")

    try:
        model = _get_model()
        # fastembed returns a generator of numpy arrays
        embeddings = list(model.embed([request.text]))
        if not embeddings:
            raise HTTPException(status_code=500, detail="embedding model returned no result")
        vec = embeddings[0].tolist()
        return EmbedResponse(embedding=vec, dimensions=len(vec), model=_MODEL_NAME)
    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Embedding failed: {e}")
        raise HTTPException(status_code=500, detail=f"embedding failed: {e}")
