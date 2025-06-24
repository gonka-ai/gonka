@ -0,0 +1,176 @@
# Model Management Flow in the Inference System

## Overview

The inference system implements a sophisticated model management architecture that enables decentralized AI inference across multiple models. The system supports dynamic model registration, participant assignment based on model capabilities, and efficient routing of inference requests to appropriate executors. Models flow through the system from genesis initialization through runtime execution, with participants organized into hierarchical epoch groups for optimal resource allocation.

## System Components

### Core Chain Components

**Model Storage Module**
- **Location**: `inference-chain/x/inference/keeper/model.go`
- **Functions**: `SetModel`, `GetAllModels`
- **Purpose**: Manages model persistence in the blockchain state using key-value storage with model ID as the primary key

**Model Message Server**
- **Location**: `inference-chain/x/inference/keeper/msg_server_register_model.go`
- **Function**: `RegisterModel`
- **Purpose**: Handles governance-based model registration transactions, validating authority permissions before storing new models

**Genesis Module**
- **Location**: `inference-chain/x/inference/module/genesis.go`
- **Functions**: `InitGenesis`, `ExportGenesis`, `getModels`
- **Purpose**: Initializes models during blockchain startup and handles genesis state export for network upgrades

### Controller/API Components

**Admin Model Registration Handler**
- **Location**: `decentralized-api/internal/server/admin/register_model_handler.go`
- **Function**: `registerModel`
- **Purpose**: Provides HTTP endpoint for submitting model registration proposals through the governance system

**Node Broker System**
- **Location**: `decentralized-api/broker/broker.go`
- **Functions**: `registerNode`, `getNodes`, `convertInferenceNodeToHardwareNode`
- **Purpose**: Manages local ML node configurations and model capabilities, converting between internal node representations and blockchain-compatible formats

**Participant Registration Module**
- **Location**: `decentralized-api/participant_registration/participant_registration.go`
- **Functions**: `getUniqueModels`, `registerGenesisParticipant`, `registerJoiningParticipant`
- **Purpose**: Discovers supported models from managed ML nodes during participant registration and submits this information to the blockchain

### Epoch Group Management

**EpochGroup Core**
- **Location**: `inference-chain/x/inference/epochgroup/epoch_group.go`
- **Functions**: `AddMember`, `CreateSubGroup`, `GetSubGroup`, `addToModelGroups`, `memberSupportsModel`
- **Purpose**: Implements hierarchical epoch groups with parent groups containing all participants and model-specific sub-groups containing only participants supporting particular models

**Random Executor Selection**
- **Location**: `inference-chain/x/inference/epochgroup/random.go`
- **Function**: `GetRandomMemberForModel`
- **Purpose**: Provides weighted random selection of participants from model-specific sub-groups for inference request routing

**Hardware Node Management**
- **Location**: `inference-chain/x/inference/keeper/hardware_node.go`
- **Functions**: `SetHardwareNodes`, `GetHardwareNodes`, `GetHardwareNodesForParticipants`
- **Purpose**: Stores and retrieves participant hardware capabilities including supported model lists

## Model Lifecycle Flow

### Phase 1: Genesis Initialization

During blockchain initialization, the genesis configuration defines the foundational models available in the network. The `GenesisState` structure in the inference module (`inference-chain/x/inference/types/genesis.pb.go`) contains a `ModelList` field populated from the genesis JSON configuration. Each genesis model includes a unique identifier, computational cost estimates measured in units of compute per token, and genesis authority attribution.

The `InitGenesis` function in the genesis module (`inference-chain/x/inference/module/genesis.go`) processes these models, validating that all genesis models have the correct authority attribution and storing them in the blockchain state. This establishes the baseline model catalog that all network participants can reference and support.

### Phase 2: Dynamic Model Registration

After network launch, new models are introduced through a governance-driven process. Participants with model registration permissions submit proposals through the admin API endpoint. The `registerModel` handler (`decentralized-api/internal/server/admin/register_model_handler.go`) constructs a `MsgRegisterModel` transaction containing the proposed model details and wraps it in a governance proposal with appropriate metadata including title, summary, and voting parameters.

The governance proposal flows through the standard Cosmos SDK governance process where network stakeholders vote on model acceptance. Upon approval, the `RegisterModel` message server (`inference-chain/x/inference/keeper/msg_server_register_model.go`) processes the transaction, validating the authority permissions and persisting the new model to the blockchain state using the `SetModel` keeper function (`inference-chain/x/inference/keeper/model.go`).

### Phase 3: Participant Model Discovery

When participants join the network, they undergo a model discovery process to determine which models their infrastructure can support. The registration system queries the `NodeBroker` (`decentralized-api/broker/broker.go`) to enumerate all managed ML nodes and extracts the unique model identifiers from each node's configuration using the `getUniqueModels` function (`decentralized-api/participant_registration/participant_registration.go`).

