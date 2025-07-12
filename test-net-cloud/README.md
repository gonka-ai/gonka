# Testnet Blockchain Environment

This document outlines the architecture of the blockchain test environment running on Kubernetes. It focuses on the roles and responsibilities of the network participants rather than the underlying Kubernetes setup.

## Network Participants

The testnet is designed to launch with a specific configuration of participants and Machine Learning (ML) nodes. The setup is divided into a "genesis" participant and several "join" participants.

### Genesis Participant

The initial state of the network is defined by a single **genesis participant**. This participant is unique because it manages multiple ML nodes from the start.

*   **k8s namespace:** `genesis`
*   **Location:** The primary blockchain node and API run on `k8s-worker-1`.
*   **ML Nodes:**
    *   `Qwen/Qwen2.5-1.5B-Instruct` (runs on `k8s-worker-1`)
    *   `Qwen/Qwen2.5-7B-Instruct` (runs on `k8s-worker-4`)

### Join Participants

After the genesis participant has initialized the network, other participants can join. The current configuration is set up for two additional participants.

1.  **Join Participant 1**
    *   **k8s namespace:** `join-k8s-worker-2`
    *   **Location:** All components run on `k8s-worker-2`.
    *   **ML Nodes:**
        *   `Qwen/Qwen2.5-7B-Instruct`

2.  **Join Participant 2**
    *   **k8s namespace:** `join-k8s-worker-3`
    *   **Location:** All components run on `k8s-worker-3`.
    *   **ML Nodes:**
        *   `Qwen/Qwen2.5-1.5B-Instruct`
