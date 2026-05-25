                                                                     

from __future__ import annotations

from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey
from pyhpke.kem_key_interface import KEMKeyInterface
from pyhpke.keys.x25519_key import X25519Key

from api.crypto.key_provider import Identity, KeyProvider


TEST_PROVIDER = "test-ephemeral"


class EphemeralKeyProvider(KeyProvider):
    def __init__(self) -> None:
        sk = X25519PrivateKey.generate()
        self._sk_key = X25519Key(sk)
        pk = sk.public_key()
        self._pk_key = X25519Key(pk)
        self._public_raw = self._pk_key.to_public_bytes()

    def identity(self) -> Identity:
        return Identity(
            public_key=self._public_raw,
            provider=TEST_PROVIDER,
            quote=None,
            measurements={},
            extra={},
        )

    def private_key(self) -> KEMKeyInterface:
        return self._sk_key

    @property
    def public_raw(self) -> bytes:
        return self._public_raw

    @public_raw.setter
    def public_raw(self, value: bytes) -> None:
        self._public_raw = value


__all__ = ["EphemeralKeyProvider", "TEST_PROVIDER"]
