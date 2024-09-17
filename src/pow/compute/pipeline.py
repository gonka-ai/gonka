from typing import Tuple
from abc import ABC
from pow.compute.utils import meets_required_zeros
from pow.compute.compute import BaseCompute


class Pipeline(ABC):
    def __init__(
        self,
        public_key: str,
        compute: BaseCompute,
        target_leading_zeros: int,
    ):
        self.public_key = public_key
        self.compute = compute
        self.nonce = 0
        self.target_leading_zeros = target_leading_zeros

    def iter(self) -> Tuple[bytes, int]:
        self.nonce = 0
        while True:
            self.nonce = self.get_next_nonce()
            hash_ = self.compute(self.nonce)
            if meets_required_zeros(hash_, self.target_leading_zeros):
                return (hash_, self.nonce)


    def get_next_nonce(self):
        return self.nonce + 1
