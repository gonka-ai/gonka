package chainevents

import "time"

type Header struct {
	AppHash       string `json:"app_hash"`
	ChainId       string `json:"chain_id"`
	ConsensusHash string `json:"consensus_hash"`
	DataHash      string `json:"data_hash"`
	EvidenceHash  string `json:"evidence_hash"`
	Height        string `json:"height"`
	LastBlockId   struct {
		Hash  string `json:"hash"`
		Parts struct {
			Hash  string `json:"hash"`
			Total int    `json:"total"`
		} `json:"parts"`
	} `json:"last_block_id"`
	LastCommitHash     string    `json:"last_commit_hash"`
	LastResultsHash    string    `json:"last_results_hash"`
	NextValidatorsHash string    `json:"next_validators_hash"`
	ProposerAddress    string    `json:"proposer_address"`
	Time               time.Time `json:"time"`
	ValidatorsHash     string    `json:"validators_hash"`
	Version            struct {
		Block string `json:"block"`
	} `json:"version"`
}

type Block struct {
	Data struct {
		Txs []string `json:"txs"`
	} `json:"data"`
	Evidence struct {
		Evidence []interface{} `json:"evidence"`
	} `json:"evidence"`
	Header     Header `json:"header"`
	LastCommit struct {
		BlockId struct {
			Hash  string `json:"hash"`
			Parts struct {
				Hash  string `json:"hash"`
				Total int    `json:"total"`
			} `json:"parts"`
		} `json:"block_id"`
		Height     string `json:"height"`
		Round      int    `json:"round"`
		Signatures []struct {
			BlockIdFlag      int       `json:"block_id_flag"`
			Signature        string    `json:"signature"`
			Timestamp        time.Time `json:"timestamp"`
			ValidatorAddress string    `json:"validator_address"`
		} `json:"signatures"`
	} `json:"last_commit"`
}

type BlockId struct {
	Hash  string `json:"hash"`
	Parts struct {
		Hash  string `json:"hash"`
		Total int    `json:"total"`
	} `json:"parts"`
}

type FinalizedBlock struct {
	Block               Block   `json:"block"`
	BlockId             BlockId `json:"block_id"`
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
		Events []struct {
			Attributes []struct {
				Index bool   `json:"index"`
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"attributes"`
			Type string `json:"type"`
		} `json:"events"`
		TxResults []struct {
			Code      int    `json:"code"`
			Codespace string `json:"codespace"`
			Data      string `json:"data"`
			Events    []struct {
				Attributes []struct {
					Index bool   `json:"index"`
					Key   string `json:"key"`
					Value string `json:"value"`
				} `json:"attributes"`
				Type string `json:"type"`
			} `json:"events"`
			GasUsed   string `json:"gas_used"`
			GasWanted string `json:"gas_wanted"`
			Info      string `json:"info"`
			Log       string `json:"log"`
		} `json:"tx_results"`
		ValidatorUpdates []interface{} `json:"validator_updates"`
	} `json:"result_finalize_block"`
}