For genesis participants, this model information is included directly in the `MsgSubmitNewParticipant` transaction via the `registerGenesisParticipant` function (`decentralized-api/participant_registration/participant_registration.go`). Joining participants send their model capabilities to existing network nodes through the seed API mechanism using the `registerJoiningParticipant` function, which relays the registration information to the blockchain.

The discovered models are stored as part of the participant's hardware node information in the `HardwareNodes` structure (`inference-chain/x/inference/types/hardware_node.pb.go`), creating a persistent mapping between participants and their supported model capabilities through the `SetHardwareNodes` function (`inference-chain/x/inference/keeper/hardware_node.go`).

### Phase 4: Epoch Group Assignment

During epoch transitions, the system organizes participants into hierarchical groups based on their model support. The `setModelsForParticipants` function in the module (`inference-chain/x/inference/module/module.go`) processes each active participant, retrieving their hardware node information using `GetHardwareNodesForParticipants` (`inference-chain/x/inference/keeper/hardware_node.go`) and extracting all supported models using the `getAllModels` utility function.

The epoch group system creates a two-level hierarchy: a parent epoch group containing all active participants regardless of model support, and model-specific sub-groups containing only participants supporting particular models. When adding members to epoch groups, the `AddMember` function (`inference-chain/x/inference/epochgroup/epoch_group.go`) automatically creates sub-groups for each model the participant supports and adds them to the appropriate sub-groups using the `addToModelGroups` function.

The `CreateSubGroup` function (`inference-chain/x/inference/epochgroup/epoch_group.go`) handles sub-group creation, checking for existing sub-groups in memory and persistent state before creating new ones. The sub-group creation process involves establishing a new `EpochGroup` instance with model-specific metadata, creating the underlying Cosmos SDK group, and updating the parent group's sub-group model list.

## Participant Assignment Flow

### Model Capability Assessment

The system continuously tracks participant model capabilities through hardware node updates. When participants modify their ML infrastructure, they submit `MsgSubmitHardwareDiff` transactions containing added, modified, or removed hardware nodes. The `SubmitHardwareDiff` message server (`inference-chain/x/inference/keeper/msg_server_submit_hardware_diff.go`) processes these updates, maintaining current mappings between participants and their supported models.

The hardware node structure (`inference-chain/x/inference/types/hardware_node.pb.go`) includes a `models` field listing all AI models that particular node can execute. This granular tracking enables precise participant assignment based on specific model requirements rather than broad capability categories.

**Important: While hardware node changes are immediately recorded on the blockchain, they only become effective for participant assignment and inference request routing during the next epoch transition when new epoch groups are formed through the `setModelsForParticipants` function.**

### Hierarchical Group Organization

The epoch group system implements intelligent participant organization through model-aware sub-grouping. The `EpochGroup` structure (`inference-chain/x/inference/epochgroup/epoch_group.go`) maintains both persistent state through `EpochGroupData` and in-memory caching through the `subGroups` map for efficient sub-group access.

When participants are added to epoch groups, the system evaluates their model support and automatically assigns them to relevant sub-groups. The `memberSupportsModel` function (`inference-chain/x/inference/epochgroup/epoch_group.go`) validates whether a participant's model list includes the target model for a specific sub-group, ensuring accurate assignment.

The sub-group assignment process is recursive-safe, with explicit model filtering to prevent infinite recursion when adding members to model-specific groups. Each sub-group maintains its own membership, weights, and validation parameters while inheriting authority and configuration from the parent group.

### Dynamic Group Management

As the network evolves, the epoch group system adapts to changing participant capabilities and model availability. The `GetSubGroup` function (`inference-chain/x/inference/epochgroup/epoch_group.go`) provides lazy sub-group creation, establishing new model-specific groups as needed when participants supporting previously unsupported models join the network.

The system maintains consistency between in-memory sub-group caches and persistent blockchain state through the `GroupDataKeeper` interface (`inference-chain/x/inference/epochgroup/interfaces.go`). Sub-groups are automatically persisted during creation and retrieved from state during system restarts or cross-participant operations.

## Model-Specific Operations

### Inference Request Routing

When inference requests arrive for specific models through the public API endpoint (`decentralized-api/internal/server/public/post_chat_handler.go`), the system employs the hierarchical epoch group structure for efficient executor selection. The `postChat` function handles incoming requests and delegates to `handleTransferRequest` for new inference requests, which calls `getExecutorForRequest` to obtain an appropriate executor.

The `getExecutorForRequest` function (`decentralized-api/internal/server/public/post_chat_handler.go`) queries the blockchain for each inference request using `queryClient.GetRandomExecutor()` with the specified model. This triggers the `GetRandomExecutor` keeper function (`inference-chain/x/inference/keeper/query_get_random_executor.go`) which receives model-specific requests and delegates to the `GetRandomMemberForModel` function (`inference-chain/x/inference/epochgroup/random.go`) in the epoch group system.

