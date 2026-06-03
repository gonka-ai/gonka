# Gonka Network Development Roadmap

Gonka is building a decentralized AI inference network that connects distributed GPU resources with developers of AI applications.

The goal of the network is to give developers access to scalable inference on open models without dependence on centralized cloud providers, while giving hosts a clear way to contribute GPU capacity and earn rewards for useful work.

## Strategic horizons

Gonka is building toward three levels of scale.

### Horizon 1. Lead DeAI

The nearest goal is to become the leading decentralized AI compute network.

- largest H100-equivalent usable capacity among DeAI networks
- multi-model and multi-modality network
- real and reliable inference usage
- strong developer and contributor activation
- strong visibility among the AI infrastructure market, hosts, developers, and ecosystem partners.

### Horizon 2. Compete with neoclouds

The next goal is to compete with AI infrastructure and neocloud providers such as Nebius, CoreWeave, Lambda and similar platforms.

- 100K–250K H100 / accelerator-equivalent capacity
- hundreds of billions of inference tokens per day
- stable API, model catalog
- enterprise / private inference paths.

### North Star. Frontier-scale decentralized compute

The long-term direction for Gonka is to become a decentralized compute layer for frontier-scale AI infrastructure.

- 1M+ H100 / accelerator-equivalent capacity
- trillions of inference tokens per day
- low-cost inference at global scale
- trusted execution, verification, and privacy architecture
- ability to train, fine-tune, and serve Gonka-native models.

## Foundation

To reach these horizons the network needs a legal and operational entity that can turn the roadmap into funded, reviewed, delivered, and maintained work, and act as a formal counterparty for contracts where needed.

The Foundation should not replace governance. Protocol-facing changes still require technical review and governance alignment.

This document proposes the Foundation as a concept. It does not define or establish the final Foundation structure. The Foundation’s legal structure, mandate, composition, treasury scope and accountability model should be defined in a separate document and approved through a separate governance process.

The Gonka Foundation responsibilities:

- maintain the strategic roadmap and funding priorities;
- fund protocol-adjacent, ecosystem, research, and infrastructure teams;
- act as a legal counterparty for vendors, partners, and service providers that cannot work directly with a DAO;
- define milestones, acceptance criteria, support periods, and handoff requirements;
- coordinate technical review for protocol-facing work;
- support host growth, developer adoption, model onboarding, security, bridges, market access, enterprise readiness, and training research;
- publish transparent status for funded work.

## Roadmap tracks overview

- **Multi-model network:** faster rollout of relevant models, optimized images, and model-specific PoC / rewards.
- **Developer and AI agent access:** SDKs and adapters, OpenRouter / aggregator readiness, delegated wallets.
- **Hosts and compute capacity growth:** deployment utilities, maintenance windows, optimized node images, AI inference ASIC / accelerator readiness.
- **Network reliability and observability:** network status, incident console, alerts, and stability reports.
- **Protocol security, hardening, and abuse prevention:** attack registry, regression suite, pricing / incentive checks, and vulnerability disclosure.
- **Market access, liquidity, and bridges:** safe asset movement, DEX liquidity, and top-tier CEX readiness.
- **Public sandbox and consumer-GPU testnet:** an environment for testing protocol upgrades, models, integration, lower-cost / consumer-grade GPU operators.
- **Enterprise and privacy-sensitive workloads:** private inference gateway and TEE.
- **Model training on a distributed network:** primitives and staged experiments for distributed training workloads.
- **Market model:** market-based mechanisms for reward distribution and token pricing to align miner incentives with real usage.
- **Marketing and demand activation:** demand generation, developer adoption, host acquisition, ecosystem partnerships, and public proof of real usage.
- **Community development and internal coordination:** community research and coordination.

## Metrics affected by roadmap projects

Every project in the roadmap should deliver measurable value against at least one of these metrics:

- New compute capacity and host base growth
- Host retention and confidence
- Inference, developer activation and real network usage
- Network reliability and trust
- Inference scaling and usable capacity
- Enterprise readiness
- Lower support burden
- Model training and research readiness
- Market visibility and ecosystem trust

