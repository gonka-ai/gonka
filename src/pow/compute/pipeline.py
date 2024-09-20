import datetime
from tqdm import tqdm
from typing import Tuple
from abc import ABC
from pow.compute.utils import meets_required_zeros
from pow.compute.compute import Compute


class Pipeline(ABC):
    def __init__(
        self,
        public_key: str,
        compute: Compute,
        target_leading_zeros: int,
    ):
        self.public_key = public_key
        self.compute = compute
        self.nonce = 0
        self.target_leading_zeros = target_leading_zeros
        self.proof = []

    def race(self, race_duration) -> Tuple[bytes, int]:
        self.nonce = 0
        start_time = datetime.datetime.now()
        time_passed = datetime.datetime.now() - start_time
        with tqdm(total=race_duration, desc='Time passed') as pbar:
            while time_passed.seconds < race_duration:
                self.nonce = self.get_next_nonce()
                hash_ = self.compute(self.nonce)
                if meets_required_zeros(hash_, self.target_leading_zeros):
                    self.proof.append((hash_, self.nonce))
                time_passed = datetime.datetime.now() - start_time
                pbar.update(time_passed.seconds - pbar.n)
        

    def get_next_nonce(self):
        return self.nonce + 1
