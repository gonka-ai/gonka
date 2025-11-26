import os
import urllib.parse
import logging
from typing import Dict, Any

import pytest
import requests

from api.inference.client import InferenceClient
from common.wait import wait_for_server

logger = logging.getLogger(__name__)


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


@pytest.fixture(scope="session")
def model_setup(inference_client: InferenceClient, urls: tuple[str, str]) -> str:
    _, vllm_url = urls
    model_name = "unsloth/Llama-3.2-1B-Instruct"
    inference_client.inference_setup(model_name, "bfloat16")
    wait_for_server(f"{vllm_url}/v1/models", timeout=300)
    return model_name


@pytest.fixture
def test_prompt() -> str:
    return "Count from 1 to 10 in words."


def generate_with_deterministic_sampling(
    vllm_url: str,
    model: str,
    prompt: str,
    seed: int,
    temperature: float = 1.0,
    use_deterministic: bool = True,
    max_tokens: int = 50
) -> Dict[str, Any]:
    url = f"{vllm_url}/v1/chat/completions"
    payload = {
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": max_tokens,
        "temperature": temperature,
        "seed": seed,
        "stream": False,
        "logprobs": True,
        "top_logprobs": 5
    }
    
    if use_deterministic:
        payload["use_deterministic_hash"] = True
    
    response = requests.post(url, json=payload)
    if response.status_code != 200:
        raise RuntimeError(f"API request failed with status {response.status_code}: {response.text}")
    
    return response.json()


@pytest.mark.e2e
def test_deterministic_parameter_accepted(urls: tuple[str, str], model_setup: str, test_prompt: str):
    _, vllm_url = urls
    
    response = generate_with_deterministic_sampling(
        vllm_url=vllm_url,
        model=model_setup,
        prompt=test_prompt,
        seed=42,
        use_deterministic=True
    )
    
    assert "choices" in response
    assert len(response["choices"]) > 0
    assert "content" in response["choices"][0]["message"]


@pytest.mark.e2e
def test_deterministic_sampling_reproducibility(urls: tuple[str, str], model_setup: str, test_prompt: str):
    _, vllm_url = urls
    seed = 42
    temperature = 1.0
    
    outputs = []
    logprobs_sequences = []
    
    for i in range(3):
        response = generate_with_deterministic_sampling(
            vllm_url=vllm_url,
            model=model_setup,
            prompt=test_prompt,
            seed=seed,
            temperature=temperature,
            use_deterministic=True,
            max_tokens=50
        )
        
        content = response["choices"][0]["message"]["content"]
        logprobs = response["choices"][0]["logprobs"]["content"]
        
        outputs.append(content)
        logprobs_sequences.append(logprobs)
    
    assert outputs[0] == outputs[1]
    assert outputs[1] == outputs[2]
    assert len(logprobs_sequences[0]) == len(logprobs_sequences[1]) == len(logprobs_sequences[2])


@pytest.mark.e2e
def test_different_seeds_produce_different_outputs(urls: tuple[str, str], model_setup: str, test_prompt: str):
    _, vllm_url = urls
    temperature = 1.0
    
    output_seed_42 = generate_with_deterministic_sampling(
        vllm_url=vllm_url,
        model=model_setup,
        prompt=test_prompt,
        seed=42,
        temperature=temperature,
        use_deterministic=True
    )["choices"][0]["message"]["content"]
    
    output_seed_123 = generate_with_deterministic_sampling(
        vllm_url=vllm_url,
        model=model_setup,
        prompt=test_prompt,
        seed=123,
        temperature=temperature,
        use_deterministic=True
    )["choices"][0]["message"]["content"]
    
    assert output_seed_42 != output_seed_123


@pytest.mark.e2e
def test_deterministic_vs_regular_sampling(urls: tuple[str, str], model_setup: str, test_prompt: str):
    _, vllm_url = urls
    seed = 42
    temperature = 1.0
    
    deterministic_output = generate_with_deterministic_sampling(
        vllm_url=vllm_url,
        model=model_setup,
        prompt=test_prompt,
        seed=seed,
        temperature=temperature,
        use_deterministic=True
    )["choices"][0]["message"]["content"]
    
    regular_output = generate_with_deterministic_sampling(
        vllm_url=vllm_url,
        model=model_setup,
        prompt=test_prompt,
        seed=seed,
        temperature=temperature,
        use_deterministic=False
    )["choices"][0]["message"]["content"]
    
    assert len(deterministic_output) > 0
    assert len(regular_output) > 0


