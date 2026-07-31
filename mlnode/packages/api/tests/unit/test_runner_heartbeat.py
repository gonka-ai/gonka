"""Unit tests for vLLM scheduler-heartbeat liveness."""

import asyncio
import threading
import time
from contextlib import suppress

import pytest
import requests as real_requests
from api.inference.vllm import runner as runner_mod
from api.inference.vllm.runner import VLLMRunner


class _FakeResp:
    def __init__(self, text, status_code=200):
        self.text = text
        self.status_code = status_code

    def raise_for_status(self):
        if self.status_code >= 400:
            raise real_requests.HTTPError(response=self)


class _Clock:
    def __init__(self):
        self.t = 1000.0

    def __call__(self):
        return self.t

    def advance(self, dt):
        self.t += dt


def _metrics(iters=None, running=None, waiting=None, series=1):
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
    r = object.__new__(VLLMRunner)
    r.VLLM_HOST = "127.0.0.1"
    r.VLLM_PORT = 5000
    r._hb = {}  # __init__ is bypassed; mirror its heartbeat-state init
    r._hb_lock = threading.Lock()
    proc = type("P", (), {"poll": staticmethod(lambda: None)})()
    r.processes = [proc] * n_instances

    clock = _Clock()
    monkeypatch.setattr(runner_mod.time, "monotonic", clock)

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


def _assert_unhealthy_after_damping(runner):
    for expected in (True, True, False):
        assert runner.is_available() is expected


def test_process_dead_is_unavailable(runner):
    runner.processes = []  # is_running() -> False
    assert runner.is_available() is False


def test_advancing_counter_is_alive(runner):
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True  # baseline
    runner._clock.advance(2)
    _serve(runner, iters=185, running=40, waiting=0)
    assert runner.is_available() is True


@pytest.mark.parametrize(
    "response",
    [_FakeResp(""), _FakeResp("not metrics", status_code=503)],
    ids=["missing-series", "http-error"],
)
def test_first_scrape_must_be_complete(runner, monkeypatch, response):
    monkeypatch.setattr(
        runner_mod.requests,
        "get",
        lambda *args, **kwargs: response,
    )
    assert runner.is_available() is False
    assert runner._hb[5001]["seen"] is False


def test_counter_regression_rebaselines(runner):
    _serve(runner, iters=90000, running=40, waiting=0)
    assert runner.is_available() is True
    runner._clock.advance(2)
    _serve(runner, iters=5, running=40, waiting=0)  # fresh engine, counter reset
    assert runner.is_available() is True
    assert runner._hb[5001]["iter"] == 5  # re-baselined to the new low value


def test_multiprocess_metrics_sum_advances(runner):
    _serve(runner, iters=100, running=40, waiting=0, series=2)  # sum=200
    assert runner.is_available() is True
    assert runner._hb[5001]["iter"] == 200
    runner._clock.advance(2)
    _serve(runner, iters=150, running=40, waiting=0, series=2)  # sum=300
    assert runner.is_available() is True


def test_idle_engine_is_alive(runner):
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True
    runner._clock.advance(500)  # way past grace
    _serve(runner, iters=100, running=0, waiting=0)  # counter frozen but no work
    assert runner.is_available() is True


def test_real_hang_reports_unhealthy_after_consecutive(runner, monkeypatch):
    monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", "120")
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True  # baseline @ t=1000
    runner._clock.advance(200)  # past 120s grace, counter still 100, work present
    _assert_unhealthy_after_damping(runner)


def test_grace_recovers_before_escalation(runner, monkeypatch):
    monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", "120")
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True
    runner._clock.advance(200)
    assert runner.is_available() is True  # 1/3
    runner._clock.advance(2)
    _serve(runner, iters=300, running=40, waiting=0)  # engine steps again
    assert runner.is_available() is True  # re-baselined, hung count reset
    assert runner._hb[5001]["hung"] == 0


@pytest.mark.parametrize(
    ("metrics", "error"),
    [
        ({"iters": None, "running": 40, "waiting": 0}, None),
        ({"iters": None, "running": 0, "waiting": 0}, None),
        ({"iters": None, "running": None, "waiting": None}, None),
        (None, real_requests.exceptions.ConnectTimeout()),
    ],
    ids=["missing-counter", "partial-idle", "blind", "connect-timeout"],
)
def test_unproven_progress_uses_grace(runner, monkeypatch, metrics, error):
    monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", "120")
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True
    baseline = runner._hb[5001]["ts"]

    if error is not None:
        _fail(runner, error)
    else:
        _serve(runner, **metrics)
    runner._clock.advance(60)
    assert runner.is_available() is True
    assert runner._hb[5001]["ts"] == baseline

    runner._clock.advance(100)
    _assert_unhealthy_after_damping(runner)


def test_hang_detection_disabled_when_grace_zero(runner, monkeypatch):
    monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", "0")
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True
    runner._clock.advance(100000)  # arbitrarily long freeze with work present
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True  # detection off


def test_grace_zero_tolerates_blind_scrapes(runner, monkeypatch):
    monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", "0")
    _serve(runner, iters=100, running=40, waiting=0)
    assert runner.is_available() is True
    runner._clock.advance(500)
    _fail(runner, TimeoutError())  # scrape fails entirely
    assert runner.is_available() is True  # still alive: detection disabled
    # but a refused connection still means dead even with detection off
    _fail(runner, real_requests.ConnectionError())
    assert runner.is_available() is False


