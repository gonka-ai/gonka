from __future__ import annotations

import hashlib
import sys
import types
from unittest.mock import patch

import pytest

from _fixtures.ephemeral_key_provider import EphemeralKeyProvider, TEST_PROVIDER

from api.crypto.envelope import recipient_key_id
from api.crypto.key_provider import (
    DstackKeyProvider,
    DstackUnavailable,
    _bind_report_data,
    _derive_x25519_seed,
    HKDF_INFO_PREFIX,
    DSTACK_KEY_PATH,
    build_key_provider,
)
from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey


def test_ephemeral_provider_yields_32_byte_x25519_pubkey():
    provider = EphemeralKeyProvider()
    identity = provider.identity()
    assert identity.provider == TEST_PROVIDER
    assert identity.quote is None
    assert identity.measurements == {}
    assert len(identity.public_key) == 32


def test_ephemeral_provider_round_trip_matches_public_key():
    from api.crypto.envelope import open_request, public_key_from_raw, seal_request

    provider = EphemeralKeyProvider()
    pk = public_key_from_raw(provider.identity().public_key)
    env, _ = seal_request(
        pk,
        "s",
        1,
        b"hi",
        recipient_key_id_hex=recipient_key_id(provider.identity().public_key),
    )
    opened = open_request(provider.private_key(), env)
    assert opened.plaintext == b"hi"


def test_two_ephemeral_providers_have_distinct_keys():
    a = EphemeralKeyProvider().identity().public_key
    b = EphemeralKeyProvider().identity().public_key
    assert a != b


def test_public_key_id_cache_can_be_invalidated():
    provider = EphemeralKeyProvider()
    first = provider.public_key_id()
    provider.public_raw = b"\x11" * 32
    provider.invalidate_public_key_id()
    second = provider.public_key_id()
    assert second == recipient_key_id(provider.identity().public_key)
    assert first != second


@pytest.mark.parametrize("value", ["none", "NONE", "off", "disabled"])
def test_factory_returns_none_for_explicit_disabled(value):
    assert build_key_provider({"MLNODE_KEY_PROVIDER": value}) is None


def test_factory_auto_detect_returns_none_when_dstack_unavailable():
    with patch(
        "api.crypto.key_provider.DstackKeyProvider",
        side_effect=DstackUnavailable("simulator-not-running"),
    ):
        assert build_key_provider({}) is None
        assert build_key_provider({"MLNODE_KEY_PROVIDER": ""}) is None
        assert build_key_provider({"MLNODE_KEY_PROVIDER": "auto"}) is None


def test_factory_auto_detect_returns_provider_when_dstack_works():
    sentinel = object()
    with patch("api.crypto.key_provider.DstackKeyProvider", return_value=sentinel):
        assert build_key_provider({}) is sentinel


@pytest.mark.parametrize("value", ["dstack", "tee", "DSTACK", "TEE"])
def test_factory_explicit_dstack_raises_when_unavailable(value):
    with patch(
        "api.crypto.key_provider.DstackKeyProvider",
        side_effect=DstackUnavailable("no socket"),
    ):
        with pytest.raises(DstackUnavailable):
            build_key_provider({"MLNODE_KEY_PROVIDER": value})


@pytest.mark.parametrize("value", ["dstack", "tee", "DSTACK", "TEE"])
def test_factory_explicit_dstack_returns_provider_when_available(value):
    sentinel = object()
    with patch("api.crypto.key_provider.DstackKeyProvider", return_value=sentinel):
        assert build_key_provider({"MLNODE_KEY_PROVIDER": value}) is sentinel


def test_factory_rejects_unknown_value():
    with pytest.raises(ValueError):
        build_key_provider({"MLNODE_KEY_PROVIDER": "not-a-thing"})


def test_dstack_provider_class_is_exported():
    import api.crypto

    assert hasattr(api.crypto, "DstackKeyProvider")
    assert api.crypto.DstackKeyProvider is DstackKeyProvider
    assert not hasattr(api.crypto, "LocalKeyProvider")


                                                                              
 
                                                                      
                                                                             


class _FakeKeyResp:
    def __init__(self, key_hex: str, signature_chain: list[str] | None = None):
        self.key = key_hex
        self.signature_chain = signature_chain or []


class _FakeQuoteResp:
    def __init__(self, quote_hex: str, report_data_summary: str | None = None):
        self.quote = quote_hex
        self.report_data = report_data_summary


_FAKE_QUOTE_MAGIC = b"QVL0"
_FAKE_REPORT_DATA_LEN = 64
_FAKE_MRTD_LEN = 48


def _encode_fake_quote(report_data: bytes, mr_td: bytes) -> bytes:
    if len(report_data) != _FAKE_REPORT_DATA_LEN:
        raise ValueError("report_data must be 64 bytes")
    if len(mr_td) != _FAKE_MRTD_LEN:
        raise ValueError("mr_td must be 48 bytes")
    return _FAKE_QUOTE_MAGIC + report_data + mr_td


def _parse_fake_quote(raw: bytes):
    if (
        len(raw) != len(_FAKE_QUOTE_MAGIC) + _FAKE_REPORT_DATA_LEN + _FAKE_MRTD_LEN
        or not raw.startswith(_FAKE_QUOTE_MAGIC)
    ):
        raise ValueError("invalid fake quote")
    offset = len(_FAKE_QUOTE_MAGIC)
    report_data = raw[offset : offset + _FAKE_REPORT_DATA_LEN]
    mr_td = raw[offset + _FAKE_REPORT_DATA_LEN : offset + _FAKE_REPORT_DATA_LEN + _FAKE_MRTD_LEN]
    report = types.SimpleNamespace(report_data=report_data, mr_td=mr_td)
    return types.SimpleNamespace(report=report, is_tdx=lambda: True)


