## Model Registration Flow

### Phase 1: Genesis Initialization

During blockchain initialization, the genesis configuration defines the foundational models available in the network. The `GenesisState` structure in the inference module (`inference-chain/x/inference/types/genesis.pb.go`) contains a `ModelList` field populated from the genesis JSON configuration. Each genesis model includes a unique identifier, computational cost estimates measured in units of compute per token, and genesis authority attribution.

The `InitGenesis` function in the genesis module (`inference-chain/x/inference/module/genesis.go`) processes these models, validating that all genesis models have the correct authority attribution and storing them in the blockchain state. This establishes the baseline model catalog that all network participants can reference and support.

### Phase 2: Dynamic Model Registration

After network launch, new models are introduced through a governance-driven process. Participants with model registration permissions submit proposals through the admin API endpoint. The `registerModel` handler (`decentralized-api/internal/server/admin/register_model_handler.go`) constructs a `MsgRegisterModel` transaction containing the proposed model details and wraps it in a governance proposal with appropriate metadata including title, summary, and voting parameters.

The governance proposal flows through the standard Cosmos SDK governance process where network stakeholders vote on model acceptance. Upon approval, the `RegisterModel` message server (`inference-chain/x/inference/keeper/msg_server_register_model.go`) processes the transaction, validating the authority permissions and persisting the new model to the blockchain state using the `SetModel` keeper function (`inference-chain/x/inference/keeper/model.go`).

### Phase 3: Participant Model Discovery

When participants join the network, they undergo a model discovery process to determine which models their infrastructure can support. The registration system queries the `NodeBroker` (`decentralized-api/broker/broker.go`) to enumerate all managed ML nodes and extracts the unique model identifiers from each node's configuration using the `getUniqueModels` function (`decentralized-api/participant_registration/participant_registration.go`). Model capabilities are not transmitted during initial participant registration for either genesis or joining participants.

Model capabilities reach the blockchain through a separate process: All participants, whether genesis or joining, rely on the automated hardware node synchronization process managed by the `nodeSyncWorker` in the broker. This background process runs every 60 seconds and submits `MsgSubmitHardwareDiff` transactions containing the participant's current MLNode configurations and their supported models. The `SubmitHardwareDiff` message server (`inference-chain/x/inference/keeper/msg_server_submit_hardware_diff.go`) processes these updates, maintaining current mappings between participants and their supported models.

The discovered models are eventually stored as part of the participant's hardware node information in the `HardwareNodes` structure (`inference-chain/x/inference/types/hardware_node.pb.go`), through `models` field listing all AI models that particular node can execute, creating a persistent mapping between participants and their supported model capabilities through the `SetHardwareNodes` function (`inference-chain/x/inference/keeper/hardware_node.go`).

While hardware node changes are immediately recorded on the blockchain, they only become effective for participant assignment and inference request routing during the next epoch transition when new epoch groups are formed through the `setModelsForParticipants` function.

### Phase 4: Epoch Group Assignment

During epoch transitions, the system organizes participants into hierarchical groups based on their model support. The `setModelsForParticipants` function in the module (`inference-chain/x/inference/module/module.go`) processes each active participant, retrieving their hardware node information using `GetHardwareNodesForParticipants` (`inference-chain/x/inference/keeper/hardware_node.go`) and extracting all supported models using the `getAllModels` utility function.

The epoch group system creates a two-level hierarchy: a parent epoch group containing all active participants regardless of model support, and model-specific sub-groups containing only participants supporting particular models. The epoch group system implements intelligent participant organization through model-aware sub-grouping. The `EpochGroup` structure (`inference-chain/x/inference/epochgroup/epoch_group.go`) maintains both persistent state through `EpochGroupData` and in-memory caching through the `subGroups` map for efficient sub-group access.

When participants are added to epoch groups, the system evaluates their model support and automatically assigns them to relevant sub-groups. The `memberSupportsModel` function (`inference-chain/x/inference/epochgroup/epoch_group.go`) validates whether a participant's model list includes the target model for a specific sub-group, ensuring accurate assignment.

Each sub-group maintains its own membership, weights, and validation parameters while inheriting authority and configuration from the parent group.

When adding members to epoch groups, the `AddMember` function (`inference-chain/x/inference/epochgroup/epoch_group.go`) automatically creates sub-groups for each model the participant supports and adds them to the appropriate sub-groups using the `addToModelGroups` function.

The `CreateSubGroup` function (`inference-chain/x/inference/epochgroup/epoch_group.go`) handles sub-group creation, checking for existing sub-groups in memory and persistent state before creating new ones. The sub-group creation process involves establishing a new `EpochGroup` instance with model-specific metadata, creating the underlying group, and updating the parent group's sub-group model list.
