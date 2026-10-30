#!/usr/bin/env python3
"""Build honest vLLM chat-completion fixtures without a GPU, weights or network.

For every case the assistant turn is rendered with the model's own chat template,
tokenized with the model's tokenizer and replayed one token per engine step
through vLLM's real OutputProcessor (incremental detokenizer, stop strings,
logprobs processor) and the real OpenAIServingChat stream / full generators
(reasoning + tool parsers, logprobs formatting, usage). The result is what a
vLLM server of this exact build would have streamed and counted for that answer.

Only logprob values and top-k alternative ids are synthetic: the sampled token
is always rank 1 with logprob -0.1, alternative r has logprob -0.1 - r.

Must run inside the vLLM image, next to a `vllm launch render` server started
with the same model arguments (see run.sh). Environment:
  MODEL, REV, ARGS   model id, pinned revision, serving arguments
  IMAGE, IMAGE_ID    image tag and id, recorded in every fixture
  TOOL_HEADER        per-model tolerance for tool-call cases (tokens)
  RENDER_URL         render server base URL (default http://localhost:8000)
Usage: gen.py <cases.json> <out_dir> [case-name ...]
"""
import asyncio
import copy
import hashlib
import inspect
import json
import os
import sys
import time
import traceback
import urllib.error
import urllib.request

import numpy as np

MODEL = os.environ["MODEL"]
REV = os.environ["REV"]
ARGS = os.environ["ARGS"].split()
IMAGE = os.environ.get("IMAGE", "")
IMAGE_ID = os.environ.get("IMAGE_ID", "")
TOOL_HEADER = int(os.environ.get("TOOL_HEADER", "0"))
RENDER_URL = os.environ.get("RENDER_URL", "http://localhost:8000")
# which chat mode the model's template uses when the request sets no chat_template_kwargs
DEFAULT_MODE = os.environ.get("DEFAULT_MODE") or None
# chat_template_kwargs of the case being built (request, local template and render server all use them)
CUR_KWARGS = {}
BASE_TOLERANCE = 8
FIXTURE_FORMAT = 1

# Gateway and host executor whose request rewrites forward_request reproduces.
GATEWAY_SRC = "gonka-ai/gonka@af2618b5acbe22df75d1c6f899fbbc8b96998e8c"
GATEWAY_BRANCH = "devshard-0.2.x-v6"
GATEWAY_DEFAULT_MAX_TOKENS = 3072  # default_request_max_tokens (gateway.go:251), runtime-configurable
GATEWAY_MAX_TOKENS_CAP = 4096      # request_max_tokens_cap (gateway.go:252), runtime-configurable
HOST_LOGPROBS_MODE = "processed_logprobs"  # chain default logprobs_mode (types/params.go:90-92)
# parameter names the gateway whitelist accepts (request_filters_parameters.go:433-697)
GATEWAY_KNOWN = {
    "messages", "seed", "n", "temperature", "repetition_penalty", "logit_bias", "stop", "stop_token_ids",
    "reasoning", "reasoning_effort", "enable_thinking", "thinking", "chat_template_kwargs",
    "thinking_token_budget", "tools", "tool_choice", "min_tokens", "bad_words", "user", "metadata",
    "stream_options", "return_token_ids", "logprobs", "top_logprobs", "response_format", "structured_outputs",
    "safety_identifier", "reasoning_split", "max_tokens", "max_completion_tokens", "min_p", "top_p", "top_k",
    "model", "stream", "skip_special_tokens", "detokenize", "parallel_tool_calls", "service_tier", "store",
    "provider", "plugins", "prompt_cache_key", "cache_key", "extra_headers", "thinking_config", "think",
    "frequency_penalty", "presence_penalty"}


def arg(name, default=None):
    return ARGS[ARGS.index(name) + 1] if name in ARGS else default


import vllm  # noqa: E402
from vllm.engine.arg_utils import EngineArgs  # noqa: E402
from vllm.entrypoints.openai.chat_completion import serving as serving_mod  # noqa: E402
from vllm.entrypoints.openai.chat_completion.protocol import ChatCompletionRequest  # noqa: E402
from vllm.entrypoints.openai.chat_completion.serving import OpenAIServingChat  # noqa: E402
from vllm.entrypoints.serve.utils.api_utils import get_max_tokens  # noqa: E402
from vllm.parser.parser_manager import ParserManager  # noqa: E402
from vllm.tokenizers import get_tokenizer  # noqa: E402
from vllm.v1.engine import EngineCoreOutput, EngineCoreRequest, FinishReason  # noqa: E402
from vllm.v1.engine.detokenizer import IncrementalDetokenizer  # noqa: E402
from vllm.v1.engine.output_processor import OutputProcessor  # noqa: E402
from vllm.v1.outputs import LogprobsLists  # noqa: E402

TOOL_PARSER = arg("--tool-call-parser")
REASONING_PARSER = arg("--reasoning-parser")
TRUST = "--trust-remote-code" in ARGS

tok = get_tokenizer(MODEL, revision=REV, tokenizer_mode=arg("--tokenizer-mode", "auto"),
                    trust_remote_code=TRUST)
model_config = EngineArgs(model=MODEL, revision=REV, tokenizer_mode=arg("--tokenizer-mode", "auto"),
                          trust_remote_code=TRUST,
                          max_model_len=int(arg("--max-model-len")) if arg("--max-model-len") else None,
                          ).create_model_config()
GEN_CONFIG = model_config.try_get_generation_config()
DEFAULT_SAMPLING = model_config.get_diff_sampling_param()
OVERRIDE_MAX_TOKENS = (DEFAULT_SAMPLING.get("max_tokens")
                       if model_config.generation_config not in ("auto", "vllm")
                       else getattr(model_config, "override_generation_config", {}).get("max_new_tokens"))
PARSER_CLS = ParserManager.get_parser(tool_parser_name=TOOL_PARSER, reasoning_parser_name=REASONING_PARSER,
                                      enable_auto_tools="--enable-auto-tool-choice" in ARGS,
                                      model_name=MODEL, is_harmony=False)
VOCAB = len(tok)


# ---------------------------------------------------------------- build identity

