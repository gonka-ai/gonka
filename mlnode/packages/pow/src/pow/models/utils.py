from dataclasses import dataclass

import torch

from common.logger import create_logger


logger = create_logger(__name__)


@dataclass
class Params:
    dim: int = 2048
    n_layers: int = 16
    n_heads: int = 16
    n_kv_heads: int = 16
    vocab_size: int = 8192
    ffn_dim_multiplier: float = 1.3
    multiple_of: int = 1024
    norm_eps: float = 1e-5
    rope_theta: float = 500000.0
    use_scaled_rope: bool = False

    seq_len: int = 16


PARAMS_V1 = Params(
    dim=1024,
    n_layers=32,
    n_heads=32,
    n_kv_heads=32,
    vocab_size=8196, 
    ffn_dim_multiplier=10.0,
    multiple_of=2048,
    norm_eps=1e-05,
    rope_theta=10000.0,
    use_scaled_rope=False,
    seq_len=128
)

PARAMS_V2 = Params(
    dim=1792,
    n_layers=64,
    n_heads=64,
    n_kv_heads=64,
    vocab_size=8196,
    ffn_dim_multiplier=10.0,
    multiple_of=4*2048,
    norm_eps=1e-5,
    rope_theta=10000.0,
    use_scaled_rope=False,
    seq_len=256,
)


def count_params(
    model: torch.nn.Module,
    print_summary: bool = True
) -> int:
    total_params = sum(p.numel() for p in model.parameters())
    if print_summary:
        logger.info(f"Total number of parameters: {total_params / 1e9:.2f}B")
    return total_params


def set_default_dtype(
    device: str,
    dtype: torch.dtype = torch.float16,
):
    device = torch.device(device)

    if device.type == "cuda" and not torch.cuda.is_available():
        logger.warning("CUDA is not available, using CPU instead")
        return

    if dtype == torch.bfloat16:
        if torch.cuda.is_bf16_supported():
            logger.info("Model is using bfloat16")
            torch.set_default_dtype(torch.bfloat16)
        else:
            logger.warning(
                "bfloat16 is not supported on this device, falling back to float16"
            )
            torch.set_default_dtype(torch.float16)
    elif dtype == torch.float16:
        logger.info("Model is using float16")
        torch.set_default_dtype(torch.float16)
    elif dtype == torch.float32:
        logger.info("Model is using float32")
        torch.set_default_dtype(torch.float32)
    else:
        logger.warning(f"Unsupported dtype {dtype}, falling back to float16")
        torch.set_default_dtype(torch.float16)
