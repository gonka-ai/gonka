.PHONY: proto test integration-test stress-test fault-test

proto:
	PATH="$$HOME/go/bin:$$PATH" protoc --proto_path=proto --go_out=. --go_opt=module=subnet proto/subnet/v1/*.proto

test:
	go test ./... -v

integration-test:
	go test ./user/ -tags stress -run 'TestStress|TestFault' -v -timeout 30m -count=1

stress-test:
	go test ./user/ -tags stress -run TestStress -v -timeout 30m -count=1

fault-test:
	go test ./user/ -tags stress -run TestFault -v -timeout 10m -count=1
