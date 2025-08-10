# Gonka

Gonka is a decentralized AI infrastructure designed to optimize computational power for AI model training and inference, offering an alternative to monopolistic, high-cost, centralized cloud providers. As AI models become increasingly complex, their computational demands surge, presenting significant challenges for developers and businesses that rely on costly, centralized resources.
## Introduction

We introduce a novel consensus mechanism, **Proof of Work 2.0**, that ensures nearly 100% of computational resources are allocated to AI workloads, rather than being wasted on securing the blockchain.
## Key Roles

- **Developers** — Use the decentralized network to run inference and LLM training.
- **Participants (Hardware Providers or Nodes)**  — Contribute computational resources and earn rewards based on their input.
## Key Features

1. A novel **“Sprint”** mechanism, where participants compete in time-bound computational Races to solve AI-relevant tasks. Instead of traditional Proof of Work (e.g., compute hashes), these Races use **transformer-based models**, aligning the computation with AI model workloads. The number of successful computations a node generates during the Race determines its **voting weight**, directly linking computational contribution to governance and task validation rights. This voting weight not only determines consensus power but also controls task allocation: nodes with higher weight are assigned a larger share of AI inferences and training workloads, and are proportionally responsible for validating others’ results. This ensures that system resources are used efficiently, with real-world tasks assigned in proportion to each node’s proven compute capacity, enabling a “one-computational-power-one-vote” principle rather than capital-based influence (see diagram 1).
2. The platform uses **Randomized Task Verification**. Instead of verifying every inference task redundantly, the system selects a subset of tasks for verification based on cryptographically secure randomization. Nodes with higher voting weight have greater responsibility for validation. This approach drastically reduces overhead to just 1–10% of tasks, while maintaining trust through probabilistic guarantees and the threat of losing rewards if caught cheating.
3. **Validation during model training** follows a similar protocol as inference. Nodes are required to complete training workloads and are subject to majority-weighted peer verification. The system handles the non-deterministic nature of AI training by applying statistical validation, allowing for slight output variances while penalizing repeated or malicious inconsistencies. Rewards are withheld until a node’s training contributions are verified as honest and complete.
4. The infrastructure leverages **DiLoCo**'s periodic synchronization approach to enable **Geo-Distributed Training** by efficiently distributing AI training tasks across a network of independent hardware providers, creating a **decentralized training environment** with minimal communication overhead. Nodes contribute compute power and receive tasks in proportion to their capabilities. Developers can initiate and fund training projects, and the system ensures workload distribution and result validation through its Proof of Work 2.0 protocol and validation layers. The platform is designed to maintain **fault tolerance and decentralized coordination**, enabling scalable training without centralized oversight.
5. A reputation score is assigned to each node and increases with consistent, honest behavior. New nodes start with zero and are subject to more frequent checks. As reputation grows, verification frequency decreases, allowing for lower overhead and higher reward efficiency. Nodes caught submitting false results lose all earned rewards for that cycle and reset their reputation, entering a phase of strict scrutiny. This encourages long-term honesty and punishes strategic cheating.