class _FakeDstackClient:
                                                                        

    SEED = bytes(range(32))
    MRTD = bytes([0x77]) * 48

    def __init__(self, *, endpoint: str | None = None) -> None:
        self.endpoint = endpoint
        self._derived_pub: bytes | None = None

    def get_key(self, key_path: str):
        x25519_priv_bytes = _derive_x25519_seed(
            self.SEED, HKDF_INFO_PREFIX + b"/" + key_path.encode()
        )
        priv = X25519PrivateKey.from_private_bytes(x25519_priv_bytes)
        from pyhpke.keys.x25519_key import X25519Key
        self._derived_pub = X25519Key(priv.public_key()).to_public_bytes()
        return _FakeKeyResp(self.SEED.hex(), signature_chain=["sig-chain-stub"])

    def get_quote(self, report_data: bytes):
        if self._derived_pub is None:
            raise AssertionError("get_key must be called before get_quote")
        bound = _bind_report_data(self._derived_pub)
        assert report_data == bound, (
            "DstackKeyProvider must pass report_data = sha256(pk)||0*32 to dstack"
        )
        raw = _encode_fake_quote(bound, self.MRTD)
        return _FakeQuoteResp(raw.hex(), report_data_summary="dstack-summary-stub")


def _install_fake_dstack(client_cls=_FakeDstackClient):
    fake_mod = types.ModuleType("dstack_sdk")
    fake_mod.DstackClient = client_cls
    sys.modules["dstack_sdk"] = fake_mod
    return fake_mod


def _install_fake_dcap(parse_quote=_parse_fake_quote):
    fake_mod = types.ModuleType("dcap_qvl")
    fake_mod.parse_quote = parse_quote
    sys.modules["dcap_qvl"] = fake_mod
    return fake_mod


@pytest.fixture
def fake_attestation_libs():
    original_dstack = sys.modules.get("dstack_sdk")
    original_dcap = sys.modules.get("dcap_qvl")
    _install_fake_dstack()
    _install_fake_dcap()
    try:
        yield
    finally:
        if original_dstack is None:
            sys.modules.pop("dstack_sdk", None)
        else:
            sys.modules["dstack_sdk"] = original_dstack
        if original_dcap is None:
            sys.modules.pop("dcap_qvl", None)
        else:
            sys.modules["dcap_qvl"] = original_dcap


def test_dstack_provider_populates_mrtd_from_parsed_quote(fake_attestation_libs):
    provider = DstackKeyProvider()
    identity = provider.identity()

    assert identity.provider == "intel-tdx-lite"
    assert identity.quote is not None and len(identity.quote) > 0

    assert identity.measurements["mrtd"] == _FakeDstackClient.MRTD.hex()
    assert identity.measurements == {"mrtd": _FakeDstackClient.MRTD.hex()}

    assert "dstack_quote_summary" in identity.extra
    assert identity.extra["dstack_signature_chain"] == ["sig-chain-stub"]


def test_dstack_provider_report_data_binds_to_derived_public_key(fake_attestation_libs):
\
                                                                             
    provider = DstackKeyProvider()
    identity = provider.identity()

    expected = hashlib.sha256(identity.public_key).digest() + b"\x00" * 32

    parsed = _parse_fake_quote(identity.quote)
    assert parsed.report.report_data == expected


def test_dstack_provider_rejects_quote_with_wrong_report_data():
    class _BadBindingClient(_FakeDstackClient):
        def get_quote(self, report_data: bytes):
            tampered = bytearray(report_data)
            tampered[0] ^= 0xFF
            raw = _encode_fake_quote(bytes(tampered), self.MRTD)
            return _FakeQuoteResp(raw.hex())

    original_dstack = sys.modules.get("dstack_sdk")
    original_dcap = sys.modules.get("dcap_qvl")
    _install_fake_dstack(_BadBindingClient)
    _install_fake_dcap()
    try:
        with pytest.raises(DstackUnavailable, match="does not bind"):
            DstackKeyProvider()
    finally:
        if original_dstack is None:
            sys.modules.pop("dstack_sdk", None)
        else:
            sys.modules["dstack_sdk"] = original_dstack
        if original_dcap is None:
            sys.modules.pop("dcap_qvl", None)
        else:
            sys.modules["dcap_qvl"] = original_dcap


def test_dstack_provider_rejects_garbage_quote():
    class _GarbageQuoteClient(_FakeDstackClient):
        def get_quote(self, report_data: bytes):
            return _FakeQuoteResp(b"\x00\x00".hex())

    original_dstack = sys.modules.get("dstack_sdk")
    original_dcap = sys.modules.get("dcap_qvl")
    _install_fake_dstack(_GarbageQuoteClient)
    _install_fake_dcap()
    try:
        with pytest.raises(DstackUnavailable, match="invalid TDX quote"):
            DstackKeyProvider()
    finally:
        if original_dstack is None:
            sys.modules.pop("dstack_sdk", None)
        else:
            sys.modules["dstack_sdk"] = original_dstack
        if original_dcap is None:
            sys.modules.pop("dcap_qvl", None)
        else:
            sys.modules["dcap_qvl"] = original_dcap
