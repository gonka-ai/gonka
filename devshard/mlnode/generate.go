package mlnode

//go:generate protoc --go_out=gen --go_opt=paths=source_relative --go-grpc_out=gen --go-grpc_opt=paths=source_relative -I../../decentralized-api/nodemanager ../../decentralized-api/nodemanager/nodemanager.proto
