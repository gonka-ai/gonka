package queryapi

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"common/utils"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/cosmos/cosmos-sdk/codec"
	cosmosed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	gogoproto "github.com/cosmos/gogoproto/proto"

	"common/queryapi/gen"
)

// protoToAPIJSON is the public query-API encoding for gogo protobuf payloads:
// integers are JSON numbers, protobuf enums are names.
//
// codec.ProtoMarshalJSON (protojson) already emits enum names but stringifies
// int64/uint64. encoding/json on the Go struct keeps numbers but emits enum
// ints. This helper starts from protojson and rewrites numeric fields.
func protoToAPIJSON(msg gogoproto.Message) (gen.RawProtoJson, error) {
	raw, err := protoToRawJSON(msg)
	if err != nil {
		return nil, err
	}
	coerceProtoJSONNumbers(msg, raw)
	return raw, nil
}

func protoToAPIJSONPtr(msg gogoproto.Message) (*gen.RawProtoJson, error) {
	raw, err := protoToAPIJSON(msg)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	return &raw, nil
}

// protoToRawJSON converts a gogo protobuf message into a JSON-safe value suitable
// for gen.RawProtoJson fields. Standard encoding/json cannot marshal messages that
// contain gogoproto Any fields (e.g. validator pubkeys).
func protoToRawJSON(msg gogoproto.Message) (gen.RawProtoJson, error) {
	if msg == nil {
		return nil, nil
	}
	if val := reflect.ValueOf(msg); val.Kind() == reflect.Pointer && val.IsNil() {
		return nil, nil
	}
	bz, err := codec.ProtoMarshalJSON(msg, nil)
	if err != nil {
		return nil, err
	}
	var out gen.RawProtoJson
	if err := json.Unmarshal(bz, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func protoToRawJSONPtr(msg gogoproto.Message) (*gen.RawProtoJson, error) {
	raw, err := protoToRawJSON(msg)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	return &raw, nil
}

func coerceProtoJSONNumbers(msg gogoproto.Message, v any) {
	obj, ok := v.(map[string]any)
	if !ok || msg == nil {
		return
	}
	rv := reflect.ValueOf(msg)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return
	}
	props := gogoproto.GetProperties(rv.Type())
	for _, p := range props.Prop {
		if p == nil || p.Name == "" {
			continue
		}
		field := rv.FieldByName(p.Name)
		if !field.IsValid() {
			continue
		}
		child, key, ok := protoJSONValue(obj, p)
		if !ok {
			continue
		}
		if p.Enum != "" {
			continue
		}
		switch field.Kind() {
		case reflect.Int, reflect.Int32, reflect.Int64:
			if n, ok := parseJSONInt(child); ok {
				obj[key] = n
			}
		case reflect.Uint, reflect.Uint32, reflect.Uint64:
			if n, ok := parseJSONUint(child); ok {
				obj[key] = n
			}
		case reflect.Pointer:
			if nested, ok := field.Interface().(gogoproto.Message); ok {
				coerceProtoJSONNumbers(nested, child)
			}
		case reflect.Struct:
			if field.CanAddr() {
				if nested, ok := field.Addr().Interface().(gogoproto.Message); ok {
					coerceProtoJSONNumbers(nested, child)
				}
			}
		case reflect.Slice:
			if field.Type().Elem().Kind() == reflect.Uint8 {
				continue
			}
			arr, ok := child.([]any)
			if !ok {
				continue
			}
			for i := range arr {
				if i >= field.Len() {
					break
				}
				elem := field.Index(i)
				var nested gogoproto.Message
				switch elem.Kind() {
				case reflect.Pointer:
					nested, _ = elem.Interface().(gogoproto.Message)
				default:
					if elem.CanAddr() {
						nested, _ = elem.Addr().Interface().(gogoproto.Message)
					}
				}
				if nested != nil {
					coerceProtoJSONNumbers(nested, arr[i])
				}
			}
		}
	}
}

func protoJSONValue(obj map[string]any, p *gogoproto.Properties) (any, string, bool) {
	for _, key := range []string{p.OrigName, p.JSONName} {
		if key == "" {
			continue
		}
		if child, ok := obj[key]; ok {
			return child, key, true
		}
	}
	return nil, "", false
}

func parseJSONInt(v any) (int64, bool) {
	switch n := v.(type) {
	case string:
		parsed, err := strconv.ParseInt(n, 10, 64)
		return parsed, err == nil
	case float64:
		return int64(n), true
	case json.Number:
		parsed, err := n.Int64()
		return parsed, err == nil
	case int64:
		return n, true
	default:
		return 0, false
	}
}

func parseJSONUint(v any) (uint64, bool) {
	switch n := v.(type) {
	case string:
		parsed, err := strconv.ParseUint(n, 10, 64)
		return parsed, err == nil
	case float64:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	case json.Number:
		parsed, err := strconv.ParseUint(n.String(), 10, 64)
		return parsed, err == nil
	case uint64:
		return n, true
	default:
		return 0, false
	}
}

// validatorToDapiJSON encodes a Comet validator in the legacy dapi shape:
// hex-uppercase address, base64 pub_key string, and numeric int64 fields.
// Legacy dapi marshaled native comet types.Validator with encoding/json, so
// voting_power / proposer_priority were JSON numbers, not strings.
func validatorToDapiJSON(v *cmtservice.Validator) (gen.RawProtoJson, error) {
	if v == nil {
		return nil, fmt.Errorf("nil validator")
	}
	if v.PubKey == nil {
		return nil, fmt.Errorf("validator %q missing pub_key", v.Address)
	}

	var sdkPubKey cosmosed25519.PubKey
	if err := gogoproto.Unmarshal(v.PubKey.Value, &sdkPubKey); err != nil {
		return nil, err
	}

	pubKeyStr := utils.PubKeyToString(&sdkPubKey)
	address, err := utils.ValidatorKeyToHexAddress(pubKeyStr)
	if err != nil {
		return nil, err
	}

	return gen.RawProtoJson(map[string]any{
		"address":           address,
		"pub_key":           pubKeyStr,
		"voting_power":      v.VotingPower,
		"proposer_priority": v.ProposerPriority,
	}), nil
}

func validatorsToRawJSON(vals []*cmtservice.Validator) ([]gen.RawProtoJson, error) {
	if len(vals) == 0 {
		return []gen.RawProtoJson{}, nil
	}

	out := make([]gen.RawProtoJson, len(vals))
	for i, v := range vals {
		raw, err := validatorToDapiJSON(v)
		if err != nil {
			return nil, err
		}
		out[i] = raw
	}
	return out, nil
}
