from datetime import datetime
from time import sleep
import hashlib
import pytest
import requests

from inference.client import InferenceClient

@pytest.fixture(scope="session")
def inference_client() -> InferenceClient:
    server_url = "http://0.0.0.0:8080"
    return InferenceClient(server_url)

@pytest.fixture
def session_identifiers() -> tuple[str, str, str]:
    date_str = datetime.now().strftime('%Y-%m-%d_%H-%M-%S')
    block_hash = hashlib.sha256(date_str.encode()).hexdigest()
    public_key = f"pub_key_1_{date_str}"
    return block_hash, public_key, date_str

@pytest.fixture(scope="session")
def model_setup(inference_client: InferenceClient) -> str:
    model_name = "unsloth/llama-3-8b-Instruct"
    inference_client.inference_setup(model_name, "bfloat16")
    sleep(50) 
    return model_name

def test_inference_completion(model_setup: str):
    url = "http://0.0.0.0:5000/v1/completions"
    payload = {
        "model": model_setup,
        "prompt": "How many R's in the word strawberry?",
        "max_tokens": 100,
        "temperature": 0
    }
    
    response = requests.post(url, json=payload)
    assert response.status_code == 200
    response_data = response.json()
    assert isinstance(response_data, dict)

