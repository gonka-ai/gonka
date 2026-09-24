#!/usr/bin/env python3
"""tok/s/GPU of a running MLNode under agent sessions, at high / low / zero cache hit.

Run inside the MLNode container with a model loaded:
    docker cp session_bench.py mlnode:/root/.cache/
    docker exec mlnode python3 /root/.cache/session_bench.py --sessions 32 --trust-remote-code
--sessions is concurrent sessions per GPU, spread over every vLLM instance on
5001-5008. First run fetches vLLM's benchmarks/multi_turn client (pinned commit)
and pandas. One run is three columns of ~4-7 min each.

Every session keeps one request in flight and resends its growing history each
turn: 6k-token shared system prompt, ~4k task context, 10-20 turns of ~800-token
tool output and 100-400-token answers. A one-off marker per request sets the hit:
  high  none                        -> the resent history is served from cache
  low   after the system prompt     -> only the system prompt is served
  zero  at the very start           -> nothing is served
Rates are slopes of the server's counters over --window seconds, taken once the
output rate has settled; billed = prompt (cache hits included) + output, prefill =
prompt the engine computed; reasoning tokens count as output. kv_overflow: KV >= 95 % or requests preempted --
past it sessions evict each other's history and the hit drops. waiting_max alone
can also come from the per-step token budget.
"""

import argparse, json, os, re, signal, subprocess, sys, threading, time, urllib.request, uuid
from pathlib import Path

HERE = Path(__file__).resolve().parent
MT_DIR = HERE / "multi_turn"
MT_URL = ("https://raw.githubusercontent.com/vllm-project/vllm/"
          "320c52b1342ad961091bb3333b867c0899907b06/benchmarks/multi_turn/")
MT_FILES = ("benchmark_serving_multi_turn.py", "bench_dataset.py", "bench_utils.py")
MODES = {"high": None, "low": "history", "zero": "all"}
PROFILE = {
    "num_turns": {"distribution": "uniform", "min": 20, "max": 40},
    "common_prefix_num_tokens": {"distribution": "constant", "value": 6000},
    "prefix_num_tokens": {"distribution": "lognormal", "average": 4000, "max": 16000},
    "num_tokens": {"distribution": "lognormal", "average": 800, "max": 4000},
}
ANSWER = {"num_tokens": {"distribution": "uniform", "min": 100, "max": 400}}
METRICS = {
    "gen": "generation_tokens_total", "prompt": "prompt_tokens_total",
    "cached": "prompt_tokens_cached_total", "preempt": "num_preemptions_total",
    "ttft_s": "time_to_first_token_seconds_sum", "ttft_n": "time_to_first_token_seconds_count",
    "itl_s": "inter_token_latency_seconds_sum", "itl_n": "inter_token_latency_seconds_count",
    "in_s": "request_prompt_tokens_sum", "in_n": "request_prompt_tokens_count",
    "running": "num_requests_running", "waiting": "num_requests_waiting",
    "kv": "kv_cache_usage_perc",
}


def run_client():
    """The multi_turn client with a cache-busting marker and a staggered start."""
    sys.path.insert(0, str(MT_DIR))
    import asyncio
    import benchmark_serving_multi_turn as mt
    bust, stagger = os.environ["SB_BUST"], float(os.environ["SB_STAGGER"])
    send_turn, client_main = mt.send_turn, mt.client_main

    async def busted_send_turn(session, client_id, conv_id, msgs, n, *a, **kw):
        if bust != "None":
            text, tag = msgs[0]["content"], f"[{uuid.uuid4().hex}] "
            m = re.search(r"\d+ is a nice number", text) if bust == "history" else None
            pos = m.start() if m else 0
            msgs = [{"role": msgs[0]["role"], "content": text[:pos] + tag + text[pos:]}] + msgs[1:]
        return await send_turn(session, client_id, conv_id, msgs, n, *a, **kw)

    async def staggered_client_main(args, req_args, client_id, *a, **kw):
        await asyncio.sleep(stagger * client_id)
        return await client_main(args, req_args, client_id, *a, **kw)

    class ReasoningAsContent:
        """The client reads only delta.content; with a reasoning parser a thinking
        model streams everything as reasoning, which the client would reject."""
        def __getattr__(self, name):
            return getattr(json, name)

        def loads(self, s, *a, **kw):
            d = json.loads(s, *a, **kw)
            try:
                delta = d["choices"][0].get("delta") or {}
                if not delta.get("content"):
                    delta["content"] = delta.get("reasoning_content") or delta.get("reasoning")
            except (KeyError, IndexError, TypeError, AttributeError):
                pass
            return d

    mt.json = ReasoningAsContent()
    mt.send_turn, mt.client_main = busted_send_turn, staggered_client_main
    sys.argv = [str(MT_DIR / MT_FILES[0])] + sys.argv[2:]
    asyncio.run(mt.main())


