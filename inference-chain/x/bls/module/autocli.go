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
				{
					RpcMethod: "BeaconKey",
					Use:       "beacon-key",
					Short:     "Query the block beacon key epoch, pending key candidate and activation",
				},
				{
					RpcMethod: "BlockBeacon",
					Use:       "block-beacon [round]",
					Short:     "Query the stored block beacon record of a round (OK with σ, or a miss)",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "round"},
					},
				},
				{
					RpcMethod: "LatestBlockBeacon",
					Use:       "latest-block-beacon",
					Short:     "Query the newest OK block beacon record",
				},
				{
					RpcMethod: "PendingBeacon",
					Use:       "pending-beacon [round]",
					Short:     "Query the block beacon message validators sign for a round",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "round"},
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
				// this line is used by ignite scaffolding # autocli/tx
			},
		},
	}
}
