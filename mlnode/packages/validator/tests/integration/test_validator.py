import pytest
import requests
from time import sleep
import os

from common.wait import wait_for_server


def test_validator_health():
    server_url = os.getenv("SERVER_URL")
    
    wait_for_server(f"{server_url}/")
    
    response = requests.get(f"{server_url}/")
    assert response.status_code == 200
    data = response.json()
    assert data["service"] == "mlnode-validator"
    assert data["status"] == "running"
    print(response.json())


def test_validator_api_health():
    server_url = os.getenv("SERVER_URL")
    
    wait_for_server(f"{server_url}/")
    
    response = requests.get(f"{server_url}/api/v1/health")
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "healthy"
    assert "vllm_status" in data
    assert "vllm_available" in data
    print(response.json())


def test_validator_cache_info():
    server_url = os.getenv("SERVER_URL")
    
    wait_for_server(f"{server_url}/")
    
    response = requests.get(f"{server_url}/api/v1/models/cache")
    assert response.status_code == 200
    data = response.json()
    assert "cache_dir" in data
    assert "repos" in data
    assert "total_size" in data
    print(response.json())


def test_full_inference_workflow():
    server_url = os.getenv("SERVER_URL")
    model1_repo = os.getenv("TEST_MODEL_1", "HuggingFaceTB/SmolLM2-135M-Instruct")
    model2_repo = os.getenv("TEST_MODEL_2", "HuggingFaceTB/SmolLM2-360M-Instruct")
    
    wait_for_server(f"{server_url}/")
    
    print(f"\n=== Testing full inference workflow ===")
    
    print(f"\n1. Downloading first model: {model1_repo}")
    response = requests.post(
        f"{server_url}/api/v1/models/download",
        json={"repo_id": model1_repo}
    )
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "success"
    assert data["repo_id"] == model1_repo
    print(f"Model download initiated: {data}")
    
    sleep(5)
    
    print(f"\n2. Setting model: {model1_repo}")
    response = requests.post(
        f"{server_url}/api/v1/models/set",
        json={
            "model": model1_repo,
            "dtype": "auto",
            "additional_args": [
                "--gpu-memory-utilization 0.5"
            ]
        }
    )
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "success"
    assert data["model"] == model1_repo
    print(f"Model set: {data}")
    
    print("\n3. Starting vLLM")
    response = requests.post(f"{server_url}/api/v1/vllm/start")
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "success"
    print(f"vLLM started: {data}")
    
    print("\n4. Waiting for vLLM to be ready...")
    max_retries = 60
    for i in range(max_retries):
        response = requests.get(f"{server_url}/api/v1/health")
        if response.status_code == 200:
            health = response.json()
            if health["vllm_available"]:
                print(f"vLLM is ready after {i+1} checks")
                break
        sleep(5)
    else:
        raise Exception("vLLM did not become ready in time")
    
    print("\n5. Submitting chat completion request")
    response = requests.post(
        f"{server_url}/api/v1/chat/completions",
        json={
            "messages": [
                {"role": "user", "content": "What is the capital of France?"}
            ],
            "max_tokens": 50,
            "temperature": 0.7
        }
    )
    assert response.status_code == 200
    data = response.json()
    assert "choices" in data
    print(f"Chat completion response: {data}")
    
    print("\n6. Stopping vLLM")
    response = requests.post(f"{server_url}/api/v1/vllm/stop")
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "success"
    print(f"vLLM stopped: {data}")
    
    sleep(5)
    
    print(f"\n7. Downloading second model: {model2_repo}")
    response = requests.post(
        f"{server_url}/api/v1/models/download",
        json={"repo_id": model2_repo}
    )
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "success"
    assert data["repo_id"] == model2_repo
    print(f"Model download initiated: {data}")
    
    sleep(5)
    
    print(f"\n8. Setting second model: {model2_repo}")
    response = requests.post(
        f"{server_url}/api/v1/models/set",
        json={
            "model": model2_repo,
            "dtype": "auto",
            "additional_args": [
                "--gpu-memory-utilization 0.3"
            ]
        }
    )
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "success"
    assert data["model"] == model2_repo
    print(f"Model set: {data}")
    
    print("\n9. Starting vLLM again")
    response = requests.post(f"{server_url}/api/v1/vllm/start")
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "success"
    print(f"vLLM started: {data}")
    
    print("\n10. Waiting for vLLM to be ready...")
    for i in range(max_retries):
        response = requests.get(f"{server_url}/api/v1/health")
        if response.status_code == 200:
            health = response.json()
            if health["vllm_available"]:
                print(f"vLLM is ready after {i+1} checks")
                break
        sleep(5)
    else:
        raise Exception("vLLM did not become ready in time")
    
    print("\n11. Submitting second chat completion request")
    response = requests.post(
        f"{server_url}/api/v1/chat/completions",
        json={
            "messages": [
                {"role": "user", "content": "What is machine learning?"}
            ],
            "max_tokens": 50,
            "temperature": 0.7
        }
    )
    assert response.status_code == 200
    data = response.json()
    assert "choices" in data
    print(f"Chat completion response: {data}")
    
    print("\n12. Stopping vLLM")
    response = requests.post(f"{server_url}/api/v1/vllm/stop")
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "success"
    print(f"vLLM stopped: {data}")
    
    print("\n=== Full inference workflow completed successfully ===\n")
