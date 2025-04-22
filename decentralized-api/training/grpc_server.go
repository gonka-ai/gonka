package training

import (
	"context"
	cosmosclient "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strconv"
)

type Server struct {
	types.UnimplementedNetworkNodeServiceServer
	cosmosClient cosmosclient.CosmosMessageClient
}

/*
	grpcurl -plaintext \
	  -protoset network_node.pb \
	  -d '{"record":{"key":"foo","value":"bar"}}' \
	  localhost:9003 \
	  network_node.v1.NetworkNodeService/SetStoreRecord

	grpcurl -plaintext \
	  -protoset network_node.pb \
	  -d '{"key":"some‑key"}' \
	  localhost:9003 \
	  network_node.v1.NetworkNodeService/GetStoreRecord
*/
func NewServer(cosmosClient cosmosclient.CosmosMessageClient) *Server {
	return &Server{
		cosmosClient: cosmosClient,
	}
}

// Implement a few key methods first:

func (s *Server) SetStoreRecord(ctx context.Context, req *types.SetStoreRecordRequest) (*types.SetStoreRecordResponse, error) {
	if req.Record == nil {
		return &types.SetStoreRecordResponse{
			Status: types.StoreRecordStatusEnum_SET_RECORD_ERROR,
		}, nil
	}

	logging.Info("SetStoreRecord called", types.Training, "key", req.Record.Key, "value", req.Record.Value)

	taskId, err := strconv.ParseUint(req.RunId, 10, 64)
	if err != nil {
		logging.Error("Failed to parse task id", types.Training, "error", err)
		return nil, err
	}

	msg := &inference.MsgSubmitTrainingKvRecord{
		Creator:     s.cosmosClient.GetAddress(),
		Participant: s.cosmosClient.GetAddress(),
		TaskId:      taskId,
		Key:         req.Record.Key,
		Value:       req.Record.Value,
	}
	txResponse, err := s.cosmosClient.SendTransaction(msg)
	if err != nil {
		logging.Error("Failed to send transaction", types.Training, "error", err)
		return &types.SetStoreRecordResponse{
			Status: types.StoreRecordStatusEnum_SET_RECORD_ERROR,
		}, err
	}

	response := inference.MsgSubmitTrainingKvRecordResponse{}
	if err = cosmosclient.WaitForResponse(*s.cosmosClient.GetContext(), s.cosmosClient.GetCosmosClient(), txResponse.TxHash, &response); err != nil {
		logging.Error("Failed to get transaction response", types.Training, "error", err)
		return &types.SetStoreRecordResponse{
			Status: types.StoreRecordStatusEnum_SET_RECORD_ERROR,
		}, err
	}

	logging.Info("MsgSubmitTrainingKvRecordResponse received", types.Training)

	return &types.SetStoreRecordResponse{
		Status: types.StoreRecordStatusEnum_SET_RECORD_OK,
	}, nil
}

func (s *Server) GetStoreRecord(ctx context.Context, req *types.GetStoreRecordRequest) (*types.GetStoreRecordResponse, error) {
	logging.Info("GetStoreRecord called", types.Training, "key", req.Key)

	taskId, err := strconv.ParseUint(req.RunId, 10, 64)
	if err != nil {
		logging.Error("Failed to parse task id", types.Training, "error", err)
		return nil, err
	}

	request := &types.QueryTrainingKvRecordRequest{
		TaskId:      taskId,
		Participant: s.cosmosClient.GetAddress(),
		Key:         req.Key,
	}
	queryClient := s.cosmosClient.NewInferenceQueryClient()
	resp, err := queryClient.TrainingKvRecord(ctx, request)
	if err != nil {
		logging.Error("Failed to get training kv record", types.Training, "error", err)
		return nil, err
	}

	logging.Info("GetStoreRecord response", types.Training, "record", resp.Record)

	return &types.GetStoreRecordResponse{
		Record: &types.Record{
			Key:   resp.Record.Key,
			Value: resp.Record.Value,
		},
	}, nil
}

func (s *Server) ListStoreKeys(ctx context.Context, req *types.StoreListKeysRequest) (*types.StoreListKeysResponse, error) {
	logging.Info("ListStoreKeys called", types.Training, "key")

	taskId, err := strconv.ParseUint(req.RunId, 10, 64)
	if err != nil {
		logging.Error("Failed to parse task id", types.Training, "error", err)
		return nil, err
	}

	queryClient := s.cosmosClient.NewInferenceQueryClient()
	resp, err := queryClient.ListTrainingKvRecordKeys(ctx, &types.QueryListTrainingKvRecordKeysRequest{
		TaskId:      taskId,
		Participant: s.cosmosClient.GetAddress(),
	})
	if err != nil {
		logging.Error("Failed to get training kv record keys", types.Training, "error", err)
		return nil, err
	}

	return &types.StoreListKeysResponse{
		Keys: resp.Keys,
	}, nil
}

func (s *Server) JoinTraining(context.Context, *types.JoinTrainingRequest) (*types.MLNodeTrainStatus, error) {
	return nil, status.Errorf(codes.Unimplemented, "method JoinTraining not implemented")
}
func (s *Server) GetJoinTrainingStatus(context.Context, *types.JoinTrainingRequest) (*types.MLNodeTrainStatus, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetJoinTrainingStatus not implemented")
}
func (s *Server) SendHeartbeat(context.Context, *types.HeartbeatRequest) (*types.HeartbeatResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SendHeartbeat not implemented")
}
func (s *Server) GetAliveNodes(context.Context, *types.GetAliveNodesRequest) (*types.GetAliveNodesResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetAliveNodes not implemented")
}
func (s *Server) SetBarrier(context.Context, *types.SetBarrierRequest) (*types.SetBarrierResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SetBarrier not implemented")
}
func (s *Server) GetBarrierStatus(context.Context, *types.GetBarrierStatusRequest) (*types.GetBarrierStatusResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetBarrierStatus not implemented")
}
