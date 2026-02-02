"""End-to-end integration tests for PoC v2 artifact-based protocol.

Test flow:
1. Deploy small model with PoC enabled
2. Start artifact generation
3. Verify artifacts received via callback
4. Parse and validate artifact vectors
5. Send validation request with received artifacts
6. Verify validation result (honest node = no fraud)
7. Stop and verify no more callbacks
"""
import os
import time
import base64
import hashlib
from datetime import datetime
from typing import List, Dict, Any

import pytest
import requests
import numpy as np

from api.inference.client import InferenceClient
from common.wait import wait_for_server


# Test configuration
GENERATION_WAIT_TIMEOUT = 60  # seconds to wait for artifacts
GENERATION_POLL_INTERVAL = 2  # seconds between polls
STOP_WAIT_TIME = 5  # seconds to wait after stop to verify no callbacks
K_DIM = 12  # expected vector dimensions


@pytest.fixture(scope="session")
def server_url() -> str:
    url = os.getenv("SERVER_URL")
    if not url:
        raise ValueError("SERVER_URL is not set")
    return url


@pytest.fixture(scope="session")
def batch_receiver_url() -> str:
    url = os.getenv("BATCH_RECEIVER_V2_URL")
    if not url:
        raise ValueError("BATCH_RECEIVER_V2_URL is not set")
    return url


@pytest.fixture(scope="session")
def vllm_url(server_url: str) -> str:
    """URL for direct vLLM access (port 5000 compatibility proxy)."""
    import urllib.parse
    hostname = urllib.parse.urlparse(server_url).hostname
    scheme = urllib.parse.urlparse(server_url).scheme
    return f"{scheme}://{hostname}:5000"


@pytest.fixture(scope="session")
def inference_client(server_url: str) -> InferenceClient:
    return InferenceClient(server_url)


@pytest.fixture
def session_ids() -> Dict[str, Any]:
    """Generate unique identifiers for test session."""
    date_str = datetime.now().strftime('%Y-%m-%d_%H-%M-%S')
    return {
        "block_hash": hashlib.sha256(date_str.encode()).hexdigest(),
        "block_height": 12345,
        "public_key": f"test_pub_key_{date_str}",
    }


def decode_artifact_vector(vector_b64: str, k_dim: int = K_DIM) -> np.ndarray:
    """Decode base64 fp16 little-endian vector and validate."""
    data = base64.b64decode(vector_b64)
    vec = np.frombuffer(data, dtype='<f2')  # little-endian float16
    assert len(vec) == k_dim, f"Expected {k_dim} dims, got {len(vec)}"
    assert not np.any(np.isnan(vec)), "Vector contains NaN"
    assert not np.any(np.isinf(vec)), "Vector contains Inf"
    return vec.astype(np.float32)


def encode_vector(vec: np.ndarray) -> str:
    """Encode numpy vector to base64 fp16 little-endian."""
    f16 = vec.astype(np.float16)
    return base64.b64encode(f16.tobytes()).decode('ascii')


def corrupt_vector_large(vector_b64: str, k_dim: int = K_DIM) -> str:
    """Corrupt a vector with large noise (L2 >> 0.02)."""
    vec = decode_artifact_vector(vector_b64, k_dim)
    vec += np.random.normal(0, 0.5, k_dim).astype(np.float32)  # Large noise
    return encode_vector(vec)


def perturb_vector_small(vector_b64: str, k_dim: int = K_DIM, max_l2: float = 1e-3) -> str:
    """Perturb vector with tiny noise (L2 ~ 1e-3, well under threshold)."""
    vec = decode_artifact_vector(vector_b64, k_dim)
    # Random direction, scaled to target L2 norm
    noise = np.random.randn(k_dim).astype(np.float32)
    noise = noise / np.linalg.norm(noise) * max_l2
    vec += noise
    return encode_vector(vec)