![The Task flow](https://github.com/user-attachments/assets/1ba81a47-f4ef-4eb1-9fcd-b6d371a20f5f)

*[Work in progress] Diagram 1. The Task flow [Source](docs/papers/InferenceFlow.png)**

For a deeper technical and conceptual explanation, check out [the White Paper](https://gonka.ai/whitepaper.pdf).
## Getting started

For the most up-to-date documentation, please visit [https://testnet.productscience.ai/](https://testnet.productscience.ai/introduction/)

To join Testnet:
- **As Developer**: Explore the [Quickstart Guide](https://testnet.productscience.ai/developer/quickstart/) to understand how to create a user account and submit an inference request using the `inferenced` CLI tool.
- **As Participant (Hardware Providers or Nodes)**:
    - Review the [Hardware Specifications](https://testnet.productscience.ai/participant/hardware-specifications/) to ensure your equipment meets the requirements.
    - Follow the [Participant Quickstart Guide](https://testnet.productscience.ai/participant/quickstart/) to set up your node and start contributing computational resources.
### Local Quickstart

This section walks you through setting up a local development environment to build and test the core components, without joining the real network or running a full MLNode.
#### 1. Environment setup
Make sure you have the following installed:
1. Git CLI
2. Go 1.22.8
3. Docker Desktop (4.37+)
4. Make
5. Java 19+
6. (Optional) A Go IDE
7. (Optional) A Kotlin IDE (for testing)
#### 2. Build the project
Clone the repository:
```
git clone https://github.com/gonka-ai/gonka.git
cd gonka (or repo name)**
```

Build chain and API nodes, and run unit tests:
```
make local-build
```
#### 3. Run local tests
There is an integration testing framework dubbed “Testermint”. This framework runs on live `api` and `chain` nodes, and emulates `ml` nodes using WireMock. It runs a local cluster of nodes using Docker and tests things very close to how they will work in a live environment. See the README.md in the [`/testermint`](https://github.com/gonka-ai/gonka/tree/main/testermint) directory for more details.

This command will build locally, deploy a small network of Docker containers, and run a set of these integration-level tests. It will take quite some time to run completely.
```
make run-tests
```
There’s also an option to just run a Docker local chain, without running the tests, use `launch-local-test-chain-w-reset.sh` script for that. The script will spin up a miniature local chain consisting of 3 participants.

To run Go unit tests for `chain` node (`inference-chain`)  and `api` node (`decentralized-api`) use `node-test` and `api-test` make targets.
## Architectural overview

Our project is built as a modular, containerized infrastructure with multiple interoperable components.
### Core components

- Network Node — This service handles all communication, including:
    - [`chain`](https://github.com/gonka-ai/gonka/tree/main/inference-chain) node that connects to the blockchain, maintains the blockchain layer, and handles consensus.
    - [`api`](https://github.com/gonka-ai/gonka/tree/main/decentralized-api) node serves as the primary coordination layer between the blockchain (`chain node`) and the AI execution environment (`ml` node). It exposes REST/gRPC endpoints for interacting with users, developers, and internal components, while managing work orchestration, validation scheduling, and result verification processes that require off-chain execution. In addition to handling user requests, it is responsible for:
        - Routing inference and training jobs to the `ml` node
        - Recording inference results and ensuring task completion
        - Scheduling and managing validation tasks
        - Reporting receipts and signatures to the chain node for consensus
        - Orchestrating Proof of Work 2.0 execution
    - Technologies: GO, Cosmos-SDK.
- `ml` node — Handles AI workload execution: training, inference, and Proof of Work 2.0. Participants run `ml`nodes to contribute compute.
    - Technologies: Python, Docker, NVIDIA CUDA, gRPC, PyTorch, vLLM.
    - Location: [MLNode GitHub Repository](https://github.com/product-science/mlnode/tree/main)
- Ethereum Node — Interfaces with Ethereum for anchoring and bridging use cases.
    - Technologies: [Work in progress]
    - Location:  [Work in progress]

![network-architecture](https://github.com/user-attachments/assets/df7aaf8a-209b-477e-8aeb-cfa423d7b10d)

*Diagram 2. The diagram outlines how components interact across the system. [Source](https://github.com/product-science/mlnode/blob/main/network-architecture.png)*
## Repository Layout

The repository is organized as follows:
```
/client-libs        # Client script to interact with the chain
/cosmovisor         # Cosmovisor binaries
/decentralized-api  # Api node
/dev_notes          # Chain developer knowledge base
/docs               # Documentation on specific aspects of the chain
/inference-chain    # Chain node
/prepare-local      # Scripts and configs for running local chain
/testermint         # Integration tests suite
```
## Testing

We support several types of tests to ensure the system’s stability and reliability:
- Unit tests – For core logic in `ml`node, `chain` node, and `api` node
- End-to-End tests – Test full task lifecycle across the network using Testermint module

Detailed instructions on running and contributing to tests are available in [`CONTRIBUTING.md`](https://github.com/gonka-ai/gonka/blob/main/CONTRIBUTING.md).
## Deployment strategy

The system is designed around **containerized microservices**. Each component runs in its own Docker container, allowing:
- Independent deployment – Components don’t need to be co-located
- Scalable compute – Easily add more `ml`nodes or `api`nodes
- Simplified redeployments – Faster updates and rollback support

We maintain deployment examples and tooling in the [pivot-deploy](https://github.com/product-science/pivot-deploy) repository. (Planned to be merged into the main repository soon).
## Policies

[Terms & Policies](https://testnet.productscience.ai/termsofuse/) governing the use of the platform.
## Support

- Reach us at hello@productscience.ai.
- [Discord](https://discord.com/invite/pUSEmYX4JP) – Join for real-time discussions, updates, and support.