The routing logic first identifies whether the request targets a specific model. For model-specific requests on parent epoch groups, the system automatically delegates to the appropriate model sub-group. The `GetSubGroup` function retrieves or creates the necessary sub-group, and the `GetRandomMember` function (`inference-chain/x/inference/epochgroup/random.go`) performs weighted random selection from eligible participants.

This routing approach ensures that inference requests are only directed to participants with verified capability to execute the requested model, improving success rates and reducing network overhead from failed assignments. **Note: Changes to participant model capabilities through `MsgSubmitHardwareDiff` transactions are recorded on-chain immediately but only take effect for inference request routing when the next epoch group is formed during epoch transitions.**

### Executor Validation

The system implements multiple validation layers to ensure executor appropriateness for model-specific requests. The participant selection process filters candidates based on hardware node status, requiring nodes to be in INFERENCE status rather than POC, TRAINING, or other non-inference modes through the `GetHardwareNodesForParticipants` function (`inference-chain/x/inference/keeper/hardware_node.go`).

Additional validation occurs through the model support verification in `memberSupportsModel`, which compares the requested model against the participant's declared model capabilities. This prevents routing requests to participants who have lost model support due to infrastructure changes.

### Load Distribution

The weighted random selection mechanism in `selectRandomParticipant` (`inference-chain/x/inference/epochgroup/random.go`) uses participant PoC weights to distribute inference load proportionally to demonstrated computational capability. The `computeCumulativeArray` function (`inference-chain/x/inference/epochgroup/random.go`) creates weight-based probability distributions that favor higher-performing participants while maintaining fairness through randomization.

This approach balances network efficiency by preferring capable participants with quality assurance through distributed assignment, preventing single-participant monopolization of inference requests while rewarding superior performance with increased assignment probability.

## Integration Points

### Chain-Controller Communication

The model management system bridges blockchain state with off-chain execution infrastructure through well-defined interfaces. The controller components query blockchain state for current model catalogs and participant assignments while maintaining local node configurations that determine actual execution capabilities.

The `CosmosMessageClient` interface (`decentralized-api/cosmos/client.go`) provides standardized blockchain interaction for model-related operations, including proposal submission, model queries, and hardware node updates. This abstraction enables controller components to interact with blockchain state without direct chain integration complexity.

### ML Node Integration

Individual ML nodes integrate with the model management system through the `NodeBroker` abstraction layer (`decentralized-api/broker/broker.go`). The broker manages node lifecycle including registration, configuration updates, and status reporting while translating between node-specific model representations and standardized blockchain formats through the `convertInferenceNodeToHardwareNode` function.

The `InferenceNodeConfig` structure (`decentralized-api/broker/types.go`) defines the contract between controllers and ML nodes, specifying model configurations with associated arguments and hardware requirements. This configuration drives both local node management and blockchain registration processes.

### Governance Integration

Model registration leverages the Cosmos SDK governance framework for decentralized decision-making on network model additions. The `SubmitProposal` function in the governance client (`decentralized-api/cosmos/governance.go`) creates properly formatted governance proposals with model registration messages as content.

The governance integration ensures that model additions undergo community review and approval rather than unilateral controller decisions, maintaining network integrity and preventing malicious or inappropriate model introduction.

## Operational Considerations

### Model Lifecycle Management

The system supports complete model lifecycle management from initial registration through deprecation and removal. While the current implementation focuses on model addition through the `RegisterModel` function (`inference-chain/x/inference/keeper/msg_server_register_model.go`), the architecture supports future extension for model versioning, deprecation marking, and removal processes through additional governance mechanisms.

Model metadata including computational cost estimates enables economic modeling and resource planning at both participant and network levels. These estimates inform pricing mechanisms and participant planning for infrastructure scaling based on expected model usage patterns through the `Model` structure (`inference-chain/x/inference/types/model.pb.go`).

### Performance Optimization

The hierarchical epoch group architecture optimizes inference request routing performance by pre-organizing participants into model-specific groups. This eliminates runtime model capability checking and enables efficient participant selection through pre-computed membership lists maintained by the `EpochGroup` structure.

The in-memory sub-group caching in `EpochGroup` instances (`inference-chain/x/inference/epochgroup/epoch_group.go`) reduces blockchain queries for frequent model-specific operations while maintaining consistency through periodic state synchronization and explicit cache invalidation during epoch transitions.

### Scalability Considerations

The model management system scales efficiently with both model count and participant count through its hierarchical organization. Model-specific sub-groups grow independently, preventing cross-model interference and enabling specialized optimization for particular model types or computational requirements through the `CreateSubGroup` function.

The weighted selection mechanism in `GetRandomMemberForModel` scales logarithmically with participant count per model rather than linearly with total network size, maintaining consistent performance as the network grows and diversifies across multiple model types and computational specializations. 