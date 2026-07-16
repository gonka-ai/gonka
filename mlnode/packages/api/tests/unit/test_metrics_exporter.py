import asyncio
import re
import time

import pytest
from unittest.mock import AsyncMock, MagicMock
from fastapi import FastAPI
from fastapi.testclient import TestClient

import api.proxy as proxy_module
from api.gpu.manager import GPUManager
from api.metrics import host_source, vllm_source
from api.metrics.allowlist import VLLM_ALLOWLIST
from api.metrics.routes import router
from api.metrics.vllm_source import filter_vllm_metrics, normalize_model_name

HF_CACHE_MODEL = (
    "/root/.cache/huggingface/hub/models--moonshotai--Kimi-K2.6/"
    "snapshots/7eb5002f6aadc958aed6a9177b7ed26bb94011bb"
)

# Canonical vLLM /metrics excerpt: allowlist families, a non-allowlist family,
# a _created series and the engine/model_name labels to be rewritten.
CANONICAL_VLLM_METRICS = f"""\
# HELP vllm:num_requests_waiting Number of requests waiting to be processed.
# TYPE vllm:num_requests_waiting gauge
vllm:num_requests_waiting{{engine="0",model_name="{HF_CACHE_MODEL}"}} 144.0
# HELP vllm:request_success_total Count of successfully processed requests.
# TYPE vllm:request_success_total counter
vllm:request_success_total{{engine="0",finished_reason="stop",model_name="{HF_CACHE_MODEL}"}} 773.0
vllm:request_success_created{{engine="0",finished_reason="stop",model_name="{HF_CACHE_MODEL}"}} 1.75e+09
# HELP vllm:time_to_first_token_seconds Histogram of time to first token.
# TYPE vllm:time_to_first_token_seconds histogram
vllm:time_to_first_token_seconds_bucket{{engine="0",le="0.001",model_name="{HF_CACHE_MODEL}"}} 0.0
vllm:time_to_first_token_seconds_count{{engine="0",model_name="{HF_CACHE_MODEL}"}} 3447.0
vllm:time_to_first_token_seconds_sum{{engine="0",model_name="{HF_CACHE_MODEL}"}} 481575.5
# HELP vllm:num_requests_waiting_by_reason Not in allowlist.
# TYPE vllm:num_requests_waiting_by_reason gauge
vllm:num_requests_waiting_by_reason{{engine="0",model_name="{HF_CACHE_MODEL}",reason="capacity"}} 144.0
python_gc_objects_collected_total{{generation="0"}} 146351.0
"""


@pytest.fixture(autouse=True)
def metrics_state(monkeypatch):
    vllm_source.reset_cache()
    host_source.reset()
    monkeypatch.setattr(proxy_module, "vllm_backend_ports", [5001, 5002])
    monkeypatch.setattr(proxy_module, "vllm_healthy", {5001: True, 5002: True})
    yield
    vllm_source.reset_cache()
    host_source.reset()


@pytest.fixture
def gpu_manager():
    manager = MagicMock(spec=GPUManager)
    manager.collect_metrics_async = AsyncMock(return_value=[])
    return manager


@pytest.fixture
def client(monkeypatch, gpu_manager):
    response = MagicMock(status_code=200, text=CANONICAL_VLLM_METRICS)
    monkeypatch.setattr(proxy_module, "call_backend", AsyncMock(return_value=response))

    app = FastAPI()
    app.state.gpu_manager = gpu_manager
    app.state.inference_manager = MagicMock(vllm_runner=None)
    app.include_router(router)
    return TestClient(app)


def test_off_returns_404(client, monkeypatch):
    monkeypatch.setenv("GONKA_METRICS", "off")
    response = client.get("/metrics")
    assert response.status_code == 404
    assert response.json() == {"detail": "Not Found"}


def test_default_is_full(client, monkeypatch):
    monkeypatch.delenv("GONKA_METRICS", raising=False)
    response = client.get("/metrics")
    assert response.status_code == 200
    assert response.headers["content-type"].startswith("text/plain")


