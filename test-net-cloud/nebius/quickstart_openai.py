#!/usr/bin/env python3
"""
Same idea as Gonka Developer Quickstart, but this testnet has no
"participants with proof" API — gonka-openai then 404s if you only pass source_url.

Fix: pass explicit endpoint as url + TA address (from /v1/identity).

  pip install gonka-openai

  export GONKA_PRIVATE_KEY='<hex>'
  export NODE_URL='http://xj7-5.s.filfox.io:19240/v1'

  # OR set both manually (no identity fetch):
  # export GONKA_ENDPOINTS='http://xj7-5.s.filfox.io:19240/v1;gonka1.....'

  export GONKA_MODEL='Qwen/Qwen3-4B-Instruct-2507'   # if 235B not on net

  python3 quickstart_openai.py
"""
import json
import os
import sys
import urllib.request

try:
    from gonka_openai import GonkaOpenAI
    from gonka_openai.utils import Endpoint
except ImportError:
    print("Run: pip install gonka-openai", file=sys.stderr)
    sys.exit(1)

os.environ.setdefault("NODE_URL", "http://xj7-5.s.filfox.io:19240/v1")

priv = os.environ.get("GONKA_PRIVATE_KEY")
if not priv:
    print("Set GONKA_PRIVATE_KEY (hex)", file=sys.stderr)
    sys.exit(1)

node_url = os.environ.get("NODE_URL", "").rstrip("/")
if not node_url.endswith("/v1"):
    node_url = node_url + "/v1"

model = os.environ.get(
    "GONKA_MODEL",
    "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8",
)

# Explicit endpoints skip broken discovery on custom testnets
raw_eps = os.environ.get("GONKA_ENDPOINTS")
if raw_eps:
    endpoints = [Endpoint.parse(e.strip()) for e in raw_eps.split(",") if e.strip()]
else:

    def fetch_transfer_address(api_v1: str) -> str:
        url = api_v1.rstrip("/") + "/identity"
        with urllib.request.urlopen(url, timeout=30) as r:
            data = json.loads(r.read().decode())
        return data["data"]["address"]

    ta = fetch_transfer_address(node_url)
    endpoints = [Endpoint(url=node_url, address=ta)]

client = GonkaOpenAI(
    gonka_private_key=priv,
    endpoints=endpoints,
)

response = client.chat.completions.create(
    model=model,
    messages=[
        {
            "role": "user",
            "content": "Write a one-sentence bedtime story about a unicorn",
        }
    ],
)

print(response.choices[0].message.content)
