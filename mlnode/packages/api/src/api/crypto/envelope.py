\
\
\
\
\
\
\
\
\
\
\
\
\
\
\
\
\
\
\
\
\
   

from __future__ import annotations

import base64
import hashlib
import os
from dataclasses import dataclass
from typing import Any

from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305
from pydantic import BaseModel, Field
from pyhpke import AEADId, CipherSuite, KDFId, KEMId
from pyhpke.kem_key_interface import KEMKeyInterface

ENVELOPE_VERSION = 1

KEM_ID = KEMId.DHKEM_X25519_HKDF_SHA256
KDF_ID = KDFId.HKDF_SHA256
AEAD_ID = AEADId.CHACHA20_POLY1305

EXPORT_LABEL_RESPONSE = b"gonka/devshard/response-key/v1"
EXPORT_KEY_LEN = 32
RESPONSE_NONCE_LEN = 12

AAD_LABEL_REQUEST = b"gonka/devshard/req/v1"
AAD_LABEL_RESPONSE = b"gonka/devshard/resp/v1"

                                                                        
                                                                     
HPKE_INFO = b"gonka/devshard/hpke-info/v1"


def make_suite() -> CipherSuite:
    return CipherSuite.new(KEM_ID, KDF_ID, AEAD_ID)


def _b64e(b: bytes) -> str:
    return base64.b64encode(b).decode("ascii")


def _b64d(s: str) -> bytes:
    return base64.b64decode(s.encode("ascii"))


def prompt_hash(plaintext: bytes) -> bytes:
                                                           
    return hashlib.sha256(plaintext).digest()


                                                                         
RECIPIENT_KEY_ID_BYTES = 8
RECIPIENT_KEY_ID_HEX_LEN = RECIPIENT_KEY_ID_BYTES * 2


def recipient_key_id(public_key_raw: bytes) -> str:
                                                                     
    if not public_key_raw:
        raise ValueError("public_key_raw must be non-empty")
    return hashlib.sha256(public_key_raw).digest()[:RECIPIENT_KEY_ID_BYTES].hex()


def build_request_aad(session_id: str, nonce: int, prompt_hash_bytes: bytes) -> bytes:
                                                                                       
    return b"\x00".join(
        [
            AAD_LABEL_REQUEST,
            session_id.encode("utf-8"),
            nonce.to_bytes(8, "big", signed=False),
            prompt_hash_bytes,
        ]
    )


def build_response_aad(session_id: str, nonce: int) -> bytes:
    return b"\x00".join(
        [
            AAD_LABEL_RESPONSE,
            session_id.encode("utf-8"),
            nonce.to_bytes(8, "big", signed=False),
        ]
    )


class EncryptedRequest(BaseModel):
                                              

    version: int = Field(default=ENVELOPE_VERSION, ge=1)
    session_id: str = Field(..., description="devshard session / escrow id")
    nonce: int = Field(..., ge=0, description="per-session monotonic nonce")
    recipient_key_id: str = Field(
        ...,
        min_length=RECIPIENT_KEY_ID_HEX_LEN,
        max_length=RECIPIENT_KEY_ID_HEX_LEN,
        pattern=r"^[0-9a-f]+$",
        description="required deterministic routing id = sha256(public_key)[:8] hex",
    )
    kem_id: int = Field(default=int(KEM_ID.value))
    kdf_id: int = Field(default=int(KDF_ID.value))
    aead_id: int = Field(default=int(AEAD_ID.value))
    enc: str = Field(..., description="HPKE encapsulated key (base64)")
    prompt_hash: str = Field(..., description="sha256(plaintext) (base64)")
    ciphertext: str = Field(..., description="HPKE-sealed body (base64)")


class EncryptedResponse(BaseModel):
                                               

    version: int = Field(default=ENVELOPE_VERSION, ge=1)
    session_id: str
    nonce: int = Field(..., ge=0)
    aead_id: int = Field(default=int(AEAD_ID.value))
    response_nonce: str = Field(..., description="AEAD nonce (base64)")
    ciphertext: str = Field(..., description="AEAD-sealed body (base64)")


@dataclass
class OpenedRequest:
\
\
                                        

    plaintext: bytes
    response_key: bytes
    session_id: str
    nonce: int


def _check_suite(req: EncryptedRequest) -> None:
    if req.version != ENVELOPE_VERSION:
        raise ValueError(f"unsupported envelope version: {req.version}")
    if (
        req.kem_id != int(KEM_ID.value)
        or req.kdf_id != int(KDF_ID.value)
        or req.aead_id != int(AEAD_ID.value)
    ):
        raise ValueError(
            "unsupported HPKE suite "
            f"(kem={req.kem_id}, kdf={req.kdf_id}, aead={req.aead_id})"
        )


