package types

import (
	"errors"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// maxDiffContentDepth sits above DiffContent, DevshardTx, the tx message,
// and one nested vote. A deeper blob fails the check.
const maxDiffContentDepth = 8

var (
	diffContentDesc protoreflect.MessageDescriptor

	errDiffContentUTF8  = errors.New("diff content string field")
	errDiffContentDepth = errors.New("diff content nesting")
)

func init() {
	diffContentDesc = new(DiffContent).ProtoReflect().Descriptor()
}

// CheckDiffContentFraming reports whether b is a well-framed DiffContent.
// It walks protowire tags and message fields. It does not allocate the
// transactions DecodeDiffWire builds. Empty b is valid.
func CheckDiffContentFraming(b []byte) error {
	return walkDiffContent(diffContentDesc, b, 0)
}

func walkDiffContent(md protoreflect.MessageDescriptor, b []byte, depth int) error {
	if depth > maxDiffContentDepth {
		return errDiffContentDepth
	}
	fields := md.Fields()
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		fd := fields.ByNumber(num)
		// proto.Unmarshal keeps a known field whose wire type does not match
		// as an unknown field. Consume it the same way, without walking it
		// as the declared message.
		if fd == nil || !diffFieldWireOK(fd, typ) {
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return protowire.ParseError(n)
			}
			b = b[n:]
			continue
		}
		switch fd.Kind() {
		case protoreflect.MessageKind:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return protowire.ParseError(m)
			}
			if err := walkDiffContent(fd.Message(), v, depth+1); err != nil {
				return err
			}
			b = b[m:]
		case protoreflect.StringKind:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return protowire.ParseError(m)
			}
			if !utf8.Valid(v) {
				return errDiffContentUTF8
			}
			b = b[m:]
		default:
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return protowire.ParseError(n)
			}
			b = b[n:]
		}
	}
	return nil
}

func diffFieldWireOK(fd protoreflect.FieldDescriptor, typ protowire.Type) bool {
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind, protoreflect.BytesKind, protoreflect.StringKind:
		return typ == protowire.BytesType
	case protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind, protoreflect.FloatKind:
		return typ == protowire.Fixed32Type
	case protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind, protoreflect.DoubleKind:
		return typ == protowire.Fixed64Type
	default:
		if typ == protowire.VarintType {
			return true
		}
		return fd.IsList() && typ == protowire.BytesType
	}
}
