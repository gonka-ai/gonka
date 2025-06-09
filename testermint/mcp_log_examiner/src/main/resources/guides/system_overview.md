# System Overview
This is a guide for the system under test for testermint logs

## Architecture

This system is a decentralized, containerized AI infrastructure optimized for running inference and training large language models. It uses a custom blockchain consensus mechanism called Proof of Work 2.0, which replaces traditional hash computations with transformer-based AI tasks. Nodes (“participants”) compete in time-limited computational Races, earning voting weight and task assignment rights based on performance. A layered validation system, including randomized checks and peer verification, ensures honest computation.

Key components include:
- chain node (Go, Cosmos-SDK): Manages blockchain state and consensus.
- api node (Go): Orchestrates inference/training tasks and handles validation logic.
- ml node (Python, PyTorch, CUDA): Executes AI workloads and submits results.
- Test suite (“Testermint”): Runs integration tests using Dockerized clusters and mocks.

Logs may reflect workload distribution, validation outcomes, task routing, voting weight calculations, and protocol execution across these nodes.

## 🌀 Epoch Overview

An **Epoch** represents a single cycle of work within the system.

At the **start of each Epoch**:

* All participants run a **Proof of Compute** process, solving time-limited AI-relevant puzzles to benchmark their available compute capacity.
* The amount of compute each participant successfully proves is **broadcast and used as the basis for power** throughout the system.

This **power score** determines:

* 💡 **Inference allocation** — nodes with more power receive a larger share of inference tasks.
* 🛡️ **Validation responsibility** — higher-power nodes are assigned more peer-validation duties.
* 🗳️ **Voting weight** — in cases where inferences are disputed or invalidated, voting power is based on proven compute.

During the **Epoch inference phase**:

* Inference tasks are randomly assigned but **weighted by power**.
* Requests typically come from developers (external users), but in test environments, other nodes may both **request and serve** inferences.
* **Providers are not paid immediately** — all payments are deferred to the end of the Epoch.

At the **end of the Epoch**:

* Providers **submit a claim** for rewards, which includes a random seed set earlier in the Epoch.
* This seed allows verification that the node **performed sufficient validation**, relative to its claimed power.
* Once verified, nodes are **rewarded** both for their inference work and through additional bonuses defined in the system's tokenomics.

This cycle creates a **power-proportional, decentralized labor and trust economy** rooted in computational proof and cryptographic accountability.
