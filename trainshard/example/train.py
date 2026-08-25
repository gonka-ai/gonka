"""A small GPT trained from scratch across the shard, one card to a node.

Nothing here is downloaded from the open internet: the run has no route out and no resolver. The
text comes from the one host:port the deploy declared as a source, and the weights start random, so
there is nothing else to fetch.

The daemon places the node on the mesh and hands it its rank, the size of the group and where rank 0
waits, so there is nothing to work out from the interface here. One process to a node, so the node
rank is the process rank.
"""

import math
import os
import time
import urllib.request

import torch
import torch.distributed as dist
import torch.nn as nn
from torch.nn import functional as F
from torch.nn.parallel import DistributedDataParallel

SOURCE = os.environ.get("SOURCE", "")
# next to this file, wherever the image put it: /workspace is the volume the daemon mounts over
LOCAL = os.path.join(os.path.dirname(os.path.abspath(__file__)), "input.txt")
RANK = int(os.environ["NODE_RANK"])
WORLD_SIZE = int(os.environ["NNODES"])
MASTER = os.environ["MASTER_ADDR"]
MASTER_PORT = int(os.environ["MASTER_PORT"])
STEPS = int(os.environ.get("STEPS", "200"))
BATCH = int(os.environ.get("BATCH", "12"))
BLOCK = int(os.environ.get("BLOCK", "256"))
LAYERS = int(os.environ.get("LAYERS", "12"))
HEADS = int(os.environ.get("HEADS", "12"))
WIDTH = int(os.environ.get("WIDTH", "768"))


class Block(nn.Module):
    def __init__(self):
        super().__init__()
        self.norm1 = nn.LayerNorm(WIDTH)
        self.attn = nn.MultiheadAttention(WIDTH, HEADS, batch_first=True)
        self.norm2 = nn.LayerNorm(WIDTH)
        self.mlp = nn.Sequential(nn.Linear(WIDTH, 4 * WIDTH), nn.GELU(), nn.Linear(4 * WIDTH, WIDTH))

    def forward(self, x, mask):
        normed = self.norm1(x)
        attended, _ = self.attn(normed, normed, normed, attn_mask=mask, need_weights=False)
        x = x + attended
        return x + self.mlp(self.norm2(x))


class GPT(nn.Module):
    def __init__(self, vocab):
        super().__init__()
        self.tokens = nn.Embedding(vocab, WIDTH)
        self.positions = nn.Embedding(BLOCK, WIDTH)
        self.blocks = nn.ModuleList(Block() for _ in range(LAYERS))
        self.norm = nn.LayerNorm(WIDTH)
        self.head = nn.Linear(WIDTH, vocab, bias=False)
        self.register_buffer("mask", torch.triu(torch.full((BLOCK, BLOCK), float("-inf")), diagonal=1))

    def forward(self, idx, targets):
        positions = torch.arange(idx.shape[1], device=idx.device)
        x = self.tokens(idx) + self.positions(positions)
        for block in self.blocks:
            x = block(x, self.mask[: idx.shape[1], : idx.shape[1]])
        logits = self.head(self.norm(x))
        return F.cross_entropy(logits.reshape(-1, logits.shape[-1]), targets.reshape(-1))


def main():
    say = print if RANK == 0 else lambda *a, **k: None

    print(f"rank {RANK} of {WORLD_SIZE}, meeting at {MASTER}:{MASTER_PORT}", flush=True)

    if os.path.exists(LOCAL):
        text = open(LOCAL).read()
    else:
        text = urllib.request.urlopen(f"http://{SOURCE}/input.txt", timeout=60).read().decode()
    alphabet = sorted(set(text))
    index = {ch: i for i, ch in enumerate(alphabet)}
    data = torch.tensor([index[ch] for ch in text], dtype=torch.long)
    say(f"{len(text)} characters, {len(alphabet)} distinct")

    dist.init_process_group(
        "nccl", init_method=f"tcp://{MASTER}:{MASTER_PORT}", rank=RANK, world_size=WORLD_SIZE
    )
    torch.cuda.set_device(0)
    print(f"rank {RANK} joined on {torch.cuda.get_device_name(0)}", flush=True)

    torch.manual_seed(1337 + RANK)
    model = GPT(len(alphabet)).cuda()
    say(f"{sum(p.numel() for p in model.parameters()) / 1e6:.1f}M parameters")
    model = DistributedDataParallel(model, device_ids=[0])
    optimiser = torch.optim.AdamW(model.parameters(), lr=3e-4)

    def batch():
        starts = torch.randint(len(data) - BLOCK - 1, (BATCH,))
        x = torch.stack([data[s : s + BLOCK] for s in starts])
        y = torch.stack([data[s + 1 : s + BLOCK + 1] for s in starts])
        return x.cuda(), y.cuda()

    # the first step pays for cuda context, cudnn choices and the first allreduce, so it is timed
    # apart from the rest rather than allowed to flatter or spoil the average
    started = time.time()
    loss = model(*batch())
    loss.backward()
    optimiser.step()
    optimiser.zero_grad(set_to_none=True)
    torch.cuda.synchronize()
    say(f"first step took {time.time() - started:.1f}s")

    started = time.time()
    for step in range(1, STEPS + 1):
        loss = model(*batch())
        loss.backward()
        optimiser.step()
        optimiser.zero_grad(set_to_none=True)
        if step % 20 == 0:
            torch.cuda.synchronize()
            per_step = (time.time() - started) / step
            print(
                f"rank {RANK} step {step} loss {loss.item():.4f} "
                f"{per_step * 1000:.0f}ms/step {BATCH * BLOCK * WORLD_SIZE / per_step:.0f} tok/s",
                flush=True,
            )

    torch.cuda.synchronize()
    per_step = (time.time() - started) / STEPS
    # a mesh that carries gradients leaves every rank holding the same weights, so a checksum of them
    # says more about the run than the loss does
    checksum = sum(p.detach().double().sum().item() for p in model.parameters())
    print(
        f"rank {RANK} done: loss {loss.item():.4f}, {per_step * 1000:.0f}ms/step, "
        f"weight checksum {checksum:.6f}",
        flush=True,
    )

    with open("/workspace/result.txt", "w") as out:
        out.write(f"rank {RANK} loss {loss.item():.4f} ms_per_step {per_step * 1000:.0f} checksum {checksum:.6f}\n")

    dist.destroy_process_group()


if __name__ == "__main__":
    main()
