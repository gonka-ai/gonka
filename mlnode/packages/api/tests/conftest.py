from __future__ import annotations

import sys
from pathlib import Path

import pytest

_TESTS_DIR = Path(__file__).resolve().parent
if str(_TESTS_DIR) not in sys.path:
    sys.path.insert(0, str(_TESTS_DIR))

from _fixtures.ephemeral_key_provider import EphemeralKeyProvider              


@pytest.fixture
def ephemeral_key_provider() -> EphemeralKeyProvider:
    return EphemeralKeyProvider()
