import torch
import hashlib
import numpy as np
from typing import (
    Callable,
    Tuple,
)
from abc import ABC, abstractmethod
from pow.compute.utils import meets_required_zeros



class BaseCompute(ABC):
    def __init__(
        self,
        public_key: str,
    ):
        self.public_key = public_key
        self.compute_function = self.get_compute_function()


    def __call__(self, nonce: int) -> bytes:
        input_tensor = self.get_input_tensor(nonce)
        output_tensor = self.compute_function(input_tensor)
        hash_ = self.get_hash(output_tensor)
        return hash_


    @abstractmethod
    def get_input_tensor(
        self,
        nonce: int,
    ) -> torch.Tensor:
        pass


    @abstractmethod
    def get_hash(self, tensor: torch.Tensor) -> bytes:
        pass


    @staticmethod
    def get_compute_function() -> Callable[[torch.Tensor], torch.Tensor]:
        raise NotImplementedError("Subclasses should implement this!")


class Compute(BaseCompute):
    def __init__(self, public_key: str):
        super().__init__(public_key)

    def get_input_tensor(self, nonce: int) -> torch.Tensor:
        seed = (hash(self.public_key) + nonce) % (2**32)
        np.random.seed(seed)
        Q = np.random.rand(10, 10)
        return torch.tensor(Q, dtype=torch.float32)

    def get_hash(self, tensor: torch.Tensor) -> bytes:
        result_hash = hashlib.sha256(tensor.numpy().tobytes()).digest()
        return result_hash

    @staticmethod
    def get_compute_function() -> Callable[[torch.Tensor], torch.Tensor]:
        def compute_function(input_tensor: torch.Tensor) -> torch.Tensor:
            np.random.seed(42)
            torch.manual_seed(42)
            K = torch.rand((10, 10), dtype=torch.float32)
            V = torch.rand((10, 10), dtype=torch.float32)
            scores = torch.matmul(input_tensor, K.transpose(-2, -1)) / torch.sqrt(torch.tensor(input_tensor.shape[1], dtype=torch.float32))
            weights = torch.nn.functional.softmax(scores, dim=-1)
            R = torch.matmul(weights, V)
            return R
        return compute_function
        


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