def test_exports_exactly_allowlist_families(client):
    body = client.get("/metrics").text
    exported = {
        re.sub(r"_(bucket|sum|count)$", "", line.split("{")[0])
        for line in body.splitlines()
        if line.startswith("vllm:")
    }
    assert exported <= VLLM_ALLOWLIST
    assert "vllm:num_requests_waiting" in exported
    assert "vllm:request_success_total" in exported
    assert "vllm:time_to_first_token_seconds" in exported
    # not in allowlist / not vllm => filtered out
    assert "vllm:num_requests_waiting_by_reason" not in body
    assert "python_gc_objects_collected_total" not in body
    assert "_created" not in body


def test_no_placement_labels(client):
    """Invariant 5: no hostname/IP/ports/provider/local paths in the output."""
    body = client.get("/metrics").text
    label_values = re.findall(r'="((?:[^"\\]|\\.)*)"', body)
    for value in label_values:
        assert "/" not in value or re.fullmatch(r"[\w.-]+/[\w.-]+", value), value
        assert ".cache" not in value
        assert "127.0.0.1" not in value
    label_names = set(re.findall(r'(\w+)="', body))
    assert not label_names & {"hostname", "ip", "port", "host", "provider", "engine"}


def test_model_name_normalized(client):
    body = client.get("/metrics").text
    assert 'model_name="moonshotai/Kimi-K2.6"' in body
    assert "/root/.cache" not in body


def test_replicas_distinguishable(client):
    body = client.get("/metrics").text
    waiting = [l for l in body.splitlines() if l.startswith("vllm:num_requests_waiting{")]
    replicas = {re.search(r'replica="(\d+)"', l).group(1) for l in waiting}
    assert replicas == {"0", "1"}


def test_schema_and_source_timestamps_present(client):
    body = client.get("/metrics").text
    assert 'gonka_metrics_schema_info{version="1"} 1' in body
    assert 'mlnode_source_scrape_timestamp_seconds{source="vllm",replica="0"}' in body


def test_hung_replica_keeps_stale_timestamp(client, monkeypatch):
    first = client.get("/metrics").text
    ts_line = [
        l for l in first.splitlines()
        if l.startswith('mlnode_source_scrape_timestamp_seconds{source="vllm",replica="1"')
    ][0]

    # replica 1 stops answering /metrics but stays "healthy"
    async def flaky(port, method, path):
        if port == 5002:
            raise TimeoutError("frozen")
        return MagicMock(status_code=200, text=CANONICAL_VLLM_METRICS)

    monkeypatch.setattr(proxy_module, "call_backend", flaky)
    second = client.get("/metrics").text

    # stale copy still served, timestamp did not advance
    assert ts_line in second
    waiting = [l for l in second.splitlines() if l.startswith("vllm:num_requests_waiting{")]
    assert {re.search(r'replica="(\d+)"', l).group(1) for l in waiting} == {"0", "1"}


def test_gpu_fields_rendered(client, gpu_manager):
    gpu_manager.collect_metrics_async = AsyncMock(return_value=[
        {
            "gpu_index": "0",
            "gpu_model": "NVIDIA B300 SXM6 AC",
            "fields": {
                "mlnode_gpu_temp_core_celsius": 71.0,
                "mlnode_gpu_power_draw_watts": 812.5,
            },
        }
    ])
    body = client.get("/metrics").text
    assert (
        'mlnode_gpu_temp_core_celsius{gpu_index="0",gpu_model="NVIDIA B300 SXM6 AC"} 71.0'
        in body
    )
    assert 'mlnode_source_scrape_timestamp_seconds{source="nvml"}' in body


def test_no_gpu_no_series(client, gpu_manager):
    gpu_manager.collect_metrics_async = AsyncMock(return_value=[])
    body = client.get("/metrics").text
    assert "mlnode_gpu_" not in body
    assert 'source="nvml"' not in body


def test_xid_counter_rendered(client):
    from api.metrics import xid_source
    xid_source.reset()
    with xid_source._lock:
        xid_source._counts[("0", 79)] += 1
    try:
        body = client.get("/metrics").text
        assert 'mlnode_gpu_xid_events_total{gpu_index="0",xid="79"} 1' in body
    finally:
        xid_source.reset()


def test_no_xid_events_no_series(client):
    from api.metrics import xid_source
    xid_source.reset()
    body = client.get("/metrics").text
    assert "mlnode_gpu_xid_events_total" not in body


