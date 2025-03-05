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
    for node_id, device_id in itertools.product(range(n_nodes), range(n_devices)):
        iterator = NonceIterator(node_id, n_nodes, device_id, n_devices)
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
        n_devices = random.randint(1, 100)
        for device_id in range(n_devices):
            iterator = NonceIterator(node_id, n_nodes, device_id, n_devices)
            sequences.append(set(itertools.islice(iterator, 100)))

    for sequence in sequences:
        assert len(sequence) == 100

    all_items = set(itertools.chain(*sequences))
    assert len(all_items) == len(sequences) * 100


def test_all_covered():
    n_nodes = 1000
    sequence = set()
    for node_id in range(n_nodes):
        n_devices = random.randint(1, 100)
        for device_id in range(n_devices):
            iterator = iter(NonceIterator(node_id, n_nodes, device_id, n_devices))
            while True:
                nonce = next(iterator)
                if nonce >= 10000000:
                    break
                assert nonce not in sequence
                sequence.add(nonce)

    for i in range(10000000):
        assert i in sequence