def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def build_identity():
    import tokenizers
    import transformers
    from huggingface_hub.constants import HF_HUB_CACHE
    snap = os.path.join(HF_HUB_CACHE, "models--" + MODEL.replace("/", "--"), "snapshots", REV)
    if not os.path.isdir(snap):
        raise RuntimeError(f"snapshot directory {snap} not found")
    files = {}
    for root, _, names in os.walk(snap):
        for n in sorted(names):
            p = os.path.join(root, n)
            if n.endswith((".safetensors", ".bin", ".pt", ".pth", ".gguf")):
                continue
            files[os.path.relpath(p, snap)] = sha256_file(p)
    combined = hashlib.sha256("".join(f"{k}:{v}\n" for k, v in sorted(files.items())).encode()).hexdigest()
    template = getattr(tok, "chat_template", None)
    probe_req = EngineCoreRequest(request_id="probe", prompt_token_ids=[0], mm_features=None,
                                  sampling_params=ChatCompletionRequest(model=MODEL, messages=[{"role": "user", "content": "x"}])
                                  .to_sampling_params(1, {}), pooling_params=None, arrival_time=0.0, lora_request=None,
                                  cache_salt=None, data_parallel_rank=None, external_req_id="probe")
    return {
        "model": MODEL,
        "revision": REV,
        "image": IMAGE,
        "image_id": IMAGE_ID,
        "variant": os.environ.get("VARIANT") or None,
        "build_note": os.environ.get("BUILD_NOTE") or None,
        "vllm_overlay": ({"source": "gonka-ai/vllm", "ref": os.environ.get("FORK_REF"), "sha": os.environ["FORK_SHA"],
                          "note": "which fork commit went into the ML node image this variant models is not known; "
                                  "this ref's name matches the gonka-poc ref that image installs"}
                         if os.environ.get("FORK_SHA") else None),
        "vllm_version": vllm.__version__,
        "transformers_version": transformers.__version__,
        "tokenizers_version": tokenizers.__version__,
        "serving_args": ARGS,
        "tokenizer_class": [c.__name__ for c in type(tok).__mro__[:3]],
        "detokenizer_class": type(IncrementalDetokenizer.from_new_request(tok, probe_req)).__name__,
        "parser_class": f"{PARSER_CLS.__module__}.{PARSER_CLS.__qualname__}",
        "eos_token_id": tok.eos_token_id,
        "generation_config_eos_token_id": GEN_CONFIG.get("eos_token_id"),
        "chat_template_sha256": hashlib.sha256(template.encode()).hexdigest() if isinstance(template, str) else None,
        "tokenizer_files_sha256": files,
        "tokenizer_sha256": combined,
    }


# ---------------------------------------------------------------- render server

def render(body):
    req = urllib.request.Request(f"{RENDER_URL}/v1/chat/completions/render", json.dumps(body).encode(),
                                 {"Content-Type": "application/json"})
    try:
        return json.load(urllib.request.urlopen(req, timeout=120)), None
    except urllib.error.HTTPError as e:
        return None, f"HTTP {e.code}: {e.read().decode(errors='replace')[:400]}"


def render_len(messages, tools, add_generation_prompt):
    body = {"model": MODEL, "messages": messages, "add_generation_prompt": add_generation_prompt}
    if CUR_KWARGS:
        body["chat_template_kwargs"] = CUR_KWARGS
    if tools:
        body["tools"] = tools
    res, err = render(body)
    return (len(res["token_ids"]), None) if res else (None, err)


# ---------------------------------------------------------------- case -> raw generation

def text_of(v):
    if isinstance(v, list):
        return "".join(p if isinstance(p, str) else p["repeat"] * p["times"] for p in v)
    return v or ""


def intended_turn(answer):
    """The answer as an OpenAI assistant message (arguments as JSON strings)."""
    turn = {"role": "assistant", "content": text_of(answer.get("content"))}
    if answer.get("reasoning"):
        turn["reasoning"] = text_of(answer["reasoning"])
    calls = answer.get("tool_calls") or []
    if calls:
        turn["tool_calls"] = [{"id": f"call_{i}", "type": "function",
                               "function": {"name": c["name"], "arguments": json.dumps(c["arguments"], ensure_ascii=False)}}
                              for i, c in enumerate(calls)]
    return turn


def local_render(messages, tools, add_generation_prompt):
    # vLLM hands templates tool-call arguments as dicts and the reasoning under both names.
    msgs = copy.deepcopy(messages)
    for m in msgs:
        if m.get("reasoning"):
            m["reasoning_content"] = m["reasoning"]
        for c in m.get("tool_calls") or []:
            c["function"]["arguments"] = json.loads(c["function"]["arguments"])
    return tok.apply_chat_template(msgs, tools=tools, tokenize=False, add_generation_prompt=add_generation_prompt,
                                   **CUR_KWARGS)


def enc(s):
    return tok.encode(s, add_special_tokens=False)


def raw_generation(messages, tools, turn, stop_ids):
    """Token ids the model emits for `turn`, ending with the stop token it would sample."""
    prefix = local_render(messages, tools, True)
    full = local_render(messages + [turn], tools, False)
    if not full.startswith(prefix):
        k = next((i for i, (a, b) in enumerate(zip(prefix, full)) if a != b), min(len(prefix), len(full)))
        raise ValueError(f"rendered assistant turn does not extend the generation prompt: prompt ends "
                         f"{prefix[max(0, k - 40):][:80]!r}, turn has {full[max(0, k - 40):][:80]!r}")
    raw = full[len(prefix):]
    ids = enc(raw)
    for j, t in enumerate(ids):
        if t in stop_ids:
            return ids[:j + 1], {"id": t, "text": tok.decode([t]), "source": "assistant turn of the chat template",
                                 "dropped_template_tail": tok.decode(ids[j + 1:])}
    nxt = ({"role": "tool", "tool_call_id": "call_0", "content": "{}"} if turn.get("tool_calls")
           else {"role": "user", "content": "Continue."})
    full2 = local_render(messages + [turn, nxt], tools, False)
    if not full2.startswith(full):
        raise ValueError("template output for the next turn does not extend the assistant turn")
    nid = enc(full2[len(full):])[0]
    if nid not in stop_ids:
        raise ValueError(f"no stop token closes the assistant turn: next token {nid} {tok.decode([nid])!r} "
                         f"not in stop set {sorted(stop_ids)}")
    return ids + [nid], {"id": nid, "text": tok.decode([nid]), "source": f"first token of the following {nxt['role']} turn",
                         "dropped_template_tail": ""}


def split_tokens(ids, spec):
    """Spell `fraction` of the multi-byte tokens of a generation as their single-byte tokens, spread evenly.
    The text stays the same, so this is a generation a sampler may emit for the answer, while a render of the
    served turn counts it canonically. Byte-level BPE only: every character of a piece is one byte and has
    its own token. `only: "whitespace"` limits it to tokens that decode to whitespace."""
    frac = spec["fraction"]
    added = set(getattr(tok, "added_tokens_decoder", None) or {})
    special = set(tok.all_special_ids) | added
    pieces = tok.convert_ids_to_tokens(ids)
    eligible = [i for i, (t, p) in enumerate(zip(ids, pieces)) if t not in special and len(p) > 1
                and (spec.get("only") != "whitespace" or not tok.decode([t]).strip())]
    pick = {eligible[j] for j in range(len(eligible)) if int((j + 1) * frac) > int(j * frac)}
    out = []
    for i, (t, p) in enumerate(zip(ids, pieces)):
        if i not in pick:
            out.append(t)
            continue
        sub = tok.convert_tokens_to_ids(list(p))
        if any(s is None or s == tok.unk_token_id or s in special for s in sub):
            raise ValueError(f"piece {p!r} of token {t} has a character without a single-byte token: {sub}")
        out += sub
    if tok.decode(out, skip_special_tokens=False) != tok.decode(ids, skip_special_tokens=False):
        raise ValueError("splitting changed the decoded text")
    return out, {"fraction": frac, "only": spec.get("only"), "eligible_tokens": len(eligible), "split_tokens": len(pick),
                 "canonical_ids": len(ids), "ids": len(out)}


# ---------------------------------------------------------------- request and sampling params

class GatewayRejected(Exception):
    def __init__(self, rule, detail):
        super().__init__(f"{rule}: {detail}")
        self.rule, self.detail = rule, detail


