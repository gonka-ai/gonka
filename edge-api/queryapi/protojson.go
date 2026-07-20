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

	"edge-api/queryapi/gen"
)

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

// validatorToDapiJSON encodes a Comet validator in the legacy dapi/testermint shape:
// hex-uppercase address, base64 pub_key string, and string-encoded int64 fields.
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
		"voting_power":      strconv.FormatInt(v.VotingPower, 10),
		"proposer_priority": strconv.FormatInt(v.ProposerPriority, 10),
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