def scrape(url):
    try:
        body = urllib.request.urlopen(url + "/metrics", timeout=3).read().decode()
    except Exception:
        return {}
    out = {}
    for k, name in METRICS.items():
        v = re.findall(rf"^vllm:{name}(?:{{[^}}]*}})?\s+(\S+)$", body, re.M)
        if v:
            out[k] = sum(map(float, v))
    return out


class Sampler(threading.Thread):
    def __init__(self, urls):
        super().__init__(daemon=True)
        self.urls, self.s, self.done = urls, [], threading.Event()

    def run(self):
        while not self.done.wait(0.5):
            agg = {}
            for u in self.urls:
                for k, v in scrape(u).items():
                    # counters and request counts add up over instances; KV use is per instance
                    agg[k] = max(agg.get(k, 0.0), v) if k == "kv" else agg.get(k, 0.0) + v
            self.s.append((time.monotonic(), agg))

    def points(self, key, t0, t1):
        return [(t, a[key]) for t, a in self.s if key in a and t0 <= t <= t1]

    def rate(self, key, t0, t1):
        w = self.points(key, t0, t1)
        if len(w) < 4:
            return 0.0
        mt, mv = sum(t for t, _ in w) / len(w), sum(v for _, v in w) / len(w)
        den = sum((t - mt) ** 2 for t, _ in w)
        return sum((t - mt) * (v - mv) for t, v in w) / den if den else 0.0

    def delta(self, key, t0, t1):
        w = self.points(key, t0, t1)
        return w[-1][1] - w[0][1] if len(w) > 1 else 0.0

    def values(self, key, t0, t1):
        return [v for _, v in self.points(key, t0, t1)] or [0.0]


