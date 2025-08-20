import pytest
import torch
from typing import List
from pow.compute.compute import Compute
from pow.models.utils import Params
from pow.data import (
    ProofBatch,
    ValidatedBatch,
    PROBABILITY_MISMATCH,
)
from pow.compute.controller import Controller, ParallelController
from pow.compute.utils import Phase
from pow.compute.gpu_group import GpuGroup
import multiprocessing as mp
import time
from itertools import count
R_ESTIMATED = 1.39635417620795


def test_gpu_operation():
    device = torch.device('cuda' if torch.cuda.is_available() else 'cpu')
    
    x = torch.tensor([1.0, 2.0, 3.0]).to(device)
    y = torch.tensor([4.0, 5.0, 6.0]).to(device)
    
    result = x + y
    
    expected = torch.tensor([5.0, 7.0, 9.0])
    torch.testing.assert_close(result.cpu(), expected)



@pytest.mark.parametrize(
    "block_hash, block_height, public_key, devices, node_id",
    [
        ("0x00", 0, "0x00", ["cuda:0"], 0),
    ],
)
def test_compute_simple(
    block_hash: str,
    block_height: int,
    public_key: str,
    devices: List[str],
    node_id: int,
):
    compute = Compute(
        params=Params(
            dim=128,
            vocab_size=128,
            n_layers=4,  # Small number of layers for testing
            n_heads=4,
            n_kv_heads=4,
            seq_len=16,  # Small sequence length
            use_scaled_rope=False,  # Disable scaled RoPE to avoid meta tensor issues
        ),
        block_hash=block_hash,
        block_height=block_height,
        public_key=public_key,
        r_target=R_ESTIMATED,
        devices=devices,
        node_id=node_id,
    )

    nonces = list(range(100))  # Reasonable batch size for small test model
    proof_batch: ProofBatch = compute(
        nonces=nonces,
        public_key=compute.public_key,
        target=compute.target,
    ).result().sort_by_nonce()
    proof_batch_val: ProofBatch = compute.validate(
        proof_batch=proof_batch,
    ).sort_by_nonce()


    assert proof_batch.nonces == proof_batch_val.nonces
    close_dists = [
        d for d, rd in zip(proof_batch.dist, proof_batch_val.dist)
        if abs(d - rd) < 0.001
    ]
    assert len(close_dists) == len(proof_batch.nonces)


@pytest.mark.parametrize(
    "block_hash, block_height, public_key, devices, node_id",
    [
        ("0x00", 0, "0x00", ["cuda:0"], 0),
    ],
)
def test_controller(
    block_hash: str,
    block_height: int,
    public_key: str,
    devices: List[str],
    node_id: int,
):
    ctx = mp.get_context("spawn")

    generated_batch_queue = ctx.Queue()
    to_validate_batch_queue = ctx.Queue()
    validated_batch_queue = ctx.Queue()
    phase = ctx.Value('i', Phase.IDLE)

    # Convert device strings to GpuGroup
    device_ids = [int(device.split(':')[1]) if ':' in device else 0 for device in devices]
    gpu_group = GpuGroup(device_ids)
    
    controller = Controller(
        idx=0,
        params=Params(
            dim=128,
            vocab_size=128,
            n_layers=4,  # Small number of layers for testing
            n_heads=4,
            n_kv_heads=4,
            seq_len=16,  # Small sequence length
            use_scaled_rope=False,  # Disable scaled RoPE to avoid meta tensor issues
        ),
        block_hash=block_hash,
        block_height=block_height,
        public_key=public_key,
        batch_size=100,
        r_target=R_ESTIMATED,
        gpu_group=gpu_group,
        iterator=count(0, 10),
        phase=phase,
        generated_batch_queue=generated_batch_queue,
        validated_batch_queue=validated_batch_queue,
        to_validate_batch_queue=to_validate_batch_queue,
        node_id=node_id,
    )

    assert generated_batch_queue.empty()
    assert to_validate_batch_queue.empty()
    assert validated_batch_queue.empty()

    controller.start()

    while not controller.is_model_initialized():
        time.sleep(0.1)

    print("Phase set to IDLE")
    controller.phase.value = Phase.GENERATE
    print("Phase set to GENERATE")

    while generated_batch_queue.empty():
        print("Generated batch queue is empty")
        time.sleep(0.1)

    print("Generated batch queue is not empty")
    batch = generated_batch_queue.get()
    assert isinstance(batch, ProofBatch)

    controller.phase.value = Phase.VALIDATE
    print("Phase set to VALIDATE")

    to_validate_batch_queue.put(batch)

    while not validated_batch_queue.empty():
        print("Validated batch queue is empty")
        time.sleep(0.1)

    print("Validated batch queue is not empty")
    validated_batch = validated_batch_queue.get()
    assert isinstance(validated_batch, ProofBatch)

    assert validated_batch.nonces == batch.nonces
    close_dists = [
        d for d, rd in zip(validated_batch.dist, batch.dist)
        if abs(d - rd) < 0.001
    ]
    assert len(close_dists) == len(batch.nonces)
    
    controller.stop()



@pytest.mark.parametrize(
    "block_hash, block_height, public_key",
    [
        ("0x00", 0, "0x00"),
    ],
)
def test_parallel_controller(
    block_hash: str,
    block_height: int,
    public_key: str,
):
    device_count = torch.cuda.device_count()
    print(f"Devices: {device_count}")
    devices = [f"cuda:{i}" for i in range(device_count)]

    controller = ParallelController(
        params=Params(
            dim=128,
            vocab_size=128,
            n_layers=4,  # Small number of layers for testing
            n_heads=4,
            n_kv_heads=4,
            seq_len=16,  # Small sequence length
            use_scaled_rope=False,  # Disable scaled RoPE to avoid meta tensor issues
        ),
        block_hash=block_hash,
        block_height=block_height,
        public_key=public_key,
        node_id=0,
        node_count=1,
        batch_size=100,
        r_target=R_ESTIMATED,
        devices=devices,
    )

    controller.start()

    while not controller.is_model_initialized():
        time.sleep(0.1)

    controller.start_generate()

    while controller.generated_batch_queue.empty():
        time.sleep(0.1)

    batch = controller.generated_batch_queue.get()
    assert isinstance(batch, ProofBatch)

    controller.start_validate()

    controller.to_validate(batch)

    while controller.validated_batch_queue.empty():
        time.sleep(0.1)

    validated_batch = controller.validated_batch_queue.get()
    assert isinstance(validated_batch, ProofBatch)

    assert validated_batch.nonces == batch.nonces
    close_dists = [
        d for d, rd in zip(validated_batch.dist, batch.dist)
        if abs(d - rd) < 0.001
    ]
    assert len(close_dists) == len(batch.nonces)

    controller.stop()
