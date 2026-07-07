"""Unit tests for VLLMRunner.is_available() scheduler-heartbeat liveness.

The check replaces vLLM's /health probe (wrong in both directions: false-positive
under load, false-negative on a silent deadlock) with a read of the Prometheus
counter vllm:iteration_tokens_total_count, which advances on every engine step
and freezes only when the engine loop stalls.

These tests drive is_available() over a controlled clock and mocked /metrics
responses, covering the steady state, the hardened race conditions
(counter-reset on engine restart, multi-process /metrics, blind scrapes,
ConnectTimeout-vs-refused classification), multi-instance aggregation, and the
config edge cases (grace disabled, malformed env).
"""
import re

import pytest
import requests as real_requests

from api.inference.vllm import runner as runner_mod
from api.inference.vllm.runner import VLLMRunner


class _FakeResp:
    def __init__(self, text):
        self.text = text


class _Clock:
    def __init__(self):
        self.t = 1000.0

    def __call__(self):
        return self.t

    def advance(self, dt):
        self.t += dt


def _metrics(iters=None, running=None, waiting=None, series=1):
    """Render a minimal /metrics body. `series` repeats the counter/gauge lines
    to emulate more than one engine registry exposed on the same port."""
    lines = []
    for k in range(series):
        if iters is not None:
            lines.append(f'vllm:iteration_tokens_total_count{{engine="{k}"}} {iters}')
        if running is not None:
            lines.append(f'vllm:num_requests_running{{engine="{k}"}} {running}')
        if waiting is not None:
            lines.append(f'vllm:num_requests_waiting{{engine="{k}"}} {waiting}')
    return "\n".join(lines) + "\n"


def _make_runner(monkeypatch, n_instances=1):
    """A VLLMRunner with __init__ bypassed, `n_instances` live processes, a
    controllable clock, and a per-port mockable /metrics endpoint."""
    r = object.__new__(VLLMRunner)
    r.VLLM_HOST = "127.0.0.1"
    r.VLLM_PORT = 5000
    r._hb = {}  # __init__ is bypassed; mirror its heartbeat-state init
    proc = type("P", (), {"poll": staticmethod(lambda: None)})()
    r.processes = [proc] * n_instances

    clock = _Clock()
    monkeypatch.setattr(runner_mod.time, "time", clock)

    # per-port response/exception; "*" is the default for unlisted ports
    state = {"*": {"resp": _metrics(iters=100, running=40, waiting=0), "exc": None}}

    def fake_get(url, timeout=None):
        port = int(url.split(":")[-1].split("/")[0])
        s = state.get(port, state["*"])
        if s["exc"] is not None:
            raise s["exc"]
        return _FakeResp(s["resp"])

    monkeypatch.setattr(runner_mod.requests, "get", fake_get)

    r._clock = clock
    r._state = state
    return r


@pytest.fixture
def runner(monkeypatch):
    return _make_runner(monkeypatch, n_instances=1)


def _serve(runner, port="*", **kw):
    runner._state[port] = {"resp": _metrics(**kw), "exc": None}


def _fail(runner, exc, port="*"):
    runner._state[port] = {"resp": "", "exc": exc}


def test_process_dead_is_unavailable(runner):
    runner.processes = []  # is_running() -> False
    assert runner.is_available() is False


def test_advancing_counter_is_alive(runner):
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True  # baseline
    runner._clock.advance(2)
    _serve(runner, iters=185, running=40, waiting=0)
    assert runner.is_available() is True


def test_counter_regression_rebaselines(runner):
    """Engine subprocess restarts and its counter resets to ~0 while work is
    present. A '>' test would false-HUNG; '!=' must re-baseline and stay alive."""
    _serve(runner, iters=90000, running=40, waiting=0)
    assert runner.is_available() is True
    runner._clock.advance(2)
    _serve(runner, iters=5, running=40, waiting=0)  # fresh engine, counter reset
    assert runner.is_available() is True
    assert runner._hb[5001]["iter"] == 5  # re-baselined to the new low value


def test_multiprocess_metrics_sum_advances(runner):
    """Two registries on one port: the summed counter advances while either
    engine steps, so the node is not read as frozen."""
    _serve(runner, iters=100, running=40, waiting=0, series=2)  # sum=200
    assert runner.is_available() is True
    assert runner._hb[5001]["iter"] == 200
    runner._clock.advance(2)
    _serve(runner, iters=150, running=40, waiting=0, series=2)  # sum=300
    assert runner.is_available() is True


def test_idle_engine_is_alive(runner):
    """No work (running==0 and waiting==0) with a frozen counter is a legitimate
    idle-wait, not a hang."""
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True
    runner._clock.advance(500)  # way past grace
    _serve(runner, iters=100, running=0, waiting=0)  # counter frozen but no work
    assert runner.is_available() is True


def test_real_hang_reports_unhealthy_after_consecutive(runner, monkeypatch):
    """Work present, counter frozen past grace: unhealthy only after
    HANG_CONSECUTIVE (3) verdicts, so a single confused scrape cannot escalate."""
    monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", "120")
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True  # baseline @ t=1000
    runner._clock.advance(200)  # past 120s grace, counter still 100, work present
    assert runner.is_available() is True   # hung count 1/3
    runner._clock.advance(2)
    assert runner.is_available() is True   # hung count 2/3
    runner._clock.advance(2)
    assert runner.is_available() is False  # hung count 3/3 -> unhealthy


