import hashlib
import numpy as np
import torch
from abc import (
    ABC,
    abstractmethod,
)
from typing import Callable



class BaseCompute(ABC):
    def __init__(
        self,
        public_key: str,
    ):
        self.public_key = public_key
        self.compute_function = self.get_compute_function()


    def __call__(self, nonce: int) -> bytes:
        input_tensor = self.get_input_tensor(nonce)
        output_tensor = self.model(input_tensor)
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


    @abstractmethod
    def get_compute_function(self) -> Callable[[torch.Tensor], torch.Tensor]:
        pass


class AttentionModel(torch.nn.Module):
    def __init__(self):
        super(AttentionModel, self).__init__()
        np.random.seed(42)
        torch.manual_seed(42)
        self.K = torch.rand((10, 10), dtype=torch.float32)
        self.V = torch.rand((10, 10), dtype=torch.float32)

    def forward(self, Q: torch.Tensor) -> torch.Tensor:
        scores = torch.matmul(Q, self.K.transpose(-2, -1)) / torch.sqrt(torch.tensor(Q.shape[1], dtype=torch.float32))
        weights = torch.nn.functional.softmax(scores, dim=-1)
        R = torch.matmul(weights, self.V)
        return R


class Compute(BaseCompute):
    def __init__(self, public_key: str):
        super().__init__(public_key)
        self.model = self.get_compute_function()

    def get_input_tensor(self, nonce: int) -> torch.Tensor:
        seed = (hash(self.public_key) + nonce) % (2**32)
        np.random.seed(seed)
        Q = np.random.rand(10, 10)
        return torch.tensor(Q, dtype=torch.float32)

    def get_hash(self, tensor: torch.Tensor) -> bytes:
        result_hash = hashlib.sha256(tensor.numpy().tobytes()).digest()
        return result_hash


    def get_compute_function(self) -> Callable[[torch.Tensor], torch.Tensor]:
        return AttentionModel()