def clear_batch_receiver(batch_receiver_url: str):
    """Clear all data from batch receiver."""
    resp = requests.post(f"{batch_receiver_url}/clear")
    resp.raise_for_status()


def get_generated_batches(batch_receiver_url: str) -> List[Dict]:
    """Get all received artifact batches from batch receiver."""
    resp = requests.get(f"{batch_receiver_url}/generated")
    resp.raise_for_status()
    return resp.json().get("batches", [])


def get_all_artifacts(batches: List[Dict]) -> List[Dict]:
    """Extract all artifacts from batches."""
    artifacts = []
    for batch in batches:
        artifacts.extend(batch.get("artifacts", []))
    return artifacts


def wait_for_artifacts(batch_receiver_url: str, min_count: int = 1, timeout: float = GENERATION_WAIT_TIMEOUT) -> List[Dict]:
    """Wait for at least min_count artifacts to be received."""
    start = time.time()
    while time.time() - start < timeout:
        batches = get_generated_batches(batch_receiver_url)
        artifacts = get_all_artifacts(batches)
        if len(artifacts) >= min_count:
            return artifacts
        time.sleep(GENERATION_POLL_INTERVAL)
    
    raise TimeoutError(f"Timeout waiting for {min_count} artifacts (got {len(get_all_artifacts(get_generated_batches(batch_receiver_url)))})")


