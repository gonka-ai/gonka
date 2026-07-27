package seed

import (
	"encoding/base64"
	"fmt"
	"os"

	"devshard/signing"
	"devshard/testenv/config"
	"devshard/testenv/mockchain/store"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"gopkg.in/yaml.v3"
)

const (
	defaultChainID            = "gonka-test"
	defaultTestenvEpochLength = int64(400)
)

// Defaults returns a minimal seeded store for unit tests and local dev.
func Defaults() *store.Store {
	s := store.New()
	s.ChainID = defaultChainID
	s.BlockHeight = 150
	s.Epoch = inferencetypes.Epoch{Index: 1, PocStartBlockHeight: 100}
	s.ParamsBlockHeight = 150
	s.Params = inferencetypes.Params{
		EpochParams:      &inferencetypes.EpochParams{EpochLength: defaultTestenvEpochLength},
		ValidationParams: &inferencetypes.ValidationParams{LogprobsMode: "raw"},
		DevshardEscrowParams: &inferencetypes.DevshardEscrowParams{
			DevshardRequestsEnabled: true,
			MaxNonce:                500,
			RefusalTimeout:          60,
			ExecutionTimeout:        1200,
			ValidationRate:          6000,
			VoteThresholdFactor:     50,
		},
	}
	host := "gonka1host000000000000000000000000000000000"
	warm := "gonka1warm000000000000000000000000000000000"
	s.Participants[host] = &inferencetypes.Participant{
		Index:        host,
		Address:      host,
		InferenceUrl: "http://versiond-router:8080",
		Status:       inferencetypes.ParticipantStatus_ACTIVE,
	}
	s.Escrows[1] = &inferencetypes.DevshardEscrow{
		Id:                        1,
		Creator:                   "gonka1creator00000000000000000000000000000",
		Amount:                    1_000_000,
		Slots:                     []string{host},
		EpochIndex:                1,
		AppHash:                   "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
		ModelId:                   "test-model",
		TokenPrice:                100,
		CreateDevshardFee:         10,
		FeePerNonce:               1,
		InferenceSealGraceNonces:  3,
		InferenceSealGraceSeconds: 30,
		AutoSealEveryNNonces:      100,
		ValidationRate:            6000,
		VoteThresholdFactor:       50,
		RefusalTimeout:            60,
		ExecutionTimeout:          1200,
	}
	s.Grantees[store.GranteeKey{
		GranterAddress: host,
		MessageTypeURL: "/inference.inference.MsgStartInference",
	}] = []inferencetypes.Grantee{{Address: warm}}
	s.EpochGroupData[store.EpochGroupKey{EpochIndex: 1, ModelID: "test-model"}] = &inferencetypes.EpochGroupData{
		EpochIndex: 1,
		ModelId:    "test-model",
		ModelSnapshot: &inferencetypes.Model{
			Id: "test-model",
			ValidationThreshold: &inferencetypes.Decimal{
				Value:    50,
				Exponent: 0,
			},
		},
	}
	s.InitNextPocStart()
	s.InitAfterLoad()
	return s
}
func Load(path string) (*store.Store, error) {
	if path == "" {
		return Defaults(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var f config.File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return FromFile(&f)
}

// FromFile builds a store from a parsed config file.
func FromFile(f *config.File) (*store.Store, error) {
	if f == nil {
		return Defaults(), nil
	}
	s := store.New()
	s.ChainID = f.ChainID
	if s.ChainID == "" {
		s.ChainID = defaultChainID
	}
	s.BlockHeight = f.BlockHeight
	if s.BlockHeight == 0 {
		s.BlockHeight = 1
	}
	s.Epoch = inferencetypes.Epoch{
		Index:               f.Epoch.Index,
		PocStartBlockHeight: f.Epoch.PocStartBlockHeight,
	}
	if s.Epoch.Index == 0 {
		s.Epoch.Index = 1
	}
	if s.Epoch.PocStartBlockHeight == 0 {
		s.Epoch.PocStartBlockHeight = s.BlockHeight - 50
	}
	if f.Epoch.ParamsBlockHeight != 0 {
		s.ParamsBlockHeight = f.Epoch.ParamsBlockHeight
	} else {
		s.ParamsBlockHeight = s.BlockHeight
	}

	logprobs := f.Params.LogprobsMode
	if logprobs == "" {
		logprobs = "raw"
	}
	maxNonce := f.Params.MaxNonce
	if maxNonce == 0 {
		maxNonce = 500
	}
	refusalTimeout := timeoutOrDefault(f.Params.RefusalTimeout, inferencetypes.DefaultDevshardRefusalTimeout)
	executionTimeout := timeoutOrDefault(f.Params.ExecutionTimeout, inferencetypes.DefaultDevshardExecutionTimeout)
	s.Params = inferencetypes.Params{
		EpochParams: &inferencetypes.EpochParams{
			EpochLength: epochLengthFromConfig(f.Epoch.EpochLength),
		},
		ValidationParams: &inferencetypes.ValidationParams{LogprobsMode: logprobs},
		DevshardEscrowParams: &inferencetypes.DevshardEscrowParams{
			DevshardRequestsEnabled: f.Params.DevshardRequestsEnabled,
			MaxNonce:                maxNonce,
			RefusalTimeout:          refusalTimeout,
			ExecutionTimeout:        executionTimeout,
			ValidationRate:          f.Params.ValidationRate,
			VoteThresholdFactor:     f.Params.VoteThresholdFactor,
		},
	}

	for _, p := range f.Participants {
		if p.Address == "" {
			continue
		}
		part := &inferencetypes.Participant{
			Index:        p.Address,
			Address:      p.Address,
			InferenceUrl: p.InferenceURL,
			Status:       inferencetypes.ParticipantStatus_ACTIVE,
		}
		s.Participants[p.Address] = part
	}

	for _, e := range f.Escrows {
		if e.ID == 0 {
			continue
		}
		s.Escrows[e.ID] = &inferencetypes.DevshardEscrow{
			Id:                        e.ID,
			Creator:                   e.Creator,
			Amount:                    e.Amount,
			Slots:                     append([]string(nil), e.Slots...),
			EpochIndex:                e.EpochIndex,
			AppHash:                   e.AppHash,
			ModelId:                   e.ModelID,
			TokenPrice:                e.TokenPrice,
			CreateDevshardFee:         e.CreateDevshardFee,
			FeePerNonce:               e.FeePerNonce,
			InferenceSealGraceNonces:  e.InferenceSealGraceNonces,
			InferenceSealGraceSeconds: e.InferenceSealGraceSeconds,
			AutoSealEveryNNonces:      e.AutoSealEveryNNonces,
			ValidationRate:            e.ValidationRate,
			VoteThresholdFactor:       e.VoteThresholdFactor,
			RefusalTimeout:            timeoutOrDefault(e.RefusalTimeout, refusalTimeout),
			ExecutionTimeout:          timeoutOrDefault(e.ExecutionTimeout, executionTimeout),
		}
	}

	// Seed AccountByAddress pubkeys from materialised private keys (payload auth).
	seedPubkey := func(hexKey string) {
		if hexKey == "" {
			return
		}
		signer, err := signing.SignerFromHex(hexKey)
		if err != nil {
			return
		}
		s.Pubkeys[signer.Address()] = base64.StdEncoding.EncodeToString(signer.CompressedPublicKeyBytes())
	}
	for _, h := range f.Hosts {
		seedPubkey(h.PrivateKeyHex)
	}
	seedPubkey(f.User.PrivateKeyHex)
	seedPubkey(f.WarmGrantee.PrivateKeyHex)

	for _, g := range f.Grantees {
		if g.GranterAddress == "" || g.MessageTypeURL == "" {
			continue
		}
		grantees := make([]inferencetypes.Grantee, 0, len(g.Grantees))
		for _, addr := range g.Grantees {
			grantees = append(grantees, inferencetypes.Grantee{
				Address: addr,
				PubKey:  s.Pubkeys[addr],
			})
		}
		s.Grantees[store.GranteeKey{
			GranterAddress: g.GranterAddress,
			MessageTypeURL: g.MessageTypeURL,
		}] = grantees
	}

	for _, eg := range f.EpochGroups {
		if eg.ModelID == "" {
			continue
		}
		epoch := eg.EpochIndex
		if epoch == 0 {
			epoch = s.Epoch.Index
		}
		val := eg.ValidationThreshold
		if val == 0 {
			val = 50
		}
		s.EpochGroupData[store.EpochGroupKey{EpochIndex: epoch, ModelID: eg.ModelID}] = &inferencetypes.EpochGroupData{
			EpochIndex: epoch,
			ModelId:    eg.ModelID,
			ModelSnapshot: &inferencetypes.Model{
				Id: eg.ModelID,
				ValidationThreshold: &inferencetypes.Decimal{
					Value:    val,
					Exponent: eg.ValidationExponent,
				},
			},
		}
	}

	if len(s.Participants) == 0 && len(s.Escrows) == 0 {
		return Defaults(), nil
	}
	if f.Epoch.NextPocStartBlockHeight != 0 {
		s.NextPocStartBlockHeight = f.Epoch.NextPocStartBlockHeight
	} else {
		s.InitNextPocStart()
	}
	s.InitAfterLoad()
	return s, nil
}

func timeoutOrDefault(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

func epochLengthFromConfig(length int64) int64 {
	if length > 0 {
		return length
	}
	return defaultTestenvEpochLength
}
