                                                                        

from __future__ import annotations

import hashlib
import os
import threading
from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import Any

from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey
from cryptography.hazmat.primitives.kdf.hkdf import HKDF
from pyhpke.kem_key_interface import KEMKeyInterface
from pyhpke.keys.x25519_key import X25519Key

from common.logger import create_logger

logger = create_logger(__name__)


PROVIDER_INTEL_TDX_LITE = "intel-tdx-lite"
PROVIDER_INTEL_TDX = "intel-tdx"
PROVIDER_AMD_SEV_SNP = "amd-sev-snp"
PROVIDER_NVIDIA_CC = "nvidia-cc"


@dataclass
class Identity:
    public_key: bytes
    provider: str
    quote: bytes | None = None
    measurements: dict[str, str] = field(default_factory=dict)
    extra: dict[str, Any] = field(default_factory=dict)


class KeyProvider(ABC):
    @abstractmethod
    def identity(self) -> Identity: ...

    @abstractmethod
    def private_key(self) -> KEMKeyInterface: ...

    def public_key_id(self) -> str:
        from api.crypto.envelope import recipient_key_id

        lock = getattr(self, "_public_key_id_lock", None)
        if lock is None:
            lock = threading.Lock()
            self._public_key_id_lock = lock
        with lock:
            cached = getattr(self, "_public_key_id_cache", None)
            if cached is None:
                cached = recipient_key_id(self.identity().public_key)
                self._public_key_id_cache = cached
            return cached

    def invalidate_public_key_id(self) -> None:
        if hasattr(self, "_public_key_id_cache"):
            delattr(self, "_public_key_id_cache")


DSTACK_KEY_PATH = os.environ.get("MLNODE_DSTACK_KEY_PATH", "gonka/hpke/x25519/v1")
TDX_MRTD_LEN = 48

                                                                         
                                                                     
HKDF_SALT = b"gonka/hpke/x25519/v1"
HKDF_INFO_PREFIX = b"gonka/hpke/x25519/v1/key"


def _derive_x25519_seed(seed_bytes: bytes, info: bytes) -> bytes:
    return HKDF(
        algorithm=hashes.SHA256(),
        length=32,
        salt=HKDF_SALT,
        info=info,
    ).derive(seed_bytes)


def _bind_report_data(public_key_bytes: bytes) -> bytes:
                                                                   
                                                        
    h = hashlib.sha256(public_key_bytes).digest()
    return h + b"\x00" * 32


class DstackUnavailable(RuntimeError):
    pass


def _extract_tdx_fields(quote_raw: bytes) -> tuple[bytes, bytes]:
    try:
        import dcap_qvl
    except ImportError as e:
        raise DstackUnavailable(
            "dcap-qvl is not installed; install mlnode-api dependencies or "
            "set MLNODE_KEY_PROVIDER=none to disable encrypted inference"
        ) from e

    try:
        parsed = dcap_qvl.parse_quote(quote_raw)
    except Exception as e:
        raise DstackUnavailable(f"dstack returned invalid TDX quote: {e}") from e

    is_tdx = getattr(parsed, "is_tdx", None)
    if callable(is_tdx) and not is_tdx():
        raise DstackUnavailable("dstack returned non-TDX quote")

    report = getattr(parsed, "report", None)
    if report is None:
        raise DstackUnavailable("dcap-qvl parsed quote without report body")

    quote_report_data = getattr(report, "report_data", None)
    if quote_report_data is None:
        raise DstackUnavailable("dcap-qvl report is missing report_data")

    mr_td = getattr(report, "mr_td", None)
    if mr_td is None:
        mr_td = getattr(report, "mrtd", None)
    if mr_td is None:
        raise DstackUnavailable("dcap-qvl report is missing mr_td")

    return bytes(quote_report_data), bytes(mr_td)


