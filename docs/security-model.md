# Security model

The protocol is based on a Proof-of-Work-like consensus that derives each host's weight from its hardware capacity for serving large LLMs, or other compute-demanding architectures. In contrast to classic Proof-of-Work, the protocol assigns this benchmark work, which is not explicitly useful, only for a short period. For most of the epoch, that compute capacity executes useful tasks.

The network approves the list of supported models, with precision and deploy params (e.g. max_context_length, kv cache quantization).
To join the network, a host must have hardware that can serve one of the approved models at the defined quality, and provide that hardware for at least one full epoch. The protocol pays reward only for whole epochs.

Structure of an epoch:

```
Hosts
|
|-- Proof-of-Compute for epoch N
|   short-term benchmark: hosts prove the compute they have, ~minutes
|
|-- Epoch N starts with weight from that PoC
|   |
|   -- Inference phase, ~24 hours
|
|-- End of epoch
```

An epoch has two phases:

1. **PoC Phase** is a short benchmark (~ mins). Hosts run model forward passes on deterministically randomized inputs at the same time, at their maximum compute capacity, and produce artifacts.
Other hosts validate those artifacts. After voting, the network assigns each host a weight based on how much work it did during PoC.

PoC validation is strict enough to reject even small differences in outputs.

2. The **Inference Phase** lasts ~24 hours. The protocol assigns computational tasks to each host according to its weight. If a host cannot execute those tasks, or executes them incorrectly (e.g. a more quantized model for inference, or less data during training), it is invalidated and removed from the epoch.

----

## Security Layers

The main security primitive to prove that a host keeps its hardware available to the network is useful work itself. Under high utilization of the protocol, a host that does not provide hardware for useful work cannot handle the assigned tasks and is removed from the epoch.
The long-term target is to derive security from assigned useful work, so less capacity is spent on extra jobs that exist only to prove hardware.
This approach requires not only high utilization (which can be achieved by assigning tasks that do not require online responses) but also the highest quality of load distribution.

For the time of protocol stabilization and maturing, additional layers are introduced:

1. **Confirmation PoC** is the same PoC, started at a random time during the inference phase, to confirm that the hardware is still present.

2. **PoC Challenge** is a long-running PoC. If a challenger assumes a host can run real hardware for the short PoC or Confirmation PoC windows but trick validation of the real, useful load, they can trigger it. It loads the challenged host at maximum capacity for the rest of the epoch. The cost equals the tokens that host would get if it served user inference requests. If the host passes, it receives that payment. If it does not pass, the payment returns to the challenger, and the host is invalidated and removed from the epoch.