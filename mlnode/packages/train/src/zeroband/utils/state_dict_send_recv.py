import io
import pickle

import torch
from torch.distributed import ProcessGroup
from torch.distributed._tensor.api import DTensor


def _object_to_tensor(obj):
    f = io.BytesIO()
    pickle.Pickler(f).dump(obj)
    byte_storage = torch.ByteStorage._from_buffer(f.getvalue())  # type: ignore[attr-defined]
    # Do not replace `torch.ByteTensor` or `torch.LongTensor` with torch.tensor and specifying dtype.
    # Otherwise, it will casue 100X slowdown.
    # See: https://github.com/pytorch/pytorch/issues/65696
    byte_tensor = torch.ByteTensor(byte_storage)
    local_size = torch.LongTensor([byte_tensor.numel()])
    return byte_tensor, local_size


def _tensor_to_object(tensor, tensor_size):
    tensor = tensor.cpu()
    buf = tensor.numpy().tobytes()[:tensor_size]
    return pickle.Unpickler(io.BytesIO(buf)).load()


def _tensor_to_placeholder(idx: int, tensor: torch.Tensor) -> str:
    return f"zeroband_tensor_{idx}_{tensor.shape}_{tensor.dtype}"


def _validate_placeholder_to_tensor(placeholder: str, tensors: list[torch.Tensor]) -> torch.Tensor:
    """
    validate that the tensor is compatible with the placeholder.
    """
    try:
        idx, shape, dtype = placeholder.split("_")[2:]
    except ValueError as e:
        raise ValueError(f"Invalid tensor placeholder {placeholder}") from e

    tensor = tensors[int(idx)]
    if shape != str(tensor.shape):
        raise ValueError(
            f"tensor {idx} try to load a tensor with shape {shape} but the tensor has shape {tensor.shape}"
        )
    if dtype != str(tensor.dtype):
        raise ValueError(
            f"tensor {idx} try to load a tensor with dtype {dtype} but the tensor has dtype {tensor.dtype}"
        )

    return tensor


def _get_sendable_state_dict(state_dict: dict) -> tuple[dict, list[torch.Tensor]]:
    """
    This function take a state dict (dict with tensor inside) and return a torch.send/recv-able format.

    It splits the state dict into two part :
    * a list of tensor
    * a dict emptied from tensor

    The order is deterministic. The function can be used in pair with  _load_sendable_state_dict
    """
    tensors: list[torch.Tensor] = []

    def _split(state_dict_, tensors_):
        new_dict = {}
        for key, value in state_dict_.items():
            if isinstance(value, dict):
                new_dict[key] = _split(value, tensors_)
            elif isinstance(value, torch.Tensor):
                idx = len(tensors_)
                tensors_.append(value)
                new_dict[key] = _tensor_to_placeholder(idx, value)
            else:
                new_dict[key] = value

        return new_dict

    state_dict = _split(state_dict, tensors)
    return state_dict, tensors


def _load_sendable_state_dict(tensors: list[torch.Tensor], state_dict: dict) -> dict:
    """
    This function take a list of tensor and a state dict and return state dict.

    The function can be used in pair with _get_sendable_state_dict
    """

    def _load(state_dict_):
        for key, value in list(state_dict_.items()):  # list needed as we modify the state_dict_ as we traverse it
            if isinstance(value, dict):
                state_dict_[key] = _load(value)
            elif isinstance(value, str) and value.startswith("zeroband_tensor_"):
                state_dict_[key] = _validate_placeholder_to_tensor(value, tensors)

        return state_dict_

    return _load(state_dict)


def send_state_dict(pg: ProcessGroup, state_dict: dict, dest_rank: int) -> None:
    non_tensored_state_dict, tensors = _get_sendable_state_dict(state_dict)
    send_tensor_and_state_dict(pg, dest_rank, non_tensored_state_dict, tensors)


def send_tensor_and_state_dict(pg: ProcessGroup, dest_rank: int, state_dict: dict, tensors: list[torch.Tensor]) -> None:
    # logger = get_logger()
    # logger.debug(f"recv tensors {get_tensor_list_signature(tensors)}")

    state_dict_tensor_buffer, size = _object_to_tensor(state_dict)
    pg.send([size], dest_rank, 0).wait()
    pg.send([state_dict_tensor_buffer], dest_rank, 0).wait()

    jobs = []
    for i, tensor in enumerate(tensors):
        buffer = tensor
        if isinstance(tensor, DTensor):
            buffer = tensor

        buffer = buffer.detach().cpu()

        jobs.append(pg.send([buffer], dest_rank, i))

    for job in jobs:
        job.wait()


def recv_state_dict(pg: ProcessGroup, src_rank: int, og_state_dict: dict) -> dict:
    size = torch.LongTensor(1)

    # Receive object sizes
    pg.recv([size], src_rank, 0).wait()
    # Tensor to receive serialized objects into.
    object_tensor = torch.empty(size.item(), dtype=torch.uint8)

    pg.recv([object_tensor], src_rank, 0).wait()
    state_dict = _tensor_to_object(object_tensor, size)

    _, tensors = _get_sendable_state_dict(og_state_dict)

    jobs = []
    datas = []
    for i, tensor in enumerate(tensors):
        buffer = tensor
        if isinstance(tensor, DTensor):
            buffer = tensor

        data = torch.empty_like(buffer, device="cpu")
        jobs.append(pg.recv([data], src_rank, i))
        datas.append(data)

    for job in jobs:
        job.wait()

    for tensor, data in zip(tensors, datas):
        if isinstance(tensor, DTensor):
            tensor = tensor
        tensor.copy_(data)

    state_dict = _load_sendable_state_dict(tensors, state_dict)

    # logger = get_logger()
    # logger.debug(f"recv tensors {get_tensor_list_signature(tensors)}")

    return state_dict