*Individual projects may also define track-specific metrics.*

> [!NOTE]
> This roadmap is intentionally written at the level of tracks and high-level projects.
>
> The projects listed here are not single implementation tasks. Each of them can later be broken down into concrete subprojects with tasks, technical scope, estimates, owners, timelines, and delivery plans.
>
> Future changes to roadmap tracks and projects should go through the Gonka Improvement Protocol process: public discussion, community feedback, a GitHub pull request showing the proposed changes to the document, and then a governance proposal to approve the update.

# Near-term priorities

In the near term, Gonka should focus on network stability, predictable operation, and real inference usage.

### Core stability and reliability

The main priority is to strengthen the core production path: inference availability, model-serving stability, PoC timing, validation, latency, uptime, and security.

This means that near-term development should prioritize bug fixes, reliability improvements, security, core inference quality, and operational clarity over lower-impact feature work.

### Economic alignment review

Economic alignment across supported models must be reviewed carefully. As Gonka scales to support multiple models, differences in demand, hardware requirements, PoC parameters, and reward logic should be balanced to prevent uneven incentives for hosts.

### Demand activation

At the same time, Gonka needs a clear demand activation plan to connect real API and inference consumers to the network.

The focus should include developer outreach, aggregator readiness, integrations with developer tools and agent frameworks, partner channels, public demos, technical examples, developer credits, and usage-based grants.

The plan should track real usage metrics such as daily tokens processed, active inference users, active applications, utilization, retained usage after credits, and consumer-to-host value flow.

# Mid- and long-term priorities

### Track 1. Multi-model network

Gonka should support models of different classes and modalities including text, coding, voice, image, and video workloads.

Developers get a wider model selection for their use cases. Hosts understand hardware requirements and the rules for participating in model groups.

#### Project 1. Model launch factory

Ready-to-use model launch packages: runtime configurations, optimized images, benchmark scenarios, hardware requirements, readiness criteria, and rollback checklist.

**Metrics:**

- **PRIMARY:** inference, developer activation and real network usage.
- **SECONDARY:** host retention and confidence.

**What this gives the network:**

Gonka can add the best relevant open LLMs faster and rely less on manual host-side launch work for each model. Developers get access to new open models sooner, and hosts get a clear way to deploy a model on suitable hardware.

#### Project 2. PoC, intents, weights, and rewards for model groups

Parameters and monitoring for PoC, model launch intents, weights, rewards, and penalties across different model-serving groups.

PoC benchmarks should maintain a strong correlation with real inference performance.

**Metrics:**

- **PRIMARY:** network reliability and trust.
- **SECONDARY:** host retention and confidence.

**What this gives the network:**

Gonka can support multiple models without breaking fairness, capacity planning, or reward logic. Hosts understand which model-serving groups they can participate in, what requirements apply, and how rewards and penalties are calculated for each group.

---

### Track 2. Developer and AI agent access

Gonka becomes easier to integrate through APIs, SDKs, common developer tools, routing aggregators, and AI agents.

#### Project 1. SDKs, adapters, and reference integrations

A set of SDKs, adapters, wrappers, sample apps, and compatibility tests for popular developer toolchains.

**Scope includes:**

- OpenAI-compatible SDK flows
- adapters / wrappers for selected AI frameworks, starting with LangChain / LangGraph
- integration with Vercel AI SDK or a similar popular app-layer SDK
- agent-runtime integrations
- coding-tool scenarios.

**Metrics:**

- **PRIMARY:** inference, developer activation and real network usage.

**What this gives the network:**

A developer can integrate Gonka through familiar tools and examples instead of learning the internal mechanics of the network.

#### Project 2. Adapter for OpenRouter / aggregators

An adapter for OpenRouter and/or a similar routing aggregation layer.

**Metrics:**

- **PRIMARY:** inference, developer activation and real network usage.

**What this gives the network:**

Gonka becomes available through a familiar entry point for developers, not only through direct integration. This lowers the barrier to first use and helps the network get real inference traffic faster.

