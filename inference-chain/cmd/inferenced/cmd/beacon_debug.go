package cmd

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/spf13/cobra"

	blstypes "github.com/productscience/inference/x/bls/types"
)

// beaconTxView is the JSON printed by `inferenced debug decode-beacon-tx`.
type beaconTxView struct {
	Outcome     string           `json:"outcome"` // "sigma" or "miss"
	Round       uint64           `json:"round"`
	KeyEpoch    uint64           `json:"key_epoch"`
	SigmaSHA256 string           `json:"sigma_sha256,omitempty"`
	SigmaLen    int              `json:"sigma_len,omitempty"`
	HintSlots   []uint32         `json:"contributors_hint,omitempty"`
	Votes       []beaconVoteView `json:"votes,omitempty"`
}

type beaconVoteView struct {
	Validator string `json:"validator"`
	Power     int64  `json:"power"`
	Flag      string `json:"flag"`
	ExtLen    int    `json:"ext_len"`
}

// decodeBeaconTxView decodes a block tx (base64, as CometBFT RPC prints it, or hex).
func decodeBeaconTxView(arg string) (*beaconTxView, error) {
	arg = strings.TrimSpace(arg)
	var raw []byte
	for _, decode := range []func(string) ([]byte, error){base64.StdEncoding.DecodeString, hex.DecodeString} {
		if b, err := decode(arg); err == nil && blstypes.IsBeaconTx(b) {
			raw = b // a hex string can also parse as base64: keep the one with the prefix
			break
		}
	}
	if raw == nil {
		return nil, fmt.Errorf("not a beacon tx (base64 or hex with the %q prefix)", blstypes.BeaconTxPrefix)
	}
	ib, err := blstypes.DecodeBeaconTx(raw)
	if err != nil {
		return nil, err
	}
	switch o := ib.Outcome.(type) {
	case *blstypes.InjectedBeacon_Sigma:
		sum := sha256.Sum256(o.Sigma.Sigma)
		v := &beaconTxView{Outcome: "sigma", Round: o.Sigma.Round, KeyEpoch: o.Sigma.KeyEpoch,
			SigmaSHA256: hex.EncodeToString(sum[:]), SigmaLen: len(o.Sigma.Sigma)}
		for i, b := range o.Sigma.ContributorsHint {
			for bit := 0; bit < 8; bit++ {
				if b&(1<<bit) != 0 {
					v.HintSlots = append(v.HintSlots, uint32(i*8+bit))
				}
			}
		}
		return v, nil
	case *blstypes.InjectedBeacon_Miss:
		var ec abci.ExtendedCommitInfo
		if err := ec.Unmarshal(o.Miss.ExtendedCommit); err != nil {
			return nil, fmt.Errorf("proof of miss: %w", err)
		}
		v := &beaconTxView{Outcome: "miss", Round: o.Miss.Round, KeyEpoch: o.Miss.KeyEpoch}
		for _, vote := range ec.Votes {
			v.Votes = append(v.Votes, beaconVoteView{
				Validator: strings.ToUpper(hex.EncodeToString(vote.Validator.Address)),
				Power:     vote.Validator.Power, Flag: vote.BlockIdFlag.String(), ExtLen: len(vote.VoteExtension),
			})
		}
		return v, nil
	default:
		return nil, fmt.Errorf("unknown beacon outcome")
	}
}

// decodeBeaconTxCmd prints the block beacon tx (Txs[0]) as JSON.
func decodeBeaconTxCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "decode-beacon-tx [base64-or-hex-tx]",
		Short: "Decode the block beacon tx (Txs[0]: σ of the previous round, or a proof of miss) as JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := decodeBeaconTxView(args[0])
			if err != nil {
				return err
			}
			out, err := json.Marshal(v)
			if err != nil {
				return err
			}
			cmd.Println(string(out))
			return nil
		},
	}
}