def run_mode(args, urls, model, gpus, mode, seed):
    work = Path(args.out) / "raw"
    work.mkdir(parents=True, exist_ok=True)
    per = args.sessions * gpus // len(urls)
    # filler text: vLLM's own sources, rotated per run, repeated to fit all conversations
    import vllm
    src = "".join(p.read_text(errors="ignore") for p in sorted(Path(vllm.__file__).parent.rglob("*.py")))
    k = seed * 1_500_000 % len(src)
    text = work / f"{args.label}_{mode}.txt"
    text.write_text((src[k:] + src[:k]) * (1 + per * 3 * 24_000 // len(src)))

    smp, procs, t_start = Sampler(urls), [], time.monotonic()
    smp.start()
    for i, u in enumerate(urls):
        cfg = work / f"{args.label}_{mode}_{i}.json"
        cfg.write_text(json.dumps({"filetype": "generate_conversations",
                                   "num_conversations": per * 3 + 2, "text_files": [str(text)],
                                   "prompt_input": PROFILE, "prompt_output": ANSWER}))
        cmd = [sys.executable, __file__, "--client", "--input-file", str(cfg), "--model", model,
               "--url", u, "--num-clients", str(per), "--max-active-conversations", str(per),
               "--request-timeout-sec", "3600", "--seed", str(seed * 100 + i), "--no-early-stop"]
        cmd += ["--trust-remote-code"] if args.trust_remote_code else []
        env = dict(os.environ, SB_BUST=str(MODES[mode]), SB_STAGGER=str(30 / per))
        procs.append(subprocess.Popen(cmd, stdout=open(work / f"{args.label}_{mode}_{i}.log", "w"),
                                      stderr=subprocess.STDOUT, env=env, start_new_session=True))

    # settled: three 20 s windows of the output rate within 10 %, timed from the first token
    t_load = t0 = None
    while t0 is None and time.monotonic() - t_start < 900:
        time.sleep(5)
        now = time.monotonic()
        r = [smp.rate("gen", now - 20 * (j + 1), now - 20 * j) for j in range(3)]
        t_load = t_load or (now if r[0] else None)
        if t_load and (now - t_load > 240 or
                       (now - t_load > 90 and min(r) > 0.9 * max(r))):
            t0 = now
    t0 = t0 or time.monotonic()
    time.sleep(args.window)
    t1 = time.monotonic()
    for p in procs:
        os.killpg(p.pid, signal.SIGKILL)
        p.wait()
    smp.done.set()
    while any(scrape(u).get("running") for u in urls):
        time.sleep(1)

    out, prompt, cached = (smp.rate(k, t0, t1) for k in ("gen", "prompt", "cached"))
    ttft_n, itl_n, in_n = (smp.delta(k, t0, t1) for k in ("ttft_n", "itl_n", "in_n"))
    running = smp.values("running", t0, t1)
    kv_max, waiting = max(smp.values("kv", t0, t1)), max(smp.values("waiting", t0, t1))
    preempt = smp.delta("preempt", t0, t1)
    return {
        "output_tps_per_gpu": round(out / gpus),
        "prefill_tps_per_gpu": round((prompt - cached) / gpus),
        "billed_tps_per_gpu": round((out + prompt) / gpus),
        "prefix_hit": round(cached / prompt, 3) if prompt else 0,
        "mean_input_tokens": round(smp.delta("in_s", t0, t1) / in_n) if in_n else None,
        "ttft_ms": round(1000 * smp.delta("ttft_s", t0, t1) / ttft_n) if ttft_n else None,
        "itl_ms": round(1000 * smp.delta("itl_s", t0, t1) / itl_n, 1) if itl_n else None,
        "running_mean": round(sum(running) / len(running)),
        "waiting_max": round(waiting),
        "kv_max": round(kv_max, 2),
        "preemptions": round(preempt),
        "kv_overflow": kv_max >= 0.95 or preempt > 0,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--sessions", type=int, required=True, help="concurrent sessions per GPU")
    ap.add_argument("--window", type=int, default=180, help="measured seconds per column")
    ap.add_argument("--ports", default="5001,5002,5003,5004,5005,5006,5007,5008")
    ap.add_argument("--label", default=os.uname().nodename)
    ap.add_argument("--out", default=str(HERE / "session_bench_out"))
    ap.add_argument("--trust-remote-code", action="store_true")
    args = ap.parse_args()

    MT_DIR.mkdir(exist_ok=True)
    for f in MT_FILES:
        if not (MT_DIR / f).exists():
            (MT_DIR / f).write_bytes(urllib.request.urlopen(MT_URL + f, timeout=60).read())
    subprocess.run([sys.executable, "-m", "pip", "install", "-q", "pandas"], check=False)
    import resource
    resource.setrlimit(resource.RLIMIT_NOFILE, (65536, 65536))

    urls, model = [], None
    for p in args.ports.split(","):
        try:
            r = urllib.request.urlopen(f"http://127.0.0.1:{p}/v1/models", timeout=2)
            model = json.loads(r.read())["data"][0]["id"]
            urls.append(f"http://127.0.0.1:{p}")
        except Exception:
            pass
    if not urls:
        sys.exit("no vLLM instance answers on " + args.ports)
    gpus = len(subprocess.run(["nvidia-smi", "-L"], capture_output=True, text=True).stdout.splitlines())

    res = {"model": model, "instances": urls, "gpus": gpus, "sessions_per_gpu": args.sessions}
    for i, mode in enumerate(MODES):
        res[mode] = run_mode(args, urls, model, gpus, mode, int(time.time()) % 97 + i)
        print(f"{mode:>4} hit: {res[mode]}", flush=True)
    Path(args.out, f"{args.label}_s{args.sessions}.json").write_text(json.dumps(res, indent=2))
    print(f"\n{model}, {gpus} GPU, {args.sessions} sessions/GPU (tok/s per GPU)")
    for key in res["high"]:
        print(f"{key:20}" + "".join(f"{str(res[m][key]):>12}" for m in MODES))


if __name__ == "__main__":
    run_client() if sys.argv[1:2] == ["--client"] else main()