#### Project 3. Delegated wallets and agent accounts

An API and service layer for delegated wallets, service accounts, and AI agents so that applications and agents can run continuously buying compute and calling inference.

**Metrics:**

- **PRIMARY:** inference, developer activation and real network usage.

**What this gives the network:**

AI agents and long-running services can buy compute and run inference within predefined limits without exposing the main key and without manual confirmation for every action. The application owner keeps control through limits, delegation revocation, and audit-trail review.

---

### Track 3. Hosts and compute capacity growth

Network growth, simple onboarding, and host retention.

#### Project 1. Hardware-neutral inference requirements

Gonka should avoid introducing protocol requirements that unnecessarily lock the network into one inference engine, hardware class, or execution model.

Future protocol, PoC, validation, privacy, and model-serving requirements should focus on inference quality rather than on a specific hardware type or implementation.

**Metrics:**

- **PRIMARY:** inference scaling and usable capacity.
- **SECONDARY:** new compute capacity and host base growth.

**What this gives the network:**

Gonka keeps a future path open for alternative inference engines and hardware classes that can meet the same quality, validation, and security requirements.

#### Project 2. Maintenance windows for hosts

The project should give a host a way to declare a maintenance window in advance, check whether the maintenance window is allowed, temporarily step out of part of its duties, and return to service without separate coordination and without being penalized for planned downtime.

**Metrics:**

- **PRIMARY:** host retention and confidence.
- **SECONDARY:** network reliability and trust.

**What this gives the network:**

The network gets a formal maintenance-window process that separates planned downtime from unplanned failures and reduces avoidable misses, penalties, and disputes.

#### Project 3. Optimized node images

A repeatable pipeline for optimized images, configs and production-ready host profiles.

The optimization process should focus on real inference performance, not only benchmark-specific PoC results. Optimized images and host profiles should be evaluated against realistic serving conditions, including encoding, decoding, different load profiles, and model-specific requirements.

**Metrics:**

- **PRIMARY:** inference scaling and usable capacity.
- **SECONDARY:** new compute capacity and host base growth; host retention and confidence.

**What this gives the network:**

Hosts get more useful inference out of the same hardware, and the network turns connected compute resources into real usable capacity.

---

### Track 4. Network reliability and observability

This track should help the network identify and resolve consensus failures, block production slowdowns, inference latency degradation, endpoint failures, recurring operational issues, and gaps in inference output quality or availability compared with centralized provider expectations.

#### Project 1. Network incident and stability center

A portal for network status, active incidents, known issues, incident runbooks, postmortem reports, and tasks created from recurring failures.

The scope should include settlement and validation status for failed, slow, or disputed inference sessions.

**Metrics:**

- **PRIMARY:** network reliability and trust.
- **SECONDARY:** lower support burden; host retention and confidence.

**What this gives the network:**

The network gets a clear incident process: an issue is recorded, assigned an owner, investigated, described, and turned into a task or regression check.

#### Project 2. External testing lab

An external testing lab for Gonka changes before they are rolled out broadly.

The lab should be operated by an external team with access to devnet and dedicated testing servers. It should test changes from protocol maintainers, funded external teams, and ecosystem contributors in realistic environments before they create risks for mainnet hosts or third-party integrations.

The purpose is to reduce the growing testing load on Protocol Maintainers and create a predictable coordination point for external teams.

**Metrics:**

- **PRIMARY:** network reliability and trust.
- **SECONDARY:** host retention and confidence.

**What this gives the network:**

Gonka gets an external testing layer for upcoming changes. Hosts get safer rollouts and fewer avoidable failures. Protocol Maintainers get additional testing capacity, structured reports, and clearer signals before wider rollout. Ecosystem teams get a place to check compatibility before changes affect users.

#### Project 3. Network documentation

A structured documentation system for hosts, developers, external contributors, and ecosystem teams.

The documentation should have a clear canonical source in a public GitHub documentation repository, where changes can be reviewed, versioned, and updated through PRs.

