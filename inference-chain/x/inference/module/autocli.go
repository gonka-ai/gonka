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
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{{ProtoField: "inferenceId"}, {ProtoField: "promptHash"}, {ProtoField: "promptPayload"}, {ProtoField: "receivedBy"}},
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
				// this line is used by ignite scaffolding # autocli/tx
			},
		},
	}
}
