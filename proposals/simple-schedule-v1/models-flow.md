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

## Related Documentation

### Model Registration Flow (`models-registration.md`)
Provides a comprehensive overview of the complete model registration lifecycle from genesis initialization through epoch group assignment. Covers the four-phase process: genesis model initialization, dynamic governance-driven model registration, participant model discovery through hardware node synchronization, and hierarchical epoch group organization. This document focuses on the end-to-end flow of how models enter the system and become available for inference routing.

### MLNode Hardware Lifecycle (`models-for-mlnode.md`)
Details the technical implementation of MLNode (Hardware Node) management including registration, configuration, and blockchain synchronization. Covers MLNode configuration sources, broker registration processes, runtime node addition, automatic synchronization with blockchain state, and model ID propagation mechanisms. This document provides deep implementation details on how participant hardware capabilities and model support are discovered, validated, and maintained in the system.

### Inference Model Usage (`models-for-inference.md`)
Explains how model names are used throughout the inference execution and validation pipeline. Covers inference request routing, model name consistency requirements, executor selection based on exact string matching, and the three-phase model-aware validation system (private validator selection, validation publication, and compliance verification). This document focuses on runtime model usage during actual inference operations and validation processes.