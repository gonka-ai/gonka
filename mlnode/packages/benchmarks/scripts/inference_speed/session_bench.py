#!/usr/bin/env python3
"""tok/s/GPU of a live MLNode under agent sessions, at three cache-hit levels.

Load: closed-loop agent sessions from vLLM's benchmarks/multi_turn (via mt_bust.py):
every session keeps one request in flight, resends its growing history each turn,
and all sessions share one system prompt. The number of sessions is an input.

The same sessions are run three times:
  high   history resent as is: the prefix cache serves most of every prompt
  low    history made unique per request: only the shared system prompt is served
  zero   the whole prompt made unique per request: nothing is served
so the three columns differ only in how much of the prompt the engine recomputes.

Each run is time-boxed: sessions start spread over --spread seconds, the server's
counters are sampled twice a second, and once the generation rate has settled a
--window of seconds is measured; then the client is killed. All rates come from
the server, so client start-up, ramp and drain never enter them.

KV overflow is flagged when, over the window, the cache is nearly full, requests
wait for room, or running requests get preempted: past that point sessions evict
each other's history and the hit rate is no longer what the workload allows.
"""

import argparse, json, os, re, signal, statistics, subprocess, sys, threading, time
import urllib.request
from pathlib import Path

HERE = os.path.dirname(os.path.abspath(__file__))
# vLLM's multi-turn benchmark client, pinned; fetched next to this script on first run
MT_COMMIT = "320c52b1342ad961091bb3333b867c0899907b06"
MT_FILES = ("benchmark_serving_multi_turn.py", "bench_dataset.py", "bench_utils.py")
MT_URL = "https://raw.githubusercontent.com/vllm-project/vllm/{}/benchmarks/multi_turn/{}"
COUNTERS = {
    "gen": "vllm:generation_tokens_total",
    "prompt": "vllm:prompt_tokens_total",
    # prompt tokens served from the prefix cache, counted once per request
    # (prefix_cache_hits_total re-counts a request on every scheduling attempt)
    "cached": "vllm:prompt_tokens_cached_total",
    "preempt": "vllm:num_preemptions_total",
    "ttft_s": "vllm:time_to_first_token_seconds_sum",
    "ttft_n": "vllm:time_to_first_token_seconds_count",
    "itl_s": "vllm:inter_token_latency_seconds_sum",
    "itl_n": "vllm:inter_token_latency_seconds_count",
    "running": "vllm:num_requests_running",
    "waiting": "vllm:num_requests_waiting",
    "kv": "vllm:kv_cache_usage_perc",
}
MODES = {"high": "none", "low": "history", "zero": "all"}

# Agent profile: a shared system prompt (tool definitions), a per-task context,
# then turns of tool output and short answers.
PROFILE = {
    "common_prefix_num_tokens": {"distribution": "constant", "value": 6000},
    "prefix_num_tokens": {"distribution": "lognormal", "average": 4000, "max": 16000},
    "num_turns": {"distribution": "uniform", "min": 20, "max": 40},
    "num_tokens": {"distribution": "lognormal", "average": 800, "max": 4000},
    "output_num_tokens": {"distribution": "uniform", "min": 100, "max": 400},
}


def scrape(url):
    out = {}
    try:
        with urllib.request.urlopen(url + "/metrics", timeout=3) as r:
            body = r.read().decode("utf-8", "replace")
    except Exception:
        return out
    for k, name in COUNTERS.items():
        vals = [float(m.group(1)) for m in re.finditer(
            rf"^{re.escape(name)}(?:\{{[^}}]*\}})?\s+([0-9.eE+-]+)$", body, re.M)]
        if vals:
            out[k] = sum(vals)
    return out


class Sampler(threading.Thread):
    def __init__(self, urls, period=0.5):
        super().__init__(daemon=True)
        self.urls, self.period, self.s = urls, period, []
        self.done = threading.Event()

    def run(self):
        while not self.done.is_set():
            t, agg = time.monotonic(), {}
            for u in self.urls:
                for k, v in scrape(u).items():
                    agg[k] = agg.get(k, 0.0) + v
            if agg:
                self.s.append((t, agg))
            self.done.wait(self.period)

    def rate(self, key, t0, t1):
        w = [(t, a[key]) for t, a in self.s if key in a and t0 <= t <= t1]
        if len(w) < 4:
            return None
        n = len(w)
        mt, mv = sum(t for t, _ in w) / n, sum(v for _, v in w) / n
        den = sum((t - mt) ** 2 for t, _ in w)
        return sum((t - mt) * (v - mv) for t, v in w) / den if den else None

    def delta(self, key, t0, t1):
        w = [a[key] for t, a in self.s if key in a and t0 <= t <= t1]
        return (w[-1] - w[0]) if len(w) >= 2 else 0.0

    def stat(self, key, t0, t1, fn=statistics.mean):
        w = [a[key] for t, a in self.s if key in a and t0 <= t <= t1]
        return fn(w) if w else 0.0