This project should be treated as an ongoing documentation maintenance process, not a one-time writing effort. It could support a documentation or technical writing team responsible for keeping the documentation up to date after protocol upgrades, incidents, recurring support questions, and operational changes.

**Metrics:**

- **PRIMARY:** network reliability and trust.
- **SECONDARY:** lower support burden; faster host onboarding.

**What this gives the network:**

Gonka gets a clear, maintained, and versioned knowledge base. Hosts and external teams get fewer ambiguous instructions and a safer path through upgrades and operational changes.

---

### Track 5. Protocol security, hardening, and abuse prevention

#### Project 1. Attack registry and regression check suite

A registry of known attack classes and automated checks that prevent these attacks from coming back in future versions.

**Metrics:**

- **PRIMARY:** network reliability and trust.
- **SECONDARY:** enterprise readiness; host retention and confidence.

**What this gives the network:**

Known attack classes and exploit scenarios become testable regression checks. The network does not repeat the same mistakes after upgrades, new model launches, or parameter changes.

#### Project 2. Economic and incentive attack simulator

A set of scenarios and simulations to test whether participants can gain an unfair advantage through pricing, rewards, validation, penalties, collateral, spam, or optimization against the mechanics.

**Metrics:**

- **PRIMARY:** network reliability and trust.

**What this gives the network:**

Pricing, rewards, penalties, and anti-abuse parameters are tested before they become expensive live-network problems, reducing the risk of mechanic gaming, unfair advantage, and false penalties for honest hosts.

#### Project 3. Vulnerability disclosure program

A public security process for reporting vulnerabilities: page, form, triage rules, severity categories, path from report to fix, and regression test.

**Metrics:**

- **PRIMARY:** network reliability and trust.
- **SECONDARY:** enterprise readiness.

**What this gives the network:**

External researchers and community participants get a safe reporting path without exposing exploit details in public chats.

---

### Track 6. Market access and liquidity, and bridges

Gonka needs a controlled path for token accessibility, liquidity, cross-chain asset movement, and exchange presence.

#### Project 1. DEX launch and top-tier CEX readiness

A controlled market-access plan for Gonka: first launch supported DEX liquidity, then prepare for top-tier CEX listings when the network has sufficient traction, liquidity, compliance readiness, and operational maturity.

**Metrics:**

- **PRIMARY:** network reliability and trust.

**What this gives the network:**

Gonka gets a clear exchange-access strategy and improves token accessibility.

#### Project 2. Cross-chain routes

Supported routes for moving assets between Gonka and external networks.

**Metrics:**

- **PRIMARY:** developer activation and real network usage.

**What this gives the network:**

The network gets official cross-chain routes for deposits and withdrawals, with defined asset mapping, transaction status, error handling, documentation, and operational monitoring.

#### Project 3. Contract security and cross-chain interop

A security review process for bridge- and interop-related contracts, configurations, and integrations. The scope includes threat modeling, internal or external review where needed, upgrade planning, regression checks after changes, and documented bridge-facing flows for integrators.

**Metrics:**

- **PRIMARY:** network reliability and trust.
- **SECONDARY:** enterprise readiness.

**What this gives the network:**

The network gets safer bridge and interop flows: fewer risky upgrades, clearer responsibility boundaries, and lower risk of economic attacks through cross-chain integrations.

---

### Track 7. Public sandbox and consumer-GPU testnet

Gonka gets safe environments for testing models, integrations, settings, Devshard scenarios, and consumer-GPU participation without risk to mainnet.

#### Project 1. Public testing sandbox

A separate test environment for experiments with models, parameters, and integrations without going to mainnet.

The sandbox should also support reproducible Devshard and protocol-level scenarios for testing upgrades, parameters, execution behavior, validation, and settlement before mainnet.

**Metrics:**

- **PRIMARY:** developer activation and real network usage.
- **SECONDARY:** network reliability and trust.

**What this gives the network:**

Developers and external teams can test models, integrations, and parameters without risk to mainnet. Hosts can try participation in the network before entering full production mode. Contributors and maintainers get a place for reproducible checks before changes are launched on the main network.

