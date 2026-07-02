package inference

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	modulev1 "github.com/productscience/inference/api/inference/inference"
)

// AutoCLIOptions implements the autocli.HasAutoCLIConfig interface.
func (am AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: modulev1.Query_ServiceDesc.ServiceName,
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "Params",
					Use:       "params",
					Short:     "Shows the parameters of the module",
				},
				{
					RpcMethod: "InferenceAll",
					Use:       "list-inference",
					Short:     "List all inference",
				},
				{
					RpcMethod:      "Inference",
					Use:            "show-inference [id]",
					Short:          "Shows a inference",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "index"}},
				},
				{
					RpcMethod: "ParticipantAll",
					Use:       "list-participant",
					Short:     "List all participant",
				},
				{
					RpcMethod:      "Participant",
					Use:            "show-participant [id]",
					Short:          "Shows a participant",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "index"}},
				},
				{
					RpcMethod:      "GetRandomExecutor",
					Use:            "get-random-executor",
					Short:          "Query get-random-executor",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},

				{
					RpcMethod:      "AccountByAddress",
					Use:            "account-by-address [address]",
					Short:          "Query account public key and balance by address",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "address"}},
				},

				{
					RpcMethod: "EpochGroupDataAll",
					Use:       "list-epoch-group-data",
					Short:     "List all epochGroupData",
				},
				{
					RpcMethod:      "EpochGroupData",
					Use:            "show-epoch-group-data [id]",
					Short:          "Shows a epochGroupData",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "epoch_index"}},
				},
				{
					RpcMethod: "SettleAmountAll",
					Use:       "list-settle-amount",
					Short:     "List all settleAmount",
				},
				{
					RpcMethod:      "SettleAmount",
					Use:            "show-settle-amount [id]",
					Short:          "Shows a settleAmount",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "participant"}},
				},
				{
					RpcMethod:      "EpochGroupValidations",
					Use:            "show-epoch-group-validations [id]",
					Short:          "Shows a epochGroupValidations",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "participant"}, {ProtoField: "epoch_index"}},
				},
				{
					RpcMethod:      "PocBatchesForStage",
					Use:            "poc-batches-for-stage [block-height]",
					Short:          "Query pocBatchesForStage",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "block_height"}},
				},
				{
					RpcMethod:      "PocValidationsForStage",
					Use:            "poc-validations-for-stage [block-height]",
					Short:          "Query pocValidationsForStage",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "block_height"}},
				},

				{
					RpcMethod:      "GetCurrentEpoch",
					Use:            "get-current-epoch",
					Short:          "Query getCurrentEpoch",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod: "TokenomicsData",
					Use:       "show-tokenomics-data",
					Short:     "show tokenomics_data",
				},
				{
					RpcMethod:      "GetUnitOfComputePriceProposal",
					Use:            "get-unit-of-compute-price-proposal",
					Short:          "Query get-unit-of-compute-price-proposal",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},

				{
					RpcMethod:      "CurrentEpochGroupData",
					Use:            "current-epoch-group-data",
					Short:          "Query CurrentEpochGroupData",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},

				{
					RpcMethod:      "ModelsAll",
					Use:            "models-all",
					Short:          "Query modelsAll",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},

				{
					RpcMethod: "InferenceTimeoutAll",
					Use:       "list-inference-timeout",
					Short:     "List all inference_timeout",
				},
				{
					RpcMethod:      "InferenceTimeout",
					Use:            "show-inference-timeout [id]",
					Short:          "Shows a inference_timeout",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "expirationHeight"}, {ProtoField: "inferenceId"}},
				},
				{
					RpcMethod:      "HardwareNodesAll",
					Use:            "hardware-nodes-all",
					Short:          "Query hardware-nodes-all",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "HardwareNodesAll",
					Use:            "hardware-nodes-all",
					Short:          "Query hardware-nodes-all",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},

				{
					RpcMethod: "InferenceValidationDetailsAll",
					Use:       "list-inference-validation-details",
					Short:     "List all inference_validation_details",
				},
				{
					RpcMethod:      "InferenceValidationDetails",
					Use:            "show-inference-validation-details [id]",
					Short:          "Shows a inference_validation_details",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "epochId"}, {ProtoField: "inferenceId"}},
				},
				{
					RpcMethod:      "GetInferenceValidationParameters",
					Use:            "get-inference-validation-parameters [ids] [requester]",
					Short:          "Query GetInferenceValidationParameters",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "ids"}, {ProtoField: "requester"}},
				},

				{
					RpcMethod: "EpochPerformanceSummaryAll",
					Use:       "list-epoch-performance-summary",
					Short:     "List all epoch_performance_summary",
				},
				{
					RpcMethod:      "EpochPerformanceSummary",
					Use:            "show-epoch-performance-summary [epoch-index]",
					Short:          "Shows all epoch_performance_summary records for a given epoch",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "epoch_index"}},
				},
				{
					RpcMethod:      "EpochPerformanceSummaryByParticipant",
					Use:            "show-epoch-performance-summary-by-participant [epoch-index] [participant-id]",
					Short:          "Shows a epoch_performance_summary record for a specific participant and epoch",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "epoch_index"}, {ProtoField: "participantId"}},
				},
				{
					RpcMethod:      "GetParticipantCurrentStats",
					Use:            "get-participant-current-stats [participant-id]",
					Short:          "Query get_participant_current_stats",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "participantId"}},
				},

				{
					RpcMethod:      "GetAllParticipantCurrentStats",
					Use:            "get-all-participant-current-stats",
					Short:          "Query get_all_participant_current_stats",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},

				{
					RpcMethod:      "GetMinimumValidationAverage",
					Use:            "get-minimum-validation-average",
					Short:          "Query get_minimum_validation_average",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},

				{
					RpcMethod: "PartialUpgradeAll",
					Use:       "list-partial-upgrade",
					Short:     "List all partial_upgrade",
				},
				{
					RpcMethod:      "PartialUpgrade",
					Use:            "show-partial-upgrade [id]",
					Short:          "Shows a partial_upgrade",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "height"}},
				},
				{
					RpcMethod:      "LastUpgradeHeight",
					Use:            "last-upgrade-height",
					Short:          "Shows the most recent applied upgrade height",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "GetAllModelCapacities",
					Use:            "all-model-capacities",
					Short:          "Get cached capacities for all models",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},

				{
					RpcMethod:      "CountPoCbatchesAtHeight",
					Use:            "count-po-c-batches-at-height [block-height]",
					Short:          "Query countPoCBatchesAtHeight",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "blockHeight"}},
				},
				{
					RpcMethod:      "CountPoCvalidationsAtHeight",
					Use:            "count-po-c-validations-at-height [block-height]",
					Short:          "Query countPoCValidationsAtHeight",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "blockHeight"}},
				},

				// Dynamic pricing queries
				{
					RpcMethod:      "GetModelPerTokenPrice",
					Use:            "model-per-token-price [model-id]",
					Short:          "Get current per-token price for a specific model",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "model_id"}},
				},
				{
					RpcMethod:      "GetAllModelPerTokenPrices",
					Use:            "all-model-per-token-prices",
					Short:          "Get current per-token prices for all models",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "GetModelCapacity",
					Use:            "model-capacity [model-id]",
					Short:          "Get cached capacity for a specific model",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "model_id"}},
				},
				// PoC v2 off-chain commit queries
				{
					RpcMethod:      "PoCV2StoreCommit",
					Use:            "poc-v2-store-commit [poc-stage-start-block-height] [participant-address]",
					Short:          "Query PoC v2 store commit for a participant",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "poc_stage_start_block_height"}, {ProtoField: "participant_address"}},
				},
				{
					RpcMethod:      "MLNodeWeightDistribution",
					Use:            "mlnode-weight-distribution [poc-stage-start-block-height] [participant-address]",
					Short:          "Query MLNode weight distribution for a participant",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "poc_stage_start_block_height"}, {ProtoField: "participant_address"}},
				},
				{
					RpcMethod:      "AllPoCV2StoreCommitsForStage",
					Use:            "all-poc-v2-store-commits [poc-stage-start-block-height]",
					Short:          "Query all PoC v2 store commits for a stage",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "poc_stage_start_block_height"}},
				},
				// Confirmation PoC queries
				{
					RpcMethod:      "ListConfirmationPoCEvents",
					Use:            "list-confirmation-poc-events [epoch-index]",
					Short:          "Query all confirmation PoC events for an epoch",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "epoch_index"}},
				},
				{
					RpcMethod:      "DevshardEscrow",
					Use:            "show-devshard-escrow [id]",
					Short:          "Query a devshard escrow by ID",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "id"}},
				},
				{
					RpcMethod:      "MaintenanceCredit",
					Use:            "maintenance-credit [participant]",
					Short:          "Query maintenance credit for a participant",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "participant"}},
				},
				{
					RpcMethod:      "MaintenanceScheduled",
					Use:            "maintenance-scheduled [participant]",
					Short:          "Query scheduled maintenance windows for a participant",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "participant"}},
				},
				{
					RpcMethod:      "MaintenanceActive",
					Use:            "maintenance-active",
					Short:          "Query all currently active maintenance windows",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "MaintenanceStatus",
					Use:            "maintenance-status [participant]",
					Short:          "Query maintenance status for a participant",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "participant"}},
				},
				{
					RpcMethod:      "MaintenanceConcurrency",
					Use:            "maintenance-concurrency [target-height]",
					Short:          "Query concurrent reserved participant count and power at a given height",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "height"}},
				},
				{
					RpcMethod:      "MaintenanceSchedulability",
					Use:            "maintenance-schedulability [participant] [start-height] [duration-blocks]",
					Short:          "Query whether a proposed maintenance window is schedulable",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "participant"}, {ProtoField: "start_height"}, {ProtoField: "duration_blocks"}},
				},
				{
					RpcMethod:      "PoCDelegation",
					Use:            "poc-delegation [participant] [model-id]",
					Short:          "Query PoC delegation state for a participant",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "participant"}, {ProtoField: "model_id", Optional: true}},
				},
				{
					RpcMethod:      "EstimateBitcoinReward",
					Use:            "estimate-bitcoin-reward [participant]",
					Short:          "Estimate Bitcoin-style reward for a participant using the current reward snapshot",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "participant"}},
				},
				{
					RpcMethod:      "ListClaimRecipients",
					Use:            "list-claim-recipients [participant]",
					Short:          "List the scheduled per-epoch claim recipient overrides for a participant",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "participant"}},
				},
				// this line is used by ignite scaffolding # autocli/query
			},
		},
		Tx: &autocliv1.ServiceCommandDescriptor{
			Service:              modulev1.Msg_ServiceDesc.ServiceName,
			EnhanceCustomCommand: true, // only required if you want to use the custom command
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "UpdateParams",
					Skip:      true, // skipped because authority gated
				},
				{
					RpcMethod: "RegisterTokenMetadata",
					Skip:      true, // skipped because authority gated
				},
				{
					RpcMethod: "ApproveBridgeTokenForTrading",
					Skip:      true, // skipped because authority gated
				},
				{
					RpcMethod: "RegisterLiquidityPool",
					Skip:      true, // skipped because authority gated
				},
				{
					RpcMethod: "ApproveIbcTokenForTrading",
					Skip:      true, // skipped because authority gated
				},
				{
					RpcMethod: "RegisterIbcTokenMetadata",
					Skip:      true, // skipped because authority gated
				},
				{
					RpcMethod:      "StartInference",
					Use:            "start-inference [inference-id] [prompt-hash] [prompt-payload] [received-by]",
					Short:          "Send a startInference tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "inference_id"}, {ProtoField: "prompt_hash"}, {ProtoField: "prompt_payload"}, {ProtoField: "requested_by"}},
				},
				{
					RpcMethod:      "FinishInference",
					Use:            "finish-inference [inference-id] [response-hash] [response-payload] [prompt-token-count] [completion-token-count] [executed-by]",
					Short:          "Send a finishInference tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "inference_id"}, {ProtoField: "response_hash"}, {ProtoField: "response_payload"}, {ProtoField: "prompt_token_count"}, {ProtoField: "completion_token_count"}, {ProtoField: "executed_by"}},
				},
				{
					RpcMethod:      "SubmitNewParticipant",
					Use:            "submit-new-participant [url]",
					Short:          "Send a submitNewParticipant tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "url"}},
				},
				{
					RpcMethod:      "Validation",
					Use:            "validation [id] [inference-id] [response-payload] [response-hash] [value]",
					Short:          "Send a validation tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "id"}, {ProtoField: "inference_id"}, {ProtoField: "response_payload"}, {ProtoField: "response_hash"}, {ProtoField: "value"}},
				},
				{
					RpcMethod:      "SubmitNewUnfundedParticipant",
					Use:            "submit-new-unfunded-participant [address] [url] [pub-key] [validator-key]",
					Short:          "Send a submitNewUnfundedParticipant tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "address"}, {ProtoField: "url"}, {ProtoField: "pub_key"}, {ProtoField: "validator_key"}},
				},
				{
					RpcMethod:      "InvalidateInference",
					Use:            "invalidate-inference [inference-id]",
					Short:          "Send a invalidateInference tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "inference_id"}},
				},
				{
					RpcMethod:      "RevalidateInference",
					Use:            "revalidate-inference [inference-id]",
					Short:          "Send a revalidateInference tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "inference_id"}},
				},
				{
					RpcMethod:      "ClaimRewards",
					Use:            "claim-rewards [seed] [poc-start-height]",
					Short:          "Send a claimRewards tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "seed"}, {ProtoField: "epoch_index"}},
				},
				{
					RpcMethod:      "SubmitPocBatch",
					Use:            "submit-poc-batch [poc-stage-start-block-height] [nonces] [dist]",
					Short:          "Send a SubmitPocBatch tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "poc_stage_start_block_height"}, {ProtoField: "nonces"}, {ProtoField: "dist"}},
				},
				{
					RpcMethod:      "SubmitSeed",
					Use:            "submit-seed [block-height] [signature]",
					Short:          "Send a submit-seed tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "epoch_index"}, {ProtoField: "signature"}},
				},
				{
					RpcMethod:      "SubmitUnitOfComputePriceProposal",
					Use:            "submit-unit-of-compute-price-proposal [price]",
					Short:          "Send a submit-unit-of-compute-price-proposal tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "price"}},
				},
				{
					RpcMethod:      "SubmitHardwareDiff",
					Use:            "submit-hardware-diff",
					Short:          "Send a SubmitHardwareDiff tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},
				{
					RpcMethod:      "CreatePartialUpgrade",
					Use:            "create-partial-upgrade [height] [node-version] [api-binaries-json]",
					Short:          "Send a create_partial_upgrade tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "height"}, {ProtoField: "nodeVersion"}, {ProtoField: "apiBinariesJson"}},
				},
				{
					RpcMethod:      "RequestBridgeMint",
					Use:            "request-bridge-mint [amount] [destination-address] [target-chain-id]",
					Short:          "Request minting of WGNK tokens on Ethereum by bridging native Gonka",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "amount"}, {ProtoField: "destination_address"}, {ProtoField: "chain_id"}},
				},
				{
					RpcMethod:      "CreateDevshardEscrow",
					Use:            "create-devshard-escrow [amount] [model-id]",
					Short:          "Create a devshard escrow",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "amount"}, {ProtoField: "model_id"}},
				},
				{
					RpcMethod: "SettleDevshardEscrow",
					Skip:      true,
				},
				{
					RpcMethod:      "SetPoCDelegation",
					Use:            "set-poc-delegation [model-id] [delegate-to]",
					Short:          "Set PoC delegation for a model",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "model_id"}, {ProtoField: "delegate_to"}},
				},
				{
					RpcMethod:      "RefusePoCDelegation",
					Use:            "refuse-poc-delegation [model-id]",
					Short:          "Refuse PoC delegation for a model",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "model_id"}},
				},
				{
					RpcMethod:      "DeclarePoCIntent",
					Use:            "declare-poc-intent [model-id]",
					Short:          "Declare intent to deploy for a model",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "model_id"}},
				},
				// this line is used by ignite scaffolding # autocli/tx
			},
		},
	}
}