def discover(ports):
    found = []
    for p in ports:
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{p}/v1/models", timeout=2) as r:
                found.append((f"http://127.0.0.1:{p}", json.loads(r.read())["data"][0]["id"]))
        except Exception:
            pass
    return found


def fetch_client(mt_dir):
    Path(mt_dir).mkdir(parents=True, exist_ok=True)
    for f in MT_FILES:
        dst = Path(mt_dir) / f
        if not dst.exists():
            with urllib.request.urlopen(MT_URL.format(MT_COMMIT, f), timeout=60) as r:
                dst.write_bytes(r.read())


def default_text_src():
    import importlib.util
    spec = importlib.util.find_spec("vllm")
    if spec and spec.submodule_search_locations:
        return list(spec.submodule_search_locations)[0]
    return os.path.dirname(os.__file__)


def gpu_count():
    r = subprocess.run("nvidia-smi --query-gpu=name --format=csv,noheader", shell=True,
                       capture_output=True, text=True)
    lines = [x.strip() for x in r.stdout.splitlines() if x.strip()]
    return len(lines), (lines[0] if lines else "?")


def build_text(src_dir, out, shift, min_chars):
    """Filler text: vLLM's own sources, rotated per run so no run reuses another's,
    repeated up to min_chars. Repeats cannot share a cached prefix: every
    conversation carries its own id right after the shared system prompt."""
    parts, size = [], 0
    for p in sorted(Path(src_dir).rglob("*.py")):
        try:
            t = p.read_text(errors="ignore")
        except Exception:
            continue
        parts.append(t)
        size += len(t)
        if size > 16_000_000:
            break
    text = "".join(parts)
    k = (shift * 1_500_000) % len(text)
    text = text[k:] + text[:k]
    Path(out).write_text(text * (1 + min_chars // len(text)))


def run_mode(args, urls, model, sessions, mode, seed, tag):
    work = Path(args.out) / "raw"
    work.mkdir(parents=True, exist_ok=True)
    per = max(1, sessions // len(urls))
    # enough conversations that no client runs out before the window ends
    convs = per * args.conv_per_session + 2
    text = work / f"{tag}_text.txt"
    build_text(args.text_src, text, seed, min_chars=convs * 24_000 + 4_000_000)
    env = dict(os.environ, MT_DIR=args.mt_dir, MT_CACHE_BUST=MODES[mode],
               MT_STAGGER=str(args.spread), MT_CLIENTS=str(per))

    smp = Sampler(urls)
    smp.start()
    t_start = time.monotonic()
    running = []
    for i, u in enumerate(urls):
        cfg = {"filetype": "generate_conversations", "num_conversations": convs,
               "text_files": [str(text)], "print_stats": False,
               "prompt_input": {k: PROFILE[k] for k in ("num_turns", "common_prefix_num_tokens",
                                                        "prefix_num_tokens", "num_tokens")},
               "prompt_output": {"num_tokens": PROFILE["output_num_tokens"]}}
        cfg_path = work / f"{tag}_i{i}.json"
        cfg_path.write_text(json.dumps(cfg))
        cmd = [sys.executable, os.path.join(HERE, "mt_bust.py"),
               "--input-file", str(cfg_path), "--model", model, "--url", u,
               "--num-clients", str(per), "--max-active-conversations", str(per),
               "--request-timeout-sec", "3600", "--seed", str(seed * 1000 + i),
               "--no-early-stop"]
        if args.trust_remote_code:
            cmd.append("--trust-remote-code")
        logf = open(work / f"{tag}_i{i}.log", "w")
        running.append((subprocess.Popen(cmd, stdout=logf, stderr=subprocess.STDOUT, env=env,
                                         cwd=args.mt_dir, start_new_session=True), logf))

    # settled: three adjacent windows of the generation rate agree. The ramp is
    # timed from the first generated token: building the conversations for
    # hundreds of sessions takes the client a while before it sends anything.
    settled_at, trace, W, t_load = None, [], args.settle_win, None
    while True:
        time.sleep(5)
        now = time.monotonic()
        r = [smp.rate("gen", now - (k + 1) * W, now - k * W) for k in range(3)]
        trace.append(round(r[0] or 0))
        if t_load is None and r[0]:
            t_load = now
        if t_load is None:
            if now - t_start > args.prep_timeout:
                break
            continue
        if all(r) and (max(r) - min(r)) / max(r) < args.settle_tol \
                and now - t_load > args.min_ramp:
            settled_at = now
            break
        if now - t_load > args.max_ramp:
            break
    t0 = settled_at or time.monotonic()
    time.sleep(args.window)
    t1 = time.monotonic()

    for p, logf in running:
        try:
            os.killpg(p.pid, signal.SIGKILL)
        except Exception:
            pass
        p.wait()
        logf.close()
    smp.done.set()
    smp.join(timeout=5)
    for _ in range(90):
        if not any(scrape(u).get("running", 0) for u in urls):
            break
        time.sleep(1)

    g = args.gpus
    out = smp.rate("gen", t0, t1) or 0.0
    prompt = smp.rate("prompt", t0, t1) or 0.0
    hit = smp.rate("cached", t0, t1) or 0.0
    ttft_n, itl_n = smp.delta("ttft_n", t0, t1), smp.delta("itl_n", t0, t1)
    ttft = smp.delta("ttft_s", t0, t1) / ttft_n if ttft_n else None
    itl = smp.delta("itl_s", t0, t1) / itl_n if itl_n else None
    kv_max = smp.stat("kv", t0, t1, max)
    waiting = smp.stat("waiting", t0, t1)
    preempt = smp.delta("preempt", t0, t1)
    overflow = []
    if kv_max >= args.kv_full:
        overflow.append(f"kv {kv_max:.0%}")
    if waiting >= 1:
        overflow.append(f"{waiting:.0f} waiting")
    if preempt > 0:
        overflow.append(f"{preempt:.0f} preempted")
    return {
        "mode": mode, "sessions": sessions, "sessions_per_gpu": round(sessions / g, 1),
        "settled": settled_at is not None, "ramp_s": round(t0 - t_start), "window_s": round(t1 - t0),
        "output_tps_per_gpu": round(out / g),
        "prefill_tps_per_gpu": round(max(prompt - hit, 0) / g),
        "billed_tps_per_gpu": round((out + prompt) / g),
        "prefix_hit": round(hit / prompt, 3) if prompt else 0.0,
        "ttft_ms": round(1000 * ttft) if ttft else None,
        "itl_ms": round(1000 * itl, 1) if itl else None,
        "tps_per_session": round(1 / itl, 1) if itl else None,
        "running": round(smp.stat("running", t0, t1), 1), "waiting": round(waiting, 1),
        "kv_mean": round(smp.stat("kv", t0, t1), 3), "kv_max": round(kv_max, 3),
        "preemptions": preempt, "kv_overflow": ", ".join(overflow) or None,
        "ramp_trace": trace,
    }


ROWS = [("output tok/s/GPU", "output_tps_per_gpu"),
        ("prefill computed tok/s/GPU", "prefill_tps_per_gpu"),
        ("billed tok/s/GPU (in+out)", "billed_tps_per_gpu"),
        ("prefix cache hit", "prefix_hit"),
        ("TTFT mean, ms", "ttft_ms"),
        ("ITL mean, ms", "itl_ms"),
        ("tok/s per session", "tps_per_session"),
        ("running / waiting", None),
        ("KV used mean / max", None),
        ("KV overflow", "kv_overflow")]


def table(res):
    cols = res["modes"]
    head = f"{'':30}" + "".join(f"{c['mode'] + ' hit':>22}" for c in cols)
    lines = [head]
    for name, key in ROWS:
        if key is None and name.startswith("running"):
            vals = [f"{c['running']:.0f} / {c['waiting']:.0f}" for c in cols]
        elif key is None:
            vals = [f"{c['kv_mean']:.0%} / {c['kv_max']:.0%}" for c in cols]
        else:
            vals = [c.get(key) for c in cols]
            vals = [("YES" if v else "no") if key == "kv_overflow" else
                    (f"{v:.0%}" if key == "prefix_hit" else ("-" if v is None else str(v)))
                    for v in vals]
        lines.append(f"{name:30}" + "".join(f"{v:>22}" for v in vals))
    for c in cols:
        if c["kv_overflow"]:
            lines.append(f"  KV overflow, {c['mode']} hit: {c['kv_overflow']}")
        if not c["settled"]:
            lines.append(f"  {c['mode']} hit: rate did not settle within --max-ramp")
    return "\n".join(lines)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--sessions", type=int, required=True, help="concurrent sessions per GPU")
    ap.add_argument("--modes", default="high,low,zero")
    ap.add_argument("--ports", default="5001,5002,5003,5004,5005,5006,5007,5008")
    ap.add_argument("--gpus", type=int, default=0, help="GPUs the instances occupy (0: all visible)")
    ap.add_argument("--label", default=os.uname().nodename)
    ap.add_argument("--window", type=int, default=90, help="measured seconds per mode")
    ap.add_argument("--spread", type=float, default=30, help="seconds to start all sessions")
    ap.add_argument("--min-ramp", type=int, default=90)
    ap.add_argument("--max-ramp", type=int, default=240)
    ap.add_argument("--settle-win", type=int, default=20)
    ap.add_argument("--prep-timeout", type=int, default=900,
                    help="seconds allowed for the client to send its first request")
    ap.add_argument("--settle-tol", type=float, default=0.10)
    ap.add_argument("--conv-per-session", type=int, default=6,
                    help="conversations generated per session")
    ap.add_argument("--kv-full", type=float, default=0.95, help="KV usage counted as full")
    ap.add_argument("--mt-dir", default=os.path.join(HERE, "multi_turn"),
                    help="vLLM benchmarks/multi_turn; fetched here if missing")
    ap.add_argument("--text-src", default=None,
                    help="directory of .py files used as prompt filler (default: vllm package)")
    ap.add_argument("--out", default=os.path.join(HERE, "session_bench_out"))
    ap.add_argument("--trust-remote-code", action="store_true")
    args = ap.parse_args()

    args.text_src = args.text_src or default_text_src()
    fetch_client(args.mt_dir)
    try:
        import pandas  # noqa: F401  (benchmarks/multi_turn needs it)
    except ImportError:
        subprocess.run([sys.executable, "-m", "pip", "install", "-q", "pandas"], check=False)
    try:
        import resource
        resource.setrlimit(resource.RLIMIT_NOFILE, (65536, 65536))
    except Exception:
        pass

    inst = discover([int(p) for p in args.ports.split(",")])
    if not inst:
        sys.exit("no vLLM instance answers on " + args.ports)
    urls, model = [u for u, _ in inst], inst[0][1]
    ngpu, gname = gpu_count()
    args.gpus = args.gpus or ngpu
    sessions = args.sessions * args.gpus
    print(f"[{args.label}] {model}, {len(urls)} instance(s), {args.gpus}x {gname}, "
          f"{args.sessions} sessions/GPU", flush=True)

    t_all, cols = time.monotonic(), []
    seed0 = int(time.time()) % 97
    for k, mode in enumerate(args.modes.split(",")):
        c = run_mode(args, urls, model, sessions, mode, seed0 + k,
                     f"{args.label}_s{args.sessions}_{mode}")
        cols.append(c)
        print(f"[{args.label}] {mode:>4} hit: out/gpu {c['output_tps_per_gpu']}  "
              f"hit {c['prefix_hit']:.0%}  ttft {c['ttft_ms']}ms  itl {c['itl_ms']}ms  "
              f"kv overflow: {c['kv_overflow'] or 'no'}"
              f"{'' if c['settled'] else '  (NOT SETTLED)'}", flush=True)

    res = {"label": args.label, "model": model, "instances": urls, "gpus": args.gpus,
           "gpu": gname, "sessions_per_gpu": args.sessions, "profile": PROFILE,
           "minutes": round((time.monotonic() - t_all) / 60, 1),
           "time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "modes": cols}
    Path(args.out).mkdir(parents=True, exist_ok=True)
    (Path(args.out) / f"{args.label}_s{args.sessions}.json").write_text(json.dumps(res, indent=2))
    print(f"\n{model}  {args.gpus}x {gname}  {args.sessions} sessions/GPU  "
          f"({res['minutes']} min)\n" + table(res), flush=True)


if __name__ == "__main__":
    main()
