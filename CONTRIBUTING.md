# Contributing guidelines
Welcome! This project is maintained by a distributed community of contributors. Contributions are welcome from everyone, not only Hosts. This guide explains how to participate successfully: how work is proposed, discussed, implemented, reviewed, and (when applicable) recognized through governance.

Bug bounty (HackerOne) and contributor rewards (community pool) are described in the [Bounty program](https://gonka.ai/docs/bounty-program/) docs.

## Where work happens and the source of truth
Gonka uses GitHub as the primary platform for public, auditable collaboration.

- **GitHub Discussions:** start here for ideas, proposals, and anything that still needs alignment. The same idea may already have been discussed, or the current approach may be a trade-off.
- **GitHub Issues:** scoped bugs, features, refactors, and tasks that are ready to implement (including `up-for-grabs`).
- **Pull Requests:** code and documentation changes, reviews, and tracking.
- **HackerOne:** security vulnerabilities. Use the form at [Report a Vulnerability](https://gonka.ai/docs/report-vulnerability/). Do not open a public GitHub issue or PR for a security issue.

Conversation can happen anywhere (Discord, calls, other forums), but key outcomes should be consolidated back into GitHub. Keeping the full history in one place makes it searchable, maintainable, and easier to review over time. GitHub is the main source of truth for public protocol work. HackerOne is the source of truth for vulnerability reports.

## Issues
This repository uses GitHub Issues for bugs, features, refactors, documentation tasks, and implementation work derived from proposals. For new ideas, start with a Discussion first (see below).

### Security vulnerabilities

Submit every security vulnerability through the [HackerOne form](https://gonka.ai/docs/report-vulnerability/). That is the only intake.

- Do not open a public GitHub issue or PR, and do not post the issue in Discord or other public channels.
- Reviewers triage the report and assign severity. A valid report is paid after triage. A fix can be paid separately; that amount is discussed individually.
- If reviewers can confirm the vulnerability is already known — for example there was already a GitHub discussion, or a report already exists on HackerOne — the report may be marked as a **duplicate** and not paid.

Details: [Bounty program](https://gonka.ai/docs/bounty-program/).

### When to open an issue
Open an issue when you want to:

- Report a non-security bug or regression
- Track a feature or enhancement that already has community support
- Request documentation changes
- Propose a refactor or technical debt cleanup
- Propose a scoped technical change that is ready for implementation

Before opening a new issue:

- Search existing issues, pull requests, and Discussions to avoid duplicates.
- If a similar thread exists, add information there instead of creating a new one.

### When to open a Proposal Discussion instead of Issues
Start with a Proposal Discussion (see below) before investing in a large implementation. The same idea may already have been discussed, or the current approach may be a trade-off.

Also open a Discussion for anything that is not yet an actionable, scoped task or may not directly result in a PR to this repository. For example:

- Anything that won’t result in a PR to Gonka repo: ecosystem/integrations/tooling initiatives
- The scope is too large for a single issue:
	- major protocol update or architecture direction
	- cross-component or long-term roadmap shaping
	- an open problem that needs research or alignment before a clear execution plan exists

### Project triage
New Issues should be routed into the project triage flow in GitHub Projects (when available):

- Create the Issue in the repository as usual.
- Add it to the Triage project.
- On entry to Triage, the Issue should receive "Status: New" (or equivalent).
- Try to actively gather early community feedback (reactions, comments, practical concerns), especially for non-trivial changes. This is in the proposer’s interest: it de-risks the work, prevents wasted effort on misaligned changes, and speeds up maintainer review. Use Discord and other platforms to kick off a discussion, then consolidate the key context back into GitHub.
- Then wait for repository maintainers or release managers to review it and assign the next status (for example: Accepted, Needs info, Needs external review, Declined).  

### Issue templates (recommended structure)
Use templates for Issues when submitting bug reports or optimization suggestions. For complex work, maintainers may request a design doc before approving implementation.
Reactions are a prioritization signal, not a hard requirement. They help maintainers prioritize when time is limited and capture demand from users who do not comment.

### How maintainers triage Issues
Maintainers typically evaluate Issues using a simple rubric:

**Impact:**

- Number of users affected
- Severity of breakage or pain
- Protocol correctness or security risk

**Feasibility and cost:**

- Effort size vs benefit
- Complexity and unknowns
- Maintenance cost

**Typical outcomes:**

- **Accepted:** approved for implementation
- **Needs info:** missing details, repro steps, or acceptance criteria
- **Needs external review:** request feedback from domain experts or community
- **Declined:** not aligned, too risky, not feasible, or solved by an alternative

### Claiming work

**Contributors may:**

- Open an Issue and assign themselves after maintainers confirm it is actionable, or
- Pick an existing Issue marked as `up-for-grabs` and leave a short claim comment such as `Starting work, ETA 3 to 5 days`

Claim before starting so others have visibility and avoid duplicate effort.

## Milestones (releases, deadlines, and scope)

Milestones are used to group Issues and PRs into a specific release or upgrade train. They make it clear what is intended to ship together and help maintainers and release managers coordinate review, testing, and on-chain upgrade preparation.

A milestone typically represents one of:

- a planned network upgrade or release (for example: `v0.x.y`)
- a short-term delivery batch maintained by the release team

### How a milestone deadline is set

A milestone deadline is a planning target, not a guarantee. It is usually set based on:

- upgrade scheduling constraints (including governance and coordination needs)
- test and stabilization time required
- dependency readiness (chain, api, ml node compatibility)
- operational constraints for Hosts and release coordination

Milestone deadlines and milestone scope are typically set and updated by:

- repository maintainers, and or
- release managers (or whoever is coordinating the specific upgrade)

Contributors should not self-assign or change milestone dates unless explicitly asked to by a maintainer or release manager.

Avoid placing these into a milestone by default:

- exploratory ideas that still need alignment (start as a Proposal Discussion instead)
- large refactors with unclear scope or acceptance criteria
- work that depends on unresolved upstream changes
- nice-to-have items that can safely slip without impacting the release goals
- changes that introduce breaking behavior without a clear migration plan and maintainer approval

If an Issue is important but not ready, track it outside the milestone and use labels or project status to reflect its readiness.

## Proposals (GitHub Discussions)
This is the primary space for discussing proposals for protocol improvements and broader Gonka ecosystem development.

Recommended guide: [https://github.com/gonka-ai/gonka/discussions/795](https://github.com/gonka-ai/gonka/discussions/795)

### What belongs in Proposals

Create a Proposal discussion for:

- **Protocol improvements:** significant updates to core protocol design and long-term architectural direction
- **External infrastructure proposals:** third-party integrations, API clients, tooling, ecosystem extensions
- **Open problems:** topics that need research, design exploration, or community alignment before a clear path forward exists

### How to write a good proposal

Keep proposals structured. A strong proposal typically includes:

- **Motivation:** the specific problem being solved
- **High-level solution:** the proposed architecture and approach
- **Implementation roadmap:** milestones if the change is complex
- **Open questions:** known unknowns to resolve via community discussion or a community call
- **Who you are:** context about experience and expertise (prior Gonka work or other reputable projects). If representing a team or company, mention it and link relevant work.

Implementation timeline and a bounty or reward expectation can be proposed as part of the discussion. Grant-style arrangements have already been used and can be put to an on-chain vote. All protocol payouts remain subject to governance approval. The chain is the source of truth for those payouts.

### Suggested flow

- Publish a Proposal in GitHub Discussions.
- Share the link in Discord `#improvement-proposals` and other channels to gather feedback.
- Gather early validation signals: reactions, upvotes, comments, concrete concerns.
- If relevant, reach out to Hosts to gather practical input and support.
- Consolidate key outcomes back into the Discussion thread so the full history stays searchable and maintainable. GitHub is the main source of truth for public protocol work.

### From Proposal to Issues

Once a Proposal has enough clarity and support, break it into scoped Issues with clear acceptance criteria. This makes execution trackable and reviewable.

## Pull request lifecycle

- Fork and branch
	- Create your branch from `main` unless maintainers or release managers explicitly ask you to base your work on an upgrade branch:
		- If you see an upgrade branch whose version is the last executed upgrade number plus 1 (for example, `v0.x.(y+1)`), it may be the current stabilization line for the next upgrade. In that case, consider rebasing your feature branch onto that branch to reduce merge conflicts and ensure compatibility with the upcoming release.
		- If unsure which base branch to use, ask in the Issue or link a short note in your PR description stating which branch you rebased onto and why.
	- Use clear and descriptive naming: `feature/xyz`, `bugfix/abc`, `refactor/component-name`.
- Create a pull request
	- Push your changes and open a pull request against the main branch.
	- Link related Issue(s) and Discussion(s) (if any).
	- Include a clear summary: what changed, why, and what to review, e.g.:
		- What problem does this solve?
		- How do you know this is a real problem?
		- How does this solve the problem?
		- What risks does this introduce? How can we mitigate those risks?
		- How do you know this PR fixes the problem?
		- Which components are affected?
		- Explain how this PR was tested and attach evidence.
		- Optional: note if you ran [ai-reviewer](https://github.com/gonka-ai/ai-reviewer) and attach non-sensitive highlights from the summary.
	- Tag relevant reviewers using @username.
	- PRs without a meaningful description will not be reviewed. The description is part of the contribution.
- Review:
	- Keep all review discussions in the PR to keep it auditable.
	- Ask other community members to comment and review. Say what to look at (correctness, tests, edge cases, performance). Do not wait for review to happen on its own.
	- Respond to feedback, push updates, and keep the PR scope focused.
	- Protocol or architecture changes must link to a Proposal Discussion where the idea was publicly iterated and reviewed.
- Merge.
	- PRs (involving protocol logic or architecture) must go through a governance voting process (described below). Voting follows a simple majority unless otherwise stated.
	- Once approved, a maintainer will merge the PR.

**Optional: AI-assisted self-review (`ai-reviewer`)**

Before opening or updating a PR, contributors are strongly encouraged to use [gonka-ai/ai-reviewer](https://github.com/gonka-ai/ai-reviewer), a single-binary CLI for structured, repo-aware review. It runs multiple specialized review personas, such as security, correctness, architecture, and project-specific lenses, and supports primers for project context and waivers for explicit policy. It is intended to complement human review, not replace it, especially as the volume of change grows.
It is also recommended to review the full presentation before using the tool in practice.

- [Full presentation recording on Discord](https://discord.com/channels/1336477374442770503/1415622117629624362/1485979219711234058)
- [Supplemental video](https://www.youtube.com/watch?v=N4F74vd_pKQ)

**Why contributors use it**

- Helps surface cross-cutting risks early, including consensus, API, and operational edge cases, by using focused review prompts rather than one generic review pass.
- Review rules and project context can live in the repository, for example, under `.ai-review/<owner>/<repo>/` or in committed files with `ai_review` frontmatter, so feedback can stay aligned with how Gonka evolves in practice.
- Each run writes auditable artifacts, including reports, findings, and token or cost statistics, under `.ai-review/.../runs/...`, making results inspectable and easier to debug.
- `--dry-run` shows which personas and primers would run without calling model APIs, which is useful for validating configuration without incurring costs.

**Disclaimer:** AI output can be wrong or incomplete; maintainers retain final judgment on all merges. Do not paste secrets into prompts or reports.

## From Discussion to Issue to PR to reward (public lifecycle)
This project aims for an auditable flow from idea to delivery to governance recognition (when applicable). Full detail: [Bounty program](https://gonka.ai/docs/bounty-program/).

- Start with a GitHub Discussion when the work still needs alignment. Get community support first.
- Then open or claim a GitHub Issue (including `up-for-grabs`). Leave a claim comment with an ETA before starting.
- Maintainers triage publicly:
	- confirm scope and acceptance criteria
	- assign labels, priority, and status
- Work is delivered as:
	- a PR in this repository, or
	- an external deliverable with clear evidence (demo, logs, benchmarks, reproducible steps)
- Review happens publicly in the PR:
	- ask other community members for comments and review
	- discussion and revisions remain visible
	- links back to the original Issue and Discussion are required
- A maintainer merges once requirements are met.
- If applicable, a contributor-reward proposal is prepared for voting together with the next on-chain vote:
	- includes links to the Issue, PR(s), and merge commit(s)
	- reward size should be justified by impact, complexity, and risk
	- the **chain** is the source of truth for protocol payouts; matching records also live in this repository. Discord `#bounty-awards` posts some of this later and can be incomplete.

Security reports follow a separate path on HackerOne (see [Security vulnerabilities](#security-vulnerabilities) above).

## Governance (on-chain)

Currently, GitHub remains the primary development platform; however, governance is handled on-chain, requiring approval by the majority for all code changes. Here’s how this hybrid approach works.

**Software Update**
- Every update must be approved by an on-chain vote.
- Update proposals include the commit hash or binary hash.
- Only after on-chain approval is code recognized as the network version.
- A REST API is available for participants to verify which version is approved.
  
**Code Integrity**
- This repository serves as the primary codebase for blockchain development and contains the current production code.
- Code ownership and governance are separated. All proposed changes to this repository are subject to voting and approval.
- Participant nodes monitor the repository for unauthorized changes in the main branch of the repo.
- If an unapproved commit is detected, all network participants are notified immediately.

Read more on [Governance](https://gonka.ai/FAQ/#governance) and how [voting](https://gonka.ai/FAQ/#voting) works.

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

## Documentation guidelines

- All relevant docs are stored in [here](https://github.com/gonka-ai/gonka-docs)
- Update docs alongside code changes that affect behavior, APIs, or assumptions
- Missing docs may delay PR approval
- If you want to add documentation that covers third-party services or a very particular setup, consider publishing it in the [Show and Tell](https://github.com/gonka-ai/gonka/discussions/categories/show-and-tell) section in Discussions first

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
**Run Performance Test (Preferred):**
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

`--node_url` right now, all requests are going through that Transfer Agent.

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
