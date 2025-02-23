from time import sleep
import datetime
import requests
import hashlib

from pow.service.client import PowClient
from pow.compute.stats import estimate_R_from_experiment
from pow.compute.compute import ProofBatch
from pow.data import ValidatedBatch
from pow.models.utils import Params

date_str = datetime.datetime.now().strftime('%Y-%m-%d_%H-%M-%S')
BLOCK_HASH = hashlib.sha256(date_str.encode()).hexdigest()
PUBLIC_KEY = f"pub_key_1_{date_str}"
BATCH_SIZE = 5000

batch_reciever_url = input("Enter the batch receiver URL: ")
server_url = input("Enter the server URL: ")

client = PowClient(server_url)

def get_proof_batches() -> list:
    response = requests.get(f"{batch_reciever_url}/generated")
    if response.status_code == 200:
        return response.json()["proof_batches"]
    raise Exception(f"Error: {response.status_code} - {response.text}")

def get_val_proof_batches() -> list:
    response = requests.get(f"{batch_reciever_url}/validated")
    if response.status_code == 200:
        return response.json()["validated_batches"]
    raise Exception(f"Error: {response.status_code} - {response.text}")

def create_correct_batch(pb: ProofBatch, n: int = 10000) -> ProofBatch:
    return ProofBatch(**{
        'public_key': pb.public_key,
        'block_hash': pb.block_hash,
        'block_height': pb.block_height,
        'nonces': [pb.nonces[0]] * n,
        'dist': [pb.dist[0]] * n
    })

def get_incorrect_nonce(pb: ProofBatch) -> int:
    for i in range(1000):
        if i not in pb.nonces:
            return i
    return None

def create_incorrect_batch(pb: ProofBatch, n: int, n_invalid: int) -> ProofBatch:
    incorrect_pb_dict = {
        'public_key': pb.public_key,
        'block_hash': pb.block_hash,
        'block_height': pb.block_height,
        'nonces': [get_incorrect_nonce(pb)] * n_invalid,
        'dist': [pb.dist[0]] * n_invalid
    }
    return ProofBatch.merge([
        create_correct_batch(pb, n-n_invalid), 
        ProofBatch(**incorrect_pb_dict)
    ])

def run_pow_test():
    R_ESTIMATE = estimate_R_from_experiment(n=8192, P=0.001, num_samples=50000)
    R_TARGET = R_ESTIMATE

    params = Params(
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

    fraud_threshold = 0.01
    
    client.init_generate(
        url=batch_reciever_url,
        block_hash=BLOCK_HASH,
        block_height=1,
        public_key=PUBLIC_KEY,
        batch_size=BATCH_SIZE,
        r_target=R_TARGET,
        fraud_threshold=fraud_threshold,
        params=params,
    )

    sleep(110)
    proof_batches = get_proof_batches()
    pb = ProofBatch(**proof_batches[-1])

    incorrect_pb = create_incorrect_batch(pb, 2000, 10)
    correct_pb = create_correct_batch(pb, 2000)

    client.start_validation()

    client.validate(correct_pb)
    sleep(15)
    client.validate(incorrect_pb)
    sleep(15)

    val_proof_batches = get_val_proof_batches()
    
    vpb = ValidatedBatch(**val_proof_batches[-2])
    print(f"Valid batch: size={len(vpb)}, invalid={vpb.n_invalid}, p_honest={vpb.probability_honest:.2e}, threshold={vpb.fraud_threshold:.2e}, fraud={vpb.fraud_detected}")

    vpb = ValidatedBatch(**val_proof_batches[-1])
    print(f"Invalid batch: size={len(vpb)}, invalid={vpb.n_invalid}, p_honest={vpb.probability_honest:.2e}, threshold={vpb.fraud_threshold:.2e}, fraud={vpb.fraud_detected}")
    
    client.stop()

if __name__ == "__main__":
    run_pow_test()
