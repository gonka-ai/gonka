
def meets_required_zeros(
    bytes: bytes,
    min_leading_zeros: int
) -> bool:
    total_bits = len(bytes) * 8
    target = (1 << (total_bits - min_leading_zeros)) - 1
    hash_int = int.from_bytes(bytes, byteorder='big')
    
    return hash_int <= target
