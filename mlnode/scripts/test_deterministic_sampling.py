#!/usr/bin/env python3
"""Quick test script to verify deterministic sampling works.
Run this after starting vLLM server with your new Docker image.
Provides a small, robust CLI helper for manual verification.
"""

from __future__ import annotations

import argparse
import sys
from typing import Any, Dict

import requests


DEFAULT_BASE_URL = "http://localhost:8000"
DEFAULT_TIMEOUT = 30


def _safe_get_model_id(models_json: Dict[str, Any]) -> str:
    data = models_json.get("data") if isinstance(models_json, dict) else None
    if data and isinstance(data, list) and len(data) > 0:
        first = data[0]
        if isinstance(first, dict) and "id" in first:
            return first["id"]
    return "test-model"


def _post_completion(base_url: str, payload: Dict[str, Any], timeout: int = DEFAULT_TIMEOUT) -> Dict[str, Any]:
    url = f"{base_url.rstrip('/')}/v1/chat/completions"
    resp = requests.post(url, json=payload, timeout=timeout)
    resp.raise_for_status()
    return resp.json()


def run_tests(base_url: str = DEFAULT_BASE_URL, timeout: int = DEFAULT_TIMEOUT) -> bool:
    print("=" * 60)
    print("Testing vLLM Deterministic Sampling")
    print("=" * 60)

    # Test 1: server up
    print("Test 1: Checking server availability...")
    try:
        resp = requests.get(f"{base_url.rstrip('/')}/v1/models", timeout=5)
        resp.raise_for_status()
        models = resp.json()
        model_id = _safe_get_model_id(models)
        print(f"Server is up. Using model id: {model_id}")
    except requests.RequestException as e:
        print(f"Cannot connect to server: {e}")
        print(f"Make sure vLLM is running at {base_url}")
        return False

    # Test 2: parameter accepted
    print("Test 2: Verifying use_deterministic_hash parameter is accepted...")
    payload = {
        "model": model_id,
        "messages": [{"role": "user", "content": "Say hello"}],
        "max_tokens": 10,
        "temperature": 1.0,
        "seed": 42,
        "use_deterministic_hash": True,
    }
    try:
        result = _post_completion(base_url, payload, timeout)
        content = result.get("choices", [])[0].get("message", {}).get("content", "")
        print(f"Parameter accepted. Example response: {content[:120]}")
    except Exception as e:
        print(f"Request failed: {e}")
        return False

    # Test 3: reproducibility
    print("Test 3: Checking reproducibility with same seed...")
    payload = {**payload, "messages": [{"role": "user", "content": "Count to 5"}], "max_tokens": 30}
    try:
        out1 = _post_completion(base_url, payload, timeout)
        out2 = _post_completion(base_url, payload, timeout)
        o1 = out1.get("choices", [])[0].get("message", {}).get("content", "")
        o2 = out2.get("choices", [])[0].get("message", {}).get("content", "")
        print("Output 1:", o1)
        print("Output 2:", o2)
        if o1 != o2:
            print("DIFFERENT outputs for same seed — deterministic sampling may not be enabled")
            return False
        print("Reproducible: outputs match")
    except Exception as e:
        print(f"Error during reproducibility test: {e}")
        return False

    # Test 4: different seeds
    print("Test 4: Checking that different seeds produce different outputs...")
    try:
        payload_a = {**payload, "seed": 42}
        payload_b = {**payload, "seed": 123}
        a = _post_completion(base_url, payload_a, timeout).get("choices", [])[0].get("message", {}).get("content", "")
        b = _post_completion(base_url, payload_b, timeout).get("choices", [])[0].get("message", {}).get("content", "")
        print(f"Seed 42 sample: {a[:120]}")
        print(f"Seed 123 sample: {b[:120]}")
        if a == b:
            print("Warning: same output for different seeds — this can happen occasionally")
        else:
            print("Different seeds produced different outputs")
    except Exception as e:
        print(f"Error during different-seed test: {e}")
        return False

    # Test 5: backward compatibility (no parameter)
    print("Test 5: Backward compatibility (request without use_deterministic_hash)...")
    try:
        payload_no = {"model": model_id, "messages": [{"role": "user", "content": "Hello"}], "max_tokens": 10, "temperature": 1.0, "seed": 42}
        r = _post_completion(base_url, payload_no, timeout)
        if not r.get("choices"):
            print("Unexpected response structure when calling without parameter")
            return False
        print("Backward compatible: call without parameter succeeded")
    except Exception as e:
        print(f"Error during backward compatibility test: {e}")
        return False

    print("=" * 60)
    print("ALL TESTS PASSED!")
    print("=" * 60)
    return True


if __name__ == "__main__":
    base_url = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8000"
    
    success = test_vllm_server(base_url)
    sys.exit(0 if success else 1)