---

### Track 8. Enterprise and privacy-sensitive workloads

Gonka should support scenarios where a user or application does not want to reveal input data to the host.

#### Project 1. TEE-backed private inference

Private inference through a TEE-capable MLNode, where user input is decrypted only inside a protected execution environment.

The project should validate the architecture and threat model for TEE-backed inference.

**Metrics:**

- **PRIMARY:** enterprise readiness.
- **SECONDARY:** network reliability and trust; inference, developer activation and real network usage.

**What this gives the network:**

Gonka gets a verifiable path for private inference: a user can send a sensitive payload so that an ordinary host does not have access to the source data.

#### Project 2. Private inference access layer

An access layer and first pilot scenarios for private inference on top of TEE-capable nodes.

**Initial pilot scenarios:**

- private RAG
- secure document processing
- internal AI agents
- inference with sensitive user data
- enterprise API flow through TEE-capable nodes.

**Metrics:**

- **PRIMARY:** enterprise readiness.

**What this gives the network:**

Enterprise users get a practical path to start using Gonka for privacy-sensitive workloads.

---

### Track 9. Model training on a distributed GPU network

Gonka should develop training as a separate class of workloads on top of shard infrastructure.

#### Project 1. Training shard primitives

Basic infrastructure for a training shard: open shard, settle shard, node allocation, training containers, API containers, all-reduce, checkpointing, recovery, artifact exchange / storage, and monitoring.

**Metrics:**

- **PRIMARY:** model training and research readiness.

**What this gives the network:**

Gonka gets the basic infrastructure needed to test training workloads on distributed GPU capacity. This becomes the foundation for further training experiments.

#### Project 2. Decentralized training and AI safety research

A long-term research track for decentralized training methods and safety-oriented training experiments on distributed network capacity.

The scope includes low-communication distributed training, DiLoCo-like approaches, experiments with new architectures, geo-distributed training, Byzantine robustness, fraud tolerance, and validation methods for training progress.

**Metrics:**

- **PRIMARY:** model training and research readiness.

**What this gives the network:**

Gonka gets a research path for training beyond conventional tightly coupled GPU clusters. The network can test whether decentralized training methods can work under real-world constraints: limited communication, uneven hardware, participant churn, adversarial behavior, and trustless coordination.

---

### Track 10. Market model

Once inference demand and GPU utilization reach a healthy baseline, the network needs market-based mechanisms for reward distribution and token pricing to align miner incentives with real usage.

#### Project 1. Market-based reward distribution

Once the inference access bottleneck is fixed and GPUs are loaded with useful work at 40%+, introduce a market mechanism into reward distribution. On an empty network a market mechanism is too vulnerable to manipulation, so reward distribution by PoC weight is used until utilization thresholds are met; this project defines the trigger conditions, the transition path, and the runtime market logic that replaces pure PoC-weight allocation.

**Metrics:**

- **PRIMARY:** real network usage and GPU utilization.
- **SECONDARY:** network reliability and trust.

**What this gives the network:**

The network gets a reward mechanism that follows real demand instead of static weights, so emissions flow toward useful work rather than idle capacity.

#### Project 2. Demand-based per-model token pricing

Token price should differ per model and be determined by demand. For example, if 99.99% of demand is for Kimi while H-100 servers running Qwen sit idle, the price of Kimi tokens should rise and find balance, while the price of Qwen stays at 1 ngonka. This project defines the per-model pricing curve, the demand signals it reacts to, the price floor, and the update cadence.

**Metrics:**

- **PRIMARY:** real network usage.
- **SECONDARY:** network reliability and trust.

**What this gives the network:**

The network gets per-model pricing that reflects actual demand, which clears congestion on hot models and keeps cold models accessible at the floor price.

#### Project 3. Reward distribution proportional to work-cons earnings

Per-epoch reward distribution should be proportional to earnings in work-cons, so miners have a direct incentive to run the models that are actually in demand. This project defines how per-epoch rewards are computed from work-cons earnings, how the proportion is enforced, and how it interacts with PoC-weight distribution during the transition.

