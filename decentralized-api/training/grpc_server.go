package training

import (
	"context"
	"decentralized-api/logging"
	networknodev1 "github.com/product-science/chain-protos/go/network_node/v1"
	"github.com/productscience/inference/x/inference/types"
)

// Server implements the NetworkNodeService interface
type Server struct {
	networknodev1.UnimplementedNetworkNodeServiceServer
	// Add any fields you need for your server state here
	// For example:
	// store    map[string]string
	// nodes    map[string]bool
	// mu       sync.RWMutex
}

// NewServer creates a new Server instance
func NewServer() *Server {
	return &Server{
		// Initialize your server state here
		// For example:
		// store: make(map[string]string),
		// nodes: make(map[string]bool),
	}
}

// Implement a few key methods first:

func (s *Server) SetStoreRecord(ctx context.Context, req *networknodev1.SetStoreRecordRequest) (*networknodev1.SetStoreRecordResponse, error) {
	if req.Record == nil {
		return &networknodev1.SetStoreRecordResponse{
			Status: networknodev1.StoreRecordStatusEnum_SET_RECORD_ERROR,
		}, nil
	}

	// Add your logic here
	// For example:
	// s.mu.Lock()
	// s.store[req.Record.Key] = req.Record.Value
	// s.mu.Unlock()
	logging.Info("SetStoreRecord called", types.Training, "key", req.Record.Key, "value", req.Record.Value)
	return &networknodev1.SetStoreRecordResponse{
		Status: networknodev1.StoreRecordStatusEnum_SET_RECORD_OK,
	}, nil
}

func (s *Server) GetStoreRecord(ctx context.Context, req *networknodev1.GetStoreRecordRequest) (*networknodev1.GetStoreRecordResponse, error) {
	// Add your logic here
	// For example:
	// s.mu.RLock()
	// value, exists := s.store[req.Key]
	// s.mu.RUnlock()
	// if !exists {
	//     return nil, status.Error(codes.NotFound, "key not found")
	// }

	logging.Info("GetStoreRecord called", types.Training, "key", req.Key)

	return &networknodev1.GetStoreRecordResponse{
		Record: &networknodev1.Record{
			Key:   req.Key,
			Value: "example value", // Replace with actual value
		},
	}, nil
}