def test_grace_recovers_before_escalation(runner, monkeypatch):
    monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", "120")
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True
    runner._clock.advance(200)
    assert runner.is_available() is True   # 1/3
    runner._clock.advance(2)
    _serve(runner, iters=300, running=40, waiting=0)  # engine steps again
    assert runner.is_available() is True   # re-baselined, hung count reset
    assert runner._hb[5001]["hung"] == 0


def test_partial_scrape_missing_counter_uses_grace(runner, monkeypatch):
    """Counter series absent but gauges present (work ongoing): alive within
    grace, dead once grace is exceeded with no counter to prove liveness."""
    monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", "120")
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True
    runner._clock.advance(60)
    _serve(runner, iters=None, running=40, waiting=0)  # counter gone
    assert runner.is_available() is True   # within grace (60s <= 120s)
    runner._clock.advance(100)             # now 160s since baseline
    _serve(runner, iters=None, running=40, waiting=0)
    assert runner.is_available() is False


def test_full_scrape_failure_is_not_masked_as_idle(runner, monkeypatch):
    """Regression guard for the idle-shortcut hole: a total /metrics failure
    (every value None) must NOT refresh the baseline and report healthy, which
    would silently disable hang detection. It must fall through to grace."""
    monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", "120")
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True   # baseline @ t=1000
    base_ts = runner._hb[5001]["ts"]
    runner._clock.advance(30)
    _serve(runner, iters=None, running=None, waiting=None)  # blind scrape
    assert runner.is_available() is True   # within grace
    assert runner._hb[5001]["ts"] == base_ts  # baseline NOT refreshed blindly
    runner._clock.advance(200)             # 230s since baseline, still blind
    _serve(runner, iters=None, running=None, waiting=None)
    assert runner.is_available() is False  # grace exceeded -> unhealthy


def test_connection_refused_is_dead(runner):
    _fail(runner, real_requests.ConnectionError())
    assert runner.is_available() is False


def test_connect_timeout_uses_grace_not_dead(runner, monkeypatch):
    """ConnectTimeout subclasses ConnectionError in requests, but a full SYN
    backlog on a saturated server is not death: it must take the grace path,
    not the immediate-dead path."""
    monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", "120")
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True   # baseline
    runner._clock.advance(2)
    _fail(runner, real_requests.exceptions.ConnectTimeout())
    assert runner.is_available() is True   # within grace -> alive
    runner._clock.advance(200)             # past grace, still timing out
    assert runner.is_available() is False  # grace exceeded -> unhealthy


def test_hang_detection_disabled_when_grace_zero(runner, monkeypatch):
    monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", "0")
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True
    runner._clock.advance(100000)  # arbitrarily long freeze with work present
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True  # detection off


def test_grace_zero_tolerates_blind_scrapes(runner, monkeypatch):
    """grace=0 must disable ALL hang escalation, including the blind-scrape
    path: a slow /metrics must not mark the node dead when detection is off."""
    monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", "0")
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True
    runner._clock.advance(500)
    _fail(runner, TimeoutError())  # scrape fails entirely
    assert runner.is_available() is True   # still alive: detection disabled
    # but a refused connection still means dead even with detection off
    _fail(runner, real_requests.ConnectionError())
    assert runner.is_available() is False


def test_malformed_grace_env_does_not_raise(runner, monkeypatch):
    """An empty or garbage MLNODE_HANG_GRACE_SEC must not raise out of
    is_available (a fleet-wide env templating mistake would otherwise turn
    every health poll into an exception -> watcher kill-loop)."""
    for bad in ("", "  ", "abc", "12.5"):
        monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", bad)
        _serve(runner, iters=100, running=40, waiting=0)
        assert runner.is_available() is True  # falls back to default 120


def test_multi_instance_hang_in_one_instance_reported(monkeypatch):
    """Instance 3 freezes with work while instances 1/2/4 keep stepping: the
    node must go unhealthy after grace + consecutive verdicts."""
    monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", "120")
    r = _make_runner(monkeypatch, n_instances=4)
    _serve(r, iters=100, running=40, waiting=0)          # default: advancing...
    _serve(r, port=5003, iters=777, running=20, waiting=0)  # ...incl. 5003
    assert r.is_available() is True  # baseline for all ports

    step = 100
    for tick in range(1, 4):
        r._clock.advance(200 if tick == 1 else 2)
        step += 85
        _serve(r, iters=step, running=40, waiting=0)     # others advance
        _serve(r, port=5003, iters=777, running=20, waiting=0)  # 5003 frozen
        expected = tick < 3  # unhealthy on the 3rd consecutive frozen verdict
        assert r.is_available() is expected, f"tick {tick}"


def test_multi_instance_refused_port_is_dead(monkeypatch):
    """A refused connection on ANY instance means that engine's API server is
    gone: the node reports dead even if other instances are healthy."""
    r = _make_runner(monkeypatch, n_instances=4)
    _serve(r, iters=100, running=40, waiting=0)
    _fail(r, real_requests.ConnectionError(), port=5002)
    assert r.is_available() is False
