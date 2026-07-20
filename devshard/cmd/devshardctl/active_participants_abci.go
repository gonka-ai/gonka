package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	activeParticipantsKeyPrefix = "ActiveParticipants/value/"
	abciStoreInferenceKeyPath   = "store/inference/key"
)

// activeParticipantsStoreKey matches inference-chain
// types.ActiveParticipantsFullKey: "ActiveParticipants/value/" + big-endian
// epoch + "/". No cosmos-sdk dependency required.
func activeParticipantsStoreKey(epoch uint64) []byte {
	key := make([]byte, 0, len(activeParticipantsKeyPrefix)+8+1)
	key = append(key, activeParticipantsKeyPrefix...)
	var epochBytes [8]byte
	binary.BigEndian.PutUint64(epochBytes[:], epoch)
	key = append(key, epochBytes[:]...)
	key = append(key, '/')
	return key
}

type chainCurrentEpochResponse struct {
	Epoch jsonUint64 `json:"epoch"`
}

type abciQueryRESTResponse struct {
	Code  uint32 `json:"code"`
	Log   string `json:"log"`
	Value string `json:"value"`
}

func (g *ChainPhaseGate) fetchEffectiveEpochIndex() (uint64, error) {
	if g == nil || g.currentEpochEndpoint == "" {
		return 0, fmt.Errorf("current epoch endpoint not configured")
	}
	resp, err := g.client.Get(g.currentEpochEndpoint)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return 0, fmt.Errorf("get_current_epoch status %d", resp.StatusCode)
	}
	var payload chainCurrentEpochResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, err
	}
	return uint64(payload.Epoch), nil
}

func (g *ChainPhaseGate) fetchActiveParticipantsGroup(epoch uint64) (chainActiveParticipantsGroup, error) {
	var empty chainActiveParticipantsGroup
	if g == nil || g.chainRESTBase == "" {
		return empty, fmt.Errorf("chain REST base not configured")
	}

	dataHex := hex.EncodeToString(activeParticipantsStoreKey(epoch))
	q := url.Values{}
	q.Set("path", abciStoreInferenceKeyPath)
	q.Set("data", dataHex)
	q.Set("prove", "false")
	endpoint := g.chainRESTBase + "/cosmos/base/tendermint/v1beta1/abci_query?" + q.Encode()

	resp, err := g.client.Get(endpoint)
	if err != nil {
		return empty, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return empty, fmt.Errorf("abci_query active participants status %d", resp.StatusCode)
	}

	var payload abciQueryRESTResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return empty, err
	}
	if payload.Code != 0 {
		return empty, fmt.Errorf("abci_query active participants code=%d log=%s", payload.Code, payload.Log)
	}
	if strings.TrimSpace(payload.Value) == "" {
		return empty, fmt.Errorf("active participants not found for epoch %d", epoch)
	}

	raw, err := decodeABCIBytes(payload.Value)
	if err != nil {
		return empty, fmt.Errorf("decode abci value: %w", err)
	}
	participants, err := unmarshalActiveParticipants(raw)
	if err != nil {
		return empty, fmt.Errorf("unmarshal active participants: %w", err)
	}
	return chainActiveParticipantsGroup{Participants: participants}, nil
}

func decodeABCIBytes(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("empty value")
	}
	// Prefer standard base64 (protobuf JSON bytes), then URL-safe, then hex.
	if raw, err := base64.StdEncoding.DecodeString(value); err == nil {
		return raw, nil
	}
	if raw, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return raw, nil
	}
	if raw, err := base64.URLEncoding.DecodeString(value); err == nil {
		return raw, nil
	}
	if raw, err := hex.DecodeString(value); err == nil {
		return raw, nil
	}
	return nil, fmt.Errorf("unsupported abci value encoding")
}

// unmarshalActiveParticipants decodes the gogo-protobuf ActiveParticipants
// bytes stored on-chain. Wire format is standard protobuf; we only keep the
// fields the gateway phase gate needs and skip the rest (seed, voting_powers).
func unmarshalActiveParticipants(data []byte) ([]chainActiveParticipant, error) {
	var out []chainActiveParticipant
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, fmt.Errorf("bad tag: %w", protowire.ParseError(n))
		}
		data = data[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			msg, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return nil, fmt.Errorf("bad participant bytes: %w", protowire.ParseError(n))
			}
			data = data[n:]
			p, err := unmarshalActiveParticipant(msg)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return nil, fmt.Errorf("skip field %d: %w", num, protowire.ParseError(n))
			}
			data = data[n:]
		}
	}
	return out, nil
}

func unmarshalActiveParticipant(data []byte) (chainActiveParticipant, error) {
	var p chainActiveParticipant
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return p, fmt.Errorf("bad participant tag: %w", protowire.ParseError(n))
		}
		data = data[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			s, n := protowire.ConsumeString(data)
			if n < 0 {
				return p, protowire.ParseError(n)
			}
			p.Index = s
			data = data[n:]
		case num == 3 && typ == protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return p, protowire.ParseError(n)
			}
			p.Weight = jsonUint64(v)
			data = data[n:]
		case num == 4 && typ == protowire.BytesType:
			s, n := protowire.ConsumeString(data)
			if n < 0 {
				return p, protowire.ParseError(n)
			}
			p.InferenceURL = s
			data = data[n:]
		case num == 5 && typ == protowire.BytesType:
			s, n := protowire.ConsumeString(data)
			if n < 0 {
				return p, protowire.ParseError(n)
			}
			p.Models = append(p.Models, s)
			data = data[n:]
		case num == 7 && typ == protowire.BytesType:
			msg, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return p, protowire.ParseError(n)
			}
			data = data[n:]
			group, err := unmarshalModelMLNodes(msg)
			if err != nil {
				return p, err
			}
			p.MLNodes = append(p.MLNodes, group)
		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return p, protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return p, nil
}

