from pow.compute.pipeline import Pipeline
from pow.compute.compute import Compute


def test_compute_reproducibility():
    public_key = "test_public_key"
    nonce = 42

    compute1 = Compute(public_key)
    hash1 = compute1(nonce)

    compute2 = Compute(public_key)
    hash2 = compute2(nonce)

    assert hash1 == hash2, "Hashes are not equal"


# TODO: do we really need this?
def test_pipeline_reproducibility():
    public_key = "test_public_key"

    compute = Compute(public_key)
    target_leading_zeros = 4

    pipeline1 = Pipeline(public_key, compute, target_leading_zeros)
    result1 = pipeline1.iter()

    pipeline2 = Pipeline(public_key, compute, target_leading_zeros)
    result2 = pipeline2.iter()

    assert result1 == result2, "Results are not equal"