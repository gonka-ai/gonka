import numpy as np
import hashlib
import time

from tqdm.notebook import tqdm


def attention(Q, K, V, dk):
    scores = np.dot(Q, K.T) / np.sqrt(dk)
    weights = np.exp(scores) / np.sum(np.exp(scores), axis=1, keepdims=True)
    return np.dot(weights, V)


def generate_input_matrix(public_key, salt, size, d):
    seed = (hash(public_key) + salt) % (2**32)
    np.random.seed(seed)
    Q = np.random.rand(size, d)
    return Q


def perform_inference(Q, K, V):
    R = attention(Q, K, V, Q.shape[1])
    result_hash = hashlib.sha256(R.tobytes()).hexdigest()
    return result_hash, R


def hash_with_leading_zeros(result_hash, difficulty):
    return result_hash.startswith('0' * difficulty)


def simulate_node_work(node_id, public_key, matrix_size, d, difficulty, work_time):
    salt_list = []
    salt = 0

    seed = (hash(public_key[:4])) % (2**32)
    np.random.seed(seed)
    K = np.random.rand(matrix_size, d)
    V = np.random.rand(matrix_size, d)

    for _ in range(work_time):
        Q = generate_input_matrix(public_key, salt, matrix_size, d)

        result_hash, result = perform_inference(Q, K, V)

        if hash_with_leading_zeros(result_hash, difficulty):
          salt_list.append(salt)

        salt += 1
    return node_id, result_hash, salt_list, time.time()


def simulate_network_computation(num_nodes, matrix_size, d, difficulty, work_time):
    node_results = []

    public_keys = [f"Node_{i}_PublicKey" for i in range(num_nodes)]

    for public_key in tqdm(public_keys):
        start_time = time.time()
        node_id = public_key
        node_id, result_hash, salt_list, end_time = simulate_node_work(node_id, public_key, matrix_size, d, difficulty, work_time)
        computation_time = end_time - start_time

        node_results.append((node_id, result_hash, salt_list, computation_time))

    return node_results
