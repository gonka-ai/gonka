import os
import pytest
import requests
import toml
import urllib.parse

from api.inference.client import InferenceClient
from zeroband.service.client import TrainClient


@pytest.fixture(scope="session")
def server_url():
    url = os.getenv("SERVER_URL")
    if not url:
        raise ValueError("SERVER_URL is not set")
    return url


@pytest.fixture(scope="session")
def train_config_dict():
    return toml.load("/app/packages/train/resources/configs/1B_3090_1x1.toml")


@pytest.fixture(scope="session")
def model_name():
    return "Qwen/Qwen3-4B-Instruct-2507"


def test_exclusive_services(
    server_url,
    train_config_dict,
    model_name,
):
    requests.post(f"{server_url}/api/v1/stop").raise_for_status()
    train_client = TrainClient(server_url)
    inference_client = InferenceClient(server_url)

    train_client.start(train_config_dict, {
        "GLOBAL_ADDR": urllib.parse.urlparse(server_url).hostname,
        "GLOBAL_PORT": "5565",
        "GLOBAL_RANK": "0",
        "GLOBAL_UNIQUE_ID": "0",
        "GLOBAL_WORLD_SIZE": "1",
        "BASE_PORT": "10001"
    })

    with pytest.raises(requests.exceptions.HTTPError) as exc_info:
        inference_client.inference_setup(model_name, "bfloat16", ["--max-model-len", "10000"])
    assert exc_info.value.response.status_code == 409

    train_client.stop()

    inference_client.inference_setup(model_name, "bfloat16", ["--max-model-len", "10000"])

    with pytest.raises(requests.exceptions.HTTPError) as exc_info:
        train_client.start(train_config_dict, {})
    assert exc_info.value.response.status_code == 409

    inference_client.inference_down()

    train_client.start(train_config_dict, {})

    with pytest.raises(requests.exceptions.HTTPError) as exc_info:
        inference_client.inference_setup(model_name, "bfloat16", ["--max-model-len", "10000"])
    assert exc_info.value.response.status_code == 409

    train_client.stop()
