package training

import (
	"context"
	cosmosclient "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	networknodev1 "github.com/product-science/chain-protos/go/network_node/v1"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

type Server struct {
	networknodev1.UnimplementedNetworkNodeServiceServer
	cosmosClient cosmosclient.CosmosMessageClient
}

func NewServer(cosmosClient cosmosclient.CosmosMessageClient) *Server {
	return &Server{
		cosmosClient: cosmosClient,
	}
}

// Implement a few key methods first:

func (s *Server) SetStoreRecord(ctx context.Context, req *networknodev1.SetStoreRecordRequest) (*networknodev1.SetStoreRecordResponse, error) {
	if req.Record == nil {
		return &networknodev1.SetStoreRecordResponse{
			Status: networknodev1.StoreRecordStatusEnum_SET_RECORD_ERROR,
		}, nil
	}

	logging.Info("SetStoreRecord called", types.Training, "key", req.Record.Key, "value", req.Record.Value)

	msg := &inference.MsgSubmitTrainingKvRecord{
		Creator:     s.cosmosClient.GetAddress(),
		Participant: s.cosmosClient.GetAddress(),
		TaskId:      999, // PRTODO: add task id to request
		Key:         req.Record.Key,
		Value:       req.Record.Value,
	}
	txResponse, err := s.cosmosClient.SendTransaction(msg)
	if err != nil {
		logging.Error("Failed to send transaction", types.Training, "error", err)
		return &networknodev1.SetStoreRecordResponse{
			Status: networknodev1.StoreRecordStatusEnum_SET_RECORD_ERROR,
		}, err
	}

	response := inference.MsgSubmitTrainingKvRecordResponse{}
	if err = cosmosclient.WaitForResponse(*s.cosmosClient.GetContext(), s.cosmosClient.GetCosmosClient(), txResponse.TxHash, &response); err != nil {
		logging.Error("Failed to get transaction response", types.Training, "error", err)
		return &networknodev1.SetStoreRecordResponse{
			Status: networknodev1.StoreRecordStatusEnum_SET_RECORD_ERROR,
		}, err
	}

	logging.Info("MsgSubmitTrainingKvRecordResponse received", types.Training)

	return &networknodev1.SetStoreRecordResponse{
		Status: networknodev1.StoreRecordStatusEnum_SET_RECORD_OK,
	}, nil
}

func (s *Server) GetStoreRecord(ctx context.Context, req *networknodev1.GetStoreRecordRequest) (*networknodev1.GetStoreRecordResponse, error) {
	logging.Info("GetStoreRecord called", types.Training, "key", req.Key)

	queryClient := s.cosmosClient.NewInferenceQueryClient()
	request := &types.QueryTrainingKvRecordRequest{
		TaskId:      999, // PRTODO: add task id to request
		Participant: s.cosmosClient.GetAddress(),
		Key:         req.Key,
	}
	resp, err := queryClient.TrainingKvRecord(ctx, request)
	if err != nil {
		logging.Error("Failed to get training kv record", types.Training, "error", err)
		return nil, err
	}

	logging.Info("GetStoreRecord response", types.Training, "record", resp.Record)

	return &networknodev1.GetStoreRecordResponse{
		Record: &networknodev1.Record{
			Key:   resp.Record.Key,
			Value: resp.Record.Value,
		},
	}, nil
}
