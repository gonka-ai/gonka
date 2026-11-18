package transport

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"devshard/transport/rpcpb"
)

const getDiffsResponseRecordsField protowire.Number = 1

type boundedCodec struct{}

func (boundedCodec) Name() string { return "proto" }

func (c boundedCodec) Marshal(msg any) ([]byte, error) {
	return c.MarshalAppend(nil, msg)
}

func (boundedCodec) MarshalAppend(dst []byte, msg any) ([]byte, error) {
	m, ok := msg.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("bounded codec: unexpected %T", msg)
	}
	return proto.MarshalOptions{}.MarshalAppend(dst, m)
}

func (boundedCodec) Unmarshal(data []byte, msg any) error {
	m, ok := msg.(proto.Message)
	if !ok {
		return fmt.Errorf("bounded codec: unexpected %T", msg)
	}
	if _, ok := m.(*rpcpb.GetDiffsResponse); ok {
		if err := checkDiffPageRecords(countWireField(data, getDiffsResponseRecordsField)); err != nil {
			return err
		}
	}
	return proto.Unmarshal(data, m)
}

func countWireField(b []byte, field protowire.Number) int {
	n := 0
	for len(b) > 0 {
		num, typ, l := protowire.ConsumeTag(b)
		if l < 0 {
			return n
		}
		b = b[l:]
		l = protowire.ConsumeFieldValue(num, typ, b)
		if l < 0 {
			return n
		}
		b = b[l:]
		if num == field {
			n++
		}
	}
	return n
}