class TestPoCv2E2E:
    """End-to-end tests for PoC v2 artifact generation and validation."""
    
    def test_full_flow(
        self,
        server_url: str,
        batch_receiver_url: str,
        vllm_url: str,
        inference_client: InferenceClient,
        session_ids: Dict[str, Any],
    ):
        """Full E2E test: deploy, generate, validate artifacts, stop."""
        
        # 1. Setup: Clear batch receiver, stop any running services
        print("Step 1: Setup - clearing batch receiver and stopping services")
        clear_batch_receiver(batch_receiver_url)
        requests.post(f"{server_url}/api/v1/stop")
        
        # 2. Deploy model with PoC enabled
        print("Step 2: Deploying model with PoC enabled")
        model_name = "Qwen/Qwen3-0.6B"
        inference_client.inference_setup(
            model=model_name,
            dtype="bfloat16",
            additional_args=[
                "--max-model-len", "512",
                "--gpu-memory-utilization", "0.8",
            ]
        )
        
        # 3. Wait for vLLM to be ready
        print("Step 3: Waiting for vLLM to be ready")
        wait_for_server(f"{vllm_url}/health", timeout=300)
        wait_for_server(f"{vllm_url}/v1/models", timeout=60)
        
        # 4. Start artifact generation
        print("Step 4: Starting artifact generation")
        init_payload = {
            "block_hash": session_ids["block_hash"],
            "block_height": session_ids["block_height"],
            "public_key": session_ids["public_key"],
            "node_id": 0,
            "node_count": 1,
            "batch_size": 32,
            "params": {
                "model": model_name,
                "seq_len": 256,
                "k_dim": K_DIM,
            },
            "url": batch_receiver_url,
        }
        
        resp = requests.post(f"{server_url}/api/v1/inference/pow/init/generate", json=init_payload)
        resp.raise_for_status()
        init_result = resp.json()
        assert init_result["status"] == "OK", f"Init failed: {init_result}"
        print(f"Init result: {init_result}")
        
        # 5. Wait for artifacts to be received
        print("Step 5: Waiting for artifacts")
        artifacts = wait_for_artifacts(batch_receiver_url, min_count=10, timeout=GENERATION_WAIT_TIMEOUT)
        print(f"Received {len(artifacts)} artifacts")
        
        # 6. Stop generation
        print("Step 6: Stopping generation")
        resp = requests.post(f"{server_url}/api/v1/inference/pow/stop")
        resp.raise_for_status()
        
        # 7. Parse and validate artifacts
        print("Step 7: Parsing and validating artifacts")
        assert len(artifacts) > 1, f"Expected more than 1 artifact, got {len(artifacts)}"
        
        validated_artifacts = []
        nonces_seen = set()
        
        for artifact in artifacts:
            nonce = artifact["nonce"]
            vector_b64 = artifact["vector_b64"]
            
            # Decode and validate vector
            vec = decode_artifact_vector(vector_b64, K_DIM)
            assert vec.shape == (K_DIM,), f"Wrong shape: {vec.shape}"
            
            # Track nonces (should be unique)
            assert nonce not in nonces_seen, f"Duplicate nonce: {nonce}"
            nonces_seen.add(nonce)
            
            validated_artifacts.append({
                "nonce": nonce,
                "vector_b64": vector_b64,
            })
        
        print(f"Validated {len(validated_artifacts)} artifacts with unique nonces")
        
        # 8. Send validation request (verify our own artifacts - should be honest)
        print("Step 8: Sending validation request")
        # Take a subset for validation
        validation_subset = validated_artifacts[:min(50, len(validated_artifacts))]
        nonces_to_validate = [a["nonce"] for a in validation_subset]
        
        generate_payload = {
            "block_hash": session_ids["block_hash"],
            "block_height": session_ids["block_height"],
            "public_key": session_ids["public_key"],
            "node_id": 0,
            "node_count": 1,
            "nonces": nonces_to_validate,
            "params": {
                "model": model_name,
                "seq_len": 256,
                "k_dim": K_DIM,
            },
            "batch_size": 32,
            "wait": True,
            "validation": {
                "artifacts": validation_subset,
            },
            "stat_test": {
                "dist_threshold": 0.02,
                "p_mismatch": 0.001,
                "fraud_threshold": 0.01,
            },
        }
        
        resp = requests.post(f"{server_url}/api/v1/inference/pow/generate", json=generate_payload)
        resp.raise_for_status()
        validation_result = resp.json()
        print(f"Validation result: {validation_result}")
        
        # 9. Check validation result - honest node should have n_mismatch=0
        print("Step 9: Checking validation result")
        assert validation_result["status"] == "completed", f"Validation not completed: {validation_result}"
        assert "n_mismatch" in validation_result, f"Missing n_mismatch: {validation_result}"
        assert "fraud_detected" in validation_result, f"Missing fraud_detected: {validation_result}"
        
        # For honest node revalidating own artifacts, expect n_mismatch=0
        n_mismatch = validation_result["n_mismatch"]
        fraud_detected = validation_result["fraud_detected"]
        print(f"n_mismatch={n_mismatch}, fraud_detected={fraud_detected}")
        
        # Allow small number of mismatches due to hardware variance
        assert n_mismatch <= 2, f"Too many mismatches for honest node: {n_mismatch}"
        assert fraud_detected == False, f"Fraud detected on honest node: {validation_result}"
        
        # 10. Verify no more callbacks after stop
        print("Step 10: Verifying no more callbacks after stop")
        count_before = len(get_all_artifacts(get_generated_batches(batch_receiver_url)))
        time.sleep(STOP_WAIT_TIME)
        count_after = len(get_all_artifacts(get_generated_batches(batch_receiver_url)))
        
        # Allow for in-flight callbacks that were sent before stop
        new_callbacks = count_after - count_before
        assert new_callbacks <= 1, f"Too many callbacks after stop: {new_callbacks}"
        print(f"Callbacks after stop: {new_callbacks} (expected 0-1)")
        
        # 11. Cleanup
        print("Step 11: Cleanup")
        inference_client.inference_down()
        
        print("E2E test completed successfully!")
    
    def test_status_reflects_state(
        self,
        server_url: str,
        batch_receiver_url: str,
        vllm_url: str,
        inference_client: InferenceClient,
        session_ids: Dict[str, Any],
    ):
        """Test that /status endpoint reflects correct state transitions."""
        
        # Setup
        clear_batch_receiver(batch_receiver_url)
        requests.post(f"{server_url}/api/v1/stop")
        
        # Deploy model
        model_name = "Qwen/Qwen3-0.6B"
        inference_client.inference_setup(
            model=model_name,
            dtype="bfloat16",
            additional_args=[
                "--max-model-len", "512",
                "--gpu-memory-utilization", "0.8",
            ]
        )
        wait_for_server(f"{vllm_url}/health", timeout=300)
        
        # Check initial status (should be IDLE or NO_BACKENDS initially)
        resp = requests.get(f"{server_url}/api/v1/inference/pow/status")
        resp.raise_for_status()
        status = resp.json()
        print(f"Initial status: {status}")
        
        # Start generation
        init_payload = {
            "block_hash": session_ids["block_hash"],
            "block_height": session_ids["block_height"],
            "public_key": session_ids["public_key"],
            "node_id": 0,
            "node_count": 1,
            "batch_size": 32,
            "params": {"model": model_name, "seq_len": 256, "k_dim": K_DIM},
        }
        
        resp = requests.post(f"{server_url}/api/v1/inference/pow/init/generate", json=init_payload)
        resp.raise_for_status()
        
        # Wait a moment for generation to start
        time.sleep(3)
        
        # Check status during generation
        resp = requests.get(f"{server_url}/api/v1/inference/pow/status")
        resp.raise_for_status()
        status = resp.json()
        print(f"Status during generation: {status}")
        assert status["status"] in ["GENERATING", "MIXED"], f"Expected GENERATING, got: {status}"
        
        # Stop generation
        resp = requests.post(f"{server_url}/api/v1/inference/pow/stop")
        resp.raise_for_status()
        
        # Wait for stop to take effect
        time.sleep(2)
        
        # Check status after stop
        resp = requests.get(f"{server_url}/api/v1/inference/pow/status")
        resp.raise_for_status()
        status = resp.json()
        print(f"Status after stop: {status}")
        assert status["status"] in ["IDLE", "STOPPED", "MIXED"], f"Expected IDLE/STOPPED, got: {status}"
        
        # Cleanup
        inference_client.inference_down()


