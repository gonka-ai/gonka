from typing import List, Tuple
from hashlib import sha256


N_SLOTS = 64


def get_weights() -> List[Tuple[str, int]]:
    return [
        ("node1", 100),
        ("node2", 200),
        ("node3", 300),
    ]


def _slot_random_val(
    app_hash: str,
    host_address: str,
    slot_idx: int,
    total_weight: int,
) -> int:
    """Generate deterministic random value for a slot index."""
    seed_data = f"{app_hash}{host_address}{slot_idx}".encode()
    hash_bytes = sha256(seed_data).digest()
    return int.from_bytes(hash_bytes[:8], "big") % total_weight


def _find_slot_address(
    random_val: int,
    all_weights: List[Tuple[str, int]],
) -> str:
    """Find which address a random value maps to (linear search)."""
    cumulative = 0
    for address, weight in all_weights:
        cumulative += weight
        if random_val < cumulative:
            return address
    return all_weights[-1][0]  # fallback to last


def get_slot(
    app_hash: str,
    host_address: str,
    all_weights: List[Tuple[str, int]],
    slot_idx: int,
) -> str:
    """
    Get a single slot by index. O(n_weights).
    Use this for incremental slot fetching.
    """
    total_weight = sum(w for _, w in all_weights)
    if total_weight == 0:
        return None
    random_val = _slot_random_val(app_hash, host_address, slot_idx, total_weight)
    return _find_slot_address(random_val, all_weights)


def get_slots(
    app_hash: str,
    host_address: str,
    all_weights: List[Tuple[str, int]],
    n_slots: int,
    start_idx: int = 0,
) -> List[str]:
    """
    Sample n_slots nodes based on weight distribution.
    
    Weight ranges:
        [0, 99]     => node1 (weight 100)
        [100, 299]  => node2 (weight 200)
        [300, 599]  => node3 (weight 300)
    
    Args:
        start_idx: Starting slot index (default 0)
        n_slots: Number of slots to return
    
    Returns slots for indices [start_idx, start_idx + n_slots)
    
    Complexity: O(n_slots log n_slots + n_weights)
    """
    total_weight = sum(w for _, w in all_weights)
    if total_weight == 0:
        return []

    randoms = []
    for i in range(n_slots):
        slot_idx = start_idx + i
        random_val = _slot_random_val(app_hash, host_address, slot_idx, total_weight)
        randoms.append((random_val, i))

    randoms.sort()
    result = [None] * n_slots
    cumulative = 0
    rand_idx = 0

    for address, weight in all_weights:
        cumulative += weight
        while rand_idx < len(randoms) and randoms[rand_idx][0] < cumulative:
            _, orig_idx = randoms[rand_idx]
            result[orig_idx] = address
            rand_idx += 1

    return result

def get_vote_from(
    host: str,
) -> bool|None:
    # just example for demo, not relevant to real vote
    return True


def validate_host(
    app_hash: str,
    host: str,
) -> bool:
    prev_weights = get_weights()
    slots = get_slots(app_hash, host, prev_weights, N_SLOTS)
    validator_votes = {}
    for slot in slots:
        if slot not in validator_votes:
            validator_votes[slot] = get_vote_from(slot)

    voted_yes = 0
    voted_no = 0
    for validator in slots:
        if validator_votes[validator]:
            voted_yes += 1
        else:
            voted_no += 1
    if voted_yes > N_SLOTS / 2:
        return True
    elif voted_no > N_SLOTS / 2:
        return False
    
    # Fallback: fetch one slot at a time until consensus
    slot_idx = N_SLOTS
    total_weight = sum(w for _, w in prev_weights)
    while slot_idx < total_weight:
        next_slot = get_slot(app_hash, host, prev_weights, slot_idx)
        if next_slot not in validator_votes:
            validator_votes[next_slot] = get_vote_from(next_slot)
        if validator_votes[next_slot]:
            voted_yes += 1
        else:
            voted_no += 1
        slot_idx += 1
        
        if voted_yes > slot_idx / 2:
            return True
        elif voted_no > slot_idx / 2:
            return False
    
    return None  # no consensus reached

if __name__ == "__main__":
    app_hash = "1234567890"
    host_address = "gonka100"
    n_slots = 64
    all_weights = get_weights()
    slots = get_slots(app_hash, host_address, all_weights, n_slots)
    print(slots)