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
					RpcMethod:      "GetInferencesWithExecutors",
					Use:            "get-inferences-with-executors [ids]",
					Short:          "Query get-inferences-with-executors",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "ids"}},
				},

				{
					RpcMethod:      "GetRandomExecutor",
					Use:            "get-random-executor",
					Short:          "Query get-random-executor",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
				},

				{
					RpcMethod:      "InferenceParticipant",
					Use:            "inference-participant [address]",
					Short:          "Query inference-participant",
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
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "pocStartBlockHeight"}},
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
					RpcMethod: "EpochGroupValidationsAll",
					Use:       "list-epoch-group-validations",
					Short:     "List all epochGroupValidations",
				},
				{
					RpcMethod:      "EpochGroupValidations",
					Use:            "show-epoch-group-validations [id]",
					Short:          "Shows a epochGroupValidations",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "participant"}, {ProtoField: "pocStartBlockHeight"}},
				},
				{
					RpcMethod:      "PocBatchesForStage",
					Use:            "poc-batches-for-stage [block-height]",
					Short:          "Query pocBatchesForStage",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "blockHeight"}},
				},

				{
					RpcMethod:      "GetCurrentEpoch",
					Use:            "get-current-epoch",
					Short:          "Query getCurrentEpoch",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{},
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
					RpcMethod:      "StartInference",
					Use:            "start-inference [inference-id] [prompt-hash] [prompt-payload] [received-by]",
					Short:          "Send a startInference tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "inferenceId"}, {ProtoField: "promptHash"}, {ProtoField: "promptPayload"}, {ProtoField: "requestedBy"}},
				},
				{
					RpcMethod:      "FinishInference",
					Use:            "finish-inference [inference-id] [response-hash] [response-payload] [prompt-token-count] [completion-token-count] [executed-by]",
					Short:          "Send a finishInference tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "inferenceId"}, {ProtoField: "responseHash"}, {ProtoField: "responsePayload"}, {ProtoField: "promptTokenCount"}, {ProtoField: "completionTokenCount"}, {ProtoField: "executedBy"}},
				},
				{
					RpcMethod:      "SubmitNewParticipant",
					Use:            "submit-new-participant [url] [models]",
					Short:          "Send a submitNewParticipant tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "url"}, {ProtoField: "models"}},
				},
				{
					RpcMethod:      "Validation",
					Use:            "validation [id] [inference-id] [response-payload] [response-hash] [value]",
					Short:          "Send a validation tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "id"}, {ProtoField: "inferenceId"}, {ProtoField: "responsePayload"}, {ProtoField: "responseHash"}, {ProtoField: "value"}},
				},
				{
					RpcMethod:      "SubmitPoC",
					Use:            "submit-poc [block-height] [nonce]",
					Short:          "Send a submit-poc tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "blockHeight"}, {ProtoField: "nonce"}},
				},
				{
					RpcMethod:      "SubmitNewUnfundedParticipant",
					Use:            "submit-new-unfunded-participant [address] [url] [models] [pub-key] [validator-key]",
					Short:          "Send a submitNewUnfundedParticipant tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "address"}, {ProtoField: "url"}, {ProtoField: "models"}, {ProtoField: "pubKey"}, {ProtoField: "validatorKey"}},
				},
				{
					RpcMethod:      "InvalidateInference",
					Use:            "invalidate-inference [inference-id]",
					Short:          "Send a invalidateInference tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "inferenceId"}},
				},
				{
					RpcMethod:      "RevalidateInference",
					Use:            "revalidate-inference [inference-id]",
					Short:          "Send a revalidateInference tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "inferenceId"}},
				},
				{
					RpcMethod:      "ClaimRewards",
					Use:            "claim-rewards [seed] [poc-start-height]",
					Short:          "Send a claimRewards tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "seed"}, {ProtoField: "pocStartHeight"}},
				},
				{
					RpcMethod:      "SubmitPocBatch",
					Use:            "submit-poc-batch [poc-stage-start-block-height] [nonces] [dist]",
					Short:          "Send a SubmitPocBatch tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "pocStageStartBlockHeight"}, {ProtoField: "nonces"}, {ProtoField: "dist"}},
				},
				{
					RpcMethod:      "SubmitPocValidation",
					Use:            "submit-poc-validation [participant-address] [poc-stage-start-block-height] [nonces] [dist] [received-dist] [r-target] [fraud-threshold] [n-invalid] [probability-honest] [fraud-detected]",
					Short:          "Send a SubmitPocValidation tx",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "participantAddress"}, {ProtoField: "pocStageStartBlockHeight"}, {ProtoField: "nonces"}, {ProtoField: "dist"}, {ProtoField: "receivedDist"}, {ProtoField: "rTarget"}, {ProtoField: "fraudThreshold"}, {ProtoField: "nInvalid"}, {ProtoField: "probabilityHonest"}, {ProtoField: "fraudDetected"}},
				},
				// this line is used by ignite scaffolding # autocli/tx
			},
		},
	}
}