class TestPoCv2FraudDetection:
    """Fraud detection tests for PoC v2 validation."""
    
    @pytest.fixture(scope="class")
    def model_and_artifacts(
        self,
        server_url: str,
        batch_receiver_url: str,
        vllm_url: str,
        inference_client: InferenceClient,
    ):
        """Setup model and generate artifacts once for all fraud tests."""
        # Setup
        clear_batch_receiver(batch_receiver_url)
        requests.post(f"{server_url}/api/v1/stop")
        
        # Generate unique session ids for this test class
        date_str = datetime.now().strftime('%Y-%m-%d_%H-%M-%S-%f')
        session_ids = {
            "block_hash": hashlib.sha256(date_str.encode()).hexdigest(),
            "block_height": 99999,
            "public_key": f"fraud_test_pubkey_{date_str}",
        }
        
        # Deploy model
        model_name = "Qwen/Qwen3-0.6B"
        inference_client.inference_setup(
            model=model_name,
            dtype="bfloat16",
            additional_args=[
                "--max-model-len", "512",
                "--gpu-memory-utilization", "0.8",
            ]
        )
        wait_for_server(f"{vllm_url}/health", timeout=300)
        wait_for_server(f"{vllm_url}/v1/models", timeout=60)
        
        # Start generation
        init_payload = {
            "block_hash": session_ids["block_hash"],
            "block_height": session_ids["block_height"],
            "public_key": session_ids["public_key"],
            "node_id": 0,
            "node_count": 1,
            "batch_size": 32,
            "params": {"model": model_name, "seq_len": 256, "k_dim": K_DIM},
            "url": batch_receiver_url,
        }
        resp = requests.post(f"{server_url}/api/v1/inference/pow/init/generate", json=init_payload)
        resp.raise_for_status()
        
        # Wait for artifacts
        artifacts = wait_for_artifacts(batch_receiver_url, min_count=60, timeout=GENERATION_WAIT_TIMEOUT)
        
        # Stop generation
        requests.post(f"{server_url}/api/v1/inference/pow/stop")
        
        # Return context for tests
        yield {
            "server_url": server_url,
            "model_name": model_name,
            "session_ids": session_ids,
            "artifacts": artifacts[:50],  # Use first 50 for validation
        }
        
        # Cleanup
        inference_client.inference_down()
    
    def test_fraud_wrong_pubkey(self, model_and_artifacts: Dict[str, Any]):
        """Test: same artifacts but different public_key should detect fraud."""
        ctx = model_and_artifacts
        server_url = ctx["server_url"]
        model_name = ctx["model_name"]
        session_ids = ctx["session_ids"]
        artifacts = ctx["artifacts"]
        
        # Prepare validation with WRONG public_key
        wrong_pubkey = "WRONG_PUBKEY_FOR_FRAUD_TEST"
        nonces = [a["nonce"] for a in artifacts]
        validation_artifacts = [{"nonce": a["nonce"], "vector_b64": a["vector_b64"]} for a in artifacts]
        
        payload = {
            "block_hash": session_ids["block_hash"],
            "block_height": session_ids["block_height"],
            "public_key": wrong_pubkey,  # Different from original!
            "node_id": 0,
            "node_count": 1,
            "nonces": nonces,
            "params": {"model": model_name, "seq_len": 256, "k_dim": K_DIM},
            "batch_size": 32,
            "wait": True,
            "validation": {"artifacts": validation_artifacts},
            "stat_test": {"dist_threshold": 0.02, "p_mismatch": 0.001, "fraud_threshold": 0.01},
        }
        
        resp = requests.post(f"{server_url}/api/v1/inference/pow/generate", json=payload)
        resp.raise_for_status()
        result = resp.json()
        
        print(f"Wrong pubkey result: {result}")
        
        # Expect fraud detection (vectors computed with different seed)
        assert result["status"] == "completed"
        assert result["n_mismatch"] > 0, f"Expected mismatches with wrong pubkey: {result}"
        assert result["fraud_detected"] == True, f"Expected fraud_detected=True: {result}"
    
    def test_fraud_wrong_nonces(self, model_and_artifacts: Dict[str, Any]):
        """Test: artifacts with mismatched nonces should return 400."""
        ctx = model_and_artifacts
        server_url = ctx["server_url"]
        model_name = ctx["model_name"]
        session_ids = ctx["session_ids"]
        artifacts = ctx["artifacts"]
        
        # Use artifacts but provide DIFFERENT nonces list
        wrong_nonces = [999990 + i for i in range(len(artifacts))]  # Completely different nonces
        validation_artifacts = [{"nonce": a["nonce"], "vector_b64": a["vector_b64"]} for a in artifacts]
        
        payload = {
            "block_hash": session_ids["block_hash"],
            "block_height": session_ids["block_height"],
            "public_key": session_ids["public_key"],
            "node_id": 0,
            "node_count": 1,
            "nonces": wrong_nonces,  # Mismatched!
            "params": {"model": model_name, "seq_len": 256, "k_dim": K_DIM},
            "batch_size": 32,
            "wait": True,
            "validation": {"artifacts": validation_artifacts},
            "stat_test": {"dist_threshold": 0.02, "p_mismatch": 0.001, "fraud_threshold": 0.01},
        }
        
        resp = requests.post(f"{server_url}/api/v1/inference/pow/generate", json=payload)
        
        print(f"Wrong nonces response: status={resp.status_code}, body={resp.text[:500]}")
        
        # Expect 400 Bad Request (nonces must match artifacts)
        assert resp.status_code == 400, f"Expected 400 for mismatched nonces, got {resp.status_code}"
    
    def test_fraud_modified_vectors(self, model_and_artifacts: Dict[str, Any]):
        """Test: 20% corrupted vectors should detect fraud."""
        ctx = model_and_artifacts
        server_url = ctx["server_url"]
        model_name = ctx["model_name"]
        session_ids = ctx["session_ids"]
        artifacts = ctx["artifacts"]
        
        # Corrupt 20% of vectors with large noise
        np.random.seed(42)  # Reproducible
        n_to_corrupt = max(1, len(artifacts) // 5)  # 20%
        corrupt_indices = set(np.random.choice(len(artifacts), n_to_corrupt, replace=False))
        
        validation_artifacts = []
        for i, a in enumerate(artifacts):
            if i in corrupt_indices:
                # Corrupt this vector
                corrupted_b64 = corrupt_vector_large(a["vector_b64"], K_DIM)
                validation_artifacts.append({"nonce": a["nonce"], "vector_b64": corrupted_b64})
            else:
                validation_artifacts.append({"nonce": a["nonce"], "vector_b64": a["vector_b64"]})
        
        nonces = [a["nonce"] for a in artifacts]
        
        payload = {
            "block_hash": session_ids["block_hash"],
            "block_height": session_ids["block_height"],
            "public_key": session_ids["public_key"],
            "node_id": 0,
            "node_count": 1,
            "nonces": nonces,
            "params": {"model": model_name, "seq_len": 256, "k_dim": K_DIM},
            "batch_size": 32,
            "wait": True,
            "validation": {"artifacts": validation_artifacts},
            "stat_test": {"dist_threshold": 0.02, "p_mismatch": 0.001, "fraud_threshold": 0.01},
        }
        
        resp = requests.post(f"{server_url}/api/v1/inference/pow/generate", json=payload)
        resp.raise_for_status()
        result = resp.json()
        
        print(f"Modified vectors result: {result}")
        print(f"Corrupted {n_to_corrupt} vectors out of {len(artifacts)}")
        
        # Expect fraud detection
        assert result["status"] == "completed"
        assert result["n_mismatch"] >= n_to_corrupt - 2, f"Expected ~{n_to_corrupt} mismatches: {result}"
        assert result["fraud_detected"] == True, f"Expected fraud_detected=True: {result}"
    
    def test_small_perturbation_passes(self, model_and_artifacts: Dict[str, Any]):
        """Test: tiny L2 perturbation (~1e-3) should still pass as honest."""
        ctx = model_and_artifacts
        server_url = ctx["server_url"]
        model_name = ctx["model_name"]
        session_ids = ctx["session_ids"]
        artifacts = ctx["artifacts"]
        
        # Perturb ALL vectors with tiny noise (L2 ~ 1e-3, well under 0.02 threshold)
        np.random.seed(123)  # Reproducible
        validation_artifacts = []
        for a in artifacts:
            perturbed_b64 = perturb_vector_small(a["vector_b64"], K_DIM, max_l2=1e-3)
            validation_artifacts.append({"nonce": a["nonce"], "vector_b64": perturbed_b64})
        
        nonces = [a["nonce"] for a in artifacts]
        
        payload = {
            "block_hash": session_ids["block_hash"],
            "block_height": session_ids["block_height"],
            "public_key": session_ids["public_key"],
            "node_id": 0,
            "node_count": 1,
            "nonces": nonces,
            "params": {"model": model_name, "seq_len": 256, "k_dim": K_DIM},
            "batch_size": 32,
            "wait": True,
            "validation": {"artifacts": validation_artifacts},
            "stat_test": {"dist_threshold": 0.02, "p_mismatch": 0.001, "fraud_threshold": 0.01},
        }
        
        resp = requests.post(f"{server_url}/api/v1/inference/pow/generate", json=payload)
        resp.raise_for_status()
        result = resp.json()
        
        print(f"Small perturbation result: {result}")
        
        # Expect to pass (within tolerance)
        assert result["status"] == "completed"
        assert result["n_mismatch"] == 0, f"Expected 0 mismatches for small perturbation: {result}"
        assert result["fraud_detected"] == False, f"Expected fraud_detected=False: {result}"