@pytest.mark.e2e
def test_deterministic_with_various_temperatures(urls: tuple[str, str], model_setup: str, test_prompt: str):
    _, vllm_url = urls
    seed = 42
    temperatures = [0.5, 0.7, 1.0, 1.5]
    
    for temp in temperatures:
        output1 = generate_with_deterministic_sampling(
            vllm_url=vllm_url,
            model=model_setup,
            prompt=test_prompt,
            seed=seed,
            temperature=temp,
            use_deterministic=True
        )["choices"][0]["message"]["content"]
        
        output2 = generate_with_deterministic_sampling(
            vllm_url=vllm_url,
            model=model_setup,
            prompt=test_prompt,
            seed=seed,
            temperature=temp,
            use_deterministic=True
        )["choices"][0]["message"]["content"]
        
        assert output1 == output2


@pytest.mark.e2e
def test_backward_compatibility_without_parameter(urls: tuple[str, str], model_setup: str, test_prompt: str):
    _, vllm_url = urls
    
    url = f"{vllm_url}/v1/chat/completions"
    payload = {
        "model": model_setup,
        "messages": [{"role": "user", "content": test_prompt}],
        "max_tokens": 50,
        "temperature": 0.7,
        "seed": 42,
        "stream": False,
        "logprobs": True,
        "top_logprobs": 3
    }
    
    response = requests.post(url, json=payload)
    assert response.status_code == 200
    
    result = response.json()
    assert "choices" in result
    assert len(result["choices"]) > 0


@pytest.mark.e2e
def test_deterministic_sampling_with_logprobs(urls: tuple[str, str], model_setup: str, test_prompt: str):
    _, vllm_url = urls
    seed = 42
    temperature = 1.0
    
    response1 = generate_with_deterministic_sampling(
        vllm_url=vllm_url,
        model=model_setup,
        prompt=test_prompt,
        seed=seed,
        temperature=temperature,
        use_deterministic=True
    )
    
    response2 = generate_with_deterministic_sampling(
        vllm_url=vllm_url,
        model=model_setup,
        prompt=test_prompt,
        seed=seed,
        temperature=temperature,
        use_deterministic=True
    )
    
    logprobs1 = response1["choices"][0]["logprobs"]["content"]
    logprobs2 = response2["choices"][0]["logprobs"]["content"]
    
    assert len(logprobs1) == len(logprobs2)
    
    for i, (pos1, pos2) in enumerate(zip(logprobs1, logprobs2)):
        assert pos1["token"] == pos2["token"]
        assert abs(pos1["logprob"] - pos2["logprob"]) < 1e-6
        
        assert len(pos1["top_logprobs"]) == len(pos2["top_logprobs"])
        for top1, top2 in zip(pos1["top_logprobs"], pos2["top_logprobs"]):
            assert top1["token"] == top2["token"]
            assert abs(top1["logprob"] - top2["logprob"]) < 1e-6


@pytest.mark.e2e
def test_deterministic_sampling_with_longer_output(urls: tuple[str, str], model_setup: str):
    _, vllm_url = urls
    seed = 42
    temperature = 1.0
    max_tokens = 100
    
    prompt = "Write a short story about a robot learning to paint."
    
    output1 = generate_with_deterministic_sampling(
        vllm_url=vllm_url,
        model=model_setup,
        prompt=prompt,
        seed=seed,
        temperature=temperature,
        use_deterministic=True,
        max_tokens=max_tokens
    )["choices"][0]["message"]["content"]
    
    output2 = generate_with_deterministic_sampling(
        vllm_url=vllm_url,
        model=model_setup,
        prompt=prompt,
        seed=seed,
        temperature=temperature,
        use_deterministic=True,
        max_tokens=max_tokens
    )["choices"][0]["message"]["content"]
    
    assert output1 == output2
    assert len(output1) > 50