def go_json(v):
    # Gateway and host both decode into map[string]any and re-encode: object keys come out sorted.
    return json.loads(json.dumps(v, sort_keys=True, ensure_ascii=False))


def forward_request(client):
    """The body the ML node receives for `client`: gateway normalisation, then the host executor's rewrite.
    Returns (body, applied); applied lists every change with the source lines that make it."""
    b = copy.deepcopy(client)
    applied = []
    gw = "devshard/cmd/devshardctl/"

    def note(src, what):
        applied.append({"source": src, "change": what})

    def reject(src, detail):
        raise GatewayRejected(f"{GATEWAY_SRC}:{src}", detail)

    unknown = sorted(set(b) - GATEWAY_KNOWN)
    if unknown:
        reject(gw + "request_filters_parameters.go:751-770", f"unsupported parameter {unknown[0]!r}")
    # gateway, message normalizers: an empty assistant content next to tool calls becomes null, an empty tool
    # result the sentinel (EmptyContentNormalizer; the other normalizers leave these cases alone)
    for i, m in enumerate(b.get("messages") or []):
        content = m.get("content")
        empty = (isinstance(content, str) and not content.strip()) or content == []
        if m.get("role") == "assistant" and empty and ("tool_calls" in m or "function_call" in m):
            m["content"] = None
            note(gw + "messagevalidators/normalizers.go:79-117, request_filters_messages.go:62",
                 f"messages[{i}].content empty -> null (assistant with tool calls)")
        elif m.get("role") == "tool" and (content is None or empty):
            m["content"] = "<empty tool result>"
            note(gw + "messagevalidators/normalizers.go:79-117, request_filters_config.go:105",
                 f"messages[{i}].content -> <empty tool result>")
    # gateway, pre-validation stage
    if MODEL == "MiniMaxAI/MiniMax-M2.7":
        for k, lines in (("enable_thinking", "468-474"), ("thinking", "478-485")):
            if k in b:
                b.pop(k)
                note(gw + f"request_filters_parameters.go:{lines}", f"strip top-level {k}")
    if "stop" in b:
        stops = [b["stop"]] if isinstance(b["stop"], str) else b["stop"]
        if len(stops) > 16 or any(not isinstance(s, str) or len(s.encode()) > 256 for s in stops):
            reject(gw + "request_filters_parameters.go:455-457", "stop: more than 16 entries or an entry over 256 bytes")
    if "chat_template_kwargs" in b:
        bad = sorted(set(b["chat_template_kwargs"]) & {
            "chat_template", "tokenize", "tools", "documents", "conversation", "continue_final_message",
            "padding", "truncation", "max_length", "return_tensors", "return_dict"})
        if bad:
            reject(gw + "paramvalidators/chat_template_kwargs.go:41-79", f"forbidden chat_template_kwargs key {bad[0]!r}")
    if b.get("tools"):
        if b.get("tool_choice") == "required":
            b["tool_choice"] = "auto"
            note(gw + "paramvalidators/tools.go:56-58", "tool_choice required -> auto")
        if "tool_choice" not in b:
            b["tool_choice"] = "auto"
            note(gw + "paramvalidators/tools.go:59-63, request_filters_parameters.go:527", "add tool_choice \"auto\"")
        for t in b["tools"]:
            if "strict" in (t.get("function") or {}):
                t["function"].pop("strict")
                note(gw + "paramvalidators/tools.go:94", "strip tools[].function.strict")
    else:
        for k in ("tools", "tool_choice", "parallel_tool_calls"):
            if k in b:
                b.pop(k)
                note(gw + "paramvalidators/tools.go:40-52,110-113", f"drop {k} (no tools)")
    if "stream_options" in b:
        if b.get("stream") is not True:
            b.pop("stream_options")
            note(gw + "paramvalidators/stream_options.go:42-44", "drop stream_options (stream not true)")
        else:
            for k in sorted(set(b["stream_options"]) - {"include_usage"}):
                b["stream_options"].pop(k)
                note(gw + "paramvalidators/stream_options.go:50-57", f"strip stream_options.{k}")
    for k in ("max_tokens", "max_completion_tokens"):
        if k in b and not (isinstance(b[k], (int, float)) and b[k] > 0):
            reject(gw + "request_filters_parameters.go:640-648", f"{k} must be positive")
    # gateway, output token limits: each present field capped; both present -> both set to the smaller
    present = [k for k in ("max_tokens", "max_completion_tokens") if k in b]
    limit = min(min(b[k], GATEWAY_MAX_TOKENS_CAP) for k in present) if present else GATEWAY_DEFAULT_MAX_TOKENS
    targets = ("max_tokens", "max_completion_tokens") if "max_completion_tokens" in b else ("max_tokens",)
    for k in targets:
        if b.get(k) != limit:
            note(gw + "request_filters.go:143-177,321-330 (cap: gateway.go:252)", f"{k} {b.get(k)!r} -> {limit}")
            b[k] = limit
    # gateway, post-limits stage
    if MODEL == "zai-org/GLM-5.3-Flash":
        kw = b.setdefault("chat_template_kwargs", {})
        if kw.get("enable_thinking") is not True:
            kw["enable_thinking"] = True
            note(gw + "request_filters_parameters.go:499-503, paramvalidators/always_on_thinking.go:6-12",
                 "chat_template_kwargs.enable_thinking = true")
    if "logit_bias" in b:
        kept = {k: v for k, v in b["logit_bias"].items() if -100 <= v <= 100}
        if kept != b["logit_bias"]:
            note(gw + "paramvalidators/handlers.go:106-136", "drop out-of-range logit_bias entries")
        b["logit_bias"] = kept
        if not kept:
            b.pop("logit_bias")
    if "n" in b and b["n"] != 1:
        b["n"] = 1
        note(gw + "request_filters_parameters.go:440-447", "n -> 1")
    if b.get("return_token_ids") is not True:
        b["return_token_ids"] = True
        note(gw + "request_filters_parameters.go:573-574", "return_token_ids = true")
    if b.get("top_logprobs", 0) > 5:
        b["top_logprobs"] = 5
        note(gw + "request_filters_parameters.go:578-580", "cap top_logprobs at 5")
    if b.get("stream") is not True or b.get("stream_options") != {"include_usage": True}:
        b["stream"], b["stream_options"] = True, {"include_usage": True}
        note(gw + "request_filters.go:133-141", "force stream with stream_options {include_usage: true}")
    # host executor: completionapi.ModifyRequestBodyWithLogprobsMode
    for k, v in (("logprobs", True), ("top_logprobs", 5), ("skip_special_tokens", False), ("return_token_ids", True)):
        if b.get(k) != v:
            b[k] = v
            note("common/completionapi/request.go:46-57", f"{k} = {json.dumps(v)}")
    budget = b["max_tokens"]
    for k in ("max_tokens", "max_completion_tokens"):
        if b.get(k) != budget:
            note("common/completionapi/request.go:164-178", f"{k} {b.get(k)!r} -> {budget}")
            b[k] = budget
    if "min_tokens" in b and not b["min_tokens"]:
        b.pop("min_tokens")
        note("common/completionapi/request.go:173-177", "drop zero min_tokens")
    if "seed" not in b:
        raise ValueError("cases must pin seed: the host default is the inference id (devshardd/inference/execute.go:37)")
    if b.get("logprobs_mode") != HOST_LOGPROBS_MODE:
        b["logprobs_mode"] = HOST_LOGPROBS_MODE
        note("common/completionapi/request.go:78-81, inference-chain/x/inference/types/params.go:90-92",
             f"logprobs_mode = {HOST_LOGPROBS_MODE} (chain default)")
    out = go_json(b)
    if json.dumps(out) != json.dumps(b):
        note(gw + "request_filters_parameters.go:220-228, common/completionapi/request.go:83",
             "re-encoded from map[string]any: object keys sorted at every level")
    return out, applied


