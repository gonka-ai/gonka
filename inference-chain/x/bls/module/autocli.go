package bls

import (
	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"

	modulev1 "github.com/productscience/inference/api/inference/bls"
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
					RpcMethod: "EpochBLSData",
					Use:       "epoch-data [epoch_id]",
					Short:     "Query BLS DKG data for a specific epoch",
					Long:      "Query complete BLS distributed key generation data for a specific epoch including participants, phase, dealer parts, verification submissions, and group public key",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "epoch_id"},
					},
				},
				{
					RpcMethod: "SigningStatus",
					Use:       "signing-status [request_id]",
					Short:     "Query the status of a threshold signing request",
					Long:      "Query the current status and details of a threshold signing request by its request ID",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "request_id"},
					},
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
					RpcMethod: "RequestThresholdSignature",
					Use:       "request-threshold-signature [current_epoch_id] [chain_id] [request_id] [data...]",
					Short:     "Request a threshold signature from the BLS module",
					Long:      "Request a threshold signature from the BLS module for the given data. The request_id must be a unique identifier for this request.",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "current_epoch_id"},
						{ProtoField: "chain_id"},
						{ProtoField: "request_id"},
						{ProtoField: "data", Varargs: true},
					},
				},
				// this line is used by ignite scaffolding # autocli/tx
			},
		},
	}
}
