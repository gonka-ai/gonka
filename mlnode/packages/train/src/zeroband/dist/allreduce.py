from typing import Optional
import torch
import torch.distributed as dist


def all_reduce(
    tensor: torch.Tensor,
    op: dist.ReduceOp = dist.ReduceOp.SUM,
    group: Optional[dist.ProcessGroup] = None,
) -> None:
    """Wrap gloo all reduce"""
    if group is None:
        group = dist.distributed_c10d._get_default_group()
    if op not in [dist.ReduceOp.SUM, dist.ReduceOp.AVG]:
        raise ValueError(f"Unsupported reduce operation {op}. Only SUM and AVG are supported.")

    # group = cast(dist.ProcessGroup, group) # just type hint stuff for IDE
    if op == dist.ReduceOp.AVG:
        # todo check numerical stability of doing post or pre div
        tensor.div_(group.size())

    dist.all_reduce(tensor, op, group=group)
