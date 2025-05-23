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

## Future plans

To achieve complete decentralization, the network repository will migrate from GitHub to a customized fork of Gitopia, a decentralized Git hosting solution built on Cosmos SDK. This fork will be integrated directly into the blockchain and hosted across currently active Participant nodes. As a result, voting on repository changes will utilize the same voting weights assigned to each Participant during the Race.
  
This will enable the network to own and manage its repository without external dependencies.
1. Hosting the repository on the network:
	1. Gitopia has already forked Cosmos SDK, and their modifications are minimal.
	2. The relevant modules will be extracted and integrated into the network. 
	3. The repository will be hosted across network nodes and governed by the same consensus mechanism as the rest of the network.
2. Decentralized governance:
	1. Gitopia’s existing DAO framework will be reused to manage repository decisions.
	2. Voting weight will be automatically assigned based on the Proof of Work.
	3. Every software update will be subject to explicit voting before being merged.
3. Access and user experience:
	1. The Gitopia web interface will be hosted on network nodes.
	2. Users will be able to access it via an IP-based connection.
4. Maintaining a GitHub mirror:
	1. For ease of collaboration, the network will maintain a mirror repository on GitHub.
	2. However, GitHub will no longer be the ground-truth version of the repository.
	3. Developers can continue contributing via GitHub, but the official version will be stored on the network itself.

**Why this approach?**
- **No centralization points.** The repository is owned and maintained by the network itself.
- **Greater security.** No single entity can alter the code without consensus. 
- **Explicit voting rights.** Control is determined by network participation, ensuring fairness.
- **No external dependencies.** Unlike GitHub, there is no need for external wallets or separate network balances to vote or propose changes.

This approach ensures a smooth transition from GitHub-based governance to complete decentralization. Initially, the network leverages GitHub’s ease of use while maintaining strict on-chain governance to prevent unauthorized changes. As the network matures, migration to the customized Gitopia fork will follow, ensuring that the network’s consensus entirely controls the repository, aligning code governance with network participation.
## Testing requirements

Before opening a PR, run unit tests and integration tests:
```
make local-build
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
