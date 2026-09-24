#!/usr/bin/env python3
"""vLLM's benchmarks/multi_turn client with a cache-reuse knob.

MT_CACHE_BUST selects how much of each request the prefix cache can serve:
  none     the session history is resent as is: high hit rate
  history  a one-off marker right after the shared system prompt: only that
           prompt is reusable, the history is recomputed every turn
  all      a one-off marker at the very start: nothing is reusable
Everything else is the stock client; usage: mt_bust.py <client args>.
"""

import os, re, sys, uuid

MT_DIR = os.environ.get("MT_DIR", os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, MT_DIR)
import benchmark_serving_multi_turn as mt  # noqa: E402

MODE = os.environ.get("MT_CACHE_BUST", "none")
# client i starts i*STAGGER/CLIENTS seconds late, so sessions do not move in lockstep
STAGGER = float(os.environ.get("MT_STAGGER", "0"))
CLIENTS = max(1, int(os.environ.get("MT_CLIENTS", "1")))
_send_turn = mt.send_turn
_client_main = mt.client_main
_CONV_TAG = re.compile(r"\d+ is a nice number")


def _bust(first):
    tag = f"[{uuid.uuid4().hex}] "
    text = first["content"]
    if MODE == "history":
        m = _CONV_TAG.search(text)
        pos = m.start() if m else 0
        return {"role": first["role"], "content": text[:pos] + tag + text[pos:]}
    return {"role": first["role"], "content": tag + text}


async def send_turn(session, client_id, conv_id, conversation_messages, messages_to_use,
                    *args, **kwargs):
    if MODE != "none":
        # a new list, same message dicts: the client writes answers into them
        conversation_messages = [_bust(conversation_messages[0])] + conversation_messages[1:]
    return await _send_turn(session, client_id, conv_id, conversation_messages,
                            messages_to_use, *args, **kwargs)


async def client_main(args, req_args, client_id, *rest, **kwargs):
    import asyncio
    await asyncio.sleep(STAGGER * client_id / CLIENTS)
    return await _client_main(args, req_args, client_id, *rest, **kwargs)


mt.send_turn = send_turn
mt.client_main = client_main

if __name__ == "__main__":
    import asyncio
    sys.argv[0] = os.path.join(MT_DIR, "benchmark_serving_multi_turn.py")
    asyncio.run(mt.main())
