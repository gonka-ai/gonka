package chainevents

import (
	"github.com/cometbft/cometbft/types"
	"time"
)

type Attribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Index bool   `json:"index"`
}

type Event struct {
	Type       string      `json:"type"`
	Attributes []Attribute `json:"attributes"`
}

type Header struct {
	AppHash            string        `json:"app_hash"`
	ChainId            string        `json:"chain_id"`
	ConsensusHash      string        `json:"consensus_hash"`
	DataHash           string        `json:"data_hash"`
	EvidenceHash       string        `json:"evidence_hash"`
	Height             string        `json:"height"`
	LastBlockId        types.BlockID `json:"last_block_id"`
	LastCommitHash     string        `json:"last_commit_hash"`
	LastResultsHash    string        `json:"last_results_hash"`
	NextValidatorsHash string        `json:"next_validators_hash"`
	ProposerAddress    string        `json:"proposer_address"`
	Time               time.Time     `json:"time"`
	ValidatorsHash     string        `json:"validators_hash"`
	Version            struct {
		Block string `json:"block"`
	} `json:"version"`
}

type LastCommit struct {
	BlockId    types.BlockID     `json:"block_id"`
	Height     string            `json:"height"`
	Round      int               `json:"round"`
	Signatures []types.CommitSig `json:"signatures"`
}

type Block struct {
	Data struct {
		Txs []string `json:"txs"`
	} `json:"data"`
	Evidence struct {
		Evidence []interface{} `json:"evidence"`
	} `json:"evidence"`
	Header     Header     `json:"header"`
	LastCommit LastCommit `json:"last_commit"`
}

type FinalizedBlock struct {
	Block               Block         `json:"block"`
	BlockId             types.BlockID `json:"block_id"`
	ResultFinalizeBlock struct {
		AppHash               string `json:"app_hash"`
		ConsensusParamUpdates struct {
			Abci struct {
			} `json:"abci"`
			Block struct {
				MaxBytes string `json:"max_bytes"`
				MaxGas   string `json:"max_gas"`
			} `json:"block"`
			Evidence struct {
				MaxAgeDuration  string `json:"max_age_duration"`
				MaxAgeNumBlocks string `json:"max_age_num_blocks"`
				MaxBytes        string `json:"max_bytes"`
			} `json:"evidence"`
			Validator struct {
				PubKeyTypes []string `json:"pub_key_types"`
			} `json:"validator"`
			Version struct {
			} `json:"version"`
		} `json:"consensus_param_updates"`
		Events    []Event `json:"events"`
		TxResults []struct {
			Code      int     `json:"code"`
			Codespace string  `json:"codespace"`
			Data      string  `json:"data"`
			Events    []Event `json:"events"`
			GasUsed   string  `json:"gas_used"`
			GasWanted string  `json:"gas_wanted"`
			Info      string  `json:"info"`
			Log       string  `json:"log"`
		} `json:"tx_results"`
		ValidatorUpdates []interface{} `json:"validator_updates"`
	} `json:"result_finalize_block"`
}
