import os
import base64
import hashlib

from fastapi import Request, HTTPException, Security
from fastapi.security import APIKeyHeader
from ecdsa import VerifyingKey, SECP256k1

SIGNER_PUBKEY = os.environ.get("SIGNER_PUBKEY", "")
signature_header = APIKeyHeader(name="X-Signature", auto_error=False)

def _load_pubkey(pubkey_b64: str) -> VerifyingKey:
    raw = base64.b64decode(pubkey_b64)
    if len(raw) == 33:
        return VerifyingKey.from_string(raw, curve=SECP256k1, valid_encodings=["compressed"])
    return VerifyingKey.from_string(raw, curve=SECP256k1)

async def verify_signature(
    request: Request,
    signature: str | None = Security(signature_header),
):
    if not SIGNER_PUBKEY:
        return
    if not signature:
        raise HTTPException(status_code=403, detail="Missing signature")
    try:
        body = await request.body()
        vk = _load_pubkey(SIGNER_PUBKEY)
        vk.verify(base64.b64decode(signature), body, hashfunc=hashlib.sha256)
    except Exception:
        raise HTTPException(status_code=403, detail="Invalid signature")