def test_normalize_model_name():
    assert normalize_model_name(HF_CACHE_MODEL) == "moonshotai/Kimi-K2.6"
    assert normalize_model_name("Qwen/Qwen2.5-0.5B-Instruct") == "Qwen/Qwen2.5-0.5B-Instruct"
    # model names may contain "--"; the org group must not swallow them
    assert normalize_model_name("hub/models--org--some--model/snapshots/x") == "org/some--model"
    # models loaded from a local directory: path never leaves the node
    assert normalize_model_name("/root/autodl-tmp/models/MiniMax-M2.7") == "MiniMax-M2.7"
    assert normalize_model_name("/data/weights/foo/") == "foo"


def test_filter_returns_meta_separately():
    meta, series = filter_vllm_metrics(CANONICAL_VLLM_METRICS, "1")
    assert any(l.startswith("# TYPE vllm:num_requests_waiting") for l in meta)
    assert not any(l.startswith("#") for l in series)
    assert all('replica="1"' in l for l in series)


def test_meta_present_even_when_replica_zero_down(client, monkeypatch):
    async def only_replica_one(port, method, path):
        if port == 5001:
            raise TimeoutError("replica 0 down")
        return MagicMock(status_code=200, text=CANONICAL_VLLM_METRICS)

    monkeypatch.setattr(proxy_module, "call_backend", only_replica_one)
    body = client.get("/metrics").text
    assert "# TYPE vllm:num_requests_waiting gauge" in body
    assert 'replica="1"' in body


def test_model_switch_invalidates_cache(client, monkeypatch):
    client.get("/metrics")
    assert vllm_source._last_good  # cache warm

    # model switch = setup_vllm_proxy() call = generation bump; the new
    # replicas are not scrapeable yet
    async def all_down(port, method, path):
        raise TimeoutError("loading new model")

    monkeypatch.setattr(proxy_module, "call_backend", all_down)
    monkeypatch.setattr(
        proxy_module, "vllm_setup_generation", proxy_module.vllm_setup_generation + 1
    )
    body = client.get("/metrics").text
    # stale series of the previous model must NOT survive the switch
    assert not any(line.startswith("vllm:") for line in body.splitlines())


def test_label_value_escaping_preserved():
    text = (
        '# TYPE vllm:request_success_total counter\n'
        'vllm:request_success_total{engine="0",finished_reason="st\\"op",'
        f'model_name="{HF_CACHE_MODEL}"}} 1.0\n'
    )
    _, series = filter_vllm_metrics(text, "0")
    assert len(series) == 1
    assert 'finished_reason="st\\"op"' in series[0]


def test_unrecognized_gonka_metrics_value_treated_as_full(client, monkeypatch):
    monkeypatch.setenv("GONKA_METRICS", "false")
    response = client.get("/metrics")
    assert response.status_code == 200  # contract is full|off; still exports


def test_host_memory_cgroup_v2_with_v1_fallback(tmp_path, monkeypatch):
    v2 = tmp_path / "v2"
    v2.mkdir()
    (v2 / "memory.current").write_text("1073741824\n")
    (v2 / "memory.max").write_text("max\n")
    monkeypatch.setattr(host_source, "CGROUP_V2_BASE", str(v2))
    monkeypatch.setattr(host_source, "CGROUP_V1_MEM_BASE", str(tmp_path / "absent"))
    values = host_source.collect_memory()
    assert values["mlnode_host_memory_used_bytes"] == 1073741824.0
    assert "mlnode_host_memory_limit_bytes" not in values  # "max" => no limit series

    v1 = tmp_path / "v1"
    v1.mkdir()
    (v1 / "memory.usage_in_bytes").write_text("536870912\n")
    (v1 / "memory.limit_in_bytes").write_text("2147483648\n")
    monkeypatch.setattr(host_source, "CGROUP_V2_BASE", str(tmp_path / "absent"))
    monkeypatch.setattr(host_source, "CGROUP_V1_MEM_BASE", str(v1))
    values = host_source.collect_memory()
    assert values["mlnode_host_memory_used_bytes"] == 536870912.0
    assert values["mlnode_host_memory_limit_bytes"] == 2147483648.0


def test_host_cpu_ratio_needs_two_samples(monkeypatch):
    samples = iter([
        (1000.0, 10.0, 2000.0),
        (1060.0, 14.0, 2100.0),
    ])
    monkeypatch.setattr(host_source, "_cpu_sample", lambda: next(samples))
    assert host_source.collect_cpu() == {}  # first scrape: no interval yet
    values = host_source.collect_cpu()
    assert values["mlnode_host_cpu_busy_ratio"] == pytest.approx(0.6)
    assert values["mlnode_host_cpu_steal_ratio"] == pytest.approx(0.04)


