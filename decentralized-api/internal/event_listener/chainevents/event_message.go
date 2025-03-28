package chainevents

type JSONRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Result  Result `json:"result"`
}

type Result struct {
	Query  string              `json:"query"`
	Data   Data                `json:"data"`
	Events map[string][]string `json:"events"`
}

type Data struct {
	Type  string                 `json:"type"`
	Value map[string]interface{} `json:"value"`
}

type Attribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Index bool   `json:"index"`
}

type Event struct {
	Type       string      `json:"type"`
	Attributes []Attribute `json:"attributes"`
}

type TxResult struct {
	Height string `json:"height"`
	Tx     string `json:"tx"`
	Result struct {
		Events []Event `json:"events"`
	} `json:"result"`
}

type Value struct {
	TxResult TxResult `json:"TxResult"`
}
