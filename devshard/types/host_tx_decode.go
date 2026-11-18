package types

import "google.golang.org/protobuf/proto"

func UnmarshalHostTx(b []byte, tx *DevshardTx) error {
	return proto.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(b, tx)
}
