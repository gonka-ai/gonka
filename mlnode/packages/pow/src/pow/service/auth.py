import os
import base64
import hashlib

SIGNER_PUBKEY = os.environ.get("SIGNER_PUBKEY", "")

def verify_signature(body: bytes, signature_b64: str) -> bool:
    if not SIGNER_PUBKEY:
        return True
    if not signature_b64:
        return False
    try:
        from ecdsa import VerifyingKey, SECP256k1
        pubkey_bytes = base64.b64decode(SIGNER_PUBKEY)
        signature = base64.b64decode(signature_b64)
        message_hash = hashlib.sha256(body).digest()
        vk = VerifyingKey.from_string(pubkey_bytes, curve=SECP256k1)
        return vk.verify(signature, message_hash, hashfunc=hashlib.sha256)
    except Exception:
        return False

