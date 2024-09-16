import pytest
import numpy as np

from pow.compute.mvp import (
    attention,
    generate_input_matrix,
    perform_inference,
    hash_with_leading_zeros,
    simulate_node_work,
)
from pow.compute.utils import (
    meets_required_zeros,
)


def test_sample():
    assert 1 + 1 == 2

def test_attention():
    Q = np.array([[1, 0], [0, 1]])
    K = np.array([[1, 0], [0, 1]])
    V = np.array([[1, 2], [3, 4]])
    dk = 2
    result = attention(Q, K, V, dk)
    expected = np.array([[1.66, 2.66], [2.33, 3.33]])
    np.testing.assert_array_almost_equal(result, expected, decimal=2)


def test_generate_input_matrix():
    public_key = "test_key"
    salt = 0
    size = 2
    d = 2
    result = generate_input_matrix(public_key, salt, size, d)
    assert result.shape == (2, 2)

def test_perform_inference():
    Q = np.array([[1, 0], [0, 1]])
    K = np.array([[1, 0], [0, 1]])
    V = np.array([[1, 2], [3, 4]])
    result_hash, R = perform_inference(Q, K, V)
    assert len(result_hash) == 64  # SHA-256 hash length
    assert R.shape == (2, 2)

def test_hash_with_leading_zeros():
    result_hash = "0000abcd"
    difficulty = 4
    assert hash_with_leading_zeros(result_hash, difficulty)

def test_simulate_node_work():
    node_id = "node_1"
    public_key = "test_key"
    matrix_size = 2
    d = 2
    difficulty = 1
    work_time = 1
    node_id, result_hash, salt_list, end_time = simulate_node_work(node_id, public_key, matrix_size, d, difficulty, work_time)
    assert len(result_hash) == 64
    assert isinstance(salt_list, list)


def test_meets_required_zeros():
    assert meets_required_zeros(b'\x00\x00\x00\x00', 32) == True
    assert meets_required_zeros(b'\x00\x00\x00\x01', 24) == True
    assert meets_required_zeros(b'\x00\x00\x00\x7F', 26) == False
    assert meets_required_zeros(b'\x00\x00\x00\x00\x00\x00\x00\xff', 56) == True
    assert meets_required_zeros(b'\x00\x00\x00\x00\x00\x00\x00\xff', 57) == False
