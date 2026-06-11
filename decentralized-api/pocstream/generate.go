package pocstream

//go:generate protoc -I ../../proto --go_out=gen --go_opt=paths=source_relative --go-grpc_out=gen --go-grpc_opt=paths=source_relative poc_callback_stream.proto