def gateway_body(case, defaults, toolsets, max_tokens):
    # The body a gateway client sends for this case; forward_request gives what the ML node receives.
    body = {"model": MODEL, "messages": case["messages"], "stream": True,
            "stream_options": {"include_usage": True}, "logprobs": True,
            "top_logprobs": defaults["top_logprobs"], "skip_special_tokens": False, "return_token_ids": True,
            "seed": case.get("seed", defaults["seed"]), "max_tokens": max_tokens, "max_completion_tokens": max_tokens}
    if case.get("tools"):
        body["tools"] = toolsets[case["tools"]]
    if case.get("stop"):
        body["stop"] = case["stop"]
    if case.get("chat_template_kwargs"):
        body["chat_template_kwargs"] = case["chat_template_kwargs"]
    if case.get("forced"):
        body["logit_bias"] = {str(case["forced"]["id_by_model"][MODEL]): case["forced"].get("bias", 100)}
    body.update(case.get("extra_body") or {})
    return body


def chat_request(body, stream):
    # A deep copy: vLLM edits message dicts in place (reasoning_content becomes reasoning), and the
    # fixture must keep the body as the ML node received it.
    b = copy.deepcopy(dict(body, stream=stream))
    if not stream:
        b.pop("stream_options", None)
    req = ChatCompletionRequest.model_validate(b)
    tool_choice = getattr(req, "tool_choice", "none")
    if PARSER_CLS is not None and (PARSER_CLS.reasoning_parser_cls is not None or tool_choice != "none"):
        adjusted = PARSER_CLS(tok, req.tools, model_config=model_config,
                              chat_template_kwargs=chat_template_kwargs(req)).adjust_request(request=req)
        req = adjusted if adjusted is not None else req
    return req


def sampling_params(req, prompt_len):
    max_tokens = get_max_tokens(model_config.max_model_len,
                                req.max_completion_tokens if req.max_completion_tokens is not None else req.max_tokens,
                                prompt_len, DEFAULT_SAMPLING, OVERRIDE_MAX_TOKENS,
                                truncate_prompt_tokens=req.truncate_prompt_tokens)
    sp = req.to_sampling_params(max_tokens, DEFAULT_SAMPLING)
    sp.update_from_generation_config(GEN_CONFIG, tok.eos_token_id)
    return sp


def stop_set(sp):
    s = set(sp.stop_token_ids or [])
    if sp.eos_token_id is not None:
        s.add(sp.eos_token_id)
    return s


# ---------------------------------------------------------------- engine replay

def step_logprobs(tid, k):
    alts = [tid] + [(tid + d) % VOCAB for d in range(1, k)]
    ids = [tid] + alts
    lps = [-0.1] + [-0.1 - r for r in range(k)]
    return LogprobsLists(np.array([ids], dtype=np.int64), np.array([lps], dtype=np.float32), np.array([1], dtype=np.int64))


def run_engine(rid, prompt_ids, sp, gen_ids):
    """Feed gen_ids one token per step; the scheduler-side stop check mirrors vllm.v1.core.sched.utils.check_stop."""
    ecr = EngineCoreRequest(request_id=rid, prompt_token_ids=prompt_ids, mm_features=None, sampling_params=sp,
                            pooling_params=None, arrival_time=time.time(), lora_request=None, cache_salt=None,
                            data_parallel_rank=None, external_req_id=rid)
    op = OutputProcessor(tok, log_stats=False, stream_interval=1)
    op.add_request(ecr, prompt=None)
    outs, used = [], 0
    for i, tid in enumerate(gen_ids):
        n = i + 1
        fr = sr = None
        if n >= sp.min_tokens:
            if tid == sp.eos_token_id:
                fr = FinishReason.STOP
            elif tid in (sp.stop_token_ids or ()):
                fr, sr = FinishReason.STOP, tid
            elif len(prompt_ids) + n >= model_config.max_model_len or n >= sp.max_tokens:
                fr = FinishReason.LENGTH
        eco = EngineCoreOutput(request_id=rid, new_token_ids=[tid],
                               new_logprobs=step_logprobs(tid, sp.logprobs) if sp.logprobs else None,
                               finish_reason=fr, stop_reason=sr)
        outs.extend(op.process_outputs([eco]).request_outputs)
        used = n
        if rid not in op.request_states:
            break
    if rid in op.request_states:
        raise RuntimeError("token list exhausted before the engine finished the request")
    return outs, gen_ids[:used]


def serving_stub():
    srv = object.__new__(OpenAIServingChat)
    srv.__dict__.update(
        parser_cls=PARSER_CLS, response_role="assistant", enable_force_include_usage=False,
        enable_log_outputs=False, enable_log_deltas=True, request_logger=None, system_fingerprint=None,
        _include_reasoning_tokens_details=bool(REASONING_PARSER), enable_prompt_tokens_details=False,
        enable_per_request_metrics=False, return_tokens_as_token_ids=False, model_config=model_config,
        enable_auto_tools="--enable-auto-tool-choice" in ARGS, chat_template=None,
        chat_template_content_format="auto", default_chat_template_kwargs={},
        default_sampling_params=DEFAULT_SAMPLING, override_max_tokens=OVERRIDE_MAX_TOKENS,
        exclude_tools_when_tool_choice_none=False, supports_browsing=False, browser_tool=None,
        supports_code_interpreter=False, python_tool=None, renderer=None)
    return srv


SRV = serving_stub()


def chat_template_kwargs(req):
    return SRV._effective_chat_template_kwargs(req)


def call(fn, **kw):
    params = inspect.signature(fn).parameters
    return fn(**{k: v for k, v in kw.items() if k in params})


async def _aiter(items):
    for x in items:
        yield x


def stream_chunks(req, outs, rid):
    md = serving_mod.RequestResponseMetadata(request_id=rid)

    async def go():
        lines = []
        async for s in call(SRV.chat_completion_stream_generator, request=req, result_generator=_aiter(outs),
                            request_id=rid, model_name=MODEL, conversation=[], tokenizer=tok, request_metadata=md,
                            chat_template_kwargs=chat_template_kwargs(req), mm_token_counts=None):
            lines.append(s)
        return lines

    lines = asyncio.run(go())
    chunks, done = [], False
    for line in lines:
        assert line.startswith("data: ") and line.endswith("\n\n"), line[:200]
        data = line[6:-2]
        if data == "[DONE]":
            done = True
            continue
        c = json.loads(data)
        if "error" in c:
            raise RuntimeError(f"stream generator emitted an error chunk: {data[:600]}")
        c["created"] = 0
        chunks.append(c)
    if not done:
        raise RuntimeError("stream did not end with [DONE]")
    return chunks


