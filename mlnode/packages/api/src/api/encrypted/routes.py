\
\
\
\
\
\
   

from __future__ import annotations

import base64
from typing import Any

import httpx
import json as _json                                            
from fastapi import APIRouter, HTTPException, Request
from pydantic import BaseModel
from pyhpke.exceptions import OpenError

import api.proxy as proxy_module
from api.crypto import (
    ENVELOPE_VERSION,
    EncryptedRequest,
    EncryptedResponse,
    open_request,
    seal_response,
)
from api.crypto.session_store import NonceReplayError, SessionNonceStore
from api.crypto.key_provider import KeyProvider
from common.logger import create_logger

logger = create_logger(__name__)

router = APIRouter()

VLLM_CHAT_PATH = "/v1/chat/completions"
                                                                     
                                          
VLLM_READ_TIMEOUT_SECONDS = 900.0
VLLM_CONNECT_TIMEOUT_SECONDS = 10.0
VLLM_WRITE_TIMEOUT_SECONDS = 30.0
VLLM_POOL_TIMEOUT_SECONDS = 10.0
ENVELOPE_VERSION_LABEL = f"v{ENVELOPE_VERSION}"


class IdentityResponse(BaseModel):
\
\
\
\
\
       

    provider: str                                                 
    envelope_version: str = ENVELOPE_VERSION_LABEL                            
    public_key: str                                       
    key_id: str = ""                                               
    quote: str | None = None                                           
    measurement: str = ""                                         
    extra: dict[str, Any] = {}                                               


def _get_key_provider(request: Request) -> KeyProvider:
\
                                                   
    provider: KeyProvider | None = getattr(request.app.state, "key_provider", None)
    if provider is None:
        raise HTTPException(
            status_code=503,
            detail="encrypted endpoint is not enabled on this node",
        )
    return provider


def _get_nonce_store(request: Request) -> SessionNonceStore:
\
                                                                 
    store: SessionNonceStore | None = getattr(request.app.state, "session_nonce_store", None)
    if store is None:
        raise HTTPException(
            status_code=503,
            detail="encrypted endpoint is misconfigured: session_nonce_store missing",
        )
    return store


@router.get("/identity", response_model=IdentityResponse)
async def get_identity(request: Request) -> IdentityResponse:
    provider = _get_key_provider(request)
    identity = provider.identity()
                                                                    
                                                              
    extra = dict(identity.extra)
    if identity.measurements:
        extra.setdefault("measurements", identity.measurements)
    primary_measurement = ""
    if identity.measurements:
                                                                                    
        if "mrtd" in identity.measurements:
            primary_measurement = identity.measurements["mrtd"]
        else:
            primary_measurement = identity.measurements[sorted(identity.measurements)[0]]
    return IdentityResponse(
        provider=identity.provider,
        envelope_version=ENVELOPE_VERSION_LABEL,
        public_key=base64.b64encode(identity.public_key).decode("ascii"),
        key_id=provider.public_key_id(),
        quote=base64.b64encode(identity.quote).decode("ascii") if identity.quote else None,
        measurement=primary_measurement,
        extra=extra,
    )


async def _call_vllm(plaintext: bytes) -> bytes:
\
                                                    
    if proxy_module.vllm_client is None:
        raise HTTPException(status_code=503, detail="vLLM client not initialised")

    try:
        body = _json.loads(plaintext)
    except _json.JSONDecodeError as e:
        raise HTTPException(status_code=400, detail=f"plaintext is not valid JSON: {e}")

    if not isinstance(body, dict):
        raise HTTPException(status_code=400, detail="plaintext must be a JSON object")

                                                                
    if body.get("stream"):
        raise HTTPException(
            status_code=400,
            detail="stream=true is not supported on the encrypted endpoint yet",
        )

    try:
        port = await proxy_module._pick_vllm_backend()
    except RuntimeError:
        raise HTTPException(status_code=503, detail="no vLLM backend available")

    url = f"http://{proxy_module.VLLM_HOST}:{port}{VLLM_CHAT_PATH}"
    try:
        upstream = await proxy_module.vllm_client.post(
            url,
            json=body,
            timeout=httpx.Timeout(
                connect=VLLM_CONNECT_TIMEOUT_SECONDS,
                write=VLLM_WRITE_TIMEOUT_SECONDS,
                read=VLLM_READ_TIMEOUT_SECONDS,
                pool=VLLM_POOL_TIMEOUT_SECONDS,
            ),
        )
    except httpx.HTTPError as exc:
        logger.exception("vLLM call failed during encrypted inference: %s", exc)
        raise HTTPException(status_code=502, detail="vLLM upstream failure") from exc
    finally:
        await proxy_module._release_vllm_backend(port)

    if upstream.status_code >= 400:
        raise HTTPException(
            status_code=upstream.status_code,
            detail=upstream.text,
        )

    return upstream.content


@router.post("/chat/completions", response_model=EncryptedResponse)
async def encrypted_chat_completions(
    request: Request,
    payload: EncryptedRequest,
) -> EncryptedResponse:
\
\
\
\
\
       
    provider = _get_key_provider(request)
    nonce_store = _get_nonce_store(request)

    own_key_id = provider.public_key_id()
    if payload.recipient_key_id != own_key_id:
        logger.info(
            "encrypted request routed to wrong key: session=%s nonce=%d hint=%s own=%s",
            payload.session_id,
            payload.nonce,
            payload.recipient_key_id,
            own_key_id,
        )
        raise HTTPException(
            status_code=421,
            detail=(
                f"envelope recipient_key_id={payload.recipient_key_id} "
                f"does not match this node's key_id={own_key_id}"
            ),
        )

    try:
        opened = open_request(provider.private_key(), payload)
    except OpenError as e:
        logger.warning(
            "encrypted request failed to open with matching key_id: session=%s nonce=%d err=%s",
            payload.session_id,
            payload.nonce,
            e,
        )
        raise HTTPException(
            status_code=400,
            detail="envelope failed to open (corrupt or tampered)",
        )
    except ValueError as e:
        logger.warning(
            "encrypted request envelope invalid: session=%s nonce=%d err=%s",
            payload.session_id,
            payload.nonce,
            e,
        )
        raise HTTPException(status_code=400, detail=str(e))

    try:
        nonce_store.check_and_record(opened.session_id, opened.nonce)
    except NonceReplayError as e:
        raise HTTPException(status_code=409, detail=str(e))

    response_bytes = await _call_vllm(opened.plaintext)

    sealed = seal_response(
        opened.response_key,
        opened.session_id,
        opened.nonce,
        response_bytes,
    )
    return sealed