def test_first_blind_scrape_allowed_when_grace_zero(runner, monkeypatch):
    monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", "0")
    _fail(runner, TimeoutError())
    assert runner.is_available() is True
    assert runner._hb[5001]["seen"] is False


def test_malformed_grace_env_does_not_raise(runner, monkeypatch):
    for bad in ("", "  ", "abc", "12.5"):
        monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", bad)
        _serve(runner, iters=100, running=40, waiting=0)
        assert runner.is_available() is True  # falls back to default 120


def test_runner_startup_timeout_uses_environment(monkeypatch):
    runner = object.__new__(VLLMRunner)
    runner.is_running = lambda: True
    runner.is_available = lambda: False
    clock = iter([0, 6, 7])
    monkeypatch.setenv("VLLM_RUNNER_TIMEOUT", "7")
    monkeypatch.setattr(runner_mod.time, "time", lambda: next(clock))
    monkeypatch.setattr(runner_mod.time, "sleep", lambda _: None)
    monkeypatch.setattr(runner_mod.logger, "error", lambda *args: None)

    assert runner._wait_for_server() is False


def test_runner_uses_image_vllm_module(monkeypatch):
    runner = VLLMRunner(model="dummy-model")
    commands = []
    process = type("P", (), {"poll": staticmethod(lambda: None)})()
    monkeypatch.setenv("MLNODE_VLLM_MODULE", "gonka_poc.entrypoint.api_router")
    monkeypatch.setattr(runner_mod.torch.cuda, "device_count", lambda: 1)
    monkeypatch.setattr(runner, "_verify_and_fix_env", lambda: None)
    monkeypatch.setattr(runner, "_wait_for_server", lambda: True)
    monkeypatch.setattr(runner_mod, "setup_vllm_proxy", lambda ports: None)
    monkeypatch.setattr(
        runner_mod.subprocess,
        "Popen",
        lambda command, **kwargs: commands.append(command) or process,
    )

    runner.start()

    assert "-m gonka_poc.entrypoint.api_router" in commands[0][2]


def test_multi_instance_hang_in_one_instance_reported(monkeypatch):
    monkeypatch.setenv("MLNODE_HANG_GRACE_SEC", "120")
    r = _make_runner(monkeypatch, n_instances=4)
    _serve(r, iters=100, running=40, waiting=0)  # default: advancing...
    _serve(r, port=5003, iters=777, running=20, waiting=0)  # ...incl. 5003
    assert r.is_available() is True  # baseline for all ports

    step = 100
    for tick in range(1, 4):
        r._clock.advance(200 if tick == 1 else 2)
        step += 85
        _serve(r, iters=step, running=40, waiting=0)  # others advance
        _serve(r, port=5003, iters=777, running=20, waiting=0)  # 5003 frozen
        expected = tick < 3  # unhealthy on the 3rd consecutive frozen verdict
        assert r.is_available() is expected, f"tick {tick}"


def test_multi_instance_refused_port_is_dead(monkeypatch):
    r = _make_runner(monkeypatch, n_instances=4)
    _serve(r, iters=100, running=40, waiting=0)
    _fail(r, real_requests.ConnectionError(), port=5002)
    assert r.is_available() is False


def test_multi_instance_scrapes_run_concurrently(monkeypatch):
    r = _make_runner(monkeypatch, n_instances=4)
    barrier = threading.Barrier(4)

    def synchronized_get(url, timeout=None):
        barrier.wait(timeout=1)
        return _FakeResp(_metrics(iters=100, running=0, waiting=0))

    monkeypatch.setattr(runner_mod.requests, "get", synchronized_get)
    assert r.is_available() is True


def test_concurrent_health_checks_are_serialized(runner, monkeypatch):
    first_started = threading.Event()
    release_first = threading.Event()
    calls = 0
    calls_lock = threading.Lock()

    def blocking_get(url, timeout=None):
        nonlocal calls
        with calls_lock:
            calls += 1
            call = calls
        if call == 1:
            first_started.set()
            release_first.wait(timeout=1)
        return _FakeResp(_metrics(iters=call, running=0, waiting=0))

    monkeypatch.setattr(runner_mod.requests, "get", blocking_get)
    first = threading.Thread(target=runner.is_available)
    second = threading.Thread(target=runner.is_available)
    first.start()
    assert first_started.wait(timeout=1)
    second.start()
    time.sleep(0.05)
    assert calls == 1
    release_first.set()
    first.join(timeout=1)
    second.join(timeout=1)
    assert calls == 2


@pytest.mark.asyncio
async def test_manager_watcher_does_not_block_event_loop():
    from api.watcher import watch_managers

    release = threading.Event()

    class BlockingManager:
        def is_healthy(self):
            release.wait(timeout=1)
            return True

    task = asyncio.create_task(watch_managers(None, [BlockingManager()], interval=0))
    started = time.monotonic()
    await asyncio.sleep(0.05)
    assert time.monotonic() - started < 0.5

    release.set()
    task.cancel()
    with suppress(asyncio.CancelledError):
        await task