def full_response(req, outs, rid):
    md = serving_mod.RequestResponseMetadata(request_id=rid)
    parser = PARSER_CLS(tok, req.tools, chat_template_kwargs=chat_template_kwargs(req), model_config=model_config) \
        if PARSER_CLS is not None else None
    res = asyncio.run(call(SRV.chat_completion_full_generator, request=req, result_generator=_aiter(outs),
                           request_id=rid, model_name=MODEL, conversation=[], tokenizer=tok, request_metadata=md,
                           parser=parser, mm_token_counts=None))
    d = json.loads(res.model_dump_json())
    if "error" in d and "choices" not in d:
        raise RuntimeError(f"full generator returned an error: {json.dumps(d)[:600]}")
    d["created"] = 0
    return d


# ---------------------------------------------------------------- analysis of the stream

def served_from_stream(chunks):
    reasoning, content, calls = [], [], {}
    tokens, finish, stop_reason, usage, by = [], None, None, None, {"content": 0, "reasoning": 0, "tool_name": 0,
                                                                  "tool_arguments": 0}
    for c in chunks:
        if c.get("usage") and not c.get("choices"):
            usage = c["usage"]
        for ch in c.get("choices") or []:
            d = ch.get("delta") or {}
            if d.get("reasoning"):
                reasoning.append(d["reasoning"])
                by["reasoning"] += len(d["reasoning"].encode())
            if d.get("content"):
                content.append(d["content"])
                by["content"] += len(d["content"].encode())
            for tc in d.get("tool_calls") or []:
                slot = calls.setdefault(tc["index"], {"index": tc["index"], "id": None, "name": "", "arguments": ""})
                if tc.get("id"):
                    slot["id"] = tc["id"]
                f = tc.get("function") or {}
                if f.get("name"):
                    slot["name"] += f["name"]
                    by["tool_name"] += len(f["name"].encode())
                if f.get("arguments"):
                    slot["arguments"] += f["arguments"]
                    by["tool_arguments"] += len(f["arguments"].encode())
            lp = (ch.get("logprobs") or {}).get("content") or []
            ids = ch.get("token_ids") or []
            if len(lp) != len(ids):
                raise RuntimeError(f"chunk carries {len(ids)} token_ids but {len(lp)} logprobs entries")
            for tid, e in zip(ids, lp):
                tokens.append({"id": tid, "token": e["token"], "bytes": e.get("bytes"),
                               "text": bytes(e["bytes"]).decode("utf-8", errors="replace") if e.get("bytes") is not None else None})
            if ch.get("finish_reason"):
                finish, stop_reason = ch["finish_reason"], ch.get("stop_reason")
    by["total"] = sum(by.values())
    return {"reasoning": "".join(reasoning), "content": "".join(content),
            "tool_calls": [calls[k] for k in sorted(calls)]}, tokens, finish, stop_reason, usage, by


def _consistent(part, full):
    if isinstance(part, dict):
        return isinstance(full, dict) and all(k in full and _consistent(v, full[k]) for k, v in part.items())
    if isinstance(part, list):
        return isinstance(full, list) and len(part) <= len(full) and all(_consistent(a, b) for a, b in zip(part, full))
    if isinstance(part, str):
        return isinstance(full, str) and full.startswith(part)
    return part == full


def args_consistent(partial, full):
    """Streamed arguments of a cut call: a text prefix of the full arguments, or JSON that the parser
    closed early whose values are prefixes of the full ones."""
    if norm(full).startswith(norm(partial)):
        return True
    try:
        return _consistent(json.loads(partial), json.loads(full))
    except ValueError:
        return False


def repaired_arguments(a):
    """Host-side repair of streamed tool-call arguments before rendering."""
    from partial_json_parser import Allow, loads
    try:
        obj = loads(a, Allow.ALL) if a.strip() else {}
    except Exception:
        obj = {}
    if not isinstance(obj, dict):
        obj = {}
    return json.dumps(obj, ensure_ascii=False)


def _value_spellings(v):
    if isinstance(v, str):
        return [v, json.dumps(v)[1:-1], json.dumps(v, ensure_ascii=False)[1:-1]]
    return [json.dumps(v, ensure_ascii=False), json.dumps(v), json.dumps(v, ensure_ascii=False, separators=(",", ":")),
            str(v)]


def cut_location(gen_text, cut_at, calls, tools):
    """Where in the generated text the cut fell, relative to the intended tool calls.

    gen_text is the whole uncut generation, cut_at the length of the served prefix. Calls are located by
    function name, arguments by key and value spelling, in order; the schema type comes from the request tools.
    """
    schema = {t["function"]["name"]: (t["function"].get("parameters") or {}).get("properties") or {} for t in tools or []}
    cursor, located = 0, []
    for i, c in enumerate(calls):
        name = c["function"]["name"]
        npos = gen_text.find(name, cursor)
        if npos < 0:
            return {"where": "unknown", "detail": f"function name {name!r} not found in the generation"}
        cursor = npos + len(name)
        args = []
        for key, val in json.loads(c["function"]["arguments"]).items():
            kpos = gen_text.find(key, cursor)
            if kpos < 0:
                return {"where": "unknown", "detail": f"argument key {key!r} not found"}
            found = [(gen_text.find(sp, kpos + len(key)), sp) for sp in _value_spellings(val)]
            found = [(p, sp) for p, sp in found if p >= 0]
            if not found:
                return {"where": "unknown", "detail": f"value of {key!r} not found"}
            vstart, sp = min(found)
            vend = vstart + len(sp)
            args.append((key, kpos, vend))
            cursor = vend
        located.append((i, name, npos, args))
    if not located or cut_at <= located[0][2]:
        return {"where": "before_function_name", "call_index": 0}
    i, name, npos, args = [x for x in located if x[2] < cut_at][-1]
    if cut_at <= npos + len(name):
        return {"where": "function_name", "call_index": i}
    for key, kpos, vend in args:
        if kpos < cut_at <= vend:
            typ = schema.get(name, {}).get(key, {}).get("type")
            return {"where": "argument", "call_index": i, "argument": key, "schema_type": typ}
        if cut_at <= kpos:
            return {"where": "between_arguments", "call_index": i}
    return {"where": "after_arguments", "call_index": i}


def norm(s):
    return "".join(s.replace("<think>", "").replace("</think>", "").split())


def flat(reasoning, content, calls):
    return norm(reasoning + content + "".join(c["name"] + c["arguments"] for c in calls))


def served_turn(served):
    turn = {"role": "assistant", "content": served["content"]}
    if served["reasoning"]:
        turn["reasoning"] = served["reasoning"]
    if served["tool_calls"]:
        turn["tool_calls"] = [{"id": c["id"] or f"call_{i}", "type": "function",
                               "function": {"name": c["name"], "arguments": c["arguments"]}}
                              for i, c in enumerate(served["tool_calls"])]
    return turn


# ---------------------------------------------------------------- one case

