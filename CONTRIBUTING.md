# Contributing guidelines
This project is maintained by a distributed team of contributors, and contributions are more than welcome. This guide outlines everything you need to know to participate — from coding standards to PR approvals and architectural proposals.
## Pull request lifecycle

1. Fork and branch 
	1. Always work on a feature branch off the main branch.
	2. Use clear and descriptive naming: `feature/xyz`, `bugfix/abc`, `refactor/component-name`.
2. Create a pull request
	1. Push your changes and open a pull request against the main branch.
	2. Link related issues (if any), and include a summary of changes.
	3. Tag relevant reviewers using @username.
3. [Work in progress] Review and voting process 
	1. PRs (involving protocol logic or architecture) must go through a voting process (described below). Voting follows a simple majority unless otherwise stated.
4. Merge. Once approved, a maintainer will merge the PR.
## [Work in progress] Governance

Currently, GitHub will remain our primary development platform, however, governance will be handled on-chain, requiring approval by the majority for all code changes. Here’s how this hybrid approach works.

**Software Update**
- Every update must be approved by an on-chain vote.
- Update proposals include the commit hash or binary hash.
- Only after on-chain approval is code recognized as the official network version.
- A REST API is available for participants to verify which version is approved.
  
**Code Integrity**
- This repository serves as the primary codebase for blockchain development and contains the current production code.
- Code ownership and governance are separated. All proposed changes to this repository are subject to voting and approval.
- Participant nodes monitor the repository for unauthorized changes in the main branch of the repo.
- If an unapproved commit is detected, all network participants are notified immediately.

## Testing requirements

Before opening a PR, run unit tests and integration tests:
```
make local-build build-docker
make run-tests
```

- Some tests must pass before a PR can be approved:
	- All unit test
	- All integration tests, minus known issues listed in `testermint/KNOW_ISSUES.md`
- To run tests with a real `ml` node (locally):
	- [Work in progress]
## Code standards
- [Work in progress]
## [Work in progress] Proposing architectural changes

Before starting significant architectural work:
1. Open a GitHub issue, describing the proposed change.
2. Share a design document (in Markdown or as a diagram).
3. Get feedback from other contributors.
4. Reach a consensus before implementation begins.
## Documentation guidelines

- All relevant docs are stored in [here](https://github.com/product-science/pivot-docs)
- Update docs alongside code changes that affect behavior, APIs, or assumptions
- Missing docs may delay PR approval

## Protobufs

- All `ml` node protobuf definitions are stored in [here](https://github.com/product-science/chain-protos/blob/main/proto/network_node/v1/network_node.proto)
- After editing the `.proto` files, copy them to the `ml` node and Inference Ignite repositories, and regenerate the bindings.
## Deployment and updates

We use Cosmovisor for managing binary upgrades, in coordination with the Cosmos SDK’s on-chain upgrade and governance modules. This approach ensures safe, automated, and verifiable upgrades for both `chain` and `api` nodes.

**How it works**
- **Cosmovisor** monitors the blockchain for upgrade instructions and automatically switches binaries at the specified block height.
- **On-chain governance proposals** (via `x/governance` and `x/upgrade`) define precisely when and how upgrades are applied.
- **`Chain` and `api` node binaries** are upgraded simultaneously to avoid compatibility issues.
- **`Api` node** continuously tracks the block height and listens for upgrade events, coordinating restarts to avoid interrupting long-running processes.
- **`Ml` node** maintains versioned APIs and employs a dual-version rollout strategy. When an `api` node update introduces a new API version, both the old and new `ml` node versions must be deployed concurrently. `Api`node then automatically switches to the new container.


## Stress Testing

We use fork of [compressa-perf](https://github.com/product-science/compressa-perf) for stress testing. 
It can be installed with `pip`:
```
pip install git+https://github.com/product-science/compressa-perf.git
```


**Run Performance Test (Preffered):**
```bash
# len of prompt in symbols: 3000
# tasks to be executed: 200  
# total parallel workers: 100
compressa-perf \
	measure \
	--node_url http://36.189.234.237:19252/ \
	--model_name Qwen/Qwen2.5-7B-Instruct \
	--create-account-testnet \
	--inferenced-path ./inferenced \
	--experiment_name test \
	--generate_prompts \
	--num_prompts 3000 \
	--prompt_length 3000 \
	--num_tasks 200 \
	--num_runners 100 \
	--max_tokens 100
```

`--node_url` right now all requests going through that Transfer Agent.

**To view performance measurements:**
```
compressa-perf list --show-metrics --show-parameters
```

**To check balances for all nodes:**
```
compressa-perf check-balances --node_url http://36.189.234.237:19252
```

**Run long term performance test:**
```
compressa-perf \
	stress \
	--node_url http://36.189.234.237:19252 \
	--model_name Qwen/Qwen2.5-7B-Instruct \
	--create-account-testnet \
	--inferenced-path ./inferenced \
	--experiment_name "stress_test" \
	--generate_prompts \
	--num_prompts 200 \
	--prompt_length 1000 \
	--num_runners 20 \
	--max_tokens 300 \
	--report_freq_min 1 \
	--account-pool-size 4
```