**Metrics:**

- **PRIMARY:** real network usage.
- **SECONDARY:** host retention and confidence.

**What this gives the network:**

The network gets miner incentives aligned with real demand, so capacity moves toward the models users actually want instead of being spread evenly across idle ones.

---

### Track 11. Marketing, demand activation, and ecosystem growth

Gonka requires a structured growth track to convert technical progress into demand and market confidence. Scaling beyond ad hoc, creator-led efforts, the network must establish repeatable marketing functions—including publications in credible platforms, podcasts, events, and creator programs—working with agencies and ambassadors to drive adoption.

The objective is measurable growth in developer activation, host confidence, and usage, proving Gonka is viable AI compute infrastructure.

Marketing experiments should begin with small budgets across multiple directions conducted by various Gonka supporters. The most successful initiatives should then receive increased budgets and be scaled into repeatable acquisition funnels.

#### Project 1. Target audience research

Quantitative and qualitative research:

- developers and AI teams that need inference;
- hosts and hardware suppliers that provide compute capacity;
- enterprise and ecosystem partners that can bring real workloads;
- protocol contributors;
- market participants and external observers that need clear evidence of traction, usage, and network maturity.

Identify primary segments, outreach channels, and requirements for Gonka development.

The scope should include outreach to target audience leveraging our network, relevant communities, media and AI influencers; open questions, 1-1 custdevs, identifying users who are actively interested in Gonka and involving them in experiments with Gonka, communicating its actual state and various abilities to participate.

**Metrics:**

- **PRIMARY:** people reached and research data collected, first successful cases we can tell in public, time to first valuable action.

**What this gives the network:**

Gonka gets a clearer understanding of who its real target users are, what they need, what prevents them from using the network, and which channels can reach them. This helps the roadmap focus on real demand, improves onboarding and communication, and creates the first validated public cases that can support further growth.

#### Project 2. Demand activation and developer usage

Programs for bringing real inference consumers to Gonka: developers, AI-agent builders, startups, AI application teams, API resellers, RAG platforms, coding tools, and other products that need external model access through an API.

The scope should include direct outreach to teams and relevant communities, paid acquisition tests, integrations with popular developer tools, hackathons, demo applications, technical tutorials, partner channels, developer credits, and usage-based grants. Credits and grants should be tied to measurable network usage: applications, adapters, benchmarks, public demos, integrations, and other work that creates real inference demand.

**Metrics:**

- **PRIMARY:** inference, developer activation and real network usage.
- **SECONDARY:** paid conversion after credits, retained usage after subsidies, active applications after 30 / 60 / 90 days, and repeat API usage.

**What this gives the network:**

Gonka gets a direct path from developer acquisition to real compute demand. Grants, credits, hackathons, and integrations become tools for creating measurable inference usage, not only community activity.

#### Project 3. Host growth and usable compute capacity

Programs for attracting compute supply from large GPU operators, AI clouds, data centers, mining companies, regional compute providers, idle GPU owners, university and research clusters, and partners working on ASICs or specialized accelerators.

The scope should include direct negotiations, host partnerships, infrastructure marketing, onboarding support, and targeted outreach to teams that already operate inference workloads. A separate focus should be placed on emerging markets and technology-sovereignty programs where Gonka can help local compute infrastructure monetize capacity and connect to the global AI compute market.

**Metrics:**

- **PRIMARY:** new compute capacity and host base growth.
- **SECONDARY:** inference scaling and usable capacity; host retention and confidence.

**What this gives the network:**

Gonka gets more usable capacity for supported models, not just more connected hardware. The network becomes more attractive to developers because it can serve real workloads, and more attractive to hosts because there is a clearer path from compute contribution to monetization.

#### Project 4. Project visibility through publications on credible platforms

The scope should include placements in tier-1 media, specialized infrastructure outlets, and analyst or research coverage.

Alongside earned media, this project should cover contributed articles and op-eds from Gonka contributors and partners, founder and engineer interviews in written and audio formats, case studies based on real network usage, and research notes or whitepapers that can be cited by third parties.