def source_line(module, qualname, needle):
    """file:line of `needle` inside a function of this build, so an expectation cites the code it rests on."""
    import importlib
    obj = importlib.import_module(module)
    for part in qualname.split("."):
        obj = getattr(obj, part)
    lines, first = inspect.getsourcelines(obj)
    for i, line in enumerate(lines):
        if needle in line:
            return f"{inspect.getsourcefile(obj)}:{first + i}: {line.strip()}"
    raise ValueError(f"{needle!r} not found in {module}.{qualname}; the expectation does not hold for this build")


def pick_utf8_tail(messages, tools, answer, spec, stops):
    """Append the first candidate character that the tokenizer splits into byte tokens at the end of the
    answer, and cut max_tokens after the first 1..max_partial of those tokens (the character stays unfinished)."""
    rejected = []
    for ch in spec["candidates"]:
        turn = intended_turn(dict(answer, content=text_of(answer.get("content")) + ch))
        g, term = raw_generation(messages, tools, turn, stops)
        texts = [tok.decode(g[:k], skip_special_tokens=False) for k in range(len(g) + 1)]
        done = next((k for k in range(len(texts)) if ch in texts[k]), None)
        if done is None:
            rejected.append({"char": ch, "why": "character not in the generation"})
            continue
        partial = []
        k = done - 1
        while k > 0 and texts[k].endswith("\ufffd") and ch not in texts[k]:
            partial.insert(0, k)
            k -= 1
        if not partial:
            rejected.append({"char": ch, "why": f"one token {g[done - 1]}"})
            continue
        start = partial[0] - 1
        j = min(len(partial), spec.get("max_partial", 3))
        return g, term, turn, {
            "char": ch, "codepoint": f"U+{ord(ch):04X}", "utf8_bytes": len(ch.encode()),
            "char_ids": g[start:done], "char_pieces": tok.convert_ids_to_tokens(g[start:done]),
            "cut_after_tokens": j, "generated_char_ids": g[start:start + j], "max_tokens": start + j,
            "rejected_candidates": rejected}
    raise ValueError(f"no candidate is split into byte tokens: {rejected}")


def expand_cases(spec):
    """One concrete case per (case, request mode). Models with modes in cases.json get the default request
    (no chat_template_kwargs, effective mode DEFAULT_MODE) plus one request per named mode; the expected answer
    is the case answer with the effective mode's overrides applied (null removes a part)."""
    modes = (spec.get("modes") or {}).get(MODEL)
    out = []
    for case in spec["cases"]:
        answer = dict(case["answer"], **(case.get("answer_by_model") or {}).get(MODEL, {}))
        if not modes:
            out.append(dict(case, answer=answer))
            continue
        if DEFAULT_MODE not in modes:
            raise RuntimeError(f"model has request modes {sorted(modes)} but DEFAULT_MODE={DEFAULT_MODE!r}")
        for name, kwargs, effective in [("default", {}, DEFAULT_MODE)] + \
                [(n, m["chat_template_kwargs"], n) for n, m in modes.items()]:
            a = dict(answer)
            for k, v in (modes[effective].get("answer") or {}).items():
                if v is None:
                    a.pop(k, None)
                else:
                    a[k] = v
            out.append(dict(case, name=case["name"] if name == "default" else f"{case['name']}@{name}",
                            base_case=case["name"], request_mode=name, effective_mode=effective,
                            chat_template_kwargs=kwargs, answer=a))
    return out