def test_own_families_carry_help_and_type(client, gpu_manager):
    gpu_manager.collect_metrics_async = AsyncMock(return_value=[
        {"gpu_index": "0", "gpu_model": "G", "fields": {"mlnode_gpu_pcie_replay_total": 0.0}},
        {"gpu_index": "1", "gpu_model": "G", "fields": {"mlnode_gpu_pcie_replay_total": 1.0}},
    ])
    body = client.get("/metrics").text
    assert "# TYPE mlnode_gpu_pcie_replay_total counter" in body
    assert "# TYPE gonka_metrics_schema_info gauge" in body
    assert "# TYPE mlnode_source_scrape_timestamp_seconds gauge" in body
    # samples of one family are contiguous under its TYPE header
    lines = body.splitlines()
    idx = lines.index("# TYPE mlnode_gpu_pcie_replay_total counter")
    assert lines[idx + 1].startswith("mlnode_gpu_pcie_replay_total{")
    assert lines[idx + 2].startswith("mlnode_gpu_pcie_replay_total{")


def test_fork_runner_without_accessor_degrades_gracefully(client):
    class LegacyRunner:
        model = "org/model"
        dtype = "auto"

    client.app.state.inference_manager = MagicMock(vllm_runner=LegacyRunner())
    response = client.get("/metrics")
    assert response.status_code == 200
    assert "mlnode_config_info" not in response.text  # degraded, not 500


def test_fork_proxy_without_generation_counter(client, monkeypatch):
    monkeypatch.delattr(proxy_module, "vllm_setup_generation", raising=False)
    response = client.get("/metrics")
    assert response.status_code == 200
    assert "vllm:num_requests_waiting" in response.text  # invalidation off, series intact


def test_every_emitted_gpu_family_has_meta():
    from api.gpu.manager import GPUManager
    from api.metrics.render import FAMILY_META

    emitted = set(GPUManager._METRIC_READERS) | {
        "mlnode_gpu_memory_used_bytes",
        "mlnode_gpu_memory_free_bytes",
    }
    missing = emitted - set(FAMILY_META)
    assert not missing, f"families without FAMILY_META entries: {missing}"


def test_setup_vllm_proxy_bumps_generation():
    before = proxy_module.vllm_setup_generation
    proxy_module.setup_vllm_proxy([5001])
    assert proxy_module.vllm_setup_generation == before + 1


def test_config_info_normalizes_model_and_keeps_unset_empty(client):
    runner = MagicMock()
    runner.get_config_summary.return_value = {
        "model": HF_CACHE_MODEL,
        "dtype": "auto",
        "max_num_seqs": 128,
        "max_model_len": 0,
        "tensor_parallel_size": 0,
        "pipeline_parallel_size": 0,
    }
    client.app.state.inference_manager = MagicMock(vllm_runner=runner)
    body = client.get("/metrics").text
    line = [l for l in body.splitlines() if l.startswith("mlnode_config_info{")][0]
    assert 'model_name="moonshotai/Kimi-K2.6"' in line  # normalized, not the path
    assert "/root/.cache" not in line
    assert 'max_num_seqs="128"' in line
    assert 'max_model_len=""' in line  # unset => empty per schema contract


@pytest.mark.asyncio
async def test_stalled_local_stage_degrades_not_hangs(gpu_manager, monkeypatch):
    # ASGITransport keeps one persistent loop: TestClient would join the
    # deliberately-abandoned worker thread at loop shutdown and hide the
    # handler's true latency.
    from httpx import ASGITransport, AsyncClient
    from api.metrics import routes as routes_module

    response_mock = MagicMock(status_code=200, text=CANONICAL_VLLM_METRICS)
    monkeypatch.setattr(proxy_module, "call_backend", AsyncMock(return_value=response_mock))

    async def stuck():
        await asyncio.sleep(30)

    gpu_manager.collect_metrics_async = MagicMock(side_effect=stuck)
    monkeypatch.setattr(routes_module, "NVML_TIMEOUT_SECONDS", 0.05)
    monkeypatch.setattr(routes_module, "HOST_TIMEOUT_SECONDS", 0.05)
    # short stall: the leaked worker is joined at test-teardown loop close
    monkeypatch.setattr(routes_module.host_source, "collect", lambda: time.sleep(3))

    app = FastAPI()
    app.state.gpu_manager = gpu_manager
    app.state.inference_manager = MagicMock(vllm_runner=None)
    app.include_router(router)

    start = time.monotonic()
    async with AsyncClient(transport=ASGITransport(app=app), base_url="http://test") as ac:
        response = await ac.get("/metrics")
    elapsed = time.monotonic() - start

    assert response.status_code == 200
    assert elapsed < 1.0, f"stalled local stages must not delay the response (took {elapsed:.2f}s)"
    assert "mlnode_gpu_" not in response.text  # degraded to missing series


