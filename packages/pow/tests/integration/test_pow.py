import os
import pytest
import requests
import datetime
import hashlib
from time import sleep

from pow.service.client import PowClient
from pow.compute.stats import estimate_R_from_experiment
from pow.compute.compute import ProofBatch
from pow.data import ValidatedBatch
from pow.models.utils import Params

@pytest.fixture(scope="session")
def server_urls():
    batch_receiver_url = os.getenv("BATCH_RECIEVER_URL")
    if not batch_receiver_url:
        raise ValueError("BATCH_RECIEVER_URL is not set")
    server_url = os.getenv("SERVER_URL")
    if not server_url:
        raise ValueError("SERVER_URL is not set")

    def wait_for_server(url):
        while True:
            try:
                response = requests.get(url)
                if response.status_code == 404 or response.ok:
                    break
            except requests.exceptions.RequestException:
                pass
            sleep(1)

    wait_for_server(batch_receiver_url)
    wait_for_server(server_url)
    return batch_receiver_url, server_url

@pytest.fixture(scope="session")
def client(server_urls):
    _, server_url = server_urls
    return PowClient(server_url)

@pytest.fixture(scope="session")
def model_params():
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
        use_scaled_rope=True,
        seq_len=4
    )

@pytest.fixture(scope="session")
def r_target():
    return estimate_R_from_experiment(n=8192, P=0.001, num_samples=50000)

@pytest.fixture(scope="session")
def unique_identifiers():
    date_str = datetime.datetime.now().strftime('%Y-%m-%d_%H-%M-%S')
    block_hash = hashlib.sha256(date_str.encode()).hexdigest()
    public_key = f"pub_key_1_{date_str}"
    return block_hash, public_key

@pytest.fixture(scope="session")
def init_generation(client, server_urls, model_params, r_target, unique_identifiers):
    batch_receiver_url, _ = server_urls
    block_hash, public_key = unique_identifiers
    client.init_generate(
        url=batch_receiver_url,
        block_hash=block_hash,
        block_height=1,
        public_key=public_key,
        batch_size=5000,
        r_target=r_target,
        fraud_threshold=0.01,
        params=model_params,
    )
    sleep(150)
    return {"block_hash": block_hash, "public_key": public_key}

def get_proof_batches(url):
    response = requests.get(f"{url}/generated")
    if response.status_code == 200:
        return response.json()["proof_batches"]
    raise Exception(f"Error: {response.status_code} - {response.text}")

def get_val_proof_batches(url):
    response = requests.get(f"{url}/validated")
    if response.status_code == 200:
        return response.json()["validated_batches"]
    raise Exception(f"Error: {response.status_code} - {response.text}")

def create_correct_batch(pb, n=10000):
    return ProofBatch(**{
        'public_key': pb.public_key,
        'block_hash': pb.block_hash,
        'block_height': pb.block_height,
        'nonces': [pb.nonces[0]] * n,
        'dist': [pb.dist[0]] * n
    })

def get_incorrect_nonce(pb):
    for i in range(1000):
        if i not in pb.nonces:
            return i
    return None

def create_incorrect_batch(pb, n, n_invalid):
    incorrect_pb_dict = {
        'public_key': pb.public_key,
        'block_hash': pb.block_hash,
        'block_height': pb.block_height,
        'nonces': [get_incorrect_nonce(pb)] * n_invalid,
        'dist': [pb.dist[0]] * n_invalid
    }
    return ProofBatch.merge([
        create_correct_batch(pb, n - n_invalid),
        ProofBatch(**incorrect_pb_dict)
    ])

@pytest.fixture
def latest_proof_batch(init_generation, server_urls):
    batch_receiver_url, _ = server_urls
    proof_batches = get_proof_batches(batch_receiver_url)
    return ProofBatch(**proof_batches[-1])

def test_estimate_r(r_target):
    assert r_target > 0

def test_generated_proofs(init_generation, server_urls):
    batch_receiver_url, _ = server_urls
    proof_batches = get_proof_batches(batch_receiver_url)
    assert len(proof_batches) > 0

def test_validate_correct_batch(client, server_urls, latest_proof_batch):
    batch_receiver_url, _ = server_urls
    client.start_validation()
    correct_pb = create_correct_batch(latest_proof_batch, n=100)
    client.validate(correct_pb)
    sleep(15)
    val_proof_batches = get_val_proof_batches(batch_receiver_url)
    assert len(val_proof_batches) > 0
    vpb = ValidatedBatch(**val_proof_batches[-1])
    assert len(vpb) == 100
    assert vpb.n_invalid == 0
    assert not vpb.fraud_detected

def test_validate_incorrect_batch(client, server_urls, latest_proof_batch):
    batch_receiver_url, _ = server_urls
    client.start_validation()
    incorrect_pb = create_incorrect_batch(latest_proof_batch, n=100, n_invalid=10)
    client.validate(incorrect_pb)
    sleep(15)
    val_proof_batches = get_val_proof_batches(batch_receiver_url)
    assert len(val_proof_batches) > 0
    vpb = ValidatedBatch(**val_proof_batches[-1])
    assert len(vpb) == 100
    assert vpb.n_invalid > 0