def build_case(case, defaults, toolsets, identity):
    global CUR_KWARGS
    # natural (uncut) request first: it fixes the stop set and the raw generation
    client_body = gateway_body(case, defaults, toolsets, case.get("max_tokens", defaults["max_tokens"]))
    body, applied = forward_request(client_body)
    natural_max = body["max_tokens"]
    # template, render server and host counts all see the forwarded request
    CUR_KWARGS = body.get("chat_template_kwargs") or {}
    tools = body.get("tools")
    # vLLM hands the template its own model_dump of the request tools (field order of its pydantic models)
    tpl_tools = [t.model_dump() for t in ChatCompletionRequest.model_validate(body).tools] if tools else None
    messages = body["messages"]
    # a model whose template forces part of the turn (e.g. reasoning) gets its own answer variant
    answer = case["answer"]
    turn = intended_turn(answer)
    checks = []

    def check(name, ok, detail="", kind="check"):
        # kind "finding": a property of vLLM itself worth reporting, not a fixture defect
        checks.append({"name": name, "ok": bool(ok), "detail": detail, "kind": kind})

    prompt_res, err = render({k: v for k, v in body.items() if k not in ("stream", "stream_options")})
    if prompt_res is None:
        raise RuntimeError(f"render of the request failed: {err}")
    prompt_ids = prompt_res["token_ids"]
    req = chat_request(body, True)
    sp = sampling_params(req, len(prompt_ids))
    stops = stop_set(sp)
    cut = case.get("cut")
    utf8 = (cut or {}).get("utf8_tail")
    case_info = {}
    if case.get("forced"):
        # the client forces one token with logit_bias; the honest generation is that token, max_tokens times
        fid = case["forced"]["id_by_model"][MODEL]
        piece, dec = tok.convert_ids_to_tokens([fid])[0], tok.decode([fid], skip_special_tokens=False)
        if dec != "\ufffd" or len(piece) != 1:
            raise ValueError(f"forced id {fid} is not a single-byte fragment here: piece {piece!r}, decodes to {dec!r}")
        gen_ids, terminator = [fid] * natural_max, None
        case_info["forced"] = {"id": fid, "piece": piece, "decoded_alone": dec, "count": natural_max}
        exp = (case["forced"].get("expect_by_model") or {}).get(MODEL)
        if exp:
            case_info["forced"]["leading_content"] = exp["leading_content"]
            case_info["forced"]["leading_content_reason"] = dict(exp["reason"], source=source_line(**exp["source"]))
    elif utf8:
        gen_ids, terminator, turn, case_info["utf8_tail"] = pick_utf8_tail(messages, tpl_tools, answer, utf8, stops)
    else:
        gen_ids, terminator = raw_generation(messages, tpl_tools, turn, stops)
        if case.get("split"):
            gen_ids, case_info["split"] = split_tokens(gen_ids, case["split"])

    local_prompt_ids = enc(local_render(messages, tpl_tools, True))
    check("local_template_matches_render_server", local_prompt_ids == prompt_ids,
          f"local {len(local_prompt_ids)} vs server {len(prompt_ids)} prompt tokens")

    if cut:
        if utf8:
            k = case_info["utf8_tail"]["max_tokens"]
        else:
            sub = cut["after_text"]
            k = next((k for k in range(1, len(gen_ids) + 1)
                      if sub in tok.decode(gen_ids[:k], skip_special_tokens=False)), None)
            if k is None:
                raise ValueError(f"cut text {sub!r} not found in the generation")
        client_body = gateway_body(case, defaults, toolsets, k)
        body, applied = forward_request(client_body)
        req = chat_request(body, True)
        sp = sampling_params(req, len(prompt_ids))

    rid = f"chatcmpl-fixture-{case['name'].replace('@', '-')}"
    outs, used_ids = run_engine(rid, prompt_ids, sp, gen_ids)
    chunks = stream_chunks(req, outs, rid)
    req_full = chat_request(body, False)
    sp_full = sampling_params(req_full, len(prompt_ids))
    outs_full, used_full = run_engine(rid, prompt_ids, sp_full, gen_ids)
    nonstream = full_response(req_full, outs_full, rid)

    served, tokens, finish, stop_reason, usage, gw_bytes = served_from_stream(chunks)
    # positions per chunk and whether the chunk carried any delta text
    pos_text, pos_delta = [], []
    for c in chunks:
        for ch in c.get("choices") or []:
            d = ch.get("delta") or {}
            txt = (d.get("content") or "") + (d.get("reasoning") or "") + "".join(
                (t.get("function") or {}).get("name", "") + (t.get("function") or {}).get("arguments", "")
                for t in d.get("tool_calls") or [])
            n_pos = len((ch.get("logprobs") or {}).get("content") or [])
            pos_delta += [txt] + [""] * (n_pos - 1) if n_pos else []
            pos_text += [bool(txt)] * n_pos
    trailing_empty = next((i for i, has in enumerate(reversed(pos_text)) if has), len(pos_text))
    last = outs[-1].outputs[0]
    engine_finish, engine_stop_reason = last.finish_reason, last.stop_reason
    is_cut = bool(cut) or engine_finish == "length"

    # compare with the authoritative render-server sampling params
    final_render, err = render({k: v for k, v in body.items() if k not in ("stream", "stream_options")})
    rsp = (final_render or {}).get("sampling_params") or {}
    # the render server omits fields left at their defaults
    for f in ("max_tokens", "stop", "skip_special_tokens", "logprobs", "min_tokens", "include_stop_str_in_output", "seed"):
        mine = getattr(sp, f)
        if f in rsp or f == "max_tokens":
            check(f"sampling_param_{f}_matches_render_server", rsp.get(f) == mine, f"render {rsp.get(f)!r} vs local {mine!r}")

    # host-side counts
    rendered_input, err_in = render_len(messages, tools, True)
    n_empty, err_empty = render_len(messages + [{"role": "assistant", "content": ""}], tools, False)
    if n_empty is None:
        raise RuntimeError(f"render of the empty assistant turn failed: {err_empty}")
    n_unrep, err_unrep = render_len(messages + [served_turn(served)], tools, False)
    repaired = copy.deepcopy(served)
    for c in repaired["tool_calls"]:
        c["arguments"] = repaired_arguments(c["arguments"])
    n_full, err_out = render_len(messages + [served_turn(repaired)], tools, False)
    location = None
    if engine_finish == "length" and turn.get("tool_calls"):
        location = cut_location(tok.decode(gen_ids, skip_special_tokens=False),
                                len(tok.decode(used_ids, skip_special_tokens=False)), turn["tool_calls"], tools)
    abstain_reason = None
    if location and location["where"] == "argument" and location.get("schema_type") != "string":
        abstain_reason = "non_string_arg_cut"
    elif n_full is None:
        # a fault rule on the voters' side, not a count
        abstain_reason = "render_error_after_repair"
    abstain = abstain_reason is not None
    rendered_output = None if n_full is None else n_full - n_empty
    claim = (usage or {}).get("completion_tokens")
    stop_tokens = max((len(enc(s)) for s in (case.get("stop") or [])), default=0)
    tool_case = bool(answer.get("tool_calls"))
    tolerance = BASE_TOLERANCE + stop_tokens + (TOOL_HEADER if tool_case else 0)
    gap = None if rendered_output is None or claim is None else claim - rendered_output

    natural_end = engine_finish == "stop" and not isinstance(engine_stop_reason, str)
    # self-checks
    check("stream_has_usage_chunk", usage is not None)
    check("positions_equal_completion_tokens", len(tokens) == claim, f"positions {len(tokens)} vs usage {claim}")
    check("positions_equal_engine_token_ids", [t["id"] for t in tokens] == used_ids,
          f"{len(tokens)} stream positions vs {len(used_ids)} generated ids")
    check("prompt_tokens_equal_rendered_input", (usage or {}).get("prompt_tokens") == rendered_input == len(prompt_ids),
          f"usage {(usage or {}).get('prompt_tokens')} render {rendered_input} engine {len(prompt_ids)}")
    if natural_end and terminator["id"] == used_ids[-1]:
        check("eos_counted_not_detokenized", tokens[-1]["id"] == terminator["id"]
              and not (served["content"] + served["reasoning"]).endswith(terminator["text"]),
              f"last position {tokens[-1]['id']} ({tokens[-1]['token']!r})")
    msg = (nonstream.get("choices") or [{}])[0].get("message") or {}
    ns_calls = [{"name": c["function"]["name"], "arguments": c["function"]["arguments"]} for c in msg.get("tool_calls") or []]
    check("stream_equals_nonstream_text", flat(served["reasoning"], served["content"], served["tool_calls"])
          == flat(msg.get("reasoning") or "", msg.get("content") or "", ns_calls),
          json.dumps({"nonstream": {"reasoning": msg.get("reasoning"), "content": msg.get("content"), "tool_calls": ns_calls}},
                     ensure_ascii=False)[:600], kind="finding" if is_cut else "check")
    check("stream_equals_nonstream_usage", (nonstream.get("usage") or {}).get("completion_tokens") == claim,
          f"nonstream {(nonstream.get('usage') or {}).get('completion_tokens')} vs stream {claim}")
    intended_calls = [{"name": c["function"]["name"], "arguments": c["function"]["arguments"]} for c in turn.get("tool_calls") or []]
    content_intended = turn["content"]
    if case.get("stop"):
        cuts = [content_intended.find(s) for s in case["stop"] if s in content_intended]
        content_intended = content_intended[:min(cuts)] if cuts else content_intended
    want = flat(turn.get("reasoning", ""), content_intended, intended_calls)
    got = flat(served["reasoning"], served["content"], served["tool_calls"])
    if is_cut:
        text_want = norm(turn.get("reasoning", "") + content_intended)
        text_got = norm(served["reasoning"] + served["content"])
        ok = text_want.startswith(text_got) and len(served["tool_calls"]) <= len(intended_calls)
        ok = ok and all(c["name"] == w["name"] and args_consistent(c["arguments"], w["arguments"])
                        for c, w in zip(served["tool_calls"], intended_calls))
        check("served_is_prefix_of_answer", ok, f"served {json.dumps(served, ensure_ascii=False)[:400]}")
    else:
        ok = got == want
        if ok and served["tool_calls"]:
            ok = [(c["name"], json.loads(c["arguments"])) for c in served["tool_calls"]] == \
                 [(c["name"], json.loads(c["arguments"])) for c in intended_calls]
        check("served_equals_answer", ok, f"served {got[:200]!r} want {want[:200]!r}")
    if location and location["where"] == "unknown":
        check("cut_location_determined", False, location["detail"])
    if not abstain:
        within = gap is not None and abs(gap) <= tolerance
        detail = f"claim {claim} rendered {rendered_output} gap {gap} tolerance {tolerance}"
        if case.get("tolerance_finding"):
            check(case["tolerance_finding"], within, detail, kind="finding")
        else:
            check("rendered_output_within_tolerance", within, detail)
    if case.get("forced"):
        n = case_info["forced"]["count"]
        check("forced_request_has_return_token_ids", body.get("return_token_ids") is True)
        lead = case_info["forced"].get("leading_content")
        if lead is None:
            check("forced_positions_in_empty_deltas", len(pos_text) == n and not any(pos_text),
                  f"{len(pos_text)} positions, {sum(pos_text)} with delta text")
        else:
            # the model's reasoning parser adds text to the first delta; every other position stays empty
            check("forced_positions_in_empty_deltas", len(pos_text) == n and pos_delta[:1] == [lead]
                  and not any(pos_text[1:]),
                  f"{len(pos_text)} positions, first delta {pos_delta[:1]!r}, {sum(pos_text[1:])} later with text")
        check("forced_finish_length", finish == "length" and engine_finish == "length" and claim == n,
              f"finish {finish}/{engine_finish} completion_tokens {claim}")
    if utf8:
        info = case_info["utf8_tail"]
        text = served["content"] + served["reasoning"]
        check("utf8_tail_dropped", trailing_empty == info["cut_after_tokens"] and info["char"] not in text
              and "\ufffd" not in text,
              f"trailing empty positions {trailing_empty}, char tokens generated {info['cut_after_tokens']}")

    return {
        "fixture_format": FIXTURE_FORMAT,
        "case": case["name"],
        "request_mode": case.get("request_mode"),
        "effective_mode": case.get("effective_mode"),
        "description": case.get("description", ""),
        "build": identity,
        "notes": {
            "logprob_values": "synthetic: sampled token rank 1 logprob -0.1; alternative r has logprob -0.1-r and "
                              "placeholder id (sampled+r) mod vocab. Token strings and bytes are produced by vLLM itself.",
            "chunking": "one engine step per token, stream consumer keeps up (no merged deltas)",
            "created": "chunk 'created' normalised to 0; system_fingerprint not set",
            "request": {
                "form": "as the ML node receives it: gateway normalisation, then the host executor rewrite",
                "gateway_source": GATEWAY_SRC,
                "gateway_branch": GATEWAY_BRANCH,
                "assumed_settings": {"default_request_max_tokens": GATEWAY_DEFAULT_MAX_TOKENS,
                                     "request_max_tokens_cap": GATEWAY_MAX_TOKENS_CAP,
                                     "force_upstream_streaming": True, "logprobs_mode": HOST_LOGPROBS_MODE},
                "client_request": client_body,
                "applied": applied,
            },
        },
        "request": body,
        "cut": cut,
        "case_info": case_info,
        "tolerance_finding": case.get("tolerance_finding"),
        "answer": turn,
        "engine": {
            "prompt_token_count": len(prompt_ids),
            "max_tokens": sp.max_tokens,
            "min_tokens": sp.min_tokens,
            "stop": sp.stop,
            "eos_token_id": sp.eos_token_id,
            "stop_token_ids": sorted(sp.stop_token_ids or []),
            "skip_special_tokens": sp.skip_special_tokens,
            "terminator": terminator,
            "generated_before_cut": len(gen_ids),
            "render_server_sampling_params": rsp,
        },
        "token_ids": used_ids,
        "tokens": tokens,
        "finish_reason": finish,
        "stop_reason": stop_reason,
        "engine_finish_reason": engine_finish,
        "engine_stop_reason": engine_stop_reason,
        "usage": usage,
        "stream": chunks,
        "response_nonstream": nonstream,
        "served": served,
        "host": {
            "positions": len(tokens),
            "rendered_input": rendered_input,
            "rendered_input_error": err_in,
            "rendered_output": rendered_output,
            "rendered_output_error": err_out,
            "rendered_output_unrepaired": None if n_unrep is None else n_unrep - n_empty,
            "rendered_output_unrepaired_error": err_unrep,
            "repaired_tool_arguments": [c["arguments"] for c in repaired["tool_calls"]],
            "cut_location": location,
            "renderer_abstains": abstain,
            "abstain_reason": abstain_reason,
            "claim": claim,
            "gap": gap,
            "tolerance": tolerance,
            "trailing_empty_positions": trailing_empty,
        },
        "gateway_bytes": gw_bytes,
        "checks": checks,
    }