def open_request(skr: KEMKeyInterface, req: EncryptedRequest) -> OpenedRequest:
\
\
                                             
    _check_suite(req)

    suite = make_suite()
    enc = _b64d(req.enc)
    ct = _b64d(req.ciphertext)
    ph = _b64d(req.prompt_hash)

    aad = build_request_aad(req.session_id, req.nonce, ph)
    recipient = suite.create_recipient_context(enc, skr, info=HPKE_INFO)
    plaintext = recipient.open(ct, aad=aad)

    response_key = recipient.export(EXPORT_LABEL_RESPONSE, EXPORT_KEY_LEN)
    return OpenedRequest(
        plaintext=plaintext,
        response_key=response_key,
        session_id=req.session_id,
        nonce=req.nonce,
    )


def seal_response(
    response_key: bytes,
    session_id: str,
    nonce: int,
    plaintext: bytes,
    *,
    response_nonce: bytes | None = None,
) -> EncryptedResponse:
\
                                                                      
    if len(response_key) != EXPORT_KEY_LEN:
        raise ValueError(f"response_key must be {EXPORT_KEY_LEN} bytes")
    if response_nonce is None:
        response_nonce = os.urandom(RESPONSE_NONCE_LEN)
    elif len(response_nonce) != RESPONSE_NONCE_LEN:
        raise ValueError(f"response_nonce must be {RESPONSE_NONCE_LEN} bytes")

    aead = ChaCha20Poly1305(response_key)
    aad = build_response_aad(session_id, nonce)
    ct = aead.encrypt(response_nonce, plaintext, aad)
    return EncryptedResponse(
        session_id=session_id,
        nonce=nonce,
        response_nonce=_b64e(response_nonce),
        ciphertext=_b64e(ct),
    )


def seal_request(
    pkr: KEMKeyInterface,
    session_id: str,
    nonce: int,
    plaintext: bytes,
    *,
    recipient_key_id_hex: str,
) -> tuple[EncryptedRequest, bytes]:
\
\
\
\
\
\
       
    if len(recipient_key_id_hex) != RECIPIENT_KEY_ID_HEX_LEN:
        raise ValueError(
            f"recipient_key_id_hex must be {RECIPIENT_KEY_ID_HEX_LEN} hex chars"
        )
    suite = make_suite()
    enc, sender = suite.create_sender_context(pkr, info=HPKE_INFO)
    ph = prompt_hash(plaintext)
    aad = build_request_aad(session_id, nonce, ph)
    ct = sender.seal(plaintext, aad=aad)
    response_key = sender.export(EXPORT_LABEL_RESPONSE, EXPORT_KEY_LEN)

    return (
        EncryptedRequest(
            session_id=session_id,
            nonce=nonce,
            recipient_key_id=recipient_key_id_hex,
            enc=_b64e(enc),
            prompt_hash=_b64e(ph),
            ciphertext=_b64e(ct),
        ),
        response_key,
    )


def open_response(response_key: bytes, resp: EncryptedResponse) -> bytes:
                                                                        
    if resp.version != ENVELOPE_VERSION:
        raise ValueError(f"unsupported response envelope version: {resp.version}")
    if resp.aead_id != int(AEAD_ID.value):
        raise ValueError(f"unsupported response AEAD: {resp.aead_id}")
    if len(response_key) != EXPORT_KEY_LEN:
        raise ValueError(f"response_key must be {EXPORT_KEY_LEN} bytes")

    aead = ChaCha20Poly1305(response_key)
    response_nonce = _b64d(resp.response_nonce)
    ct = _b64d(resp.ciphertext)
    aad = build_response_aad(resp.session_id, resp.nonce)
    return aead.decrypt(response_nonce, ct, aad)


def public_key_from_raw(raw: bytes) -> KEMKeyInterface:
                                                                    
    from pyhpke.keys.x25519_key import X25519Key

    if len(raw) != 32:
        raise ValueError(f"X25519 public key must be 32 bytes, got {len(raw)}")
    return X25519Key.from_public_bytes(raw)


def private_key_from_raw(raw: bytes) -> KEMKeyInterface:
                                                                     
    from pyhpke.keys.x25519_key import X25519Key

    if len(raw) != 32:
        raise ValueError(f"X25519 private key must be 32 bytes, got {len(raw)}")
    return X25519Key.from_private_bytes(raw)


def _safe_envelope_summary(req: EncryptedRequest) -> dict[str, Any]:
                                                           
    return {
        "version": req.version,
        "session_id": req.session_id,
        "nonce": req.nonce,
        "recipient_key_id": req.recipient_key_id,
        "kem_id": req.kem_id,
        "kdf_id": req.kdf_id,
        "aead_id": req.aead_id,
        "ciphertext_len": len(req.ciphertext),
    }
