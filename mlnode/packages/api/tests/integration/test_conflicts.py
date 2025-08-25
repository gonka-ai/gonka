import os
import pytest
import requests
import toml
import urllib.parse
import hashlib
from datetime import datetime
from pow.models.utils import Params
from pow.service.client import PowClient
from api.inference.client import InferenceClient
from zeroband.service.client import TrainClient

@pytest.fixture(scope="session")
def server_url():
    url = os.getenv("SERVER_URL")
    if not url:
        raise ValueError("SERVER_URL is not set")
    return url

@pytest.fixture(scope="session")
def batch_reciever_url():
    url = os.getenv("BATCH_RECIEVER_URL")
    if not url:
        raise ValueError("BATCH_RECIEVER_URL is not set")
    return url

@pytest.fixture(scope="session")
def block_hash():
    now_str = datetime.now().strftime('%Y-%m-%d_%H-%M-%S')
    return hashlib.sha256(now_str.encode()).hexdigest()

@pytest.fixture(scope="session")
def public_key():
    return f"pub_key_1_{datetime.now().strftime('%Y-%m-%d_%H-%M-%S')}"

@pytest.fixture(scope="session")
def train_config_dict():
    return toml.load("/app/packages/train/resources/configs/1B_3090_1x1.toml")

@pytest.fixture(scope="session")
def model_name():
    return "Qwen/Qwen2.5-7B-Instruct"

@pytest.fixture(scope="session")
def pow_params():
    return Params(
        dim=512,
        n_layers=64,
        n_heads=128,
        n_kv_heads=128,
        vocab_size=8192,
        ffn_dim_multiplier=16.0,
        multiple_of=1024,
        norm_eps=1e-05,
        rope_theta=500000.0,
        use_scaled_rope=False,
        seq_len=4
    )

def test_exclusive_services(
    server_url,
    batch_reciever_url,
    block_hash,
    public_key,
    train_config_dict,
    model_name,
    pow_params
):
    requests.post(f"{server_url}/api/v1/stop").raise_for_status()
    train_client = TrainClient(server_url)
    inference_client = InferenceClient(server_url)
    pow_client = PowClient(server_url)

    # Start TRAIN (should succeed)
    train_client.start(train_config_dict, {
        "GLOBAL_ADDR": urllib.parse.urlparse(server_url).hostname,
        "GLOBAL_PORT": "5565",
        "GLOBAL_RANK": "0",
        "GLOBAL_UNIQUE_ID": "0",
        "GLOBAL_WORLD_SIZE": "1",
        "BASE_PORT": "10001"
    })

    with pytest.raises(requests.exceptions.HTTPError) as exc_info:
        inference_client.inference_setup(model_name, "bfloat16")
    assert exc_info.value.response.status_code == 409

    with pytest.raises(requests.exceptions.HTTPError) as exc_info:
        pow_client.init_generate(
            node_id=0,
            node_count=1,
            url=batch_reciever_url,
            block_hash=block_hash,
            block_height=1,
            public_key=public_key,
            batch_size=5000,
            r_target=4,
            fraud_threshold=0.01,
            params=pow_params,
        )
    assert exc_info.value.response.status_code == 409

    train_client.stop()

    print("Setting up inference")
    inference_client.inference_setup(model_name, "bfloat16")

    with pytest.raises(requests.exceptions.HTTPError) as exc_info:
        pow_client.init_generate(
            node_id=0,
            node_count=1,
            url=batch_reciever_url,
            block_hash=block_hash,
            block_height=1,
            public_key=public_key,
            batch_size=5000,
            r_target=4,
            fraud_threshold=0.01,
            params=pow_params,
        )
    assert exc_info.value.response.status_code == 409

    with pytest.raises(requests.exceptions.HTTPError) as exc_info:
        train_client.start(train_config_dict, {})
    assert exc_info.value.response.status_code == 409

    inference_client.inference_down()

    pow_client.init_generate(
        node_id=0,
        node_count=1,
        url=batch_reciever_url,
        block_hash=block_hash,
        block_height=1,
        public_key=public_key,
        batch_size=5000,
        r_target=4,
        fraud_threshold=0.01,
        params=pow_params,
    )

    with pytest.raises(requests.exceptions.HTTPError) as exc_info:
        train_client.start(train_config_dict, {})
    assert exc_info.value.response.status_code == 409

    with pytest.raises(requests.exceptions.HTTPError) as exc_info:
        inference_client.inference_setup(model_name, "bfloat16")
    assert exc_info.value.response.status_code == 409

    pow_client.stop()