def main():
    cases_path, out_dir = sys.argv[1], sys.argv[2]
    only = set(sys.argv[3:])
    spec = json.load(open(cases_path))
    os.makedirs(out_dir, exist_ok=True)
    identity = build_identity()
    print(f"model={MODEL} vllm={vllm.__version__} parser={identity['parser_class']} "
          f"detokenizer={identity['detokenizer_class']} tokenizer_sha256={identity['tokenizer_sha256'][:16]}", flush=True)
    summary = []
    for case in expand_cases(spec):
        if only and case["name"] not in only and case.get("base_case") not in only:
            continue
        path = os.path.join(out_dir, f"{case['name']}.json")
        try:
            fx = build_case(case, spec["defaults"], spec["toolsets"], identity)
            with open(path, "w") as f:
                json.dump(fx, f, ensure_ascii=False, indent=1)
            if os.path.exists(path[:-5] + ".error.json"):
                os.remove(path[:-5] + ".error.json")
            failed = [c for c in fx["checks"] if not c["ok"]]
            h = fx["host"]
            row = {"case": case["name"], "ok": True, "finish": fx["finish_reason"], "claim": h["claim"],
                   "positions": h["positions"], "rendered_output": h["rendered_output"], "gap": h["gap"],
                   "tolerance": h["tolerance"], "abstain": h["abstain_reason"], "bytes": fx["gateway_bytes"]["total"],
                   "failed_checks": [c["name"] + ": " + c["detail"] for c in failed if c["kind"] == "check"],
                   "findings": [c["name"] + ": " + c["detail"] for c in failed if c["kind"] == "finding"]}
        except GatewayRejected as e:
            rej = {"case": case["name"], "model": MODEL, "rejected_by": e.rule, "detail": e.detail,
                   "gateway_source": GATEWAY_SRC, "gateway_branch": GATEWAY_BRANCH}
            with open(path[:-5] + ".rejected.json", "w") as f:
                json.dump(rej, f, ensure_ascii=False, indent=1)
            for stale in (path, path[:-5] + ".error.json"):
                if os.path.exists(stale):
                    os.remove(stale)
            row = {"case": case["name"], "ok": True, "rejected": f"{e.rule}: {e.detail}"}
        except Exception as e:
            err = {"case": case["name"], "model": MODEL, "error": f"{type(e).__name__}: {e}",
                   "traceback": traceback.format_exc()}
            with open(path[:-5] + ".error.json", "w") as f:
                json.dump(err, f, ensure_ascii=False, indent=1)
            if os.path.exists(path):
                os.remove(path)
            row = {"case": case["name"], "ok": False, "error": err["error"]}
        summary.append(row)
        print(json.dumps(row, ensure_ascii=False)[:1500], flush=True)
    # a partial run (selected cases) updates its rows and keeps the others
    spath = os.path.join(out_dir, "summary.json")
    if only and os.path.exists(spath):
        done = {r["case"] for r in summary}
        summary = [r for r in json.load(open(spath)).get("cases", []) if r["case"] not in done] + summary
    with open(spath, "w") as f:
        json.dump({"build": identity, "cases": summary}, f, ensure_ascii=False, indent=1)


if __name__ == "__main__":
    main()
