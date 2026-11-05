#!/usr/bin/env python3
"""Re-verify generated fixtures from their JSON alone (no vLLM needed) and print a summary table.

Usage: check.py [fixtures_dir]   (default: fixtures next to this script)
Exit code 1 if any fixture fails a check or any case failed to generate.
"""
import glob
import json
import os
import sys


def norm(s):
    return "".join(s.replace("<think>", "").replace("</think>", "").split())


def verify(fx):
    problems = []
    reasoning, content, calls, positions, ids, by = "", "", {}, 0, [], 0
    finish_chunks = 0
    for c in fx["stream"]:
        for ch in c.get("choices") or []:
            d = ch.get("delta") or {}
            reasoning += d.get("reasoning") or ""
            content += d.get("content") or ""
            by += len((d.get("reasoning") or "").encode()) + len((d.get("content") or "").encode())
            for tc in d.get("tool_calls") or []:
                slot = calls.setdefault(tc["index"], ["", ""])
                f = tc.get("function") or {}
                slot[0] += f.get("name") or ""
                slot[1] += f.get("arguments") or ""
                by += len((f.get("name") or "").encode()) + len((f.get("arguments") or "").encode())
            lp = (ch.get("logprobs") or {}).get("content") or []
            positions += len(lp)
            ids += ch.get("token_ids") or []
            finish_chunks += ch.get("finish_reason") is not None
    served = fx["served"]
    if (reasoning, content) != (served["reasoning"], served["content"]) or \
            [calls[k] for k in sorted(calls)] != [[t["name"], t["arguments"]] for t in served["tool_calls"]]:
        problems.append("stream deltas do not reproduce the served turn")
    msg = fx["response_nonstream"]["choices"][0]["message"]
    ns = norm((msg.get("reasoning") or "") + (msg.get("content") or "") +
              "".join(t["function"]["name"] + t["function"]["arguments"] for t in msg.get("tool_calls") or []))
    st = norm(reasoning + content + "".join(calls[k][0] + calls[k][1] for k in sorted(calls)))
    if ns != st:
        # on a max_tokens cut the non-streaming parser repairs or drops partial tool arguments
        (findings_pre := []).append("stream text differs from the non-streamed response")
    else:
        findings_pre = []
    usage = fx["usage"]
    if not (positions == len(fx["token_ids"]) == usage["completion_tokens"] == len(fx["tokens"])):
        problems.append(f"positions {positions}, token_ids {len(fx['token_ids'])}, usage {usage['completion_tokens']}")
    if ids != fx["token_ids"]:
        problems.append("chunk token_ids do not concatenate to token_ids")
    if finish_chunks != 1:
        problems.append(f"{finish_chunks} chunks carry a finish_reason")
    eng = fx["engine"]
    if fx["engine_finish_reason"] == "length":
        if len(fx["token_ids"]) != eng["max_tokens"]:
            problems.append(f"length finish after {len(fx['token_ids'])} tokens, max_tokens {eng['max_tokens']}")
    elif isinstance(fx["engine_stop_reason"], str):
        pass  # stop string: text cut by the detokenizer, all generated tokens stay in usage
    elif fx["token_ids"][-1] != eng["terminator"]["id"]:
        problems.append("natural finish does not end with the stop token")
    if usage["prompt_tokens"] != fx["host"]["rendered_input"]:
        problems.append(f"prompt_tokens {usage['prompt_tokens']} vs rendered input {fx['host']['rendered_input']}")
    if by != fx["gateway_bytes"]["total"]:
        problems.append(f"gateway bytes {by} vs recorded {fx['gateway_bytes']['total']}")
    h = fx["host"]
    findings = []
    if h["renderer_abstains"]:
        findings.append(f"renderer abstains: {h['abstain_reason']}"
                        + (f" ({h['rendered_output_error'][:120]})" if h["abstain_reason"] == "render_error_after_repair" else ""))
    elif h["gap"] is None or abs(h["gap"]) > h["tolerance"]:
        msg = f"rendered output {h['rendered_output']} vs claim {h['claim']} outside tolerance {h['tolerance']}"
        (findings if fx.get("tolerance_finding") else problems).append(
            (fx["tolerance_finding"] + ": " if fx.get("tolerance_finding") else "") + msg)
    for f in findings_pre:
        (findings if fx["engine_finish_reason"] == "length" else problems).append(f)
    for c in fx["checks"]:
        if not c["ok"]:
            (findings if c.get("kind") == "finding" else problems).append(f"{c['name']}: {c['detail'][:200]}")
    return problems, findings


def main():
    root = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(os.path.abspath(__file__)), "fixtures")
    bad = 0
    checked = 0
    print(f"{'model':28s} {'case':32s} {'finish':10s} {'engine':6s} {'claim':>5s} {'pos':>5s} {'rend':>5s} {'gap':>4s} {'tol':>3s} "
          f"{'abst':4s} {'bytes':>6s}  status")
    for d in sorted(glob.glob(os.path.join(root, "*"))):
        if not os.path.isdir(d):
            continue
        model = os.path.basename(d)
        for p in sorted(glob.glob(os.path.join(d, "*.json"))):
            name = os.path.basename(p)
            if name == "summary.json":
                continue
            checked += 1
            fx = json.load(open(p))
            if name.endswith(".error.json"):
                bad += 1
                print(f"{model[:28]:28s} {fx['case'][:32]:32s} ERROR {fx['error'][:200]}")
                continue
            if name.endswith(".rejected.json"):
                # the gateway refuses this request, so there is no served answer to verify
                print(f"{model[:28]:28s} {fx['case'][:32]:32s} REJECTED by gateway {fx['rejected_by']}: {fx['detail'][:160]}")
                continue
            problems, findings = verify(fx)
            bad += bool(problems)
            h = fx["host"]
            print(f"{model[:28]:28s} {fx['case'][:32]:32s} {fx['finish_reason']:10s} {fx['engine_finish_reason']:6s} {h['claim']:5d} {h['positions']:5d} "
                  f"{str(h['rendered_output']):>5s} {str(h['gap']):>4s} {h['tolerance']:3d} {'yes' if h['renderer_abstains'] else 'no':4s} "
                  f"{fx['gateway_bytes']['total']:6d}  {'ok' if not problems else 'FAIL: ' + ' | '.join(problems)}"
                  + (f"  [finding: {' | '.join(findings)}]" if findings else ""))
    if not checked:
        print(f"No fixtures found in {root}", file=sys.stderr)
        sys.exit(1)
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