func unmarshalModelMLNodes(data []byte) (chainModelMLNodes, error) {
	var group chainModelMLNodes
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return group, protowire.ParseError(n)
		}
		data = data[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			msg, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return group, protowire.ParseError(n)
			}
			data = data[n:]
			node, err := unmarshalMLNodeInfo(msg)
			if err != nil {
				return group, err
			}
			group.MLNodes = append(group.MLNodes, node)
		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return group, protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return group, nil
}

func unmarshalMLNodeInfo(data []byte) (chainMLNodeInfo, error) {
	var node chainMLNodeInfo
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return node, protowire.ParseError(n)
		}
		data = data[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			s, n := protowire.ConsumeString(data)
			if n < 0 {
				return node, protowire.ParseError(n)
			}
			node.NodeID = s
			data = data[n:]
		case num == 3 && typ == protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return node, protowire.ParseError(n)
			}
			node.PoCWeight = jsonUint64(v)
			data = data[n:]
		case num == 4 && typ == protowire.BytesType:
			// packed repeated bool
			packed, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return node, protowire.ParseError(n)
			}
			data = data[n:]
			for len(packed) > 0 {
				v, vn := protowire.ConsumeVarint(packed)
				if vn < 0 {
					return node, protowire.ParseError(vn)
				}
				node.TimeslotAllocation = append(node.TimeslotAllocation, v != 0)
				packed = packed[vn:]
			}
		case num == 4 && typ == protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return node, protowire.ParseError(n)
			}
			node.TimeslotAllocation = append(node.TimeslotAllocation, v != 0)
			data = data[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return node, protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return node, nil
}

// marshalActiveParticipants encodes gateway participant structs into the same
// wire shape the chain stores. Used by tests to mock abci_query values.
func marshalActiveParticipants(participants []chainActiveParticipant) ([]byte, error) {
	var out []byte
	for _, p := range participants {
		msg, err := marshalActiveParticipant(p)
		if err != nil {
			return nil, err
		}
		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendBytes(out, msg)
	}
	return out, nil
}

func marshalActiveParticipant(p chainActiveParticipant) ([]byte, error) {
	var out []byte
	if p.Index != "" {
		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendString(out, p.Index)
	}
	if p.Weight != 0 {
		out = protowire.AppendTag(out, 3, protowire.VarintType)
		out = protowire.AppendVarint(out, uint64(p.Weight))
	}
	if p.InferenceURL != "" {
		out = protowire.AppendTag(out, 4, protowire.BytesType)
		out = protowire.AppendString(out, p.InferenceURL)
	}
	for _, model := range p.Models {
		out = protowire.AppendTag(out, 5, protowire.BytesType)
		out = protowire.AppendString(out, model)
	}
	for _, group := range p.MLNodes {
		msg, err := marshalModelMLNodes(group)
		if err != nil {
			return nil, err
		}
		out = protowire.AppendTag(out, 7, protowire.BytesType)
		out = protowire.AppendBytes(out, msg)
	}
	return out, nil
}

func marshalModelMLNodes(group chainModelMLNodes) ([]byte, error) {
	var out []byte
	for _, node := range group.MLNodes {
		msg, err := marshalMLNodeInfo(node)
		if err != nil {
			return nil, err
		}
		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendBytes(out, msg)
	}
	return out, nil
}

func marshalMLNodeInfo(node chainMLNodeInfo) ([]byte, error) {
	var out []byte
	if node.NodeID != "" {
		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendString(out, node.NodeID)
	}
	if node.PoCWeight != 0 {
		out = protowire.AppendTag(out, 3, protowire.VarintType)
		out = protowire.AppendVarint(out, uint64(node.PoCWeight))
	}
	if len(node.TimeslotAllocation) > 0 {
		var packed []byte
		for _, b := range node.TimeslotAllocation {
			var v uint64
			if b {
				v = 1
			}
			packed = protowire.AppendVarint(packed, v)
		}
		out = protowire.AppendTag(out, 4, protowire.BytesType)
		out = protowire.AppendBytes(out, packed)
	}
	return out, nil
}

func writeABCIQueryActiveParticipants(w http.ResponseWriter, participants []chainActiveParticipant) error {
	raw, err := marshalActiveParticipants(participants)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	payload := abciQueryRESTResponse{
		Code:  0,
		Value: base64.StdEncoding.EncodeToString(raw),
	}
	return json.NewEncoder(w).Encode(payload)
}

func writeCurrentEpoch(w http.ResponseWriter, epoch uint64) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]string{
		"epoch": strconv.FormatUint(epoch, 10),
	})
}
