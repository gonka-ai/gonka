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
