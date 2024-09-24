import hashlib
import numpy as np
import torch
import torch.nn as nn
import torch.nn.functional as F
from abc import (
    ABC,
    abstractmethod,
)
from typing import Callable



# class BaseCompute(ABC):
#     def __init__(
#         self,
#         public_key: str,
#         device,
#     ):
#         self.public_key = public_key
#         self.device = device


#     def __call__(self, nonce: int) -> bytes:
#         input_tensor = self.get_input_tensor(nonce)
#         output_tensor = self.model(input_tensor)
#         hash_ = self.get_hash(output_tensor)
#         return hash_


#     @abstractmethod
#     def get_input_tensor(
#         self,
#         nonce: int,
#     ) -> torch.Tensor:
#         pass


#     @abstractmethod
#     def get_hash(self, tensor: torch.Tensor) -> bytes:
#         pass


#     @abstractmethod
#     def get_compute_function(self) -> Callable[[torch.Tensor], torch.Tensor]:
#         pass


class AttentionModel(torch.nn.Module):
    def __init__(self, K, V):
        super(AttentionModel, self).__init__()
        self.K = K
        self.V = V

    def forward(self, Q: torch.Tensor) -> torch.Tensor:
        scores = torch.matmul(Q, self.K.transpose(-2, -1)) / torch.sqrt(torch.tensor(Q.shape[1], dtype=torch.float32))
        weights = F.softmax(scores, dim=-1)
        R = torch.matmul(weights, self.V)
        return R


class Compute:
    def __init__(self, public_key: str, device, hid):
        self.public_key = public_key
        self.device = device
        self.hid = hid
        self.rng = torch.Generator(device=device)
        self.rng.manual_seed(42)
        self.model = self.get_compute_function()
        

    def get_input_tensor(self, nonce: int) -> torch.Tensor:
        seed = (hash(self.public_key) + nonce) % (2**32)
        self.rng.manual_seed(seed)
        Q = torch.randn(size=(self.hid, self.hid), generator=self.rng, device=self.device, dtype=torch.float32)
        return Q

    def get_hash(self, tensor: torch.Tensor) -> bytes:
        if self.device.type == 'cuda':
            values = tensor.cpu().numpy()
        else:
            values = tensor.numpy()
        result_hash = hashlib.sha256(values.tobytes()).digest()
        return result_hash

    def get_compute_function(self) -> Callable[[torch.Tensor], torch.Tensor]:
        self.K = torch.randn(size=(self.hid, self.hid), generator=self.rng, device=self.device, dtype=torch.float32)
        self.V = torch.randn(size=(self.hid, self.hid), generator=self.rng, device=self.device, dtype=torch.float32)
        print(self.K)
        print(self.V)
        return AttentionModel(self.K, self.V).to(self.device)

    def __call__(self, nonce: int) -> bytes:
        input_tensor = self.get_input_tensor(nonce)
        output_tensor = self.model(input_tensor)
        hash_ = self.get_hash(output_tensor)
        return hash_, output_tensor
    
    def forward(self, input_tensor: torch.Tensor) -> torch.Tensor:
        return self.model(input_tensor)