**Metrics:**

- **PRIMARY:** number and tier of publications on credible platforms.
- **SECONDARY:** search visibility for target queries on Google and AI search systems.

**What this gives the network:**

Publications on credible platforms make it easier to discuss Gonka on Reddit and other target forums with an established trust base. They also improve Gonka’s visibility and discoverability on Google, AI search systems, and across target audiences such as hosts, developers, partners, and infrastructure-focused communities.

#### Project 5. Ecosystem narrative, distribution, and public trust

A coordinated external distribution function for making Gonka understandable, visible, and credible across the AI infrastructure, DeAI, developer, host, investor, and institutional markets.

The scope should include media, podcasts, conference stages, public discussions, creator and ambassador programs, technical authors, analyst relations, institutional relationships.

This project should also build relationships with research organizations, developer communities, universities, AI labs, IT clusters, and institutions that can bring usage, compute supply, credibility, market visibility, or access to new regions.

**Metrics:**

- **PRIMARY:** community growth, market visibility, and network trust.
- **SECONDARY:** referral traffic, inbound partner interest, analyst mentions, backlinks, citations, media invitations, event invitations.

**What this gives the network:**

Gonka gets a consistent external presence and a stronger market narrative. Public information stays accurate, scam risk and misinterpretation decrease, and external audiences clearly understand why Gonka matters and how it differs from competing infrastructure. This establishes a durable trust layer that lowers the cost of every other marketing activity. 

Community discussions, ambassador outreach, partner negotiations, and sales conversations all become significantly easier when credible third-party coverage already exists and can be cited.

---

### Track 12. Community development and internal coordination

This track focuses on improving coordination inside the existing community, strengthening contributor interaction, understanding ecosystem participant needs, and supporting the long-term health of the Gonka ecosystem as the network grows.

#### Project 1. Community research and contributor insights

Research focused on understanding the needs, participation patterns, communication challenges, and long-term retention of ecosystem participants, including hosts, developers, ecosystem contributors, researchers, investors, and ecosystem supporters.

The scope should include contributor engagement and ecosystem participation, retention and disengagement factors, onboarding friction, communication bottlenecks and coordination issues, regional and language distribution, and factors that encourage long-term ecosystem participation and advocacy.

**Metrics:**

- **PRIMARY:** contributor retention and recurring ecosystem participation.
- **SECONDARY:** identification and resolution of major ecosystem coordination bottlenecks.

**What this gives the network:**

The network gains a clearer understanding of what keeps ecosystem participants engaged long-term, what creates friction, and where coordination, communication, or ecosystem support should be improved.

#### Project 2. Internal communication and coordination channels

This project focuses on improving ecosystem communication, coordination, and community-driven platforms to help participants exchange information, coordinate initiatives, and reduce communication fragmentation.

The scope may include support of existing ecosystem communication channels, knowledge-sharing formats across contributor groups, and contributor-driven media and communication formats such as blogs, podcasts, and ecosystem discussions.

**Metrics:**

- **PRIMARY:** reduction of ecosystem communication fragmentation.
- **SECONDARY:** feedback on ecosystem communication and coordination quality.

**What this gives the network:**

Contributors and ecosystem participants get clearer ways to exchange ideas, coordinate initiatives, discuss important ecosystem topics, and collaborate without relying only on fragmented chat discussions.

---

## Rules for external teams and funded work

**Open source by default.**

Unless there is a strong reason otherwise, the work should live in a public repo and be maintainable by the community.

**Support period is part of the scope.**

A project is not considered done at the first release. Stabilization, support, and updates after the initial release are required.

**Handoff package is required.**

The team must describe how another team can maintain or extend the result.

**Protocol-facing changes require review by Protocol Maintainers.**

Anything that touches PoC, validation, rewards, penalties, fees, mainnet behavior, supported models, settlement, bridge-facing flows, or delegated wallets must go through review / testing.

**Closed operational dependencies should not be the default.**

Critical network infrastructure should not depend by default on a closed SaaS controlled exclusively by a single external team.