class DstackKeyProvider(KeyProvider):
                                                                          

    def __init__(
        self,
        *,
        endpoint: str | None = None,
        key_path: str = DSTACK_KEY_PATH,
        provider_id: str = PROVIDER_INTEL_TDX_LITE,
    ) -> None:
        try:
            from dstack_sdk import DstackClient
        except ImportError as e:
            raise DstackUnavailable(
                "dstack-sdk is not installed; install the mlnode-api extra or "
                "set MLNODE_KEY_PROVIDER=none to disable encrypted inference"
            ) from e

        try:
            self._client = DstackClient(endpoint=endpoint) if endpoint else DstackClient()
        except Exception as e:
            raise DstackUnavailable(f"dstack client init failed: {e}") from e

        try:
            key_resp = self._client.get_key(key_path)
        except Exception as e:
            raise DstackUnavailable(f"dstack get_key({key_path!r}) failed: {e}") from e

        raw_seed = bytes.fromhex(key_resp.key)
        if len(raw_seed) < 32:
            raise DstackUnavailable(
                f"dstack returned {len(raw_seed)}-byte key, expected >= 32"
            )
        seed = raw_seed[:32]

        x25519_priv_bytes = _derive_x25519_seed(seed, HKDF_INFO_PREFIX + b"/" + key_path.encode())
        priv = X25519PrivateKey.from_private_bytes(x25519_priv_bytes)
        self._sk_key = X25519Key(priv)
        pub = priv.public_key()
        self._pk_key = X25519Key(pub)
        self._public_raw = self._pk_key.to_public_bytes()

        report_data = _bind_report_data(self._public_raw)
        try:
            quote_resp = self._client.get_quote(report_data)
        except Exception as e:
            raise DstackUnavailable(f"dstack get_quote() failed: {e}") from e

        self._quote_raw = bytes.fromhex(quote_resp.quote)
        self._provider_id = provider_id
        self._signature_chain = getattr(key_resp, "signature_chain", None)

        quote_report_data, mr_td = _extract_tdx_fields(self._quote_raw)

        if quote_report_data != report_data:
            raise DstackUnavailable(
                "dstack quote report_data does not bind to sha256(public_key)||0*32; "
                "dstack tappd may have ignored our binding"
            )

        if len(mr_td) != TDX_MRTD_LEN:
            raise DstackUnavailable(
                f"dstack quote mr_td must be {TDX_MRTD_LEN} bytes, got {len(mr_td)}"
            )

        self._measurements = {"mrtd": mr_td.hex()}

        self._extra: dict[str, Any] = {}
        report_summary = getattr(quote_resp, "report_data", None)
        if report_summary:
            self._extra["dstack_quote_summary"] = report_summary

        logger.info(
            "DstackKeyProvider: derived X25519 key from dstack key_path=%s endpoint=%s mrtd=%s",
            key_path, endpoint or "<default-socket>", self._measurements["mrtd"],
        )

    def identity(self) -> Identity:
        extra = dict(self._extra)
        if self._signature_chain is not None:
            extra["dstack_signature_chain"] = self._signature_chain
        return Identity(
            public_key=self._public_raw,
            provider=self._provider_id,
            quote=self._quote_raw,
            measurements=dict(self._measurements),
            extra=extra,
        )

    def private_key(self) -> KEMKeyInterface:
        return self._sk_key


_DEFAULT_VALUE = "auto"


def build_key_provider(env: dict[str, str] | None = None) -> KeyProvider | None:
\
\
\
\
\
\
       
    source = env if env is not None else os.environ
    raw = source.get("MLNODE_KEY_PROVIDER", _DEFAULT_VALUE).strip().lower()

    if raw in ("none", "off", "disabled"):
        return None

    if raw in ("", _DEFAULT_VALUE):
        try:
            return DstackKeyProvider()
        except DstackUnavailable as e:
            logger.info(
                "MLNODE_KEY_PROVIDER auto-detect: dstack not available (%s); "
                "encrypted inference will be disabled for this process",
                e,
            )
            return None

    if raw in ("dstack", "tee"):
        return DstackKeyProvider()

    raise ValueError(
        f"unknown MLNODE_KEY_PROVIDER value: {raw!r} "
        "(expected one of: auto, none, dstack, tee)"
    )
