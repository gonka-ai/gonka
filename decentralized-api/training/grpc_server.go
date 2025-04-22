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
	inference.UnimplementedNetworkNodeServiceServer
	cosmosClient cosmosclient.CosmosMessageClient
}

/*
	grpcurl -plaintext \
	  localhost:9003 \
	  list

	grpcurl -plaintext \
	  -d '{"run_id": "1", "record":{"key":"foo","value":"bar"}}' \
	  localhost:9003 \
	  inference.inference.NetworkNodeService/SetStoreRecord

	grpcurl -plaintext \
	  -d '{"run_id": "1", "key":"foo"}' \
	  localhost:9003 \
	  inference.inference.NetworkNodeService/GetStoreRecord

	grpcurl -plaintext \
	  -d '{"run_id": "1"}' \
	  localhost:9003 \
	  inference.inference.NetworkNodeService/ListStoreKeys
*/
func NewServer(cosmosClient cosmosclient.CosmosMessageClient) *Server {
	return &Server{
		cosmosClient: cosmosClient,
	}
}

// Implement a few key methods first:

func (s *Server) SetStoreRecord(ctx context.Context, req *inference.SetStoreRecordRequest) (*inference.SetStoreRecordResponse, error) {
	if req.Record == nil {
		return &inference.SetStoreRecordResponse{
			Status: inference.StoreRecordStatusEnum_SET_RECORD_ERROR,
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
	response := inference.MsgSubmitTrainingKvRecordResponse{}

	err = cosmosclient.SendTransactionBlocking(ctx, s.cosmosClient, msg, &response)
	if err != nil {
		logging.Error("Failed to send transaction", types.Training, "error", err)
		return nil, err
	}

	logging.Info("MsgSubmitTrainingKvRecordResponse received", types.Training)

	return &inference.SetStoreRecordResponse{
		Status: inference.StoreRecordStatusEnum_SET_RECORD_OK,
	}, nil
}

func (s *Server) GetStoreRecord(ctx context.Context, req *inference.GetStoreRecordRequest) (*inference.GetStoreRecordResponse, error) {
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

	return &inference.GetStoreRecordResponse{
		Record: &inference.Record{
			Key:   resp.Record.Key,
			Value: resp.Record.Value,
		},
	}, nil
}

func (s *Server) ListStoreKeys(ctx context.Context, req *inference.StoreListKeysRequest) (*inference.StoreListKeysResponse, error) {
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

	return &inference.StoreListKeysResponse{
		Keys: resp.Keys,
	}, nil
}

func (s *Server) JoinTraining(ctx context.Context, req *inference.JoinTrainingRequest) (*inference.MLNodeTrainStatus, error) {
	msg := inference.MsgJoinTraining{
		Creator: s.cosmosClient.GetAddress(),
		Req:     req,
	}
	resp := inference.MsgJoinTrainingResponse{}
	err := cosmosclient.SendTransactionBlocking(ctx, s.cosmosClient, &msg, &resp)
	if err != nil {
		logging.Error("Failed to send transaction", types.Training, "error", err)
		return nil, err
	}

	return resp.Status, nil
}

func (s *Server) GetJoinTrainingStatus(context.Context, *inference.JoinTrainingRequest) (*inference.MLNodeTrainStatus, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetJoinTrainingStatus not implemented")
}
func (s *Server) SendHeartbeat(context.Context, *inference.HeartbeatRequest) (*inference.HeartbeatResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SendHeartbeat not implemented")
}
func (s *Server) GetAliveNodes(context.Context, *inference.GetAliveNodesRequest) (*inference.GetAliveNodesResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetAliveNodes not implemented")
}
func (s *Server) SetBarrier(context.Context, *inference.SetBarrierRequest) (*inference.SetBarrierResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SetBarrier not implemented")
}
func (s *Server) GetBarrierStatus(context.Context, *inference.GetBarrierStatusRequest) (*inference.GetBarrierStatusResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetBarrierStatus not implemented")
}
