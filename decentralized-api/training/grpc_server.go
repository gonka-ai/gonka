package training

import (
	"context"
	cosmosclient "decentralized-api/cosmosclient"
	"decentralized-api/logging"

	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

type Server struct {
	inference.UnimplementedNetworkNodeServiceServer
	cosmosClient cosmosclient.CosmosMessageClient
	executor     *Executor
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
func NewServer(cosmosClient cosmosclient.CosmosMessageClient, executor *Executor) *Server {
	return &Server{
		cosmosClient: cosmosClient,
		executor:     executor,
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

	msg := &inference.MsgSubmitTrainingKvRecord{
		Creator:     s.cosmosClient.GetAccountAddress(),
		Participant: s.cosmosClient.GetAccountAddress(),
		TaskId:      req.RunId,
		Key:         req.Record.Key,
		Value:       req.Record.Value,
	}
	response := inference.MsgSubmitTrainingKvRecordResponse{}

	err := s.cosmosClient.SendTransactionSyncNoRetry(msg, &response)
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

	request := &types.QueryTrainingKvRecordRequest{
		TaskId: req.RunId,
		Key:    req.Key,
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

	queryClient := s.cosmosClient.NewInferenceQueryClient()
	resp, err := queryClient.ListTrainingKvRecordKeys(ctx, &types.QueryListTrainingKvRecordKeysRequest{
		TaskId: req.RunId,
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
		Creator: s.cosmosClient.GetAccountAddress(),
		Req:     req,
	}
	resp := inference.MsgJoinTrainingResponse{}
	err := s.cosmosClient.SendTransactionSyncNoRetry(&msg, &resp)
	if err != nil {
		logging.Error("Failed to send transaction", types.Training, "error", err)
		return nil, err
	}

	return resp.Status, nil
}

func (s *Server) GetJoinTrainingStatus(ctx context.Context, req *inference.JoinTrainingRequest) (*inference.MLNodeTrainStatus, error) {
	msg := inference.MsgJoinTrainingStatus{
		Creator: s.cosmosClient.GetAccountAddress(),
		Req:     req,
	}
	resp := inference.MsgJoinTrainingStatusResponse{}
	err := s.cosmosClient.SendTransactionSyncNoRetry(&msg, &resp)
	if err != nil {
		logging.Error("Failed to send transaction", types.Training, "error", err)
		return nil, err
	}

	return resp.Status, nil
}

func (s *Server) SendHeartbeat(ctx context.Context, req *inference.HeartbeatRequest) (*inference.HeartbeatResponse, error) {
	logging.Info("SendHeartbeat called", types.Training, "req", req)

	// TODO: executor.Heartbeat(...)
	// TODO: probably call it unconditionally. Even if transaction fails

	msg := inference.MsgTrainingHeartbeat{
		Creator: s.cosmosClient.GetAccountAddress(),
		Req:     req,
	}
	resp := inference.MsgTrainingHeartbeatResponse{}
	err := s.cosmosClient.SendTransactionSyncNoRetry(&msg, &resp)
	if err != nil {
		logging.Error("Failed to send transaction", types.Training, "error", err)
		return nil, err
	}

	return resp.Resp, nil
}

func (s *Server) GetAliveNodes(ctx context.Context, req *inference.GetAliveNodesRequest) (*inference.GetAliveNodesResponse, error) {
	logging.Info("GetAliveNodes called", types.Training)

	queryClient := s.cosmosClient.NewInferenceQueryClient()
	queryReq := &types.QueryTrainingAliveNodesRequest{
		Req: &types.GetAliveNodesRequest{
			RunId:     req.RunId,
			OuterStep: req.OuterStep,
		},
	}

	resp, err := queryClient.TrainingAliveNodes(ctx, queryReq)
	if err != nil {
		logging.Error("Failed to get alive nodes", types.Training, "error", err)
		return nil, err
	}

	logging.Info("GetAliveNodes response", types.Training, "resp", resp)

	return &inference.GetAliveNodesResponse{
		AliveNodes: resp.Resp.AliveNodes,
	}, nil
}

func (s *Server) SetBarrier(ctx context.Context, req *inference.SetBarrierRequest) (*inference.SetBarrierResponse, error) {
	logging.Info("SetBarrier called", types.Training)

	msg := inference.MsgSetBarrier{
		Creator: s.cosmosClient.GetAccountAddress(),
		Req:     req,
	}
	resp := inference.MsgSetBarrierResponse{}
	err := s.cosmosClient.SendTransactionSyncNoRetry(&msg, &resp)
	if err != nil {
		logging.Error("Failed to send transaction", types.Training, "error", err)
		return nil, err
	}
	return resp.Resp, nil
}

func (s *Server) GetBarrierStatus(ctx context.Context, req *inference.GetBarrierStatusRequest) (*inference.GetBarrierStatusResponse, error) {
	logging.Info("GetBarrierStatus called", types.Training)

	queryClient := s.cosmosClient.NewInferenceQueryClient()
	queryReq := &types.QueryTrainingBarrierRequest{
		Req: &types.GetBarrierStatusRequest{
			BarrierId: req.BarrierId,
			RunId:     req.RunId,
			OuterStep: req.OuterStep,
		},
	}
	resp, err := queryClient.TrainingBarrier(ctx, queryReq)
	if err != nil {
		logging.Error("Failed to get training barrier status", types.Training, "error", err)
		return nil, err
	}

	logging.Info("GetBarrierStatus response", types.Training, "resp", resp)

	return &inference.GetBarrierStatusResponse{
		AllReady:   resp.Resp.AllReady,
		NotReady:   resp.Resp.NotReady,
		AliveNodes: resp.Resp.AliveNodes,
	}, nil
}
