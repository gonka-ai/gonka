# Parallel Local Testing

## Quick Start

Run all tests in parallel using LXD containers:

```bash
cd testermint
./run-parallel-tests.sh
```

Run specific tests:

```bash
./run-parallel-tests.sh --tests "InferenceTests,GovernanceTests"
```

Adjust parallelism:

```bash
./run-parallel-tests.sh --parallel 8
```

## Prerequisites

Install and initialize LXD (first time only):

```bash
sudo snap install lxd
sudo lxd init --auto
sudo usermod -aG lxd $USER
newgrp lxd
```

Build the golden LXD image before running tests:

```bash
cd testermint
./setup-base-image.sh
```

This pre-loads Docker images, Java, and Gradle dependencies into a reusable LXD image.

Rebuild the golden image whenever code changes to update Docker images.

## Results and Logs

After test execution, all artifacts are stored in:

```
parallel-test-results/<TestClassName>/
```

Each test class directory contains:

- `test-results/` - JUnit XML test results
- `reports/` - HTML test reports
- `testermint-logs/` - Testermint execution logs
- `docker-logs/` - Container logs from chain and API nodes
- `docker-ps.txt` - Container states snapshot
- `docker-images.txt` - Docker images snapshot

## Key Features

- Isolated test execution in ephemeral LXD containers
- Full Docker support with pre-loaded images
- Parallel execution with GNU parallel
- Complete artifact collection per test class
- No cross-test contamination
- Fast startup from golden image

## Architecture

Each test runs in an isolated LXD container with:
- Pre-loaded Docker images for chain, API, and mock server
- Pre-cached Gradle dependencies
- Full nested Docker support
- Independent test chain instance
- 8GB RAM, 4 CPU limit per container

