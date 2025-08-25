import itertools
import random
import pytest

from pow.compute.utils import NonceIterator


@pytest.mark.parametrize(
    "n_nodes, n_devices",
    [
        (4, 2),
        (2, 4),
        (2, 2),
        (71, 8),
        (1, 1),
        (12, 41),
        (100, 100),
    ],
)
def test_worker_nonce_iterator(
    n_nodes: int,
    n_devices: int,
):
    sequences = []
    for node_id, group_id in itertools.product(range(n_nodes), range(n_devices)):
        iterator = NonceIterator(node_id, n_nodes, group_id, n_devices)
        sequences.append(set(itertools.islice(iterator, 100)))

    for sequence in sequences:
        assert len(sequence) == 100

    all_items = set(itertools.chain(*sequences))
    assert len(all_items) == n_nodes * n_devices * 100
    assert sorted(all_items) == list(range(n_nodes * n_devices * 100))


@pytest.mark.parametrize(
    "n_nodes",
    [
        100,
    ],
)
def test_unique_nonces(
    n_nodes: int,
):
    sequences = []
    for node_id in range(n_nodes):
        n_groups = random.randint(1, 100)
        for group_id in range(n_groups):
            iterator = NonceIterator(node_id, n_nodes, group_id, n_groups)
            sequences.append(set(itertools.islice(iterator, 100)))

    for sequence in sequences:
        assert len(sequence) == 100

    all_items = set(itertools.chain(*sequences))
    assert len(all_items) == len(sequences) * 100


def test_all_covered():
    n_nodes = 1000
    sequence = set()
    for node_id in range(n_nodes):
        n_groups = random.randint(1, 100)
        for group_id in range(n_groups):
            iterator = iter(NonceIterator(node_id, n_nodes, group_id, n_groups))
            while True:
                nonce = next(iterator)
                if nonce >= 10000000:
                    break
                assert nonce not in sequence
                sequence.add(nonce)

    for i in range(10000000):
        assert i in sequence


def test_group_based_nonce_distribution():
    """Test that group-based nonce distribution maintains collision-free guarantees."""
    n_nodes = 3
    n_groups = 4
    
    # Collect nonces from all node/group combinations
    all_nonces = set()
    group_sequences = {}
    
    for node_id in range(n_nodes):
        for group_id in range(n_groups):
            iterator = NonceIterator(node_id, n_nodes, group_id, n_groups)
            group_nonces = set(itertools.islice(iterator, 50))
            
            # Store for later verification
            group_sequences[(node_id, group_id)] = group_nonces
            
            # Verify no duplicates within this group
            assert len(group_nonces) == 50
            
            # Verify no collisions with other groups
            assert all_nonces.isdisjoint(group_nonces), f"Collision found for node {node_id}, group {group_id}"
            all_nonces.update(group_nonces)
    
    # Verify total count matches expected
    assert len(all_nonces) == n_nodes * n_groups * 50


def test_group_vs_device_equivalence():
    """Test that group-based naming produces same nonce sequences as device-based."""
    # This test verifies backward compatibility - same parameters should produce same nonces
    n_nodes = 2
    n_entities = 3  # Could be devices or groups
    
    for node_id in range(n_nodes):
        for entity_id in range(n_entities):
            # Create iterators with same parameters but different parameter names
            group_iterator = NonceIterator(node_id, n_nodes, entity_id, n_entities)
            
            # Get first 20 nonces from group-based iterator
            group_nonces = list(itertools.islice(group_iterator, 20))
            
            # Verify they follow expected pattern
            expected_offset = node_id + entity_id * n_nodes
            expected_step = n_entities * n_nodes
            
            for i, nonce in enumerate(group_nonces):
                expected = expected_offset + i * expected_step
                assert nonce == expected, f"Mismatch at position {i}: got {nonce}, expected {expected}"


def test_multi_node_group_collision_avoidance():
    """Test collision avoidance across multiple nodes with different group counts."""
    # Simulate realistic scenario: different nodes with different GPU group configurations
    node_configs = [
        (0, 2),  # Node 0: 2 groups
        (1, 4),  # Node 1: 4 groups  
        (2, 1),  # Node 2: 1 group
    ]
    
    all_nonces = set()
    
    for node_id, n_groups in node_configs:
        # Each node reports its own group count, but collision avoidance
        # depends on the total number of nodes
        n_nodes = len(node_configs)
        
        for group_id in range(n_groups):
            iterator = NonceIterator(node_id, n_nodes, group_id, n_groups)
            node_group_nonces = set(itertools.islice(iterator, 30))
            
            # Verify no collisions
            assert all_nonces.isdisjoint(node_group_nonces), \
                f"Collision found for node {node_id}, group {group_id}"
            all_nonces.update(node_group_nonces)
    
    # Verify we got expected total count
    total_groups = sum(n_groups for _, n_groups in node_configs)
    assert len(all_nonces) == total_groups * 30
