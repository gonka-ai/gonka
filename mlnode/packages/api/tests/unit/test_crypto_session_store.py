                                                                         

from __future__ import annotations

import pytest

from api.crypto.session_store import NonceReplayError, SessionNonceStore


def test_strictly_increasing_nonces_are_accepted():
    store = SessionNonceStore()
    for n in range(1, 10):
        store.check_and_record("s", n)


def test_equal_nonce_is_rejected():
    store = SessionNonceStore()
    store.check_and_record("s", 5)
    with pytest.raises(NonceReplayError):
        store.check_and_record("s", 5)


def test_smaller_nonce_is_rejected():
    store = SessionNonceStore()
    store.check_and_record("s", 5)
    with pytest.raises(NonceReplayError):
        store.check_and_record("s", 4)


def test_sessions_are_independent():
    store = SessionNonceStore()
    store.check_and_record("a", 10)
    store.check_and_record("b", 1)
    with pytest.raises(NonceReplayError):
        store.check_and_record("a", 5)


def test_reset_forgets_session():
    store = SessionNonceStore()
    store.check_and_record("a", 10)
    store.reset("a")
    store.check_and_record("a", 1)


def test_capacity_eviction_drops_oldest():
    times = [0.0]

    def clock():
        return times[0]

    store = SessionNonceStore(max_sessions=2, ttl_seconds=10_000, clock=clock)

    times[0] = 1.0
    store.check_and_record("oldest", 1)
    times[0] = 2.0
    store.check_and_record("middle", 1)
    times[0] = 3.0
    store.check_and_record("newest", 1)                            

                                                 
    times[0] = 4.0
    with pytest.raises(NonceReplayError):
        store.check_and_record("newest", 1)

                                                        
    store.check_and_record("oldest", 1)


def test_ttl_eviction_runs_at_capacity():
    times = [0.0]

    def clock():
        return times[0]

    store = SessionNonceStore(max_sessions=2, ttl_seconds=5.0, clock=clock)

    times[0] = 0.0
    store.check_and_record("a", 1)
    times[0] = 1.0
    store.check_and_record("b", 1)

    times[0] = 100.0                 
    store.check_and_record("c", 1)                      

    store.check_and_record("a", 1)
    store.check_and_record("b", 1)
