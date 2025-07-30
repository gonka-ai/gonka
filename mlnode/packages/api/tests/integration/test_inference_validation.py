import os
import urllib.parse
import logging
from typing import Dict, Any, List, Tuple

import pytest
import requests

from api.inference.client import InferenceClient
from common.wait import wait_for_server
from api.inference.top_tokens import (
    TopLogProbsSequence,
    compare_token_sequences,
    compare_logprobs
)

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
def model_setup_big(inference_client: InferenceClient, urls: tuple[str, str]) -> str:
    _, vllm_url = urls
    model_name = "Qwen/Qwen2.5-7B-Instruct"
    inference_client.inference_setup(model_name, "bfloat16")
    wait_for_server(f"{vllm_url}/v1/models", timeout=300)
    return model_name

@pytest.fixture(scope="session")
def model_setup_small(inference_client: InferenceClient, urls: tuple[str, str]) -> str:
    _, vllm_url = urls
    model_name = "unsloth/Llama-3.2-1B-Instruct"
    inference_client.inference_setup(model_name, "bfloat16")
    wait_for_server(f"{vllm_url}/v1/models", timeout=300)
    return model_name

@pytest.fixture
def test_prompt() -> str:
    return "Who won the world series in 2020? Сгенерируй интересный смешной и оригинальный текст"

@pytest.fixture
def enforced_str() -> str:
    return "The thrilling tale of the 2020 World Series! It was a season like no other, with teams battling it out on the field while also navigating the challenges of a global pandemic. But in the end, there could only be one champion.\n\nAnd that champion was... the Los Angeles Dodgers! ThatHUI right, the Boys in Blue brought home their first World Series title since 1988, and"

def run_inference_request(vllm_url: str, model: str, prompt: str) -> Dict[str, Any]:
    url = f"{vllm_url}/v1/chat/completions"
    payload = {
        "model": model,
        "messages": [
            {"role": "user", "content": prompt}
        ],
        "max_tokens": 80,
        "temperature": 0.5,
        "seed": 42,
        "stream": False,
        "logprobs": True,
        "top_logprobs": 3
    }
    
    response = requests.post(url, json=payload)
    if response.status_code != 200:
        logger.error(f"Error in inference API request: {response.text}")
        raise RuntimeError(f"Inference API request failed with status {response.status_code}")
    
    return response.json()

def run_validation_request(vllm_url: str, model: str, prompt: str, enforced_str: str) -> Dict[str, Any]:
    url = f"{vllm_url}/v1/chat/completions"
    payload = {
        "model": model,
        "messages": [
            {"role": "user", "content": prompt}
        ],
        "max_tokens": 80,
        "temperature": 0.5,
        "seed": 42,
        "stream": False,
        "logprobs": True,
        "top_logprobs": 3,
        "enforced_str": enforced_str
    }
    
    response = requests.post(url, json=payload)
    if response.status_code != 200:
        logger.error(f"Error in validation API request: {response.text}")
        raise RuntimeError(f"Validation API request failed with status {response.status_code}")
    
    return response.json()

def analyze_token_matches(match_results: List[bool]) -> Tuple[int, int, float]:
    total_tokens = len(match_results)
    matching_tokens = sum(match_results)
    match_percentage = (matching_tokens / total_tokens) * 100 if total_tokens > 0 else 0
    
    return matching_tokens, total_tokens, match_percentage

@pytest.mark.xfail(reason="Might fail but shouldn't break the pipeline", strict=False)
def test_same_model_inference_validation(
    model_setup_small: str,
    urls: tuple[str, str],
    test_prompt: str,
):
    """Test comparing inference and validation requests on the same model with the same prompt"""
    _, vllm_url = urls
    
    inference_response = run_inference_request(
        vllm_url, model_setup_small, test_prompt
    )

    response_text = inference_response['choices'][0]['message']['content']
    
    validation_response = run_validation_request(
        vllm_url, model_setup_small, test_prompt, response_text
    )
    
    inference_sequence = TopLogProbsSequence.from_json(inference_response)
    validation_sequence = TopLogProbsSequence.from_json(validation_response)
    
    token_matches = compare_token_sequences(inference_sequence, validation_sequence)
    
    assert inference_sequence is not None
    assert validation_sequence is not None
    assert len(inference_sequence) > 0
    assert len(validation_sequence) > 0
    assert all(token_matches), f"Tokens do not match:\n{inference_sequence}\n{validation_sequence}"


def test_different_models_inference_validation(
    inference_client: InferenceClient,
    urls: tuple[str, str],
    test_prompt: str,
    enforced_str: str
):
    _, vllm_url = urls
    
    # Set up the small model and run inference
    inference_client.inference_setup("unsloth/Llama-3.2-1B-Instruct", "bfloat16")
    # Wait for the server to be ready with the new model
    wait_for_server(f"{vllm_url}/v1/models", timeout=300)
    inference_response = run_inference_request(vllm_url, "unsloth/Llama-3.2-1B-Instruct", test_prompt)
    
    # Set up the big model and run validation
    inference_client.inference_setup("Qwen/Qwen2.5-7B-Instruct", "bfloat16")
    # Wait for the server to be ready with the new model
    wait_for_server(f"{vllm_url}/v1/models", timeout=300)
    validation_response = run_validation_request(
        vllm_url, "Qwen/Qwen2.5-7B-Instruct", test_prompt, enforced_str
    )
    
    inference_sequence = TopLogProbsSequence.from_json(inference_response)
    validation_sequence = TopLogProbsSequence.from_json(validation_response)

    token_matches = compare_token_sequences(inference_sequence, validation_sequence)
    assert not(all(token_matches))
