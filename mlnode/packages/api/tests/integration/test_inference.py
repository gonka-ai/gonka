import os
import urllib.parse
from datetime import datetime
from time import sleep
import hashlib
import pytest
import requests

from api.inference.client import InferenceClient
from common.wait import wait_for_server


@pytest.fixture(scope="session")
def urls() -> tuple[str, str]:
    server_url = os.getenv("SERVER_URL")
    if not server_url:
        raise ValueError("SERVER_URL is not set")
    vllm_url = urllib.parse.urlparse(server_url).hostname
    scheme = urllib.parse.urlparse(server_url).scheme
    return server_url, f"{scheme}://{vllm_url}:5000"

@pytest.fixture(scope="session")
def inference_client(urls: tuple[str, str]) -> InferenceClient:
    server_url, _ = urls
    return InferenceClient(server_url)

@pytest.fixture
def session_identifiers() -> tuple[str, str, str]:
    date_str = datetime.now().strftime('%Y-%m-%d_%H-%M-%S')
    block_hash = hashlib.sha256(date_str.encode()).hexdigest()
    public_key = f"pub_key_1_{date_str}"
    return block_hash, public_key, date_str

@pytest.fixture(scope="session")
def model_setup(inference_client: InferenceClient, urls: tuple[str, str]) -> str:
    _, vllm_url = urls
    model_name = "Qwen/Qwen2.5-7B-Instruct"
    inference_client.inference_setup(model_name, "bfloat16")
    wait_for_server(f"{vllm_url}/v1/models", timeout=300)
    return model_name

def test_inference_completion(model_setup: str, urls: tuple[str, str]):
    _, vllm_url = urls
    url = f"{vllm_url}/v1/chat/completions"
    payload = {
        "model": model_setup,
        "messages": [
            {"role": "user", "content": "Who won the world series in 2020? Generate a funny and original text."}
        ],
        "max_tokens": 80,
        "temperature": 0.5,
        "seed": 42,
        "stream": False,
        "logprobs": 1,
        "top_logprobs": 3
    }

    response = requests.post(url, json=payload)
    assert response.status_code == 200
    response_data = response.json()
    assert isinstance(response_data, dict)