def test_inflight_scrape_across_generation_bump_discarded(client, monkeypatch):
    client.get("/metrics")
    assert vllm_source._last_good

    gate = asyncio.Event()

    async def slow_backend(port, method, path):
        await gate.wait()
        return MagicMock(status_code=200, text=CANONICAL_VLLM_METRICS)

    monkeypatch.setattr(proxy_module, "call_backend", slow_backend)

    async def race():
        vllm_source.reset_cache()
        task = asyncio.create_task(vllm_source.collect(1.0))  # suspends in gather
        await asyncio.sleep(0.05)
        # model switch happens while the scrape is in flight
        proxy_module.vllm_setup_generation += 1
        vllm_source._last_good.clear()
        vllm_source._cache_generation = proxy_module.vllm_setup_generation
        gate.set()
        await task
        return dict(vllm_source._last_good)

    cache_after = asyncio.get_event_loop_policy().new_event_loop().run_until_complete(race())
    assert cache_after == {}  # stale in-flight results were discarded


def test_escape_label_values():
    from api.metrics.render import escape

    assert escape('a"b') == 'a\\"b'
    assert escape("a\\b") == "a\\\\b"
    assert escape("a\nb") == "a\\nb"


def test_stage_budgets_fit_dapi_per_node_timeout():
    # dapi's collector allows 5s per node; stages run sequentially
    from api.metrics.routes import HOST_TIMEOUT_SECONDS, NVML_TIMEOUT_SECONDS
    from api.metrics.vllm_source import SCRAPE_TIMEOUT_SECONDS

    assert SCRAPE_TIMEOUT_SECONDS + NVML_TIMEOUT_SECONDS + HOST_TIMEOUT_SECONDS < 5


def test_relative_local_path_normalized():
    # multi-segment relative path is clearly a path => basename
    assert normalize_model_name("local/models/MiniMax-M2.7") == "MiniMax-M2.7"
    # a single-slash value is indistinguishable from a canonical HF id => kept
    assert normalize_model_name("org/model") == "org/model"


@pytest.mark.asyncio
async def test_stalled_stage_not_reentered():
    import threading

    from api.metrics import routes as routes_module

    routes_module._pending_stages.clear()
    routes_module._warned_stalled.clear()
    release = threading.Event()
    submissions = {"n": 0}

    def stalled():
        submissions["n"] += 1
        release.wait()

    with pytest.raises(TimeoutError):
        await routes_module._bounded(asyncio.to_thread(stalled), 0.05, "test-src")
    # real scrapes arrive many loop iterations later — yield the loop
    await asyncio.sleep(0.05)
    with pytest.raises(TimeoutError):
        await routes_module._bounded(asyncio.to_thread(stalled), 10, "test-src")
    assert submissions["n"] == 1  # second call skipped: no new worker parked

    release.set()
    await asyncio.sleep(0.1)  # let the parked thread finish
    # source recovers once the abandoned task completes
    result = await routes_module._bounded(asyncio.to_thread(lambda: 42), 1, "test-src")
    assert result == 42
    routes_module._pending_stages.clear()
    routes_module._warned_stalled.clear()
    release = asyncio.Event()

    async def stall():
        await release.wait()

    with pytest.raises(TimeoutError):
        await routes_module._bounded(stall(), 0.05, "test-src")
    # previous task still parked: a second call must skip instantly
    start = time.monotonic()
    with pytest.raises(TimeoutError):
        await routes_module._bounded(stall(), 10, "test-src")
    assert time.monotonic() - start < 0.05  # skipped, not re-parked
    release.set()
    await asyncio.sleep(0)
    routes_module._pending_stages.clear()
