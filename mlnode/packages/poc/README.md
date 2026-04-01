# PoC Package Notes

PoC v2 implementation now lives in the forked vLLM repository:

- [gonka-ai/vllm](https://github.com/gonka-ai/vllm)
- Path: `./vllm/poc`

That code includes `callbacks.py`, used when:

- PoC nonces are calculated
- PoC validations are performed

## Why we are adding patch here

This repository keeps the network-layer integration with `decentralized-api`, which is cleaner to maintain here.

We add local callback overrides under:

- `./mlnode/packages/poc/patches`

Then in `mlnode/packages/api/Dockerfile`, we copy this patch into the image to overwrite the `callbacks.py` shipped in the vLLM image.

## Signing callback example (`POC_SIGNATURE_KEY`)

The patched callbacks include an example where callback payloads are signed with the key defined by the `POC_SIGNATURE_KEY` environment variable.

This can be used to protect `decentralized-api` callback endpoints that may need to remain publicly reachable for infrastructure reasons (for example, mixed hosting providers for ML nodes and API nodes where closing all ports is difficult).
